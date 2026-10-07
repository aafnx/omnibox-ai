#requires -Version 7.4
[CmdletBinding()]
param([string]$InstallDirectory = 'C:\tools\ai', [switch]$NoProfileUpdate,
    [switch]$NonInteractive, [switch]$Reconfigure, [string]$DefaultBackend = '', [string]$Context = '', [switch]$StartServer,
    [string]$PanelHost = '', [int]$PanelPort = 18090, [string]$PanelRemoteAddresses = 'LocalSubnet', [switch]$NoPanelSetup,
    [string]$PanelPublicUrl = '', [string]$PanelTrustedProxyAddresses = '', [int]$ApiPort = 18090, [string]$ApiBaseUrl = '', [switch]$NoApi)

$ErrorActionPreference = 'Stop'
if (-not $IsWindows) { throw 'Установщик предназначен для Windows.' }
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
try {
    $principal = [Security.Principal.WindowsPrincipal]::new($identity)
    if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
        throw 'Запусти установку в повышенной SSH-сессии или PowerShell от администратора.'
    }
} finally { $identity.Dispose() }

Import-Module (Join-Path $PSScriptRoot 'src/Setup.Helpers.psm1') -Force
Import-Module (Join-Path $PSScriptRoot 'src/Service.Install.psm1') -Force
Import-Module (Join-Path $PSScriptRoot 'src/Install.Transaction.psm1') -Force
$aiModule = Import-Module (Join-Path $PSScriptRoot 'src/OmniBox.Ai.psm1') -Force -PassThru
$sourceRoot = $PSScriptRoot
$package = Import-PowerShellDataFile -LiteralPath (Join-Path $PSScriptRoot 'package.psd1')
$version = (Get-Content -LiteralPath (Join-Path $PSScriptRoot 'VERSION') -Raw).Trim()
if ($version -notmatch '^\d+\.\d+\.\d+$') { throw 'Некорректный файл VERSION в пакете установки.' }
$configLoader = {
    param($Directory)
    $path = Join-Path $Directory 'config.json'
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { $path = Join-Path $sourceRoot 'config.example.json' }
    & $aiModule { param($Path) Read-AiConfig $Path } $path
}.GetNewClosure()
$contextReader = { param($Backend) & $aiModule { param($Backend) Get-AiConfiguredContext $Backend } $Backend }.GetNewClosure()
if ($Reconfigure -and $NonInteractive) { throw '-Reconfigure и -NonInteractive нельзя использовать вместе.' }
# not-started: nothing changed; rolled-back / rollback-failed: see message; installed: committed.
$global:OmniBoxInstallResult = @{ status = 'not-started'; message = ''; snapshot = '' }
# One installation at a time, whether started by ai update, the web worker or by hand.
$installLock = Enter-AiInstallLock
$transaction = $null; $runtimeStage = $null
try {
$pwsh = (Get-Command pwsh.exe -CommandType Application -ErrorAction Stop | Select-Object -First 1).Source
if ($pwsh.Contains('%') -or $pwsh.Contains('"') -or $pwsh -match '[^\x20-\x7E]') { throw 'Для ai.cmd нужен ASCII-путь PowerShell без % и кавычек.' }

function Undo-InterruptedInstallation([string]$Directory) {
    if (-not [IO.Path]::IsPathFullyQualified($Directory) -or -not (Test-Path -LiteralPath $Directory -PathType Container)) { return }
    $interrupted = Get-AiInterruptedInstallTransaction $Directory
    if ($null -eq $interrupted) { return }
    Enter-AiControlLock $installLock
    Write-Host "Предыдущая установка была прервана. Восстанавливаем состояние до неё: $($interrupted.snapshot)"
    $result = Undo-AiInstallTransaction $interrupted $pwsh 'Предыдущая установка прервана.'
    if (-not $result.restored) { throw "Прерванную установку не удалось откатить: $($result.errors -join '; '). Снимок: $($result.snapshot)" }
    Write-Host 'Состояние до прерванной установки восстановлено.'
}
Undo-InterruptedInstallation $InstallDirectory

$installed = Test-Path -LiteralPath (Join-Path $InstallDirectory '.omnibox-ai-install') -PathType Leaf
if ($installed -and -not $Reconfigure) { $NonInteractive = $true; Write-Host "OmniBox AI $version`: обновление, настройки сохраняются." }
else { Write-Host "OmniBox AI $version`: установка." }
if(-not $NonInteractive){Write-Host 'Enter сохраняет предложенное значение.'}
$setup = Get-AiSetupOptions -InstallDirectory $InstallDirectory -ConfigLoader $configLoader -ContextReader $contextReader -NonInteractive:$NonInteractive -Reconfigure:$Reconfigure -DefaultBackend $DefaultBackend -Context $Context -StartServer:$StartServer
$InstallDirectory = $setup.directory
if (-not $Reconfigure -and (Test-Path -LiteralPath (Join-Path $InstallDirectory '.omnibox-ai-install') -PathType Leaf)) { $NonInteractive = $true }

if (-not [IO.Path]::IsPathFullyQualified($InstallDirectory)) { throw 'Укажи абсолютный путь установки.' }
$destination = [IO.Path]::GetFullPath($InstallDirectory).TrimEnd('\')
$source = [IO.Path]::GetFullPath($PSScriptRoot).TrimEnd('\')
if ($destination -eq [IO.Path]::GetPathRoot($destination).TrimEnd('\') -or
    $destination -in @('C:\tools', 'C:\ai', $env:USERPROFILE.TrimEnd('\'), $source)) {
    throw 'Выбери отдельную папку установки, например C:\tools\ai, не корень и не папку исходников.'
}
if ($destination.Contains('%') -or $destination.Contains('"') -or $destination -match '[^\x20-\x7E]') { throw 'Для ai.cmd нужен ASCII-путь установки без % и кавычек, например C:\tools\ai.' }
$markerPath = Join-Path $destination '.omnibox-ai-install'
if (Test-Path -LiteralPath $destination) {
    if (-not (Test-Path -LiteralPath $destination -PathType Container)) { throw 'Путь установки уже занят файлом.' }
    Undo-InterruptedInstallation $destination
    # A rolled-back first installation leaves only its snapshots in backups.
    $foreign = @(Get-ChildItem -LiteralPath $destination -Force | Where-Object { -not ($_.Name -eq 'backups' -and $_.PSIsContainer -and @(Get-ChildItem -LiteralPath $_.FullName -Filter 'snapshot.json' -Recurse -Depth 1).Count) })
    if (-not (Test-Path -LiteralPath $markerPath) -and $foreign.Count) {
        throw 'Папка не пуста и не является установленным omnibox-ai. Выбери другую папку.'
    }
    if ((Get-Item -LiteralPath $destination).Attributes -band [IO.FileAttributes]::ReparsePoint -or
        @(Get-ChildItem -LiteralPath $destination -Recurse -Force | Where-Object { $_.Attributes -band [IO.FileAttributes]::ReparsePoint }).Count) { throw 'Ссылки/junction внутри папки установки запрещены.' }
}
$existing = Get-Command ai -ErrorAction SilentlyContinue | Select-Object -First 1
if ($null -ne $existing) {
    if ($existing.CommandType -notin @('Application', 'ExternalScript') -or
        [IO.Path]::GetDirectoryName($existing.Source).TrimEnd('\') -ne $destination) {
        throw "Команда ai уже существует: $($existing.Source). Устрани конфликт имени перед установкой."
    }
}
$files = @($package.Files)
foreach ($file in $files) {
    if (-not (Test-Path -LiteralPath (Join-Path $source $file) -PathType Leaf)) { throw "Нет файла исходников: $file" }
}
if ($NoPanelSetup -and $null -ne (Get-Service -Name 'omnibox-ai' -ErrorAction SilentlyContinue)) { throw 'Веб-панель уже установлена. Обновляй без -NoPanelSetup, чтобы корректно остановить и запустить её службу.' }
$panelService = $null
if (-not $NoPanelSetup) {
    Import-Module (Join-Path $source 'src/Panel.Install.psm1') -Force
    $panelArgs = @{ Directory = $destination; HostName = $PanelHost; NonInteractive = $NonInteractive }
    if ($PSBoundParameters.ContainsKey('PanelRemoteAddresses')) { $panelArgs.RemoteAddresses = $PanelRemoteAddresses }
    if ($PSBoundParameters.ContainsKey('PanelPort')) { $panelArgs.Port = $PanelPort }
    if ($PSBoundParameters.ContainsKey('PanelPublicUrl')) { $panelArgs.PublicUrl = $PanelPublicUrl }
    if ($PSBoundParameters.ContainsKey('PanelTrustedProxyAddresses')) { $panelArgs.TrustedProxyAddresses = $PanelTrustedProxyAddresses }
    $panelOptions = Get-AiPanelInstallOptions @panelArgs
    $apiArgs=@{Directory=$destination;HostName=$panelOptions.allowed_hosts[0];Backend=$setup.backend;NonInteractive=$NonInteractive;NoApi=$NoApi;PanelPort=$panelOptions.port;PublicUrl=$panelOptions.public_origin}
    if($PSBoundParameters.ContainsKey('ApiPort')){$apiArgs.Port=$ApiPort}
    if($PSBoundParameters.ContainsKey('ApiBaseUrl')){$apiArgs.BaseUrl=$ApiBaseUrl}
    $gatewayOptions=Get-AiGatewayInstallOptions @apiArgs
    # Ensure an existing service with our ID belongs to this installation before stopping it.
    $panelService = Get-CimInstance Win32_Service -Filter "Name='omnibox-ai'" -ErrorAction Stop
    if ($null -ne $panelService -and -not(Test-AiOwnedServiceCommand $panelService.PathName $pwsh $destination (Join-Path $destination 'panel-service.xml'))) { throw 'Имя omnibox-ai занято другой Windows-службой.' }
}

# Values that must survive the installation. Only settings the user explicitly asked to change
# (wizard or parameters) and documented migrations may differ afterwards.
$overrides = @{}
if ($Reconfigure -or $PSBoundParameters.ContainsKey('DefaultBackend')) { $overrides['config.default_backend'] = $setup.backend }
if (-not $NoPanelSetup) {
    $panelKeys = @{ PanelPort = 'port'; PanelRemoteAddresses = 'remote_addresses'; PanelPublicUrl = 'public_origin'; PanelTrustedProxyAddresses = 'trusted_proxy_addresses' }
    foreach ($parameter in $panelKeys.Keys) { if ($Reconfigure -or $PSBoundParameters.ContainsKey($parameter)) { $overrides['panel.' + $panelKeys[$parameter]] = $panelOptions[$panelKeys[$parameter]] } }
    $gatewayKeys = @{ ApiPort = 'port'; ApiBaseUrl = 'base_url' }
    foreach ($parameter in $gatewayKeys.Keys) { if ($Reconfigure -or $PSBoundParameters.ContainsKey($parameter)) { $overrides['gateway.' + $gatewayKeys[$parameter]] = $gatewayOptions[$gatewayKeys[$parameter]] } }
    if ($Reconfigure -or $NoApi) { $overrides['gateway.enabled'] = $gatewayOptions.enabled }
    if ($Reconfigure) { $overrides['gateway.require_key'] = $gatewayOptions.require_key; $overrides['gateway.key_hash'] = $gatewayOptions.key_hash }
    # Migration rule: allowed_hosts keeps every existing host and may gain only these.
    $extra = @($panelOptions.allowed_hosts[0], '127.0.0.1', 'localhost', [Environment]::MachineName.ToLowerInvariant())
    if ($panelOptions.public_origin) { $extra += ([Uri]$panelOptions.public_origin).Host }
    $overrides['allowed_hosts.extra'] = $extra
}

Enter-AiControlLock $installLock
# Compile and probe an isolated runtime before stopping services or overwriting files.
$runtimeStage = New-AiCheckedServiceRuntime -SourceDirectory $source -PwshPath $pwsh
$panelWasRunning = $null -ne $panelService -and $panelService.State -ne 'Stopped'
if ($panelWasRunning) {
    try { $panelProcess=Get-Process -Id $panelService.ProcessId -ErrorAction SilentlyContinue;Stop-Service 'omnibox-ai' -NoWait -ErrorAction Stop; (Get-Service 'omnibox-ai').WaitForStatus('Stopped', [TimeSpan]::FromSeconds(30));if($null -ne $panelProcess -and -not $panelProcess.WaitForExit(15000)){throw 'Panel host did not exit.'} }
    catch { try { Start-Service 'omnibox-ai' -ErrorAction Stop } catch { }; throw "Панель не остановилась; установка не начата: $($_.Exception.Message)" }
}

# Everything from here to the commit is one transaction: any failure restores the snapshot.
$serviceProfiles = [ordered]@{}
if (-not $NoPanelSetup) { $serviceProfiles['omnibox-ai'] = Join-Path $destination 'panel-service.xml' }
$externalFiles = @()
foreach ($name in $setup.config.backends.Keys) {
    $backend = $setup.config.backends[$name]
    $serviceProfiles[$backend.service] = $backend.service_config
    $externalFiles += @($backend.service_config, ($backend.service_config + '.ai-previous'), ($backend.service_config + '.ai-state.json'))
}
$snapshotFiles = @($files) + @('config.json', 'panel.json', 'panel-sessions.dpapi', 'gateway.json', 'storage.json', 'panel-service.xml', '.omnibox-ai-install', 'src/Service.Host.exe', 'panel-service.exe')
try { $transaction = New-AiInstallTransaction -Directory $destination -Files $snapshotFiles -ServiceProfiles $serviceProfiles -ExternalFiles $externalFiles -PanelWasRunning $panelWasRunning -Version $version }
catch { if ($panelWasRunning) { try { Start-Service 'omnibox-ai' -ErrorAction Stop } catch { } }; throw "Резервная копия не создана; установка не начата: $($_.Exception.Message)" }
$global:OmniBoxInstallResult.snapshot = $transaction.snapshot
$expectedSettings = Get-AiExpectedInstallSettings $transaction.settings $overrides
Write-Host "Резервная копия: $($transaction.snapshot)"
try {
    Add-AiInstallStep $transaction 'files'
    foreach ($file in $files) {
        $target = Join-Path $destination $file
        $null = New-Item -ItemType Directory -Path (Split-Path $target -Parent) -Force
        Copy-Item -LiteralPath (Join-Path $source $file) -Destination $target -Force
        Unblock-File -LiteralPath $target
    }
    $configPath = Join-Path $destination 'config.json'
    if (-not (Test-Path -LiteralPath $configPath)) {
        Copy-Item -LiteralPath (Join-Path $destination 'config.example.json') -Destination $configPath
    }
    Add-AiInstallStep $transaction 'services'
    Import-Module (Join-Path $destination 'src/Service.Install.psm1') -Force
    $serviceBackup=Install-AiNativeServices -Directory $destination -PwshPath $pwsh -Config $setup.config -ValidatedRuntimePath (Join-Path $runtimeStage 'src/Service.Host.exe')
    $oldPanelWrapper=Join-Path $destination 'panel-service.exe'
    if($null -ne $panelService -and $panelService.PathName.Trim().Trim('"') -ieq $oldPanelWrapper -and (Test-Path -LiteralPath $oldPanelWrapper)){Copy-Item -LiteralPath $oldPanelWrapper -Destination (Join-Path $serviceBackup 'panel-winsw.exe')}
    Add-AiInstallStep $transaction 'config'
    $setup.config.default_backend = $setup.backend
    Set-Content -LiteralPath $configPath -Value ($setup.config | ConvertTo-Json -Depth 10) -Encoding utf8
    # Exact PowerShell location; %* preserves the user's command-line quoting in cmd.
    $launcher = "@echo off`r`n`"$pwsh`" -NoLogo -NoProfile -ExecutionPolicy Bypass -File `"%~dp0ai.ps1`" %*`r`nexit /b %errorlevel%`r`n"
    Set-Content -LiteralPath (Join-Path $destination 'ai.cmd') -Value $launcher -Encoding ascii -NoNewline
    Set-Content -LiteralPath $markerPath -Value "omnibox-ai $version" -Encoding utf8
    # Settings are compared before the panel starts, so its own writes cannot hide or cause a difference.
    $verify = { Add-AiInstallStep $transaction 'verify'; Assert-AiInstallSettings $expectedSettings $destination }
    if (-not $NoPanelSetup) {
        Add-AiInstallStep $transaction 'panel'
        Install-AiPanelService -Directory $destination -PwshPath $pwsh -Config $setup.config -Options $panelOptions -GatewayOptions $gatewayOptions -BeforeStart $verify
        Add-AiInstallStep $transaction 'panel-started'
    } else { & $verify }
    Complete-AiInstallTransaction $transaction
} catch {
    $cause = $_.Exception.Message
    Write-Warning "Установка не завершилась: $cause"
    Write-Host 'Восстанавливаем предыдущую установку…'
    $recovery = Undo-AiInstallTransaction $transaction $pwsh $cause
    if ($recovery.restored) {
        $global:OmniBoxInstallResult.status = 'rolled-back'
        $global:OmniBoxInstallResult.message = "Установка не завершилась: $cause Предыдущая версия, настройки и команды служб восстановлены; панель запущена в прежнем состоянии. Снимок: $($recovery.snapshot)"
    } else {
        $global:OmniBoxInstallResult.status = 'rollback-failed'
        $global:OmniBoxInstallResult.message = "Установка не завершилась: $cause Автоматическое восстановление неполное: $($recovery.errors -join '; '). Снимок: $($recovery.snapshot)"
    }
    throw $global:OmniBoxInstallResult.message
}
$global:OmniBoxInstallResult.status = 'installed'

# Committed. Cleanup and user-requested actions below do not undo the installation.
if(-not $NoPanelSetup){
    try {
        $legacyPaths=@(Get-Content -LiteralPath (Join-Path $serviceBackup 'legacy-wrappers.json') -Raw|ConvertFrom-Json)
        foreach($legacy in $legacyPaths){if(Test-Path -LiteralPath $legacy){Remove-Item -LiteralPath $legacy -Force}}
        if(Test-Path -LiteralPath (Join-Path $serviceBackup 'panel-winsw.exe')){Remove-Item -LiteralPath $oldPanelWrapper -Force}
    } catch { Write-Warning "Старые файлы WinSW не удалены: $($_.Exception.Message)" }
}
$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
$entries = @($userPath -split ';' | Where-Object { $_ })
$present = @($entries | Where-Object { [Environment]::ExpandEnvironmentVariables($_).TrimEnd('\') -ieq $destination }).Count -gt 0
if (-not $present) {
    [Environment]::SetEnvironmentVariable('Path', (($entries + $destination) -join ';'), 'User')
}
if (@($env:Path -split ';' | Where-Object { $_.TrimEnd('\') -ieq $destination }).Count -eq 0) {
    $env:Path += ";$destination"
}
if (-not $NoProfileUpdate) {
    # New SSH shells read this PATH-only snippet even when sshd inherits an old environment.
    # Preserve the existing prompt and every other profile statement; backup before editing.
    Import-Module (Join-Path $destination 'src/Install.Helpers.psm1') -Force
    Update-AiPowerShellProfile -ProfilePath $PROFILE.CurrentUserAllHosts -InstallDirectory $destination -BackupDirectory (Join-Path $destination 'backups/profiles')
}
if ($null -ne $setup.context) { Invoke-Ai -Arguments @('context', $setup.backend, [string]$setup.context) -ConfigPath $configPath }
if ($setup.start) {
    $service = Get-Service -Name $setup.config.backends[$setup.backend].service -ErrorAction Stop
    $action = if ([string]$service.Status -eq 'Stopped') { 'start' } else { 'restart' }
    Invoke-Ai -Arguments @($action, $setup.backend) -ConfigPath $configPath
}
# Housekeeping after commit: old snapshots, expired logs and temp files of killed runs. Never fails the install.
try {
    Import-Module (Join-Path $destination 'src/Storage.Helpers.psm1') -Force
    $null = Remove-AiOldBackups $destination 3 @($transaction.snapshot)
    $null = Invoke-AiStorageCleanup $destination $setup.config
} catch { Write-Host "Очистка старых файлов пропущена: $($_.Exception.Message)" }
# A first install changes PATH, which only new sessions see.
if (-not $installed) { Write-Host 'Команда ai доступна в новых сессиях терминала.' }
Write-Host "Готово: OmniBox AI $version в $destination. Состояние: ai status · справка: ai help"

# Delegate the installing user's existing gh login to SYSTEM, encrypted and ACL protected.
Import-Module (Join-Path $destination 'src/Web.Update.psm1') -Force
$null=Sync-AiUpdateCredential $destination

} finally {
    if ($runtimeStage -and (Test-Path -LiteralPath $runtimeStage)) { Remove-Item -LiteralPath $runtimeStage -Recurse -Force }
    Exit-AiInstallLock $installLock
}
