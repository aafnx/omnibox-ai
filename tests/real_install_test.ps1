#requires -Version 7.4
# Real installation on a disposable Windows machine (CI): installs with real services, panel,
# ACL and firewall, then checks a forced failure during an update and a successful update.
# It registers the fixed service name omnibox-ai, so it never runs on a machine that has one.
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$repo = Split-Path $PSScriptRoot -Parent
if (-not $IsWindows) { Write-Host 'SKIP real installation test: Windows only'; exit 0 }
if ($env:OMNIBOX_AI_REAL_INSTALL_TEST -ne '1') { Write-Host 'SKIP real installation test: set OMNIBOX_AI_REAL_INSTALL_TEST=1 on a disposable machine'; exit 0 }
if ($null -ne (Get-Service -Name 'omnibox-ai' -ErrorAction SilentlyContinue)) { throw 'omnibox-ai is already installed here; the real installation test needs a disposable machine.' }

function Assert($Condition, [string]$Message) { if (-not $Condition) { throw $Message } }
$script:passed = 0
function Test([string]$Name, [scriptblock]$Action) { & $Action; $script:passed++; Write-Host "PASS $Name" }

$suffix = [Guid]::NewGuid().ToString('N').Substring(0, 6)
$fixture = Join-Path ([IO.Path]::GetTempPath()) ('omnibox-real-install-' + $suffix)
$source = Join-Path $fixture 'source'; $destination = Join-Path $fixture 'ai'
$port = 18190
$backends = @{ bonsai = "omnibox-ci-bonsai-$suffix"; ollama = "omnibox-ci-ollama-$suffix" }
$pwsh = Join-Path $PSHOME 'pwsh.exe'
$oldUserPath = [Environment]::GetEnvironmentVariable('Path', 'User')

function Get-Panel([int]$Seconds = 60) {
    $deadline = [DateTime]::UtcNow.AddSeconds($Seconds)
    while ([DateTime]::UtcNow -lt $deadline) {
        try { return (Invoke-WebRequest -Uri "http://127.0.0.1:$port/" -TimeoutSec 3 -SkipHttpErrorCheck).StatusCode } catch { Start-Sleep -Milliseconds 500 }
    }
    return 0
}
function Get-Command-Of([string]$Name) { (Get-CimInstance Win32_Service -Filter "Name='$Name'").PathName }
function Get-Settings {
    $panel = Get-Content (Join-Path $destination 'panel.json') -Raw | ConvertFrom-Json -AsHashtable
    $gateway = Get-Content (Join-Path $destination 'gateway.json') -Raw | ConvertFrom-Json -AsHashtable
    $config = Get-Content (Join-Path $destination 'config.json') -Raw | ConvertFrom-Json -AsHashtable
    return @{ password = $panel.credential.hash; hosts = (@($panel.allowed_hosts | ForEach-Object { ([string]$_).ToLowerInvariant() } | Sort-Object -Unique) -join ','); port = $panel.port; key = $gateway.key_hash; base = $gateway.base_url; model = $config.backends.bonsai.active_model }
}

