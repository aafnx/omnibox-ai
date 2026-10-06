Set-StrictMode -Version Latest
function New-AiNativeServiceCommand {
 param([string]$PwshPath,[string]$Directory,[string]$Profile)
 foreach($v in @($PwshPath,$Directory,$Profile)){if($v.Contains('"') -or $v.Contains("`n") -or $v.Contains("`r")){throw 'Invalid service path'}}
 return '"'+$PwshPath+'" -NoLogo -NoProfile -ExecutionPolicy Bypass -File "'+(Join-Path $Directory 'service.ps1')+'" -Profile "'+$Profile+'"'
}
function Install-AiNativeServices {
 param([string]$Directory,[string]$PwshPath,$Config,[string]$ValidatedRuntimePath)
 $backup=Join-Path $Directory ('backups/services-'+[Guid]::NewGuid().ToString('N'));$null=New-Item -ItemType Directory -Path $backup -Force
 $changes=@();$existing=@{};$legacyWrappers=@()
 # Validate every service before any stop or registration change.
 foreach($name in ($Config.backends.Keys|Sort-Object)){
  $b=$Config.backends[$name];$old=Get-CimInstance Win32_Service -Filter "Name='$($b.service)'" -ErrorAction Stop
  $expected=[IO.Path]::ChangeExtension($b.service_config,'.exe');$command=New-AiNativeServiceCommand $PwshPath $Directory $b.service_config
  if($null -ne $old -and $old.PathName.Trim().Trim('"') -ine $expected -and $old.PathName -ine $command){throw "Service $($b.service) belongs to another program"}
  $existing[$name]=$old
 }
 foreach($old in $existing.Values){if($null -ne $old -and $old.State -ne 'Stopped'){$process=Get-Process -Id $old.ProcessId -ErrorAction SilentlyContinue;Stop-Service $old.Name -NoWait -ErrorAction Stop;(Get-Service $old.Name).WaitForStatus('Stopped',[TimeSpan]::FromSeconds(30));if($null -ne $process -and -not $process.WaitForExit(15000)){throw 'Service host process did not exit.'}}}
 $dll=Join-Path $Directory 'src/Service.Host.dll'
 if(Test-Path $dll){Copy-Item -LiteralPath $dll -Destination (Join-Path $backup 'Service.Host.dll');Remove-Item -LiteralPath $dll -Force}
 try{
  if($ValidatedRuntimePath){Copy-Item -LiteralPath $ValidatedRuntimePath -Destination $dll -Force}
  else{& $PwshPath -NoLogo -NoProfile -File (Join-Path $Directory 'scripts/build-service.ps1');if($LASTEXITCODE -ne 0 -or -not (Test-Path -LiteralPath $dll)){throw 'Cannot compile native service runtime.'};Test-AiNativeServiceRuntime $Directory $PwshPath}
 }catch{if(Test-Path (Join-Path $backup 'Service.Host.dll')){Copy-Item (Join-Path $backup 'Service.Host.dll') $dll -Force};throw}
 try{
 foreach($name in ($Config.backends.Keys|Sort-Object)){
  $b=$Config.backends[$name];$old=Get-CimInstance Win32_Service -Filter "Name='$($b.service)'" -ErrorAction Stop
  $expected=[IO.Path]::ChangeExtension($b.service_config,'.exe')
  $command=New-AiNativeServiceCommand $PwshPath $Directory $b.service_config
  if($null -ne $old -and $old.PathName.Trim().Trim('"') -ine $expected -and $old.PathName -ine $command){throw "Service $($b.service) belongs to another program"}
  if($null -ne $old -and $old.State -ne 'Stopped'){Stop-Service $b.service -NoWait -ErrorAction Stop;(Get-Service $b.service).WaitForStatus('Stopped',[TimeSpan]::FromSeconds(30))}
  Copy-Item -LiteralPath $b.service_config -Destination (Join-Path $backup "$name.xml")
  if($null -ne $old -and $old.PathName.Trim().Trim('"') -ieq $expected -and (Test-Path -LiteralPath $expected)){Copy-Item -LiteralPath $expected -Destination (Join-Path $backup "$name-winsw.exe");$legacyWrappers+=$expected}
  $changes+=@{service=$b.service;old=$old;command=$command}
  if($null -eq $old){$null=New-Service -Name $b.service -BinaryPathName $command -StartupType Manual -DisplayName "OmniBox AI · $name"}
  else{Set-Service -Name $b.service -BinaryPathName $command -StartupType Manual -ErrorAction Stop}
  $b.runtime_path=$b.service_config+'.runtime.json'
 }
 }catch{
  foreach($change in $changes){try{if($null -ne $change.old){Set-Service -Name $change.service -BinaryPathName $change.old.PathName -ErrorAction Stop}else{Remove-Service $change.service -ErrorAction Stop}}catch{Write-Warning "Cannot restore service $($change.service): $_"}}
  throw
 }
 ConvertTo-Json -InputObject @($legacyWrappers)|Set-Content -LiteralPath (Join-Path $backup 'legacy-wrappers.json')
 return $backup
}
Export-ModuleMember -Function New-AiNativeServiceCommand,Install-AiNativeServices

