Set-StrictMode -Version Latest
function Set-AiServiceCommand {
 [CmdletBinding()]
 param(
  [Parameter(Mandatory)][ValidatePattern('^[A-Za-z0-9_.-]+$')][string]$Name,
  [Parameter(Mandatory)][string]$Command,
  [ValidateSet('Automatic','Manual','Disabled')][string]$StartupType
 )
 if([string]::IsNullOrWhiteSpace($Command) -or $Command.Contains("`n") -or $Command.Contains("`r")){throw 'Invalid service command'}
 $service=Get-CimInstance Win32_Service -Filter "Name='$Name'" -ErrorAction Stop
 if($null -eq $service){throw "Service $Name does not exist"}
 $arguments=@{PathName=$Command}
 if($StartupType){$arguments.StartMode=$StartupType}
 $result=Invoke-CimMethod -InputObject $service -MethodName Change -Arguments $arguments -OperationTimeoutSec 30 -ErrorAction Stop
 if($null -eq $result -or $null -eq $result.ReturnValue){throw "Service $Name change returned no status"}
 if($result.ReturnValue -ne 0){throw "Service $Name change failed (Win32_Service.Change code $($result.ReturnValue))"}
 $updated=Get-CimInstance Win32_Service -Filter "Name='$Name'" -ErrorAction Stop
 $mode=if($StartupType -eq 'Automatic'){'Auto'}else{$StartupType}
 if($null -eq $updated -or $updated.PathName -cne $Command -or ($StartupType -and $updated.StartMode -ne $mode)){throw "Service $Name change verification failed"}
}
function New-AiNativeServiceCommand {
 param([string]$PwshPath,[string]$Directory,[string]$Profile)
 foreach($v in @($PwshPath,$Directory,$Profile)){if($v.Contains('"') -or $v.Contains("`n") -or $v.Contains("`r")){throw 'Invalid service path'}}
 return '"'+(Join-Path $Directory 'src/Service.Host.exe')+'" -Profile "'+$Profile+'"'
}
function New-AiLegacyServiceCommand {
 param([string]$PwshPath,[string]$Directory,[string]$Profile)
 return '"'+$PwshPath+'" -NoLogo -NoProfile -ExecutionPolicy Bypass -File "'+(Join-Path $Directory 'service.ps1')+'" -Profile "'+$Profile+'"'
}
function Test-AiOwnedServiceCommand {
 param([string]$Command,[string]$PwshPath,[string]$Directory,[string]$Profile)
 $wrapper=[IO.Path]::ChangeExtension($Profile,'.exe')
 return $Command.Trim() -iin @((New-AiNativeServiceCommand $PwshPath $Directory $Profile),(New-AiLegacyServiceCommand $PwshPath $Directory $Profile),$wrapper,('"'+$wrapper+'"'))
}
function Install-AiNativeServices {
 param([string]$Directory,[string]$PwshPath,$Config,[string]$ValidatedRuntimePath)
 $backup=Join-Path $Directory ('backups/services-'+[Guid]::NewGuid().ToString('N'));$null=New-Item -ItemType Directory -Path $backup -Force
 $changes=@();$existing=@{};$legacyWrappers=@()
 # Validate every service before any stop or registration change.
 foreach($name in ($Config.backends.Keys|Sort-Object)){
  $b=$Config.backends[$name];$old=Get-CimInstance Win32_Service -Filter "Name='$($b.service)'" -ErrorAction Stop
  $expected=[IO.Path]::ChangeExtension($b.service_config,'.exe');$command=New-AiNativeServiceCommand $PwshPath $Directory $b.service_config
  if($null -ne $old -and -not(Test-AiOwnedServiceCommand $old.PathName $PwshPath $Directory $b.service_config)){throw "Service $($b.service) belongs to another program"}
  $existing[$name]=$old
 }
 foreach($old in $existing.Values){if($null -ne $old -and $old.State -ne 'Stopped'){$process=Get-Process -Id $old.ProcessId -ErrorAction SilentlyContinue;Stop-Service $old.Name -NoWait -ErrorAction Stop;(Get-Service $old.Name).WaitForStatus('Stopped',[TimeSpan]::FromSeconds(30));if($null -ne $process -and -not $process.WaitForExit(15000)){throw 'Service host process did not exit.'}}}
 $executable=Join-Path $Directory 'src/Service.Host.exe'
 if(Test-Path $executable){Copy-Item -LiteralPath $executable -Destination (Join-Path $backup 'Service.Host.exe');Remove-Item -LiteralPath $executable -Force}
 try{
  if($ValidatedRuntimePath){Copy-Item -LiteralPath $ValidatedRuntimePath -Destination $executable -Force}
  else{& $PwshPath -NoLogo -NoProfile -File (Join-Path $Directory 'scripts/build-service.ps1');if($LASTEXITCODE -ne 0 -or -not (Test-Path -LiteralPath $executable)){throw 'Cannot compile native service runtime.'};Test-AiNativeServiceRuntime $Directory $PwshPath}
 }catch{if(Test-Path (Join-Path $backup 'Service.Host.exe')){Copy-Item (Join-Path $backup 'Service.Host.exe') $executable -Force};throw}
 try{
 foreach($name in ($Config.backends.Keys|Sort-Object)){
  $b=$Config.backends[$name];$old=Get-CimInstance Win32_Service -Filter "Name='$($b.service)'" -ErrorAction Stop
  $expected=[IO.Path]::ChangeExtension($b.service_config,'.exe')
  $command=New-AiNativeServiceCommand $PwshPath $Directory $b.service_config
  if($null -ne $old -and -not(Test-AiOwnedServiceCommand $old.PathName $PwshPath $Directory $b.service_config)){throw "Service $($b.service) belongs to another program"}
  if($null -ne $old -and $old.State -ne 'Stopped'){Stop-Service $b.service -NoWait -ErrorAction Stop;(Get-Service $b.service).WaitForStatus('Stopped',[TimeSpan]::FromSeconds(30))}
  Copy-Item -LiteralPath $b.service_config -Destination (Join-Path $backup "$name.xml")
  if($null -ne $old -and $old.PathName.Trim().Trim('"') -ieq $expected -and (Test-Path -LiteralPath $expected)){Copy-Item -LiteralPath $expected -Destination (Join-Path $backup "$name-winsw.exe");$legacyWrappers+=$expected}
  $changes+=@{service=$b.service;old=$old;command=$command}
  if($null -eq $old){$null=New-Service -Name $b.service -BinaryPathName $command -StartupType Manual -DisplayName "OmniBox AI · $name"}
  else{Set-AiServiceCommand -Name $b.service -Command $command -StartupType Manual}
  $b.runtime_path=$b.service_config+'.runtime.json'
 }
 }catch{
  foreach($change in $changes){try{if($null -ne $change.old){$mode=if($change.old.StartMode -eq 'Auto'){'Automatic'}else{$change.old.StartMode};Set-AiServiceCommand -Name $change.service -Command $change.old.PathName -StartupType $mode}else{Remove-Service $change.service -ErrorAction Stop}}catch{Write-Warning "Cannot restore service $($change.service): $_"}}
  if(Test-Path -LiteralPath (Join-Path $backup 'Service.Host.exe')){Copy-Item -LiteralPath (Join-Path $backup 'Service.Host.exe') -Destination $executable -Force}
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
 $x=[xml]'<service><id/><executable/><arguments>-NoLogo -NoProfile -NonInteractive -Command "while ($true) { Start-Sleep -Seconds 60 }"</arguments><workingdirectory/><logpath/></service>'
 $x.service.id=$name;$x.service.executable=$PwshPath;$x.service.workingdirectory=$Directory;$x.service.logpath=Join-Path $Directory 'logs';$x.Save($profile)
 $ready=$profile+'.ready'
 $probeCode="[IO.File]::WriteAllText('"+$ready.Replace("'","''")+"','ready'); while (`$true) { Start-Sleep -Seconds 60 }"
 $x.service.arguments='-NoLogo -NoProfile -NonInteractive -EncodedCommand '+[Convert]::ToBase64String([Text.Encoding]::Unicode.GetBytes($probeCode));$x.Save($profile)
 $pidToCheck=$null
 try{
  $null=New-Service -Name $name -BinaryPathName (New-AiNativeServiceCommand $PwshPath $Directory $profile) -StartupType Manual
  # Exercise the same Windows API used to migrate existing services before app changes.
  Set-AiServiceCommand -Name $name -Command (New-AiNativeServiceCommand $PwshPath $Directory $profile) -StartupType Manual
  Start-Service $name -ErrorAction Stop;(Get-Service $name).WaitForStatus('Running',[TimeSpan]::FromSeconds(15))
  $runtime=Get-Content -LiteralPath ($profile+'.runtime.json') -Raw|ConvertFrom-Json;$pidToCheck=$runtime.pid
  $deadline=[DateTime]::UtcNow.AddSeconds(10)
  while(-not(Test-Path -LiteralPath $ready) -and [DateTime]::UtcNow -lt $deadline){Start-Sleep -Milliseconds 100}
  if(-not(Test-Path -LiteralPath $ready)){throw 'SCM connected, but the PowerShell probe did not execute.'}
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
   $details+=@($events|Where-Object {$_.Message -like "*$name*" -or ($_.Properties.Value -join ' ') -like "*$name*"}|Select-Object -First 3|ForEach-Object{if($_.Message){$_.Message}else{$_.Properties.Value -join ' | '}})
  }
  throw ('Проверка Windows service runtime не пройдена: '+$cause+"`n"+($details -join "`n")+$(if(Test-Path -LiteralPath (Join-Path $Directory 'logs')){"`nЖурналы: "+(Join-Path $Directory 'logs')}else{"`nФайлы журналов не созданы. Команда запуска: "+(New-AiNativeServiceCommand $PwshPath $Directory $profile)}))
 }finally{
  $s=Get-Service $name -ErrorAction SilentlyContinue;if($null -ne $s){if($s.Status -ne 'Stopped'){Stop-Service $name -ErrorAction SilentlyContinue};Remove-Service $name -ErrorAction SilentlyContinue}
  Remove-Item -LiteralPath $profile,($profile+'.runtime.json'),$ready -Force -ErrorAction SilentlyContinue
 }
}
Export-ModuleMember -Function New-AiNativeServiceCommand,Install-AiNativeServices,Test-AiNativeServiceRuntime