try {
    $null = New-Item -ItemType Directory -Path $fixture
    Copy-Item -LiteralPath $repo -Destination $source -Recurse
    Remove-Item -LiteralPath (Join-Path $source '.git') -Recurse -Force -ErrorAction SilentlyContinue
    # Disposable engine services: profiles start a harmless PowerShell loop, never a model.
    $config = Get-Content (Join-Path $source 'config.example.json') -Raw | ConvertFrom-Json -AsHashtable
    foreach ($name in @($config.backends.Keys)) {
        $b = $config.backends[$name]; $b.service = $backends[$name]
        $b.service_config = Join-Path $fixture "$name\$($backends[$name]).xml"; $b.log_directory = Join-Path $fixture "$name\logs"
        $null = New-Item -ItemType Directory -Path (Split-Path $b.service_config -Parent) -Force
        $x = [xml]'<service><id/><executable/><arguments/><workingdirectory/><logpath/></service>'
        $x.service.id = $backends[$name]; $x.service.executable = $pwsh; $x.service.workingdirectory = Split-Path $b.service_config -Parent; $x.service.logpath = $b.log_directory
        $x.service.arguments = $(if ($b.type -eq 'prism') { '-NoLogo -NoProfile -Command "while ($true) { Start-Sleep 60 }" -m model.gguf -c 32768' } else { '-NoLogo -NoProfile -Command "while ($true) { Start-Sleep 60 }"' })
        if ($b.type -eq 'ollama') { $e = $x.CreateElement('env'); $e.SetAttribute('name', 'OLLAMA_CONTEXT_LENGTH'); $e.SetAttribute('value', '32768'); $null = $x.service.AppendChild($e) }
        $x.Save($b.service_config)
    }
    $config.backends.bonsai.active_model = 'fixture-model'
    $config | ConvertTo-Json -Depth 12 | Set-Content (Join-Path $source 'config.example.json')
    $install = Join-Path $source 'install.ps1'
    $arguments = @{ InstallDirectory = $destination; NonInteractive = $true; NoProfileUpdate = $true; PanelHost = '127.0.0.1'; PanelPort = $port; ApiPort = $port }

    Test 'first installation registers native services, ACL-protected panel and firewall' {
        & $install @arguments
        Assert ($global:OmniBoxInstallResult.status -eq 'installed') 'Installation not committed'
        Assert ((Get-Panel) -eq 200) 'Panel does not answer'
        foreach ($service in @('omnibox-ai') + @($backends.Values)) { Assert ((Get-Command-Of $service) -like '*src\Service.Host.exe*') "$service is not on the native runtime" }
        foreach ($service in $backends.Values) { Assert ((Get-Service $service).Status -eq 'Stopped') "$service started by the installer" }
        Assert ($null -ne (Get-NetFirewallRule -Name 'omnibox-ai-panel' -ErrorAction SilentlyContinue)) 'Firewall rule missing'
        Assert (-not (Test-Path (Join-Path $destination '.omnibox-ai-transaction.json'))) 'Journal left after commit'
    }

    # Make the installed copy an "older version" with a legacy engine command and model history.
    Set-Content (Join-Path $destination 'VERSION') '0.9.0'; Set-Content (Join-Path $destination '.omnibox-ai-install') 'omnibox-ai 0.9.0'
    Import-Module (Join-Path $destination 'src/Service.Install.psm1') -Force
    $bonsaiXml = $config.backends.bonsai.service_config
    $legacy = New-AiLegacyServiceCommand $pwsh $destination $bonsaiXml
    Set-AiServiceCommand -Name $backends.bonsai -Command $legacy -StartupType Manual
    Set-Content ($bonsaiXml + '.ai-previous') '<service><id>previous</id></service>'; Set-Content ($bonsaiXml + '.ai-state.json') '{"schema_version":1}'
    $settings = Get-Settings
    $hashes = @{}; foreach ($path in @((Join-Path $destination 'panel.json'), (Join-Path $destination 'gateway.json'), $bonsaiXml, ($bonsaiXml + '.ai-previous'), ($bonsaiXml + '.ai-state.json'))) { $hashes[$path] = (Get-FileHash $path).Hash }

    foreach ($step in @('services', 'panel', 'panel-started')) {
        Test "forced failure at '$step' restores the previous version, commands, settings and running panel" {
            $env:OMNIBOX_AI_INSTALL_FAIL_STEP = $step
            $failed = $null
            try { & $install @arguments } catch { $failed = $_.Exception.Message } finally { $env:OMNIBOX_AI_INSTALL_FAIL_STEP = $null }
            Assert ($failed -like '*восстановлены*') "Failure not reported as restored: $failed"
            Assert ($global:OmniBoxInstallResult.status -eq 'rolled-back') "Result: $($global:OmniBoxInstallResult.status)"
            Assert ((Get-Content (Join-Path $destination 'VERSION') -Raw).Trim() -eq '0.9.0') 'Previous version not restored'
            Assert ((Get-Command-Of $backends.bonsai) -ceq $legacy) 'Legacy engine command not restored'
            Assert ((Get-Service 'omnibox-ai').Status -eq 'Running') 'Panel not running after rollback'
            Assert ((Get-Panel) -eq 200) 'Restored panel does not answer'
            foreach ($path in $hashes.Keys) { Assert ((Get-FileHash $path).Hash -eq $hashes[$path]) "Changed after rollback: $path" }
            Assert (-not (Test-Path (Join-Path $destination '.omnibox-ai-transaction.json'))) 'Journal left after rollback'
        }
    }

    Test 'successful update migrates commands and keeps settings by value' {
        & $install @arguments
        Assert ($global:OmniBoxInstallResult.status -eq 'installed') 'Update not committed'
        Assert ((Get-Content (Join-Path $destination 'VERSION') -Raw).Trim() -eq (Get-Content (Join-Path $repo 'VERSION') -Raw).Trim()) 'New version not installed'
        Assert ((Get-Command-Of $backends.bonsai) -like '*src\Service.Host.exe*') 'Legacy command not migrated'
        $after = Get-Settings
        foreach ($key in $settings.Keys) { Assert ($after[$key] -ceq $settings[$key]) "Update changed $key" }
        foreach ($path in @(($bonsaiXml + '.ai-previous'), ($bonsaiXml + '.ai-state.json'))) { Assert ((Get-FileHash $path).Hash -eq $hashes[$path]) "Update changed $path" }
        Assert ((Get-Panel) -eq 200) 'Updated panel does not answer'
    }

    # ai update <version>: downgrade to the oldest installable release and back, through the real
    # Invoke-AiUpdate. Only GitHub is replaced: releases.json and installers come from git history.
    $releases = Get-Content (Join-Path $repo 'releases.json') -Raw | ConvertFrom-Json
    $oldest = @($releases.releases)[-1]
    $updater = Import-Module (Join-Path $destination 'src/Update.Helpers.psm1') -Force -PassThru
    & $updater {
        param($Repo)
        $script:repo = $Repo
        function script:New-AiGitHubSource { @{ gh = 'git-history'; secret = $null } }
        function script:Get-AiGitHubFile {
            param($Source, [string]$Path, [string]$Ref)
            if ($Path -eq 'releases.json' -or $Ref -eq 'main') { return ,[IO.File]::ReadAllBytes((Join-Path $script:repo $Path)) }
            $info = [Diagnostics.ProcessStartInfo]::new('git'); $info.UseShellExecute = $false; $info.RedirectStandardOutput = $true
            foreach ($a in @('-C', $script:repo, 'show', "$($Ref):$Path")) { $info.ArgumentList.Add($a) }
            $p = [Diagnostics.Process]::Start($info); $memory = [IO.MemoryStream]::new()
            try { $p.StandardOutput.BaseStream.CopyTo($memory); $p.WaitForExit(); if ($p.ExitCode -ne 0) { throw "git show $Ref failed" }; return ,$memory.ToArray() } finally { $p.Dispose(); $memory.Dispose() }
        }
    } $repo
    Test "ai update $($oldest.version) downgrades and keeps settings; panel works on the old version" {
        Invoke-AiUpdate -InstallDirectory $destination -Version $oldest.version
        Assert ((Get-Content (Join-Path $destination '.omnibox-ai-install') -Raw) -match [regex]::Escape($oldest.version)) 'Old version marker missing'
        Assert ((Get-Panel) -eq 200) 'Downgraded panel does not answer'
        $after = Get-Settings
        foreach ($key in $settings.Keys) { Assert ($after[$key] -ceq $settings[$key]) "Downgrade changed $key" }
        foreach ($service in $backends.Values) { Assert ((Get-Service $service).Status -eq 'Stopped') "$service started by the downgrade" }
    }
    Test 'ai update <current> returns from the old version to the newest one' {
        Invoke-AiUpdate -InstallDirectory $destination -Version (Get-Content (Join-Path $repo 'VERSION') -Raw).Trim()
        Assert ($global:OmniBoxInstallResult.status -eq 'installed') 'Upgrade from the old version not committed'
        Assert ((Get-Content (Join-Path $destination 'VERSION') -Raw).Trim() -eq (Get-Content (Join-Path $repo 'VERSION') -Raw).Trim()) 'Newest version not installed'
        $after = Get-Settings
        foreach ($key in $settings.Keys) { Assert ($after[$key] -ceq $settings[$key]) "Upgrade changed $key" }
        Assert ((Get-Panel) -eq 200) 'Panel does not answer after returning to the newest version'
    }
    Write-Host "Real installation tests: $script:passed passed"
} finally {
    foreach ($service in @('omnibox-ai') + @($backends.Values)) {
        $s = Get-Service $service -ErrorAction SilentlyContinue
        if ($null -ne $s) { if ($s.Status -ne 'Stopped') { Stop-Service $service -Force -ErrorAction SilentlyContinue }; Remove-Service $service -ErrorAction SilentlyContinue }
    }
    Get-NetFirewallRule -Name 'omnibox-ai-panel', 'omnibox-ai-api' -ErrorAction SilentlyContinue | Remove-NetFirewallRule -ErrorAction SilentlyContinue
    [Environment]::SetEnvironmentVariable('Path', $oldUserPath, 'User')
    if (Test-Path -LiteralPath $fixture) { Remove-Item -LiteralPath $fixture -Recurse -Force -ErrorAction SilentlyContinue }
}
