#requires -Version 7.4
# Runs the actual installer body in a disposable copy. Only platform/admin bootstrap
# and Windows OS providers are replaced; module imports and installer flow are real.
$ErrorActionPreference='Stop'
$repo=Split-Path $PSScriptRoot -Parent
$fixture=Join-Path ([IO.Path]::GetTempPath()) ('omnibox-install-flow-'+[Guid]::NewGuid().ToString('N'))
$source=Join-Path $fixture 'source';$destination=Join-Path $fixture 'installed'
$oldUserProfile=$env:USERPROFILE;$oldPath=$env:Path;$oldUsername=$env:USERNAME;$oldUserPath=[Environment]::GetEnvironmentVariable("Path","User")
$global:FlowFailProbe=$false;$global:FlowServices=@{};$global:FlowCalls=[Collections.Generic.List[string]]::new()
function Assert($Value,[string]$Message){if(-not $Value){throw $Message}}
function global:Get-Command {
 param($Name,$CommandType,$ErrorAction)
 if($Name -eq 'pwsh.exe'){return [pscustomobject]@{Source=(Join-Path $PSHOME $(if($IsWindows){'pwsh.exe'}else{'pwsh'}));CommandType='Application'}}
 if($Name -in @('ai','gh')){return $null}
 Microsoft.PowerShell.Core\Get-Command @PSBoundParameters
}
function global:Get-Process {param($Id,$ErrorAction)if($Id -eq 0){return $null};Microsoft.PowerShell.Management\Get-Process @PSBoundParameters}
function global:Get-Service {param($Name,$ErrorAction)return $global:FlowServices[$Name]}
function global:Get-CimInstance {param($ClassName,$Filter,$ErrorAction)if($Filter -match "Name='([^']+)'"){return $global:FlowServices[$Matches[1]]}}
function global:New-Service {
 param($Name,$BinaryPathName,$StartupType,$DisplayName)
 $s=[pscustomobject]@{Name=$Name;PathName=$BinaryPathName;Status='Stopped';State='Stopped';StartType=$StartupType;ProcessId=0}
 $s|Add-Member ScriptMethod WaitForStatus {param($Status,$Timeout)if($this.Status -ne [string]$Status){throw 'Fixture service not in expected state'}}
 $global:FlowServices[$Name]=$s;$global:FlowCalls.Add('new:'+ $Name)
 return $s
}
function global:Set-Service {param($Name,$BinaryPathName,$StartupType,$ErrorAction)
 $s=$global:FlowServices[$Name];if($BinaryPathName){$s.PathName=$BinaryPathName};if($StartupType){$s.StartType=$StartupType}
 $global:FlowCalls.Add('set:'+ $Name)
}
function global:Start-Service {param($Name,$ErrorAction)
 if($global:FlowFailProbe -and $Name -like 'omnibox-ai-probe-*'){
  $profile=[regex]::Match($global:FlowServices[$Name].PathName,'-Profile "([^"]+)"$').Groups[1].Value
  $logDir=Join-Path (Split-Path $profile -Parent) 'logs';$null=New-Item -ItemType Directory -Path $logDir -Force
  Set-Content (Join-Path $logDir ($Name+'.bootstrap.log')) 'Fixture bootstrap diagnostic: access denied'
  throw 'Fixture SCM startup failure'
 }
 $s=$global:FlowServices[$Name];$s.Status='Running';$s.State='Running';$global:FlowCalls.Add('start:'+ $Name)
 if($s.PathName -match '-Profile "([^"]+)"$'){Set-Content -LiteralPath ($Matches[1]+'.runtime.json') -Value '{"pid":null}'}
}
function global:Stop-Service {param($Name,$ErrorAction,[switch]$NoWait)$s=$global:FlowServices[$Name];$s.Status='Stopped';$s.State='Stopped';$global:FlowCalls.Add('stop:'+ $Name)}
function global:Remove-Service {param($Name,$ErrorAction)$global:FlowServices.Remove($Name);$global:FlowCalls.Add('remove:'+ $Name)}
function global:Unblock-File {param($LiteralPath)}
function global:Get-NetTCPConnection {param($LocalPort,$State,$ErrorAction)return $null}
function global:Get-NetFirewallRule {param($Name,$ErrorAction)return $null}
function global:Remove-NetFirewallRule {param($Name,$ErrorAction)}
function global:New-NetFirewallRule {param($Name,$DisplayName,$Direction,$Action,$Enabled,$Protocol,$LocalPort,$Profile,$RemoteAddress)$global:FlowCalls.Add('firewall:'+ $Name)}
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
 Assert (Test-Path (Join-Path $destination 'src/Service.Host.dll')) 'Runtime not built'
 Assert ($global:FlowCalls.Contains('firewall:omnibox-ai-panel')) 'Firewall boundary not reached'
 $previousPanel=Get-Content (Join-Path $destination 'panel.json') -Raw|ConvertFrom-Json -AsHashtable
 $previousGateway=Get-Content (Join-Path $destination 'gateway.json') -Raw|ConvertFrom-Json -AsHashtable
 # A running fake panel has PID 0; the process provider supplies no real host.
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
 Write-Host 'PASS real installer body completes first installation and upgrade with mocked Windows providers'
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
 $bootstrap=Join-Path $fixture 'bootstrap';$null=New-Item -ItemType Directory -Path $bootstrap
 Copy-Item (Join-Path $repo 'service.ps1') $bootstrap
 $bootstrapProfile=Join-Path $bootstrap 'fixture.xml';Set-Content $bootstrapProfile '<service/>'
 $bootstrapFailed=$false
 try{& (Join-Path $bootstrap 'service.ps1') -Profile $bootstrapProfile}catch{$bootstrapFailed=$true}
 Assert $bootstrapFailed 'Missing assembly did not fail'
 Assert (Test-Path (Join-Path $bootstrap 'logs/fixture.bootstrap.log')) 'Early service error was not logged'
 $script:reconfigurePrompts=0
 $null=Get-AiSetupOptions -InstallDirectory $destination -Reconfigure -ConfigLoader {param($d)Get-Content (Join-Path $d 'config.json') -Raw|ConvertFrom-Json -AsHashtable} -ContextReader {param($b)'196608'} -ReadAnswer {param($prompt)$script:reconfigurePrompts++;''}
 Assert ($script:reconfigurePrompts -eq 4) 'Explicit reconfigure did not open setup prompts'
 Write-Host 'PASS early assembly failures are logged and explicit reconfigure opens the wizard'


}finally{
 foreach($name in @('Get-Command','Get-Process','Get-Service','Get-CimInstance','New-Service','Set-Service','Start-Service','Stop-Service','Remove-Service','Get-NetTCPConnection','Get-NetFirewallRule','Remove-NetFirewallRule','New-NetFirewallRule','Invoke-WebRequest','Unblock-File','Read-Host')){Remove-Item ('Function:global:'+ $name) -ErrorAction SilentlyContinue}
 $env:USERPROFILE=$oldUserProfile;$env:Path=$oldPath;$env:USERNAME=$oldUsername
 if($IsWindows){[Environment]::SetEnvironmentVariable("Path",$oldUserPath,"User")}
 Remove-Variable FlowServices,FlowCalls,FlowFailProbe -Scope Global -ErrorAction SilentlyContinue
 if(Test-Path $fixture){Remove-Item -LiteralPath $fixture -Recurse -Force}
}
