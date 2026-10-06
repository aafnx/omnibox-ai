#requires -Version 7.4
# Runs the actual installer body in a disposable copy. Only platform/admin bootstrap
# and Windows OS providers are replaced; module imports and installer flow are real.
$ErrorActionPreference='Stop'
$repo=Split-Path $PSScriptRoot -Parent
$fixture=Join-Path ([IO.Path]::GetTempPath()) ('omnibox-install-flow-'+[Guid]::NewGuid().ToString('N'))
$source=Join-Path $fixture 'source';$destination=Join-Path $fixture 'installed'
$oldUserProfile=$env:USERPROFILE;$oldPath=$env:Path;$oldUsername=$env:USERNAME;$oldUserPath=[Environment]::GetEnvironmentVariable("Path","User")
$global:FlowFailChange=$null;$global:FlowFailProbe=$false;$global:FlowFailFirewall=$false;$global:FlowServices=@{};$global:FlowCalls=[Collections.Generic.List[string]]::new()
function Assert($Value,[string]$Message){if(-not $Value){throw $Message}}
function global:Get-Command {
 param($Name,$CommandType,$ErrorAction)
 if($Name -eq 'pwsh.exe'){return [pscustomobject]@{Source=(Join-Path $PSHOME $(if($IsWindows){'pwsh.exe'}else{'pwsh'}));CommandType='Application'}}
 if($Name -in @('ai','gh')){return $null}
 Microsoft.PowerShell.Core\Get-Command @PSBoundParameters
}
function global:Get-Process {param($Id,$ErrorAction)if($Id -eq 0){return $null};Microsoft.PowerShell.Management\Get-Process @PSBoundParameters}
function global:Get-Service {param($Name,$ErrorAction)return $global:FlowServices[$Name]}
function global:Get-CimInstance {param($ClassName,$Filter,$ErrorAction)if($Filter -match "Name='([^']+)'"){$s=$global:FlowServices[$Matches[1]];if($null -ne $s){return $s.PSObject.Copy()}}}
function global:New-Service {
 param($Name,$BinaryPathName,$StartupType,$DisplayName)
 $s=[pscustomobject]@{Name=$Name;PathName=$BinaryPathName;Status='Stopped';State='Stopped';StartType=$StartupType;StartMode=$(if($StartupType -eq 'Automatic'){'Auto'}else{$StartupType});ProcessId=0}
 $s|Add-Member ScriptMethod WaitForStatus {param($Status,$Timeout)if($this.Status -ne [string]$Status){throw 'Fixture service not in expected state'}}
 $global:FlowServices[$Name]=$s;$global:FlowCalls.Add('new:'+ $Name)
 return $s
}
function global:Set-Service {
 [CmdletBinding()]param($Name,$StartupType)
 $s=$global:FlowServices[$Name];if($StartupType){$s.StartType=$StartupType;$s.StartMode=if($StartupType -eq 'Automatic'){'Auto'}else{$StartupType}}
 $global:FlowCalls.Add('set:'+ $Name)
}
function global:Invoke-CimMethod {
 [CmdletBinding()]param($InputObject,$MethodName,$Arguments,$OperationTimeoutSec)
 if($MethodName -ne 'Change'){throw 'Unexpected CIM method'}
 if($global:FlowFailChange -eq $InputObject.Name){$global:FlowFailChange=$null;return [pscustomobject]@{ReturnValue=2}}
 $s=$global:FlowServices[$InputObject.Name];$s.PathName=$Arguments.PathName
 if($Arguments.ContainsKey('StartMode')){$s.StartType=$Arguments.StartMode;$s.StartMode=if($Arguments.StartMode -eq 'Automatic'){'Auto'}else{$Arguments.StartMode}}
 $global:FlowCalls.Add('change:'+ $InputObject.Name)
 return [pscustomobject]@{ReturnValue=0}
}
function global:Start-Service {param($Name,$ErrorAction)
 if($global:FlowFailProbe -and $Name -like 'omnibox-ai-probe-*'){
  $profile=[regex]::Match($global:FlowServices[$Name].PathName,'-Profile "([^"]+)"$').Groups[1].Value
  $logDir=Join-Path (Split-Path $profile -Parent) 'logs';$null=New-Item -ItemType Directory -Path $logDir -Force
  Set-Content (Join-Path $logDir ($Name+'.bootstrap.log')) 'Fixture bootstrap diagnostic: access denied'
  throw 'Fixture SCM startup failure'
 }
 $s=$global:FlowServices[$Name];$s.Status='Running';$s.State='Running';$global:FlowCalls.Add('start:'+ $Name)
 if($s.PathName -match '-Profile "([^"]+)"$'){Set-Content -LiteralPath ($Matches[1]+'.runtime.json') -Value '{"pid":null}';Set-Content -LiteralPath ($Matches[1]+'.ready') -Value 'ready'}
}
function global:Stop-Service {param($Name,$ErrorAction,[switch]$NoWait)$s=$global:FlowServices[$Name];$s.Status='Stopped';$s.State='Stopped';$global:FlowCalls.Add('stop:'+ $Name)}
function global:Remove-Service {param($Name,$ErrorAction)$global:FlowServices.Remove($Name);$global:FlowCalls.Add('remove:'+ $Name)}
function global:Unblock-File {param($LiteralPath)}
function global:Get-NetTCPConnection {param($LocalPort,$State,$ErrorAction)return $null}
function global:Get-NetFirewallRule {param($Name,$ErrorAction)return $null}
function global:Remove-NetFirewallRule {param($Name,$ErrorAction)}
function global:New-NetFirewallRule {param($Name,$DisplayName,$Direction,$Action,$Enabled,$Protocol,$LocalPort,$Profile,$RemoteAddress)if($global:FlowFailFirewall){throw 'Fixture firewall provider failure'};$global:FlowCalls.Add('firewall:'+ $Name)}
function global:Invoke-WebRequest {param($Uri,$TimeoutSec)if($Uri -notlike 'http://127.0.0.1:*'){throw 'Unexpected fixture request'};return @{StatusCode=200}}
try{
 $null=New-Item -ItemType Directory -Path $fixture
 Copy-Item -LiteralPath $repo -Destination $source -Recurse
 $env:USERNAME='fixture-user';$env:USERPROFILE=Join-Path $fixture 'profile';$null=New-Item -ItemType Directory -Path $env:USERPROFILE
 if(-not $env:Path){$env:Path=$env:PATH}
 $text=Get-Content (Join-Path $source 'install.ps1') -Raw
 $ast=[Management.Automation.Language.Parser]::ParseInput($text,[ref]$null,[ref]$null)
 $start=$text.IndexOf("Import-Module (Join-Path `$PSScriptRoot 'src/Setup.Helpers.psm1')")
 # Keep the real parameter block and every installer statement after bootstrap.
 $testScript=$text.Substring(0,$ast.ParamBlock.Extent.EndOffset)+"`n`$ErrorActionPreference='Stop'`n"+$text.Substring($start)
 Set-Content (Join-Path $source 'install.ps1') $testScript
 # ACL invokes WindowsIdentity directly: replace this OS boundary in fixture only.
 Add-Content (Join-Path $source 'src/Panel.Install.psm1') "`nfunction Protect-AiPanelDirectory { param(`$Directory) }"
 $config=Get-Content (Join-Path $source 'config.example.json') -Raw|ConvertFrom-Json -AsHashtable
 foreach($name in $config.backends.Keys){
  $profile=Join-Path $fixture ($name+'.xml');$config.backends[$name].service_config=$profile
  $config.backends[$name].log_directory=Join-Path $fixture 'logs'
  if($name -eq 'bonsai'){Set-Content $profile '<service><id>bonsai</id><arguments>-c 196608</arguments></service>'}
  else{Set-Content $profile '<service><id>ollama</id><env name="OLLAMA_CONTEXT_LENGTH" value="32768"/></service>'}
  $wrapper=[IO.Path]::ChangeExtension($profile,'.exe');Set-Content $wrapper 'test wrapper'
  $null=New-Service $name $wrapper 'Manual'
 }
 $config|ConvertTo-Json -Depth 12|Set-Content (Join-Path $source 'config.example.json')
 # Existing caller exports must survive the panel module's dependency imports.
 Import-Module (Join-Path $source 'src/Setup.Helpers.psm1') -Force
 Import-Module (Join-Path $source 'src/Service.Install.psm1') -Force
 Import-Module (Join-Path $source 'src/Panel.Install.psm1') -Force
 Assert ([bool](Microsoft.PowerShell.Core\Get-Command New-AiNativeServiceCommand -ErrorAction SilentlyContinue)) 'Native command export disappeared'
 Assert ([bool](Microsoft.PowerShell.Core\Get-Command Get-AiSetupOptions -ErrorAction SilentlyContinue)) 'Setup command export disappeared'
 $null=New-AiNativeServiceCommand (Join-Path $PSHOME 'pwsh') $destination (Join-Path $destination 'panel.xml')
 Write-Host 'PASS installer import sequence preserves setup and native service commands'
 & (Join-Path $source 'install.ps1') -InstallDirectory $destination -NonInteractive -NoProfileUpdate -PanelHost '192.168.1.77'
 Assert ((Get-Service 'omnibox-ai').Status -eq 'Running') 'Panel not started'
 Assert ((Get-Service 'bonsai').Status -eq 'Stopped') 'Bonsai unexpectedly started'
 Assert ((Get-Service 'ollama').Status -eq 'Stopped') 'Ollama unexpectedly started'
 Assert (Test-Path (Join-Path $destination 'src/Service.Host.exe')) 'Runtime not built'
 Assert ($global:FlowCalls.Contains('firewall:omnibox-ai-panel')) 'Firewall boundary not reached'
 $previousPanel=Get-Content (Join-Path $destination 'panel.json') -Raw|ConvertFrom-Json -AsHashtable
 $previousGateway=Get-Content (Join-Path $destination 'gateway.json') -Raw|ConvertFrom-Json -AsHashtable
 # A running fake panel has PID 0; the process provider supplies no real host.
 (Get-Service 'omnibox-ai').PathName=New-AiLegacyServiceCommand (Join-Path $PSHOME $(if($IsWindows){'pwsh.exe'}else{'pwsh'})) $destination (Join-Path $destination 'panel-service.xml')
 (Get-Service 'bonsai').PathName=New-AiLegacyServiceCommand (Join-Path $PSHOME $(if($IsWindows){'pwsh.exe'}else{'pwsh'})) $destination $config.backends.bonsai.service_config
 $previousPanel.allowed_hosts+=@('custom.example');$previousPanel|ConvertTo-Json -Depth 12|Set-Content (Join-Path $destination 'panel.json')
 function global:Read-Host {throw 'Upgrade unexpectedly prompted'}
 & (Join-Path $source 'install.ps1') -InstallDirectory $destination -NoProfileUpdate
 $panel=Get-Content (Join-Path $destination 'panel.json') -Raw|ConvertFrom-Json -AsHashtable
 $gateway=Get-Content (Join-Path $destination 'gateway.json') -Raw|ConvertFrom-Json -AsHashtable
 Assert ('custom.example' -in $panel.allowed_hosts) 'Upgrade dropped allowed host'
 Assert ($panel.credential.hash -ceq $previousPanel.credential.hash) 'Upgrade changed password'
 Assert ($gateway.key_hash -ceq $previousGateway.key_hash) 'Upgrade changed API key'
 Assert ($gateway.base_url -ceq $previousGateway.base_url) 'Upgrade changed endpoint'
 Assert ((Get-Service 'omnibox-ai').Status -eq 'Running') 'Upgraded panel not started'
 Assert ((Get-Service 'omnibox-ai').PathName -match 'src[\\/]Service\.Host\.exe') 'Legacy panel was not migrated'
 Assert ((Get-Service 'bonsai').PathName -match 'src[\\/]Service\.Host\.exe') 'Legacy backend was not migrated'
 Write-Host 'PASS real installer body completes first installation and upgrade with mocked Windows providers'
 # --- U1: every failure after the snapshot restores the previous installation ---
 Import-Module (Join-Path $source 'src/Install.Transaction.psm1') -Force
 $pwshPath=Join-Path $PSHOME $(if($IsWindows){'pwsh.exe'}else{'pwsh'})
 # Make the installed copy distinguishable from the package and give it model history and gh access.
 Set-Content -LiteralPath (Join-Path $destination 'VERSION') -Value '0.9.0'
 Set-Content -LiteralPath (Join-Path $destination '.omnibox-ai-install') -Value 'omnibox-ai 0.9.0'
 Add-Content -LiteralPath (Join-Path $destination 'src/Telemetry.ps1') -Value '# previous version'
 $bonsaiXml=$config.backends.bonsai.service_config
 Set-Content -LiteralPath ($bonsaiXml+'.ai-previous') -Value '<service><id>bonsai</id><arguments>-c 32768</arguments></service>'
 Set-Content -LiteralPath ($bonsaiXml+'.ai-state.json') -Value '{"schema_version":1,"pending":false}'
 $null=New-Item -ItemType Directory -Path (Join-Path $destination 'updates/auth') -Force
 Set-Content -LiteralPath (Join-Path $destination 'updates/auth/github.dpapi') -Value 'fixture encrypted credential'
 function Get-InstallState {
  $state=[ordered]@{}
  foreach($item in @(Get-ChildItem -LiteralPath $destination -Recurse -File -Force|Where-Object {$_.FullName -notmatch '[\\/](backups|logs)[\\/]' -and $_.Name -notmatch '\.(runtime\.json|ready)$'})){$state[$item.FullName.Substring($destination.Length)]=(Get-FileHash -LiteralPath $item.FullName).Hash}
  foreach($path in @($bonsaiXml,($bonsaiXml+'.ai-previous'),($bonsaiXml+'.ai-state.json'),$config.backends.ollama.service_config)){$state[$path]=(Get-FileHash -LiteralPath $path).Hash}
  foreach($name in @('omnibox-ai','bonsai','ollama')){$s=Get-Service $name;$state['service:'+$name]="$($s.PathName)|$($s.StartMode)|$($s.Status)"}
  return $state
 }
 function Assert-SameState($Before,[string]$Label){
  $after=Get-InstallState
  foreach($key in @(@($Before.Keys)+@($after.Keys)|Select-Object -Unique)){Assert ($Before[$key] -ceq $after[$key]) "$Label changed $key"}
 }
 $marker=Join-Path $destination '.omnibox-ai-transaction.json'
 $before=Get-InstallState
 Assert ((Get-Service 'omnibox-ai').Status -eq 'Running') 'Fixture panel must run before failure tests'
 $failures=[ordered]@{}
 foreach($step in @('files','services','config','verify','panel','panel-started')){$failures["step $step"]=[scriptblock]::Create("`$env:OMNIBOX_AI_INSTALL_FAIL_STEP='$step'")}
 $failures['firewall error after panel.json/gateway.json were written']={$global:FlowFailFirewall=$true}
 foreach($name in $failures.Keys){
  & $failures[$name]
  $failed=$null
  try{& (Join-Path $source 'install.ps1') -InstallDirectory $destination -NoProfileUpdate}catch{$failed=$_.Exception.Message}
  finally{$env:OMNIBOX_AI_INSTALL_FAIL_STEP=$null;$global:FlowFailFirewall=$false}
  Assert ($failed -like '*восстановлены*') "$name did not report restoration: $failed"
  Assert ($global:OmniBoxInstallResult.status -eq 'rolled-back') "$name result is $($global:OmniBoxInstallResult.status)"
  Assert (-not (Test-Path -LiteralPath $marker)) "$name left an unfinished journal"
  Assert ((Get-Content -LiteralPath (Join-Path $destination 'VERSION') -Raw).Trim() -eq '0.9.0') "$name kept the new version"
  Assert-SameState $before $name
 }
 Write-Host 'PASS failure at every installer step and inside panel setup restores files, settings, history, gh access and services; panel runs'

 # A second installer (another process) is refused before any change.
 $ready=Join-Path $fixture 'install-lock-ready'
 $holder=Start-Process -FilePath $pwshPath -ArgumentList @('-NoLogo','-NoProfile','-Command',"`$m=[Threading.Mutex]::new(`$false,'Global\omnibox-ai-install-v1');`$null=`$m.WaitOne();Set-Content '$ready' x;Start-Sleep 60") -PassThru
 try{
  $deadline=[DateTime]::UtcNow.AddSeconds(30);while(-not(Test-Path $ready) -and [DateTime]::UtcNow -lt $deadline){Start-Sleep -Milliseconds 100}
  $refused=$null;try{& (Join-Path $source 'install.ps1') -InstallDirectory $destination -NoProfileUpdate}catch{$refused=$_.Exception.Message}
  Assert ($refused -like '*Уже выполняется другая установка*') "Concurrent installer not refused: $refused"
  Assert ($global:OmniBoxInstallResult.status -eq 'not-started') 'Refused installer reported changes'
  Assert-SameState $before 'refused concurrent installer'
 }finally{$holder.Kill();$holder.WaitForExit()}
 Write-Host 'PASS concurrent installation is refused without changes'
 # A snapshot that cannot be written changes nothing and restarts the panel.
 $panelFile=Join-Path $destination 'panel.json';$panelHash=(Get-FileHash $panelFile).Hash
 $backupsDir=Join-Path $destination 'backups';$parked=Join-Path $fixture 'parked-backups'
 Move-Item -LiteralPath $backupsDir -Destination $parked;Set-Content -LiteralPath $backupsDir -Value 'a file where the snapshot directory should be'
 $failed=$null
 try{& (Join-Path $source 'install.ps1') -InstallDirectory $destination -NoProfileUpdate}catch{$failed=$_.Exception.Message}
 finally{Remove-Item -LiteralPath $backupsDir -Force;Move-Item -LiteralPath $parked -Destination $backupsDir}
 Assert ($failed -like '*установка не начата*') "Snapshot failure not reported: $failed"
 Assert ($global:OmniBoxInstallResult.status -eq 'not-started') "Unexpected result $($global:OmniBoxInstallResult.status)"
 Assert ((Get-Service 'omnibox-ai').Status -eq 'Running') 'Panel left stopped after a refused installation'
 Assert ((Get-FileHash $panelFile).Hash -eq $panelHash) 'Refused installation changed panel.json'
 Write-Host 'PASS installation without a snapshot changes nothing and leaves the panel running'

 # An installer process that died midway leaves a running journal; the next run restores first.
 $profiles=[ordered]@{'omnibox-ai'=(Join-Path $destination 'panel-service.xml')};$external=@()
 foreach($name in $config.backends.Keys){$profiles[$name]=$config.backends[$name].service_config;$external+=@($config.backends[$name].service_config,($config.backends[$name].service_config+'.ai-previous'),($config.backends[$name].service_config+'.ai-state.json'))}
 $manifest=Import-PowerShellDataFile (Join-Path $source 'package.psd1')
 $crashed=New-AiInstallTransaction -Directory $destination -Files (@($manifest.Files)+@('config.json','panel.json','gateway.json','panel-service.xml','.omnibox-ai-install','src/Service.Host.exe','panel-service.exe')) -ServiceProfiles $profiles -ExternalFiles $external -PanelWasRunning $true -Version 'crashed'
 Add-AiInstallStep $crashed 'files'
 Set-Content -LiteralPath (Join-Path $destination 'VERSION') -Value 'half-copied'
 Set-Content -LiteralPath (Join-Path $destination 'panel.json') -Value '{"truncated":'
 Set-Content -LiteralPath ($bonsaiXml+'.ai-state.json') -Value 'damaged'
 Stop-Service 'omnibox-ai';(Get-Service 'bonsai').PathName='"C:\half\migrated.exe"'
 Assert (Test-Path -LiteralPath $marker) 'Crash journal missing'
 & (Join-Path $source 'install.ps1') -InstallDirectory $destination -NoProfileUpdate
 Assert ($global:OmniBoxInstallResult.status -eq 'installed') 'Installer after crash did not finish'
 Assert (-not (Test-Path -LiteralPath $marker)) 'Crash journal remained'
 $panelAfter=Get-Content (Join-Path $destination 'panel.json') -Raw|ConvertFrom-Json -AsHashtable
 Assert ($panelAfter.credential.hash -ceq $previousPanel.credential.hash -and 'custom.example' -in $panelAfter.allowed_hosts) 'Crash recovery lost panel settings'
 Assert ((Get-FileHash ($bonsaiXml+'.ai-state.json')).Hash -eq $before[$bonsaiXml+'.ai-state.json']) 'Crash recovery lost model history'
 Assert ((Get-Content -LiteralPath (Join-Path $destination 'VERSION') -Raw).Trim() -eq (Get-Content (Join-Path $source 'VERSION') -Raw).Trim()) 'New version not installed after recovery'
 Assert ((Get-Service 'bonsai').PathName -match 'src[\\/]Service\.Host\.exe') 'Backend command not migrated after recovery'
 Assert ((Get-Service 'omnibox-ai').Status -eq 'Running') 'Panel not running after recovery and installation'
 Write-Host 'PASS interrupted installation is rolled back before the next installation completes'

 # The web worker owns both locks and runs the installer in its own thread.
 $workerLock=Enter-AiInstallLock;Enter-AiControlLock $workerLock
 try{& (Join-Path $source 'install.ps1') -InstallDirectory $destination -NoProfileUpdate}finally{Exit-AiInstallLock $workerLock}
 Assert ($global:OmniBoxInstallResult.status -eq 'installed') 'Installer did not re-enter locks owned by its thread'
 Write-Host 'PASS installer re-enters install and control locks held by the web worker thread'
 # A nonzero Windows provider result midway through migration must restore detached snapshots.
 $migrationConfig=Get-Content (Join-Path $destination 'config.json') -Raw|ConvertFrom-Json -AsHashtable
 $oldCommands=@{}
 foreach($name in @('bonsai','ollama')){
  $svc=Get-Service $name
  $svc.PathName=New-AiLegacyServiceCommand (Join-Path $PSHOME 'pwsh') $destination $migrationConfig.backends[$name].service_config
  $oldCommands[$name]=$svc.PathName
 }
 $checkedExe=Join-Path $fixture 'checked.exe';Copy-Item (Join-Path $destination 'src/Service.Host.exe') $checkedExe
 $beforeCalls=$global:FlowCalls.Count;$global:FlowFailChange='ollama'
 $failed=$false
 try{Install-AiNativeServices $destination (Join-Path $PSHOME 'pwsh') $migrationConfig $checkedExe}catch{$failed=$_.Exception.Message -like '*Win32_Service.Change code 2*'}
 Assert $failed 'Nonzero CIM return code was ignored'
 foreach($name in @('bonsai','ollama')){Assert ((Get-Service $name).PathName -ceq $oldCommands[$name]) 'Failed migration did not restore original command'}
 Assert (-not (@($global:FlowCalls|Select-Object -Skip $beforeCalls)|Where-Object {$_ -like 'start:*'})) 'Failed migration started a model'
 Write-Host 'PASS failed second service change restores original commands without model start'

 # An SCM preflight failure must leave both running panel and installed source intact.
 $beforeHash=(Get-FileHash (Join-Path $destination 'service.ps1')).Hash
 $beforeCalls=$global:FlowCalls.Count;$global:FlowFailProbe=$true;$failedStage=$null
 try {
  & (Join-Path $source 'install.ps1') -InstallDirectory $destination -NoProfileUpdate
  throw 'Expected probe failure'
 }catch{
  Assert ($_.Exception.Message -like '*Fixture bootstrap diagnostic: access denied*') 'Probe detail missing'
  $failedStage=($_.Exception.Message -split 'Диагностика: ')[-1].Trim()
 }
 Assert ((Get-Service 'omnibox-ai').Status -eq 'Running') 'Failed preflight stopped panel'
 Assert ((Get-FileHash (Join-Path $destination 'service.ps1')).Hash -eq $beforeHash) 'Failed preflight changed installed source'
 Assert (-not (@($global:FlowCalls|Select-Object -Skip $beforeCalls) -contains 'stop:omnibox-ai')) 'Panel stopped before probe'
 Assert (Test-Path (Join-Path $failedStage 'logs')) 'Failed preflight lost diagnostics'
 Remove-Item -LiteralPath $failedStage -Recurse -Force
 Write-Host 'PASS failed SCM preflight preserves running installation and reports bootstrap detail'
 $script:reconfigurePrompts=0
 $null=Get-AiSetupOptions -InstallDirectory $destination -Reconfigure -ConfigLoader {param($d)Get-Content (Join-Path $d 'config.json') -Raw|ConvertFrom-Json -AsHashtable} -ContextReader {param($b)'196608'} -ReadAnswer {param($prompt)$script:reconfigurePrompts++;''}
 Assert ($script:reconfigurePrompts -eq 4) 'Explicit reconfigure did not open setup prompts'
 Write-Host 'PASS explicit reconfigure opens the wizard'


}finally{
 foreach($name in @('Get-Command','Get-Process','Get-Service','Get-CimInstance','Invoke-CimMethod','New-Service','Set-Service','Start-Service','Stop-Service','Remove-Service','Get-NetTCPConnection','Get-NetFirewallRule','Remove-NetFirewallRule','New-NetFirewallRule','Invoke-WebRequest','Unblock-File','Read-Host')){Remove-Item ('Function:global:'+ $name) -ErrorAction SilentlyContinue}
 $env:USERPROFILE=$oldUserProfile;$env:Path=$oldPath;$env:USERNAME=$oldUsername
 if($IsWindows){[Environment]::SetEnvironmentVariable("Path",$oldUserPath,"User")}
 $env:OMNIBOX_AI_INSTALL_FAIL_STEP=$null
 Remove-Variable FlowServices,FlowCalls,FlowFailProbe,FlowFailChange,FlowFailFirewall,OmniBoxInstallResult -Scope Global -ErrorAction SilentlyContinue
 if(Test-Path $fixture){Remove-Item -LiteralPath $fixture -Recurse -Force}
}
