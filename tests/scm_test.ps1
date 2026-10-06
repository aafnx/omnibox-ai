#requires -Version 7.4
# Real Windows SCM integration: compiles Service.Host.exe, registers disposable services
# and checks start/stop, Win32_Service.Change, crash handling and process tree cleanup.
# Needs elevated PowerShell on Windows (GitHub windows-latest runners are elevated).
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$repo = Split-Path $PSScriptRoot -Parent
if (-not $IsWindows) { Write-Host 'SKIP real SCM tests: Windows kernel is unavailable'; exit 0 }
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
try { $admin = ([Security.Principal.WindowsPrincipal]::new($identity)).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator) } finally { $identity.Dispose() }
if (-not $admin) { throw 'Run real SCM tests in elevated PowerShell.' }

$script:passed = 0
function Assert($Condition, [string]$Message) { if (-not $Condition) { throw $Message } }
function Test([string]$Name, [scriptblock]$Action) { & $Action; $script:passed++; Write-Host "PASS $Name" }
function Wait-Until([scriptblock]$Condition, [int]$Seconds, [string]$Message) {
    $deadline = [DateTime]::UtcNow.AddSeconds($Seconds)
    while (-not (& $Condition)) {
        if ([DateTime]::UtcNow -gt $deadline) { throw $Message }
        Start-Sleep -Milliseconds 200
    }
}

$pwsh = Join-Path $PSHOME 'pwsh.exe'
$fixture = Join-Path ([IO.Path]::GetTempPath()) ('omnibox-scm-test-' + [Guid]::NewGuid().ToString('N'))
$services = [Collections.Generic.List[string]]::new()
Import-Module (Join-Path $repo 'src/Service.Install.psm1') -Force

