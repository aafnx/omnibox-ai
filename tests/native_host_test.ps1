#requires -Version 7.4
$ErrorActionPreference='Stop'
$root=Split-Path $PSScriptRoot -Parent
$fixture=Join-Path ([IO.Path]::GetTempPath()) ('omnibox-native-test-'+[guid]::NewGuid().ToString('N'))
$previousEnv=$env:OMNIBOX_NATIVE_TEST
function Assert($value,[string]$message){if(-not $value){throw $message}}
try{
 $null=New-Item -ItemType Directory -Path (Join-Path $fixture 'src'),(Join-Path $fixture 'scripts') -Force
 Copy-Item (Join-Path $root 'src/Service.Host.cs') (Join-Path $fixture 'src')
 Copy-Item (Join-Path $root 'scripts/build-service.ps1') (Join-Path $fixture 'scripts')
 & (Join-Path $PSHOME $(if($IsWindows){'pwsh.exe'}else{'pwsh'})) -NoLogo -NoProfile -File (Join-Path $fixture 'scripts/build-service.ps1')
 Assert ($LASTEXITCODE -eq 0) 'Compiler failed'
 $exe=Join-Path $fixture 'src/Service.Host.exe'
 $assembly=[Reflection.Assembly]::Load([IO.File]::ReadAllBytes($exe))
 Assert ($null -ne $assembly.EntryPoint) 'Service binary lacks an executable entry point'
 Assert ($assembly.EntryPoint.ReturnType -eq [int]) 'Service binary has incorrect entry point'
 Write-Host 'PASS service source compiles as a standalone executable with an entry point'
 $profile=Join-Path $fixture 'model.xml';$env:OMNIBOX_NATIVE_TEST='fixture-value'
 Set-Content $profile '<service><id>fixture</id><executable>%BASE%/model.exe</executable><arguments>--text &quot;hello &amp; world&quot;</arguments><workingdirectory>%BASE%</workingdirectory><logpath>%BASE%/logs</logpath><env name="SETTING" value="%OMNIBOX_NATIVE_TEST%"/></service>'
 $result=[OmniBox.ServiceHost]::ReadProfile([string]$profile)
 Assert ($result.Id -eq 'fixture') 'Service ID lost'
 Assert ($result.Executable -eq ($fixture+'/model.exe')) 'Base path expansion wrong'
 Assert ($result.Arguments -eq '--text "hello & world"') 'Argument quoting/entity decoding lost'
 Assert ($result.Environment['SETTING'] -eq 'fixture-value') 'Environment expansion wrong'
 Assert ($result.DirectoryPath -eq $fixture) 'Working directory wrong'
 Write-Host 'PASS executable profile parser preserves quoted arguments, environment and paths'
 Set-Content $profile '<!DOCTYPE service [<!ENTITY injected "external">]><service><id>&injected;</id></service>'
 $rejected=$false;try{$null=[OmniBox.ServiceHost]::ReadProfile([string]$profile)}catch{$rejected=$true}
 Assert $rejected 'DTD unexpectedly accepted'
 Set-Content $profile '<service><id>fixture</id></service>'
 $rejected=$false;try{$null=[OmniBox.ServiceHost]::ReadProfile([string]$profile)}catch{$rejected=$true}
 Assert $rejected 'Incomplete profile unexpectedly accepted'
 Write-Host 'PASS executable profile parser rejects DTD and incomplete launch settings'
 if($IsWindows){
  $identity=[Security.Principal.WindowsIdentity]::GetCurrent()
  try{$admin=([Security.Principal.WindowsPrincipal]::new($identity)).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)}finally{$identity.Dispose()}
  if(-not $admin){throw 'Run native SCM smoke test in elevated PowerShell.'}
  Import-Module (Join-Path $root 'src/Service.Install.psm1') -Force
  Test-AiNativeServiceRuntime $fixture (Join-Path $PSHOME 'pwsh.exe')
  Write-Host 'PASS real Windows SCM starts/stops the native host and its probe child'
 }else{Write-Host 'SKIP real Windows SCM smoke test: Windows kernel is unavailable'}
}finally{
 $env:OMNIBOX_NATIVE_TEST=$previousEnv
 if(Test-Path $fixture){Remove-Item -LiteralPath $fixture -Recurse -Force}
}
