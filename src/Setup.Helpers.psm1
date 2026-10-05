Set-StrictMode -Version Latest

function Get-AiSetupOptions {
    param([string]$InstallDirectory = 'C:\tools\ai', [scriptblock]$ConfigLoader, [scriptblock]$ContextReader,
        [switch]$NonInteractive, [string]$DefaultBackend = '', [string]$Context = '', [switch]$StartServer,
        [scriptblock]$ReadAnswer = { param($Prompt) Read-Host $Prompt })
    if (-not $NonInteractive) {
        $answer = & $ReadAnswer "Папка установки [$InstallDirectory]"
        if (-not [string]::IsNullOrWhiteSpace($answer)) { $InstallDirectory = $answer.Trim() }
    }
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
        $answer = & $ReadAnswer 'Запустить выбранный сервер сейчас? При exclusive=true другой сервер остановится [нет]'
        if ([string]::IsNullOrWhiteSpace($answer) -or $answer.Trim().ToLowerInvariant() -in @('нет', 'н', 'no', 'n')) { $start = $false }
        elseif ($answer.Trim().ToLowerInvariant() -in @('да', 'д', 'yes', 'y')) { $start = $true }
        else { throw 'Ответ на запуск: да или нет.' }
    }
    return @{ directory = $InstallDirectory; config = $config; backend = $DefaultBackend; context = $tokens; start = $start }
}

Export-ModuleMember -Function Get-AiSetupOptions
