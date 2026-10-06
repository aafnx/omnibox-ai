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
$aiModule = Import-Module (Join-Path $PSScriptRoot 'src/OmniBox.Ai.psm1') -Force -PassThru
$sourceRoot = $PSScriptRoot
$configLoader = {
    param($Directory)
    $path = Join-Path $Directory 'config.json'
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { $path = Join-Path $sourceRoot 'config.example.json' }
    & $aiModule { param($Path) Read-AiConfig $Path } $path
}.GetNewClosure()
$contextReader = { param($Backend) & $aiModule { param($Backend) Get-AiConfiguredContext $Backend } $Backend }.GetNewClosure()
if ($Reconfigure -and $NonInteractive) { throw '-Reconfigure и -NonInteractive нельзя использовать вместе.' }
$installed = Test-Path -LiteralPath (Join-Path $InstallDirectory '.omnibox-ai-install') -PathType Leaf
if ($installed -and -not $Reconfigure) { $NonInteractive = $true; Write-Host 'Обновление: существующие настройки сохраняются, мастер не запускается.' }
Write-Host 'OmniBox AI 0.9.3 — установка / обновление на этом ПК'
Write-Host 'Используются уже установленные службы Bonsai и Ollama. Enter сохраняет предложенное значение.'
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
    if (-not (Test-Path -LiteralPath $markerPath) -and @(Get-ChildItem -LiteralPath $destination -Force).Count) {
        throw 'Папка не пуста и не является установленным omnibox-ai. Выбери другую папку.'
    }
    if ((Get-Item -LiteralPath $destination).Attributes -band [IO.FileAttributes]::ReparsePoint -or
        @(Get-ChildItem -LiteralPath $destination -Recurse -Force | Where-Object { $_.Attributes -band [IO.FileAttributes]::ReparsePoint }).Count) { throw 'Ссылки/junction внутри папки установки запрещены.' }
}
$pwsh = (Get-Command pwsh.exe -CommandType Application -ErrorAction Stop | Select-Object -First 1).Source
if ($pwsh.Contains('%') -or $pwsh.Contains('"') -or $pwsh -match '[^\x20-\x7E]') { throw 'Для ai.cmd нужен ASCII-путь PowerShell без % и кавычек.' }
$existing = Get-Command ai -ErrorAction SilentlyContinue | Select-Object -First 1
if ($null -ne $existing) {
    if ($existing.CommandType -notin @('Application', 'ExternalScript') -or
        [IO.Path]::GetDirectoryName($existing.Source).TrimEnd('\') -ne $destination) {
        throw "Команда ai уже существует: $($existing.Source). Устрани конфликт имени перед установкой."
    }
}
$files = @('ai.ps1', 'ai.cmd', 'panel.ps1', 'service.ps1', 'src/Service.Host.cs', 'src/Service.Install.psm1', 'scripts/build-service.ps1', 'src/OmniBox.Ai.psm1', 'src/Panel.Engine.ps1', 'src/Panel.Cli.ps1', 'src/Telemetry.ps1', 'src/Inference.Stats.cs', 'src/Gateway.Helpers.psm1', 'src/OpenAi.Proxy.cs', 'src/Model.Catalog.psm1', 'src/Panel.Server.psm1', 'src/Panel.Security.psm1', 'src/Panel.Install.psm1', 'src/Install.Helpers.psm1', 'src/Update.Helpers.psm1', 'src/Web.Update.psm1', 'scripts/update-worker.ps1', 'scripts/get-install.ps1', 'src/Setup.Helpers.psm1', 'web/index.html', 'web/style.css', 'web/app.js', 'web/favicon.svg', 'web/brand-light.svg', 'web/brand-dark.svg', 'web/inter-400.woff.base64', 'web/inter-600.woff.base64', 'web/inter-license.txt', 'config.example.json', 'README.md', 'docs/commands.md', 'docs/architecture.md', 'docs/install.md', 'docs/web-panel.md', 'docs/reverse-proxy.md', 'docs/model-api.md', 'docs/models.md', 'docs/monitoring.md')
foreach ($file in $files) {
    if (-not (Test-Path -LiteralPath (Join-Path $source $file) -PathType Leaf)) { throw "Нет файла исходников: $file" }
}
if ($NoPanelSetup -and $null -ne (Get-Service -Name 'omnibox-ai' -ErrorAction SilentlyContinue)) { throw 'Веб-панель уже установлена. Обновляй без -NoPanelSetup, чтобы корректно остановить и запустить её службу.' }
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
    $expectedWrapper = Join-Path $destination 'panel-service.exe'
    $nativeCommand=New-AiNativeServiceCommand $pwsh $destination (Join-Path $destination 'panel-service.xml')
    if ($null -ne $panelService -and -not(Test-AiOwnedServiceCommand $panelService.PathName $pwsh $destination (Join-Path $destination 'panel-service.xml'))) { throw 'Имя omnibox-ai занято другой Windows-службой.' }
}
# Compile and probe an isolated runtime before stopping services or overwriting files.
$runtimeStage = New-AiCheckedServiceRuntime -SourceDirectory $source -PwshPath $pwsh
try {
if (-not $NoPanelSetup) {
    if ($null -ne $panelService -and $panelService.State -ne 'Stopped') { $panelProcess=Get-Process -Id $panelService.ProcessId -ErrorAction SilentlyContinue;Stop-Service 'omnibox-ai' -NoWait -ErrorAction Stop; (Get-Service 'omnibox-ai').WaitForStatus('Stopped', [TimeSpan]::FromSeconds(30));if($null -ne $panelProcess -and -not $panelProcess.WaitForExit(15000)){throw 'Panel host did not exit.'} }
}
if (Test-Path -LiteralPath $markerPath) {
    $backup = Join-Path $destination ('backups/' + (Get-Date -Format 'yyyyMMdd-HHmmss') + '-' + [Guid]::NewGuid().ToString('N').Substring(0, 8))
    $null = New-Item -ItemType Directory -Path $backup -Force
    foreach ($file in ($files + @('config.json', 'panel.json', 'gateway.json', 'panel-service.xml', '.omnibox-ai-install'))) {
        $oldFile = Join-Path $destination $file
        if (Test-Path -LiteralPath $oldFile -PathType Leaf) {
            $backupFile = Join-Path $backup $file
            $null = New-Item -ItemType Directory -Path (Split-Path $backupFile -Parent) -Force
            Copy-Item -LiteralPath $oldFile -Destination $backupFile
        }
    }
    Write-Host "Резервная копия: $backup"
}
$null = New-Item -ItemType Directory -Path $destination -Force
foreach ($file in $files) {
    $target = Join-Path $destination $file
    $null = New-Item -ItemType Directory -Path (Split-Path $target -Parent) -Force
    Copy-Item -LiteralPath (Join-Path $source $file) -Destination $target -Force
    Unblock-File -LiteralPath $target
}
$configPath = Join-Path $destination 'config.json'
if (-not (Test-Path -LiteralPath $configPath)) {
    Copy-Item -LiteralPath (Join-Path $destination 'config.example.json') -Destination $configPath
} else { Write-Host 'Существующие параметры config.json сохранены.' }
Import-Module (Join-Path $destination 'src/Service.Install.psm1') -Force
$serviceBackup=Install-AiNativeServices -Directory $destination -PwshPath $pwsh -Config $setup.config -ValidatedRuntimePath (Join-Path $runtimeStage 'src/Service.Host.exe')
$oldPanelWrapper=Join-Path $destination 'panel-service.exe'
if($null -ne $panelService -and $panelService.PathName.Trim().Trim('"') -ieq $oldPanelWrapper -and (Test-Path -LiteralPath $oldPanelWrapper)){Copy-Item -LiteralPath $oldPanelWrapper -Destination (Join-Path $serviceBackup 'panel-winsw.exe')}
$setup.config.default_backend = $setup.backend
Set-Content -LiteralPath $configPath -Value ($setup.config | ConvertTo-Json -Depth 10) -Encoding utf8
# Exact PowerShell location; %* preserves the user's command-line quoting in cmd.
$launcher = "@echo off`r`n`"$pwsh`" -NoLogo -NoProfile -ExecutionPolicy Bypass -File `"%~dp0ai.ps1`" %*`r`nexit /b %errorlevel%`r`n"
Set-Content -LiteralPath (Join-Path $destination 'ai.cmd') -Value $launcher -Encoding ascii -NoNewline
Set-Content -LiteralPath $markerPath -Value 'omnibox-ai 0.9.3' -Encoding utf8

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
Write-Host "Установлено: $destination"
if ($null -ne $setup.context) { Invoke-Ai -Arguments @('context', $setup.backend, [string]$setup.context) -ConfigPath $configPath }
if ($setup.start) {
    $service = Get-Service -Name $setup.config.backends[$setup.backend].service -ErrorAction Stop
    $action = if ([string]$service.Status -eq 'Stopped') { 'start' } else { 'restart' }
    Invoke-Ai -Arguments @($action, $setup.backend) -ConfigPath $configPath
} else { Write-Host 'Запуск не выбран. Новый контекст, если задан, применится при следующем запуске.' }
if (-not $NoPanelSetup) { Install-AiPanelService -Directory $destination -PwshPath $pwsh -Config $setup.config -Options $panelOptions -GatewayOptions $gatewayOptions }
if(-not $NoPanelSetup){
    $legacyPaths=@(Get-Content -LiteralPath (Join-Path $serviceBackup 'legacy-wrappers.json') -Raw|ConvertFrom-Json)
    foreach($legacy in $legacyPaths){if(Test-Path -LiteralPath $legacy){Remove-Item -LiteralPath $legacy -Force}}
    if(Test-Path -LiteralPath (Join-Path $serviceBackup 'panel-winsw.exe')){Remove-Item -LiteralPath $oldPanelWrapper -Force}
}
Write-Host 'WinSW заменён встроенным Windows service runtime. Параметры запуска и таймер простоя сохранены.'
Write-Host 'В этом процессе PATH обновлён. Если установщик запущен через дочерний pwsh, переподключись по SSH или добавь путь в прежнюю сессию вручную.'
Write-Host 'Проверка: ai help; ai context bonsai; ai status'

# Delegate the installing user's existing gh login to SYSTEM, encrypted and ACL protected.
Import-Module (Join-Path $destination 'src/Web.Update.psm1') -Force
if(Sync-AiUpdateCredential $destination){Write-Host 'Доступ gh подключён к обновлениям веб-панели (Windows DPAPI).'}

} finally { if ($runtimeStage -and (Test-Path -LiteralPath $runtimeStage)) { Remove-Item -LiteralPath $runtimeStage -Recurse -Force } }