function New-AiCheckedServiceRuntime {
 param([string]$SourceDirectory,[string]$PwshPath)
 $stage=Join-Path ([IO.Path]::GetTempPath()) ('omnibox-ai-runtime-'+[Guid]::NewGuid().ToString('N'))
 $null=New-Item -ItemType Directory -Path (Join-Path $stage 'src'),(Join-Path $stage 'scripts'),(Join-Path $stage 'logs') -Force
 Copy-Item -LiteralPath (Join-Path $SourceDirectory 'src/Service.Host.cs') -Destination (Join-Path $stage 'src')
 Copy-Item -LiteralPath (Join-Path $SourceDirectory 'scripts/build-service.ps1') -Destination (Join-Path $stage 'scripts')
 try {
  & $PwshPath -NoLogo -NoProfile -File (Join-Path $stage 'scripts/build-service.ps1')
  if($LASTEXITCODE -ne 0 -or -not(Test-Path -LiteralPath (Join-Path $stage 'src/Service.Host.exe'))){throw 'Cannot compile native service runtime.'}
  Test-AiNativeServiceRuntime $stage $PwshPath
  return $stage
 }catch{
  # Keep this isolated failure report; the existing installation has not been changed.
  throw ('Проверка нового runtime остановила установку до изменения приложения. '+$_.Exception.Message+"`nДиагностика: "+$stage)
 }
}
Export-ModuleMember -Function Set-AiServiceCommand,New-AiNativeServiceCommand,New-AiLegacyServiceCommand,Test-AiOwnedServiceCommand,Install-AiNativeServices,Test-AiNativeServiceRuntime,New-AiCheckedServiceRuntime
