Set-StrictMode -Version Latest

function Read-AiYesNo {
    param([string]$Prompt, [bool]$Default = $false, [scriptblock]$ReadAnswer = { param($Text) Read-Host $Text })
    $hint = if ($Default) { 'Y/n' } else { 'y/N' }
    $answer = & $ReadAnswer "$Prompt [$hint]"
    if ([string]::IsNullOrWhiteSpace($answer)) { return $Default }
    switch ($answer.Trim().ToLowerInvariant()) {
        { $_ -in @('yes','y') } { return $true }
        { $_ -in @('no','n') } { return $false }
        default { throw 'Please answer yes/no or y/n.' }
    }
}

function Get-AiSetupOptions {
    param([string]$InstallDirectory = 'C:\tools\ai', [scriptblock]$ConfigLoader, [scriptblock]$ContextReader,
        [switch]$NonInteractive, [switch]$Reconfigure, [string]$DefaultBackend = '', [string]$Context = '', [switch]$StartServer,
        [scriptblock]$ReadAnswer = { param($Prompt) Read-Host $Prompt })
    if (-not $Reconfigure -and [IO.File]::Exists([IO.Path]::Combine($InstallDirectory, '.omnibox-ai-install'))) { $NonInteractive = $true }
    if (-not $NonInteractive) {
        $answer = & $ReadAnswer "Папка установки [$InstallDirectory]"
        if (-not [string]::IsNullOrWhiteSpace($answer)) { $InstallDirectory = $answer.Trim() }
    }
    if (-not $Reconfigure -and [IO.File]::Exists([IO.Path]::Combine($InstallDirectory, '.omnibox-ai-install'))) { $NonInteractive = $true }
    $config = & $ConfigLoader $InstallDirectory
    if (-not $DefaultBackend) { $DefaultBackend = $config.default_backend }
    if (-not $NonInteractive) {
        $answer = & $ReadAnswer "Сервер по умолчанию ($($config.backends.Keys -join ', ')) [$DefaultBackend]"
        if (-not [string]::IsNullOrWhiteSpace($answer)) { $DefaultBackend = $answer.Trim().ToLowerInvariant() }
    }
    if (-not $config.backends.Contains($DefaultBackend)) { throw "Неизвестный сервер '$DefaultBackend'." }
    $current = & $ContextReader $config.backends[$DefaultBackend]
    if (-not $NonInteractive) {
        $defaultContext = if ($Context) { $Context } else { $current }
        $answer = & $ReadAnswer "Контекст: 32k, 128k, 192k или число токенов [$defaultContext]"
        if (-not [string]::IsNullOrWhiteSpace($answer)) { $Context = $answer.Trim() }
    }
    $tokens = $null
    if ($Context) {
        if ($Context -notmatch '^(?<number>[0-9]+)(?<unit>[kKmM]?)$') { throw 'Контекст: число токенов или 32k/128k/192k.' }
        $number = [long]$Matches.number
        $factor = switch ($Matches.unit.ToLowerInvariant()) { 'k' { 1024 } 'm' { 1048576 } default { 1 } }
        if ($number -lt 1 -or $number -gt 1048576 -or $number * $factor -lt 1024 -or $number * $factor -gt 1048576) { throw 'Контекст должен быть от 1024 до 1048576 токенов.' }
        if ([string]($number * $factor) -ne [string]$current) { $tokens = [int]($number * $factor) }
    }
    $start = $StartServer.IsPresent
    if (-not $NonInteractive) {
        $start = Read-AiYesNo -Prompt 'Запустить выбранный сервер сейчас? Другой управляемый сервер остановится' -ReadAnswer $ReadAnswer
    }
    return @{ directory = $InstallDirectory; config = $config; backend = $DefaultBackend; context = $tokens; start = $start }
}

Export-ModuleMember -Function Get-AiSetupOptions, Read-AiYesNo
