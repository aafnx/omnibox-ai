#requires -Version 7.4
[CmdletBinding()]
param([string]$InstallDirectory = 'C:\tools\ai', [switch]$NoProfileUpdate,
    [switch]$NonInteractive, [string]$DefaultBackend = '', [string]$Context = '', [switch]$StartServer)

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
$aiModule = Import-Module (Join-Path $PSScriptRoot 'src/OmniBox.Ai.psm1') -Force -PassThru
$sourceRoot = $PSScriptRoot
$configLoader = {
    param($Directory)
    $path = Join-Path $Directory 'config.json'
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { $path = Join-Path $sourceRoot 'config.example.json' }
    & $aiModule { param($Path) Read-AiConfig $Path } $path
}.GetNewClosure()
$contextReader = { param($Backend) & $aiModule { param($Backend) Get-AiConfiguredContext $Backend } $Backend }.GetNewClosure()
Write-Host 'OmniBox AI 0.3.0 — установка на этом ПК'
Write-Host 'Используются уже установленные службы Bonsai и Ollama. Enter сохраняет предложенное значение.'
$setup = Get-AiSetupOptions -InstallDirectory $InstallDirectory -ConfigLoader $configLoader -ContextReader $contextReader -NonInteractive:$NonInteractive -DefaultBackend $DefaultBackend -Context $Context -StartServer:$StartServer
$InstallDirectory = $setup.directory

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
$files = @('ai.ps1', 'ai.cmd', 'src/OmniBox.Ai.psm1', 'src/Install.Helpers.psm1', 'src/Setup.Helpers.psm1', 'config.example.json', 'README.md', 'docs/commands.md', 'docs/architecture.md', 'docs/install.md')
foreach ($file in $files) {
    if (-not (Test-Path -LiteralPath (Join-Path $source $file) -PathType Leaf)) { throw "Нет файла исходников: $file" }
}
if (Test-Path -LiteralPath $markerPath) {
    $backup = Join-Path $destination ('backups/' + (Get-Date -Format 'yyyyMMdd-HHmmss') + '-' + [Guid]::NewGuid().ToString('N').Substring(0, 8))
    $null = New-Item -ItemType Directory -Path $backup -Force
    foreach ($file in ($files + @('config.json', '.omnibox-ai-install'))) {
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
} else { Write-Host 'Существующие параметры config.json сохранены; сервер по умолчанию — по выбору мастера.' }
$setup.config.default_backend = $setup.backend
Set-Content -LiteralPath $configPath -Value ($setup.config | ConvertTo-Json -Depth 10) -Encoding utf8
# Exact PowerShell location; %* preserves the user's command-line quoting in cmd.
$launcher = "@echo off`r`n`"$pwsh`" -NoLogo -NoProfile -ExecutionPolicy Bypass -File `"%~dp0ai.ps1`" %*`r`nexit /b %errorlevel%`r`n"
Set-Content -LiteralPath (Join-Path $destination 'ai.cmd') -Value $launcher -Encoding ascii -NoNewline
Set-Content -LiteralPath $markerPath -Value 'omnibox-ai 0.3.0' -Encoding utf8

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
Write-Host 'Автозапуск и таймер простоя сохранены. Перед игрой: ai stop'
Write-Host 'В этом процессе PATH обновлён. Если установщик запущен через дочерний pwsh, переподключись по SSH или добавь путь в прежнюю сессию вручную.'
Write-Host 'Проверка: ai help; ai context bonsai; ai status'