function Test-AiNativeServiceRuntime {
 param([string]$Directory,[string]$PwshPath)
 $name='omnibox-ai-probe-'+[Guid]::NewGuid().ToString('N').Substring(0,8)
 $profile=Join-Path $Directory ($name+'.xml')
 $x=[xml]'<service><id/><executable/><arguments>-NoLogo -NoProfile -Command "while ($true) { Start-Sleep -Seconds 60 }"</arguments><workingdirectory/><logpath/></service>'
 $x.service.id=$name;$x.service.executable=$PwshPath;$x.service.workingdirectory=$Directory;$x.service.logpath=Join-Path $Directory 'logs';$x.Save($profile)
 $pidToCheck=$null
 try{
  $null=New-Service -Name $name -BinaryPathName (New-AiNativeServiceCommand $PwshPath $Directory $profile) -StartupType Manual
  Start-Service $name -ErrorAction Stop;(Get-Service $name).WaitForStatus('Running',[TimeSpan]::FromSeconds(15))
  $runtime=Get-Content -LiteralPath ($profile+'.runtime.json') -Raw|ConvertFrom-Json;$pidToCheck=$runtime.pid
  Stop-Service $name -NoWait -ErrorAction Stop;(Get-Service $name).WaitForStatus('Stopped',[TimeSpan]::FromSeconds(30))
  if($null -ne $pidToCheck -and (Get-Process -Id $pidToCheck -ErrorAction SilentlyContinue)){throw 'Service probe child did not stop.'}
 }catch{
  $cause=$_.Exception.Message
  $details=@()
  foreach($suffix in @('bootstrap','host','err')){
   $log=Join-Path $Directory ('logs/'+$name+'.'+$suffix+'.log')
   if(Test-Path -LiteralPath $log){$details+=@(Get-Content -LiteralPath $log -Tail 12)}
  }
  if($IsWindows){
   $events=Get-WinEvent -FilterHashtable @{LogName='System';ProviderName='Service Control Manager';StartTime=(Get-Date).AddMinutes(-2)} -MaxEvents 30 -ErrorAction SilentlyContinue
   $details+=@($events|Where-Object {$_.Message -like "*$name*"}|Select-Object -First 3 -ExpandProperty Message)
  }
  throw ('Проверка Windows service runtime не пройдена: '+$cause+"`n"+($details -join "`n")+"`nЖурналы: "+(Join-Path $Directory 'logs'))
 }finally{
  $s=Get-Service $name -ErrorAction SilentlyContinue;if($null -ne $s){if($s.Status -ne 'Stopped'){Stop-Service $name -ErrorAction SilentlyContinue};Remove-Service $name -ErrorAction SilentlyContinue}
  Remove-Item -LiteralPath $profile,($profile+'.runtime.json') -Force -ErrorAction SilentlyContinue
 }
}
Export-ModuleMember -Function New-AiNativeServiceCommand,Install-AiNativeServices,Test-AiNativeServiceRuntime

function New-AiCheckedServiceRuntime {
 param([string]$SourceDirectory,[string]$PwshPath)
 $stage=Join-Path ([IO.Path]::GetTempPath()) ('omnibox-ai-runtime-'+[Guid]::NewGuid().ToString('N'))
 $null=New-Item -ItemType Directory -Path (Join-Path $stage 'src'),(Join-Path $stage 'scripts') -Force
 Copy-Item -LiteralPath (Join-Path $SourceDirectory 'service.ps1') -Destination $stage
 Copy-Item -LiteralPath (Join-Path $SourceDirectory 'src/Service.Host.cs') -Destination (Join-Path $stage 'src')
 Copy-Item -LiteralPath (Join-Path $SourceDirectory 'scripts/build-service.ps1') -Destination (Join-Path $stage 'scripts')
 try {
  & $PwshPath -NoLogo -NoProfile -File (Join-Path $stage 'scripts/build-service.ps1')
  if($LASTEXITCODE -ne 0 -or -not(Test-Path -LiteralPath (Join-Path $stage 'src/Service.Host.dll'))){throw 'Cannot compile native service runtime.'}
  Test-AiNativeServiceRuntime $stage $PwshPath
  return $stage
 }catch{
  # Keep this isolated failure report; the existing installation has not been changed.
  throw ('Проверка нового runtime остановила установку до изменения приложения. '+$_.Exception.Message+"`nДиагностика: "+$stage)
 }
}
Export-ModuleMember -Function New-AiNativeServiceCommand,Install-AiNativeServices,Test-AiNativeServiceRuntime,New-AiCheckedServiceRuntime