function New-FixtureService {
    # The child starts a grandchild (as a model server with runners would), records both PIDs and waits.
    param([string]$Name)
    $profile = Join-Path $fixture ($Name + '.xml')
    $state = Join-Path $fixture ($Name + '.pids')
    $code = @"
`$g = Start-Process -FilePath '$($pwsh.Replace("'", "''"))' -ArgumentList '-NoLogo -NoProfile -NonInteractive -Command Start-Sleep -Seconds 600' -NoNewWindow -PassThru
[IO.File]::WriteAllText('$($state.Replace("'", "''"))', "`$PID `$(`$g.Id)")
while (`$true) { Start-Sleep -Seconds 60 }
"@
    $x = [xml]'<service><id/><executable/><arguments/><workingdirectory/><logpath/></service>'
    $x.service.id = $Name; $x.service.executable = $pwsh
    $x.service.arguments = '-NoLogo -NoProfile -NonInteractive -EncodedCommand ' + [Convert]::ToBase64String([Text.Encoding]::Unicode.GetBytes($code))
    $x.service.workingdirectory = $fixture; $x.service.logpath = Join-Path $fixture 'logs'; $x.Save($profile)
    $command = New-AiNativeServiceCommand $pwsh $fixture $profile
    $null = New-Service -Name $Name -BinaryPathName $command -StartupType Manual
    $services.Add($Name)
    return @{ name = $Name; profile = $profile; state = $state; command = $command }
}
function Get-FixturePids($Service) {
    Wait-Until { Test-Path -LiteralPath $Service.state } 20 "$($Service.name): child did not report its PIDs"
    $values = (Get-Content -LiteralPath $Service.state -Raw).Trim() -split ' '
    return @{ child = [int]$values[0]; grandchild = [int]$values[1] }
}
function Test-ProcessGone([int]$Id) { $null -eq (Get-Process -Id $Id -ErrorAction SilentlyContinue) }

try {
    $null = New-Item -ItemType Directory -Path (Join-Path $fixture 'src'), (Join-Path $fixture 'scripts'), (Join-Path $fixture 'logs') -Force
    Copy-Item (Join-Path $repo 'src/Service.Host.cs') (Join-Path $fixture 'src')
    Copy-Item (Join-Path $repo 'scripts/build-service.ps1') (Join-Path $fixture 'scripts')
    & $pwsh -NoLogo -NoProfile -File (Join-Path $fixture 'scripts/build-service.ps1')
    Assert ($LASTEXITCODE -eq 0 -and (Test-Path (Join-Path $fixture 'src/Service.Host.exe'))) 'Service.Host.exe was not built'
    $suffix = [Guid]::NewGuid().ToString('N').Substring(0, 8)

    Test 'installer preflight probe passes on a real SCM' {
        Test-AiNativeServiceRuntime $fixture $pwsh
    }

    $svc = New-FixtureService ('omnibox-ai-scmtest-' + $suffix)
    Test 'SCM starts the host and the child; runtime.json reports the child PID' {
        Start-Service $svc.name
        (Get-Service $svc.name).WaitForStatus('Running', [TimeSpan]::FromSeconds(20))
        $ids = Get-FixturePids $svc
        $runtime = Get-Content -LiteralPath ($svc.profile + '.runtime.json') -Raw | ConvertFrom-Json
        Assert ($runtime.running -eq $true) 'runtime.json does not report running'
        Assert ($runtime.pid -eq $ids.child) "runtime.json PID $($runtime.pid) differs from child $($ids.child)"
        Assert (-not (Test-ProcessGone $ids.grandchild)) 'Grandchild did not start'
    }

    Test 'Win32_Service.Change updates command and start mode with verification' {
        Set-AiServiceCommand -Name $svc.name -Command $svc.command -StartupType Disabled
        Assert ((Get-CimInstance Win32_Service -Filter "Name='$($svc.name)'").StartMode -eq 'Disabled') 'Start mode not changed'
        Set-AiServiceCommand -Name $svc.name -Command $svc.command -StartupType Manual
        $info = Get-CimInstance Win32_Service -Filter "Name='$($svc.name)'"
        Assert ($info.StartMode -eq 'Manual' -and $info.PathName -ceq $svc.command) 'Command or start mode not restored'
        $failed = $false
        try { Set-AiServiceCommand -Name ('omnibox-ai-missing-' + $suffix) -Command $svc.command } catch { $failed = $true }
        Assert $failed 'Change of a missing service did not fail'
    }

    Test 'SCM stop ends the child and its whole process tree' {
        $ids = Get-FixturePids $svc
        Stop-Service $svc.name
        (Get-Service $svc.name).WaitForStatus('Stopped', [TimeSpan]::FromSeconds(30))
        Wait-Until { (Test-ProcessGone $ids.child) -and (Test-ProcessGone $ids.grandchild) } 15 'Process tree survived service stop'
        $runtime = Get-Content -LiteralPath ($svc.profile + '.runtime.json') -Raw | ConvertFrom-Json
        Assert ($runtime.running -eq $false) 'runtime.json still reports running'
        Assert ((Get-CimInstance Win32_Service -Filter "Name='$($svc.name)'").ExitCode -eq 0) 'Requested stop reported an error'
    }

    Test 'child crash stops the service with an error and leaves no descendants' {
        Remove-Item -LiteralPath $svc.state -Force
        Start-Service $svc.name
        (Get-Service $svc.name).WaitForStatus('Running', [TimeSpan]::FromSeconds(20))
        $ids = Get-FixturePids $svc
        Stop-Process -Id $ids.child -Force
        (Get-Service $svc.name).WaitForStatus('Stopped', [TimeSpan]::FromSeconds(30))
        Wait-Until { Test-ProcessGone $ids.grandchild } 15 'Grandchild survived the crashed child'
        $exit = (Get-CimInstance Win32_Service -Filter "Name='$($svc.name)'").ExitCode
        Assert ($exit -eq 1066) "Crash reported exit code $exit instead of 1066"
        Assert ((Get-Service $svc.name).Status -eq 'Stopped') 'Crashed service restarted by itself'
    }

    Test 'mismatched service ID is refused before the child starts' {
        $other = New-FixtureService ('omnibox-ai-scmid-' + $suffix)
        $x = [xml](Get-Content -LiteralPath $other.profile -Raw); $x.service.id = 'another-service'; $x.Save($other.profile)
        try { Start-Service $other.name -ErrorAction Stop } catch { }
        Wait-Until { (Get-Service $other.name).Status -eq 'Stopped' } 30 'Service with a foreign profile kept running'
        Assert (-not (Test-Path -LiteralPath $other.state)) 'Child started for a foreign profile'
        Assert ((Get-Content -LiteralPath (Join-Path $fixture "logs/$($other.name).host.log") -Raw) -like '*Service ID differs*') 'Refusal not logged'
    }
    Write-Host "Real SCM tests: $script:passed passed"
} finally {
    foreach ($name in $services) {
        $s = Get-Service $name -ErrorAction SilentlyContinue
        if ($null -ne $s) {
            if ($s.Status -ne 'Stopped') { Stop-Service $name -Force -ErrorAction SilentlyContinue }
            Remove-Service $name -ErrorAction SilentlyContinue
        }
    }
    if (Test-Path -LiteralPath $fixture) { Remove-Item -LiteralPath $fixture -Recurse -Force -ErrorAction SilentlyContinue }
}
