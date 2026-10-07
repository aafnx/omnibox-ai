Set-StrictMode -Version Latest
Import-Module (Join-Path $PSScriptRoot 'File.Read.psm1')

$script:AiVersion = (Get-Content -LiteralPath (Join-Path (Split-Path $PSScriptRoot -Parent) 'VERSION') -Raw).Trim()

function Read-AiConfig {
    param([string]$Path)
    try { $text = Read-AiSharedText $Path }
    catch [IO.FileNotFoundException], [IO.DirectoryNotFoundException] {
        throw "Нет конфигурации: $Path. Запусти install.ps1 или создай config.json из config.example.json."
    }
    $config = $text | ConvertFrom-Json -AsHashtable
    if ($config.schema_version -ne 1) { throw 'Неподдерживаемая schema_version; ожидается 1.' }
    if ($config.backends -isnot [System.Collections.IDictionary] -or $config.backends.Count -eq 0) {
        throw 'backends должен содержать хотя бы один сервер.'
    }
    if (-not $config.backends.Contains($config.default_backend)) { throw 'default_backend не найден в backends.' }
    if ($config.exclusive -isnot [bool]) { throw 'exclusive должен быть true или false.' }
    foreach ($key in @('startup_timeout_seconds', 'stop_timeout_seconds')) {
        if ($config[$key] -isnot [long] -and $config[$key] -isnot [int]) { throw "$key должен быть целым числом." }
        if ($config[$key] -lt 1 -or $config[$key] -gt 3600) { throw "$key должен быть от 1 до 3600." }
    }
    $serviceNames = [System.Collections.Generic.HashSet[string]]::new([StringComparer]::OrdinalIgnoreCase)
    foreach ($name in $config.backends.Keys) {
        if ($name -notmatch '^[a-z][a-z0-9_-]*$') { throw "Некорректное имя сервера: $name" }
        $backend = $config.backends[$name]
        if ($backend -isnot [System.Collections.IDictionary]) { throw "Некорректная конфигурация $name." }
        if ($backend.type -notin @('prism', 'ollama')) { throw "Неизвестный движок: $($backend.type)" }
        if ($backend.service -notmatch '^[a-zA-Z0-9_.-]+$') { throw "Некорректное имя службы: $name" }
        if (-not $serviceNames.Add($backend.service)) { throw 'Разные серверы не могут управлять одной службой.' }
        $uri = $null
        if (-not [Uri]::TryCreate($backend.base_url, [UriKind]::Absolute, [ref]$uri) -or
            $uri.Scheme -ne 'http' -or -not $uri.IsLoopback -or $uri.UserInfo -or
            $uri.AbsolutePath -ne '/' -or $uri.Query -or $uri.Fragment) {
            throw "base_url для $name должен быть локальным HTTP-адресом без пути, например http://127.0.0.1:18080."
        }
        foreach ($key in @('log_directory', 'log_prefix', 'service_config')) {
            if ([string]::IsNullOrWhiteSpace($backend[$key])) { throw "Не задан $key для $name." }
        }
        if ($backend.log_prefix -notmatch '^[a-zA-Z0-9_.-]+$') { throw "Некорректный log_prefix для $name." }
        if ($backend.type -eq 'ollama' -and [string]::IsNullOrWhiteSpace($backend.executable)) {
            throw "Не задан executable для $name."
        }
        if ($backend.Contains('models_directory') -and ($backend.models_directory -isnot [string] -or -not [IO.Path]::IsPathFullyQualified($backend.models_directory))) { throw "models_directory для $name должен быть полным путём." }
        if($backend.Contains('active_model') -and ($backend.active_model -isnot [string] -or $backend.active_model.Length -gt 200 -or $backend.active_model -notmatch '^[a-zA-Z0-9][a-zA-Z0-9_./:-]*$' -or $backend.active_model.Contains('..'))){throw "Некорректное имя выбранной модели: $name"}
    }
    return $config
}

function Get-AiBackend {
    param($Config, [string]$Name)
    if (-not $Name) { $Name = $Config.default_backend }
    if (-not $Config.backends.Contains($Name)) {
        throw "Неизвестный движок '$Name'. Доступны: $(@($Config.backends.Keys | ForEach-Object { Get-AiCliName $Config $_ }) -join ', ')."
    }
    return $Config.backends[$Name]
}

# Messages name engines by runtime (PrismML, Ollama); the config key stays a service detail.
function Get-AiEngineName {
    param($Config, [string]$Name)
    if (-not $Config.backends.Contains($Name)) { return $Name }
    $type = $Config.backends[$Name].type
    $label = switch ($type) { 'prism' { 'PrismML' } 'ollama' { 'Ollama' } default { $Name } }
    if (@($Config.backends.Values | Where-Object { $_.type -eq $type }).Count -gt 1) { return "$label ($Name)" }
    return $label
}

# The name to type in ai commands: prismml for the single PrismML engine.
function Get-AiCliName {
    param($Config, [string]$Name)
    if ($Config.backends.Contains($Name) -and $Config.backends[$Name].type -eq 'prism' -and -not $Config.backends.Contains('prismml') -and
        @($Config.backends.Values | Where-Object { $_.type -eq 'prism' }).Count -eq 1) { return 'prismml' }
    return $Name
}

function ConvertTo-AiContext {
    param([string]$Value)
    if ($Value -notmatch '^(?<number>[0-9]+)(?<unit>[kKmM]?)$') { throw 'Контекст: целое число токенов или 32k/128k/192k.' }
    $number = [long]$Matches.number
    $factor = switch ($Matches.unit.ToLowerInvariant()) { 'k' { 1024 } 'm' { 1048576 } default { 1 } }
    if ($number -lt 1 -or $number -gt 1048576) { throw 'Контекст должен быть от 1024 до 1048576 токенов.' }
    $tokens = $number * $factor
    if ($tokens -lt 1024 -or $tokens -gt 1048576) { throw 'Контекст должен быть от 1024 до 1048576 токенов.' }
    return [int]$tokens
}

function Read-AiServiceXml {
    param($Backend)
    $path = $Backend.service_config
    try { $text = Read-AiSharedText $path }
    catch [IO.FileNotFoundException], [IO.DirectoryNotFoundException] { throw "Нет XML службы: $path" }
    $settings = [Xml.XmlReaderSettings]::new()
    $settings.DtdProcessing = [Xml.DtdProcessing]::Prohibit
    $settings.XmlResolver = $null
    $source = [IO.StringReader]::new($text)
    $reader = [Xml.XmlReader]::Create($source, $settings)
    try {
        $xml = [Xml.XmlDocument]::new()
        $xml.PreserveWhitespace = $true
        $xml.Load($reader)
    } finally { $reader.Dispose(); $source.Dispose() }
    $ids = @($xml.SelectNodes('/service/id'))
    if ($ids.Count -ne 1 -or $ids[0].InnerText -cne $Backend.service) {
        throw "XML не принадлежит службе '$($Backend.service)'; изменения отменены."
    }
    return ,$xml
}

function Get-AiPrismArguments {
    param([Xml.XmlDocument]$Xml)
    $nodes = @($Xml.SelectNodes('/service/arguments'))
    if ($nodes.Count -ne 1) { throw 'Для изменения контекста нужен один элемент <arguments> в XML.' }
    # Keep quoted paths/prompts intact: do not match -c embedded in a quoted filename.
    return @([regex]::Matches($nodes[0].InnerText, '"(?:[^"]|"")*"|\S+') | ForEach-Object Value)
}

function Get-AiConfiguredContext {
    param($Backend)
    $xml = Read-AiServiceXml $Backend
    if ($Backend.type -eq 'ollama') {
        $settings = @($xml.SelectNodes('/service/env') | Where-Object { $_.GetAttribute('name') -eq 'OLLAMA_CONTEXT_LENGTH' })
        if ($settings.Count -gt 1) { throw 'В XML повторяется OLLAMA_CONTEXT_LENGTH.' }
        if ($settings.Count) { return $settings[0].GetAttribute('value') }
        return '(runtime default)'
    }
    $tokens = @(Get-AiPrismArguments $xml)
    $value = '(model default)'
    for ($i = 0; $i -lt $tokens.Count; $i++) {
        if ($tokens[$i] -in @('-c', '--ctx-size')) {
            if ($i + 1 -ge $tokens.Count) { throw 'В XML у параметра контекста нет значения.' }
            $value = $tokens[++$i].Trim('"')
        } elseif ($tokens[$i] -match '^--ctx-size=(.+)$') { $value = $Matches[1].Trim('"') }
    }
    return $value
}

function New-AiContextXml {
    param($Backend, [int]$Context)
    $xml = Read-AiServiceXml $Backend
    if ($Backend.type -eq 'ollama') {
        $settings = @($xml.SelectNodes('/service/env') | Where-Object { $_.GetAttribute('name') -eq 'OLLAMA_CONTEXT_LENGTH' })
        if ($settings.Count -gt 1) { throw 'В XML повторяется OLLAMA_CONTEXT_LENGTH; изменения отменены.' }
        $node = if ($settings.Count) { $settings[0] } else { $xml.CreateElement('env') }
        $node.SetAttribute('name', 'OLLAMA_CONTEXT_LENGTH')
        $node.SetAttribute('value', [string]$Context)
        if (-not $settings.Count) { $null = $xml.DocumentElement.AppendChild($node) }
    } else {
        $tokens = @(Get-AiPrismArguments $xml)
        $updated = [Collections.Generic.List[string]]::new()
        for ($i = 0; $i -lt $tokens.Count; $i++) {
            if ($tokens[$i] -in @('-c', '--ctx-size')) {
                if ($i + 1 -ge $tokens.Count -or $tokens[$i + 1].Trim('"') -notmatch '^\d+$') {
                    throw 'Некорректный параметр контекста в XML; изменения отменены.'
                }
                $i++
            } elseif ($tokens[$i] -match '^--ctx-size=(.*)$') {
                if ($Matches[1].Trim('"') -notmatch '^\d+$') { throw 'Некорректный параметр контекста в XML; изменения отменены.' }
            } else { $updated.Add($tokens[$i]) }
        }
        $updated.Add('-c'); $updated.Add([string]$Context)
        $xml.SelectSingleNode('/service/arguments').InnerText = $updated -join ' '
    }
    return ,$xml
}

function Save-AiServiceXml {
    param($Backend, [Xml.XmlDocument]$Xml)
    $path = [IO.Path]::GetFullPath($Backend.service_config)
    $history = Read-AiRecoveryState $Backend
    $paths = @($path, "$path.ai-previous", "$path.ai-state.json")
    $snapshot = @{}
    foreach ($item in $paths) { $snapshot[$item] = if ([IO.File]::Exists($item)) { [IO.File]::ReadAllBytes($item) } else { $null } }
    $stream = [IO.MemoryStream]::new()
    try {
        $settings = [Xml.XmlWriterSettings]::new()
        $settings.Encoding = [Text.UTF8Encoding]::new($false)
        $settings.CloseOutput = $false
        $writer = [Xml.XmlWriter]::Create($stream, $settings)
        try { $Xml.Save($writer) } finally { $writer.Dispose() }
        $bytes = $stream.ToArray()
        # Consecutive unapplied edits keep the same last working configuration.
        $previous = if ($null -ne $history -and $history.pending) { $snapshot["$path.ai-previous"] } else { $snapshot[$path] }
        $state = @{ schema_version = 1; current_sha256 = Get-AiBytesHash $bytes; previous_sha256 = Get-AiBytesHash $previous; pending = $true }
        try {
            Write-AiAtomicBytes "$path.ai-previous" $previous
            Write-AiAtomicBytes $path $bytes
            Write-AiRecoveryState $Backend $state
        } catch {
            $cause = $_.Exception.Message
            $errors = @()
            foreach ($item in $paths) {
                try {
                    if ($null -eq $snapshot[$item]) { if ([IO.File]::Exists($item)) { [IO.File]::Delete($item) } }
                    else { Write-AiAtomicBytes $item $snapshot[$item] }
                } catch { $errors += $_.Exception.Message }
            }
            if ($errors.Count) { throw "Не удалось сохранить XML: $cause. Восстановление файлов: $($errors -join '; ')" }
            throw "Не удалось сохранить XML; прежние файлы восстановлены: $cause"
        }
    } finally { $stream.Dispose() }
}

function Get-AiBytesHash {
    param([byte[]]$Bytes)
    return [Convert]::ToHexString([Security.Cryptography.SHA256]::HashData($Bytes)).ToLowerInvariant()
}

function Write-AiAtomicBytes {
    param([string]$Path, [byte[]]$Bytes)
    $temp = "$Path.tmp-$([Guid]::NewGuid().ToString('N'))"
    try {
        [IO.File]::WriteAllBytes($temp, $Bytes)
        if ([IO.File]::Exists($Path)) { [IO.File]::Replace($temp, $Path, [System.Management.Automation.Language.NullString]::Value) }
        else { [IO.File]::Move($temp, $Path) }
    } finally { if ([IO.File]::Exists($temp)) { [IO.File]::Delete($temp) } }
}

function Write-AiRecoveryState {
    param($Backend, $State)
    $bytes = [Text.UTF8Encoding]::new($false).GetBytes(($State | ConvertTo-Json -Compress))
    Write-AiAtomicBytes "$($Backend.service_config).ai-state.json" $bytes
}

function Read-AiRecoveryState {
    param($Backend)
    $path = $Backend.service_config
    $stateExists = [IO.File]::Exists("$path.ai-state.json")
    $previousExists = [IO.File]::Exists("$path.ai-previous")
    if (-not $stateExists -and -not $previousExists) { return $null }
    if (-not $stateExists -or -not $previousExists) { throw 'История отката неполная. Проверь файлы .ai-state.json и .ai-previous рядом с XML.' }
    $state = Read-AiSharedText "$path.ai-state.json" | ConvertFrom-Json -AsHashtable
    if ($state.schema_version -ne 1 -or $state.pending -isnot [bool] -or
        $state.current_sha256 -notmatch '^[a-f0-9]{64}$' -or $state.previous_sha256 -notmatch '^[a-f0-9]{64}$') {
        throw 'Некорректная история отката; XML не изменён.'
    }
    if ((Get-AiBytesHash ([IO.File]::ReadAllBytes($path))) -cne $state.current_sha256 -or
        (Get-AiBytesHash ([IO.File]::ReadAllBytes("$path.ai-previous"))) -cne $state.previous_sha256) {
        throw 'XML или предыдущие настройки изменены вне ai. Автоматическая перезапись отменена; проверь историю отката.'
    }
    return $state
}

function Restore-AiServiceXml {
    param($Backend)
    $state = Read-AiRecoveryState $Backend
    if ($null -eq $state) { throw 'Нет предыдущих настроек. Они появятся после изменения контекста через ai.' }
    $path = $Backend.service_config
    Write-AiAtomicBytes $path ([IO.File]::ReadAllBytes("$path.ai-previous"))
    $state.current_sha256 = $state.previous_sha256
    $state.pending = $false
    Write-AiRecoveryState $Backend $state
}

function Invoke-AiBackendStart {
    param($Config, [string]$Name, [string]$Command, [AllowNull()][Xml.XmlDocument]$PreparedXml)
    $backend = Get-AiBackend $Config $Name
    $service = Get-AiService $backend.service
    if ($null -eq $service) { throw "Служба '$($backend.service)' не установлена." }
    if ([string]$service.StartType -eq 'Disabled') { throw 'Целевая служба отключена; XML не изменён.' }
    $state = Read-AiRecoveryState $backend
    $wasRunning = [string]$service.Status -eq 'Running'
    if ($Command -eq 'start' -and [string]$service.Status -ne 'Stopped' -and
        ($null -ne $PreparedXml -or ($null -ne $state -and $state.pending))) {
        throw "Новые настройки ещё не применены. Используй ai restart $Name."
    }
    try {
        if ($Command -eq 'restart') { Stop-AiBackend $Config $Name }
        if ($null -ne $PreparedXml) { Save-AiServiceXml $backend $PreparedXml }
        Start-AiBackend $Config $Name
    } catch {
        $cause = $_.Exception.Message
        try {
            $pending = Read-AiRecoveryState $backend
            if ($null -eq $pending -or -not $pending.pending) { throw "Нет ожидающих применения настроек для отката." }
            # Stop first. Never rewrite configuration under a process that failed to stop.
            Stop-AiBackend $Config $Name
            Restore-AiServiceXml $backend
            if ($wasRunning) { Start-AiBackend $Config $Name }
        } catch { throw "Запуск не выполнен: $cause. Откат: $($_.Exception.Message)" }
        throw "Запуск с новыми настройками не выполнен: $cause. Предыдущий XML восстановлен; сервер $(if ($wasRunning) { 'снова запущен' } else { 'оставлен остановленным' })."
    }
    $state = Read-AiRecoveryState $backend
    if ($null -ne $state -and $state.pending) { $state.pending = $false; Write-AiRecoveryState $backend $state }
}

function Read-AiStartOptions {
    param([string[]]$Arguments, [string]$DefaultBackend)
    $name = $DefaultBackend; $named = $false; $context = $null
    for ($i = 0; $i -lt $Arguments.Count; $i++) {
        $arg = $Arguments[$i]
        if ($arg -in @('--context', '-c')) {
            if ($null -ne $context -or $i + 1 -ge $Arguments.Count) { throw 'Укажи --context один раз со значением, например --context 128k.' }
            $context = ConvertTo-AiContext $Arguments[++$i]
        } elseif ($arg -match '^--context=(.*)$') {
            if ($null -ne $context) { throw 'Укажи --context только один раз.' }
            $context = ConvertTo-AiContext $Matches[1]
        } elseif ($arg.StartsWith('-') -or $named) { throw "Неизвестный аргумент '$arg'. Использование: ai start/restart [движок] [--context 128k]" }
        else { $name = $arg; $named = $true }
    }
    return @{ name = $name; context = $context }
}

# OS adapters are intentionally small; unit tests substitute them without Windows/GPU.
function Assert-AiAdministrator {
    if (-not $IsWindows) { throw 'Управление службами доступно только в Windows.' }
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    try {
        $principal = [Security.Principal.WindowsPrincipal]::new($identity)
        if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
            throw 'Нужна повышенная SSH-сессия или PowerShell, запущенный от администратора.'
        }
    } finally { $identity.Dispose() }
}

function Get-AiService {
    param([string]$Name)
    if (-not $IsWindows) { throw 'Статус служб доступен только в Windows.' }
    try { return Get-Service -Name $Name -ErrorAction Stop }
    catch {
        if ($_.FullyQualifiedErrorId -like 'NoServiceFoundForGivenName*') { return $null }
        throw
    }
}

function Start-AiService {
    param([string]$Name)
    Start-Service -Name $Name -ErrorAction Stop
}

function Stop-AiService {
    param([string]$Name, [int]$TimeoutSeconds)
    Stop-Service -Name $Name -ErrorAction Stop
    $service = Get-AiService $Name
    if ($null -ne $service) { $service.WaitForStatus('Stopped', [TimeSpan]::FromSeconds($TimeoutSeconds)) }
}

function Invoke-AiProbe {
    param($Backend, [string]$Path)
    $headers=@{};if($Backend.Contains('upstream_api_key') -and $Backend.upstream_api_key){$headers.Authorization='Bearer '+$Backend.upstream_api_key}
    Invoke-RestMethod -Uri ($Backend.base_url.TrimEnd('/') + $Path) -Headers $headers -TimeoutSec 2 -ErrorAction Stop
}

function Invoke-AiDelay { Start-Sleep -Milliseconds 500 }

function Invoke-AiLocked {
    param([scriptblock]$Action)
    $mutex = [Threading.Mutex]::new($false, 'Global\omnibox-ai-control-v1')
    $acquired = $false
    try {
        try { $acquired = $mutex.WaitOne([TimeSpan]::FromSeconds(10)) }
        catch [Threading.AbandonedMutexException] { $acquired = $true }
        if (-not $acquired) { throw 'Другая команда ai ещё управляет серверами. Повтори после её завершения.' }
        & $Action
    } finally {
        if ($acquired) { $mutex.ReleaseMutex() }
        $mutex.Dispose()
    }
}

function Stop-AiBackend {
    param($Config, [string]$Name)
    $backend = Get-AiBackend $Config $Name
    $service = Get-AiService $backend.service
    $engine = Get-AiEngineName $Config $Name
    if ($null -eq $service) { Write-Host "$engine`: служба не установлена."; return }
    if ([string]$service.Status -eq 'Stopped') { Write-Host "$engine`: уже остановлен."; return }
    Stop-AiService -Name $backend.service -TimeoutSeconds $Config.stop_timeout_seconds
    Write-Host "$engine`: остановлен."
}

function Wait-AiReady {
    param($Config, [string]$Name)
    $backend = Get-AiBackend $Config $Name
    $watch = [Diagnostics.Stopwatch]::StartNew()
    while ($watch.Elapsed.TotalSeconds -lt $Config.startup_timeout_seconds) {
        $service = Get-AiService $backend.service
        if ($null -eq $service -or [string]$service.Status -eq 'Stopped') {
            throw "$(Get-AiEngineName $Config $Name): служба завершилась. Журнал: ai logs $(Get-AiCliName $Config $Name)."
        }
        try {
            if ($backend.type -eq 'prism') {
                $health = Invoke-AiProbe $backend '/health'
                if ($health.status -eq 'ok') { return }
            } else {
                $health = Invoke-AiProbe $backend '/api/version'
                if ($health.version) { return }
            }
        } catch { Write-Verbose $_.Exception.Message }
        Invoke-AiDelay
    }
    throw "$(Get-AiEngineName $Config $Name): API не ответил за $($Config.startup_timeout_seconds) с. Служба оставлена как есть; проверь ai status и ai logs $(Get-AiCliName $Config $Name)."
}

function Set-AiSingleModelLimits {
    param($Config,[string]$Name,$Service)
    $backend=Get-AiBackend $Config $Name
    $xml=Read-AiServiceXml $backend;$original=$xml.OuterXml;$xml=New-AiSingleModelXml $backend $xml
    if($xml.OuterXml -cne $original){
        if([string]$Service.Status -ne 'Stopped'){Stop-AiBackend $Config $Name}
        Save-AiServiceXml $backend $xml
    }
}

function Start-AiBackend {
    param($Config, [string]$Name)
    if (-not $Name) { $Name = $Config.default_backend }
    $backend = Get-AiBackend $Config $Name
    # Check the target before disturbing any other backend.
    $service = Get-AiService $backend.service
    if ($null -eq $service) { throw "Служба '$($backend.service)' не установлена. ai не устанавливает движки." }
    if ([string]$service.StartType -eq 'Disabled') { throw "Служба '$($backend.service)' отключена. Другие серверы не остановлены." }
    if ($backend.type -eq 'prism' -and (Test-Path -LiteralPath $backend.service_config -PathType Leaf) -and -not (Get-AiLaunchSettings $backend).model) { throw "$(Get-AiEngineName $Config $Name): модель не выбрана. Выберите GGUF в разделе «Модели и настройки»." }
    Set-AiSingleModelLimits $Config $Name $service
    $service=Get-AiService $backend.service
    foreach ($other in $Config.backends.Keys) {
        if ($other -ne $Name) { Stop-AiBackend $Config $other }
    }
    if ([string]$service.Status -notin @('Running', 'StartPending')) { Start-AiService $backend.service }
    Write-Host "$(Get-AiEngineName $Config $Name): запуск…"
    Wait-AiReady $Config $Name
    $state=Read-AiRecoveryState $backend
    if($null -ne $state -and $state.pending){$state.pending=$false;Write-AiRecoveryState $backend $state}
    Write-Host "$(Get-AiEngineName $Config $Name): готов · $($backend.base_url)/v1"
}

function Get-AiStatus {
    param($Config)
    foreach ($name in $Config.backends.Keys) {
        $backend = $Config.backends[$name]
        $service = Get-AiService $backend.service
        $row = [ordered]@{
            Backend = $name; Engine = $backend.type
            Service = if ($null -eq $service) { 'Missing' } else { [string]$service.Status }
            Startup = if ($null -eq $service) { '-' } else { [string]$service.StartType }
            API = '-'; Model = '-'; Context = $null; Sleeping = $null
        }
        # Never contact a stopped backend; status must not start anything.
        if ($null -ne $service -and [string]$service.Status -eq 'Running') {
            try {
                if ($backend.type -eq 'prism') {
                    $props = Invoke-AiProbe $backend '/props'
                    $row.API = 'Ready'
                    $row.Sleeping = $props.is_sleeping
                    $row.Context = $props.default_generation_settings.n_ctx
                    if ($props.PSObject.Properties['model_path']) { $row.Model = [IO.Path]::GetFileName($props.model_path) }
                } else {
                    $null = Invoke-AiProbe $backend '/api/version'
                    $ps = Invoke-AiProbe $backend '/api/ps'
                    $row.API = 'Ready'
                    $models = @($ps.models | ForEach-Object { $_.name })
                    $row.Model = if ($models.Count) { $models -join ', ' } else { '(none)' }
                    if(@($ps.models).Count -eq 1 -and $ps.models[0].PSObject.Properties['context_length']){$row.Context=$ps.models[0].context_length}
                }
            } catch { $row.API = 'Unavailable' }
        }
        [pscustomobject]$row
    }
}

# Engine first, then its state and model: "PrismML: работает · API готов".
function Format-AiStatusRow {
    param($Config, $Row)
    $state = switch ($Row.Service) { 'Running' { 'работает' } 'Stopped' { 'остановлен' } 'StartPending' { 'запускается' } 'StopPending' { 'останавливается' } 'Missing' { 'служба не установлена' } default { $Row.Service } }
    $api = switch ($Row.API) { 'Ready' { ' · API готов' } 'Unavailable' { ' · API недоступен' } default { '' } }
    "$(Get-AiEngineName $Config $Row.Backend): $state$api"
    if ($Row.Service -eq 'Running') {
        $model = if ($Row.Model -in @('-', '(none)', '')) { 'не загружена' } else { $Row.Model }
        $details = @("модель: $model")
        if ($null -ne $Row.Context) { $details += "контекст $($Row.Context)" }
        if ($Row.Sleeping) { $details += 'модель выгружена до запроса' }
        '  ' + ($details -join ' · ')
    }
}

function Show-AiGpu {
    $nvidia = Get-Command nvidia-smi -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($null -eq $nvidia) { Write-Host 'nvidia-smi не найден; видеопамять не показана.'; return }
    $savedExit = $global:LASTEXITCODE
    try {
        Write-Host "`nGPU:"
        & $nvidia.Source --query-gpu=name,memory.total,memory.used,memory.free --format=csv
    } finally { $global:LASTEXITCODE = $savedExit }
}

function Show-AiLogs {
    param($Config, [string]$Name, [int]$Tail = 60, [string]$Stream = 'all')
    $backend = Get-AiBackend $Config $Name
    if ($Tail -lt 1 -or $Tail -gt 10000) { throw 'Число строк должно быть от 1 до 10000.' }
    $streams = if ($Stream -eq 'all') { @('err', 'out', 'host') } else { @($Stream) }
    if ($Stream -notin @('all', 'err', 'out', 'host', 'wrapper')) { throw 'Журнал: all, err, out или host.' }
    foreach ($item in $streams) {
        $path = Join-Path $backend.log_directory "$($backend.log_prefix).$item.log"
        Write-Host "`n--- $path ---"
        if (Test-Path -LiteralPath $path -PathType Leaf) { Get-Content -LiteralPath $path -Tail $Tail }
        else { Write-Host 'Журнал пока отсутствует.' }
    }
}

function Invoke-AiOllama {
    param($Config, [string[]]$NativeArguments, [string]$ConfigPath = '')
    $matches = @($Config.backends.Values | Where-Object type -eq 'ollama')
    if ($matches.Count -ne 1) { throw 'Для передачи команд нужен ровно один backend с type=ollama.' }
    if($NativeArguments.Count -and $NativeArguments[0] -eq 'serve'){throw 'Используй ai start ollama: отдельный serve не поддерживается.'}
    if($NativeArguments.Count -gt 1 -and $NativeArguments[0] -eq 'run' -and -not $NativeArguments[1].StartsWith('-')){
        Assert-AiModelName $NativeArguments[1]
        if(-not $ConfigPath){throw 'Для run нужен путь конфигурации.'}
        $name=@($Config.backends.Keys|Where-Object{$Config.backends[$_].type -eq 'ollama'})[0]
        Assert-AiAdministrator
        Invoke-AiLocked {
            $service=Get-AiService $matches[0].service
            if($null -eq $service -or [string]$service.Status -ne 'Running'){throw 'Сначала запусти ai start ollama.'}
            Start-AiBackend $Config $name
            Clear-AiOtherOllamaModels $matches[0] $NativeArguments[1] $Config.stop_timeout_seconds
            Set-AiSelectedModel $ConfigPath $name $NativeArguments[1]
        }
    }
    # The profile names the version the service runs; config keeps the path of the original install.
    $exe = $matches[0].executable
    try { $profileExe = Get-AiProfileExecutable (Read-AiServiceXml $matches[0]) $matches[0].service_config; if ($profileExe -and (Test-Path -LiteralPath $profileExe -PathType Leaf)) { $exe = $profileExe } } catch { }
    if (-not (Test-Path -LiteralPath $exe -PathType Leaf)) { throw "Ollama не найден: $exe" }
    $oldHost = [Environment]::GetEnvironmentVariable('OLLAMA_HOST', 'Process')
    $oldModels = [Environment]::GetEnvironmentVariable('OLLAMA_MODELS', 'Process')
    try {
        # Point the client at our configured server, not an unrelated global OLLAMA_HOST.
        $env:OLLAMA_HOST = $matches[0].base_url.TrimEnd('/')
        $xmlPath = $matches[0].service_config
        if (Test-Path -LiteralPath $xmlPath -PathType Leaf) {
            $xml = [xml](Get-Content -LiteralPath $xmlPath -Raw)
            $modelSetting = @($xml.SelectNodes('/service/env') | Where-Object { $_.GetAttribute('name') -eq 'OLLAMA_MODELS' })
            if ($modelSetting.Count) { $env:OLLAMA_MODELS = $modelSetting[0].GetAttribute('value') }
        }
        $PSNativeCommandArgumentPassing = 'Standard'
        & $exe @NativeArguments
    } finally {
        [Environment]::SetEnvironmentVariable('OLLAMA_HOST', $oldHost, 'Process')
        [Environment]::SetEnvironmentVariable('OLLAMA_MODELS', $oldModels, 'Process')
    }
}

function Show-AiPower {
    $powercfg = Join-Path $env:SystemRoot 'System32/powercfg.exe'
    $savedExit = $global:LASTEXITCODE
    $failures = @()
    try {
        foreach ($arguments in @(@('/lastwake'), @('/waketimers'), @('/requests'), @('/devicequery', 'wake_armed'))) {
            Write-Host "`npowercfg $($arguments -join ' '):"
            & $powercfg @arguments
            if ($LASTEXITCODE -ne 0) { $failures += ($arguments -join ' ') }
        }
    } finally { $global:LASTEXITCODE = $savedExit }
    if ($failures.Count) { throw "powercfg не выполнил: $($failures -join ', ')" }
}

function Show-AiHelp {
    "OmniBox AI $script:AiVersion — управление локальными серверами"
    @'

ai start [движок] [--context 128k]  Запуск; по умолчанию prismml. Другой движок остановится.
ai stop [prismml|ollama]       Остановка; без имени — все настроенные серверы.
ai restart [движок] [--context 128k] Перезапуск, при необходимости с новым контекстом.
ai context [движок] [128k]    Просмотр/сохранение контекста для следующего запуска.
ai restore [движок]          Возврат предыдущих настроек движка.
ai status                    Движки, их модели, контекст и видеопамять.
ai power                     Причины пробуждения, таймеры и блокировки сна Windows.
ai storage [clean]           Место на диске; clean — удалить устаревшие журналы и временные файлы.
ai engine                    Установленные движки и их версии.
ai engine versions prismml    Доступные версии движка (prismml или ollama) на GitHub.
ai engine install ollama [v]  Установить движок или переключить на версию; без версии — последняя.
ai engine update prismml      Обновить движок до последней версии; при сбое запуска — откат.
ai engine remove ollama       Удалить службу и файлы движка; модели остаются на диске.
ai logs [движок] [N] [stream] Последние N строк; stream: all, err, out, host.
ai endpoint [движок]          OpenAI-совместимый адрес (с localhost самого ПК).
ai config                    Путь и содержимое конфигурации.
ai update [--token]           Установка последней версии через gh; --token — ввод токена вручную.
ai update --list              Доступные версии с примечаниями; отмечены последняя и установленная.
ai update 0.9.5               Установка указанной версии: обновление, переустановка или откат.
ai access                    Адреса панели/API, режим ключа, доверенный NAS.
ai password reset [--generate] Сброс пароля панели; Enter — генерация.
ai panel info/reset-password  Те же команды восстановления панели.
ai ollama [аргументы]         Передача в Ollama; serve запрещён, run меняет выбранную модель.
ai run/pull/list/ps/show/...  Сокращения команд Ollama, не команды PrismML.
ai version                   Версия ai.
ai help                      Эта справка.

Остановить все серверы: ai stop. Статус и журналы не загружают модель.
Контекст: 32k=32768, 128k=131072, 192k=196608 токенов.
ai context prismml 128k — сохранить, без перезапуска работающего сервера.
ai restart prismml --context 128k — сохранить и применить сейчас.
prismml — имя движка; его служба Windows по-прежнему называется bonsai, и это имя тоже работает.
ai start --context не меняет уже работающую модель: используй restart.
У Ollama это default OLLAMA_CONTEXT_LENGTH; модель/API могут переопределить num_ctx.
Ошибка запуска после смены контекста: автоматический откат предыдущего XML.
Ollama проверяется до готовности API; ошибки последующей загрузки модели не отслеживаются.
Автозапуск и таймер простоя не меняются. Историей XML управляет ai.
'@
}

function Invoke-Ai {
    param([AllowEmptyCollection()][string[]]$Arguments = @(), [string]$ConfigPath)
    $command = if ($Arguments.Count) { $Arguments[0].ToLowerInvariant() } else { 'help' }
    [string[]]$rest = @(if ($Arguments.Count -gt 1) { $Arguments[1..($Arguments.Count - 1)] })
    if ($command -in @('help', '--help', '-h')) { Show-AiHelp; return }
    if ($command -in @('version', '--version')) { $script:AiVersion; return }
    if ($command -in @('access','panel','password')) {
        $root=Split-Path ([IO.Path]::GetFullPath($ConfigPath)) -Parent
        if($command -eq 'access'){if($rest.Count){throw 'Использование: ai access'};$rest=@('info')}
        if($command -eq 'password'){if(-not $rest.Count -or $rest[0] -ne 'reset'){throw 'ai password reset [--generate]'};$rest=@('reset-password')+@($rest|Select-Object -Skip 1)}
        Invoke-AiPanelCli -Arguments $rest -Root $root
        return
    }
    if($command -eq 'update'){
        $usage='Использование: ai update [версия | --list] [--token]'
        $useToken=@($rest|Where-Object{$_ -eq '--token'}).Count
        $other=@($rest|Where-Object{$_ -ne '--token'})
        if($useToken -gt 1 -or $other.Count -gt 1){throw $usage}
        $list=$false;$version=''
        if($other.Count){
            if($other[0] -in @('--list','list')){$list=$true}
            elseif($other[0] -match '^v?\d+\.\d+\.\d+$'){$version=$other[0].TrimStart('v')}
            else{throw $usage}
        }
        # Listing only reads GitHub; installing changes services and needs elevation.
        if(-not $list){Assert-AiAdministrator}
        $root=Split-Path $PSScriptRoot -Parent
        Import-Module (Join-Path $PSScriptRoot 'Update.Helpers.psm1') -Force
        Invoke-AiUpdate -InstallDirectory $root -UseToken:([bool]$useToken) -Version $version -List:$list
        return
    }
    $config = Read-AiConfig $ConfigPath
    # The engine is shown as PrismML; its service and config key keep the historical name.
    $prism = @($config.backends.Keys | Where-Object { $config.backends[$_].type -eq 'prism' })
    if ($command -in @('start','stop','restart','context','restore','logs','endpoint') -and $prism.Count -eq 1 -and -not $config.backends.Contains('prismml')) {
        $rest = @($rest | ForEach-Object { if ($_ -eq 'prismml') { $prism[0] } else { $_ } })
    }
    switch ($command) {
        { $_ -in @('start', 'stop', 'restart') } {
            if ($command -eq 'stop') {
                if ($rest.Count -gt 1) { throw 'Использование: ai stop [движок]' }
                $options = @{ name = $(if ($rest.Count) { $rest[0] } else { $config.default_backend }); context = $null }
            } else { $options = Read-AiStartOptions $rest $config.default_backend }
            $name = $options.name
            $backend = Get-AiBackend $config $name
            Assert-AiAdministrator
            Invoke-AiLocked {
                $preparedXml = $null
                if ($null -ne $options.context) {
                    $service = Get-AiService $backend.service
                    if ($null -eq $service) { throw "Служба '$($backend.service)' не установлена." }
                    if ([string]$service.StartType -eq 'Disabled') { throw 'Целевая служба отключена; XML не изменён.' }
                    if ($command -eq 'start' -and [string]$service.Status -ne 'Stopped') {
                        throw "Сервер уже работает или меняет состояние. Для нового контекста: ai restart $name --context $($options.context)"
                    }
                    # Validate the XML before stopping a working server.
                    $preparedXml = New-AiContextXml $backend $options.context
                }
                if ($command -ne 'stop') {
                    Invoke-AiBackendStart $config $name $command $preparedXml
                } else {
                        $names = if ($rest.Count) { @($name) } else { @($config.backends.Keys) }
                        # Attempt every backend even if one fails; never hide a failed stop.
                        $failures = @()
                        foreach ($item in $names) {
                            try { Stop-AiBackend $config $item }
                            catch { $failures += "$item`: $($_.Exception.Message)" }
                        }
                        if ($failures.Count) { throw ($failures -join '; ') }
                }
            }
        }
        'restore' {
            if ($rest.Count -gt 1) { throw 'Использование: ai restore [движок]' }
            $name = if ($rest.Count) { $rest[0] } else { $config.default_backend }
            $backend = Get-AiBackend $config $name
            Assert-AiAdministrator
            Invoke-AiLocked {
                $state = Read-AiRecoveryState $backend
                if ($null -eq $state) { throw 'Нет предыдущих настроек для восстановления.' }
                if (-not $state.pending -and $state.current_sha256 -ceq $state.previous_sha256) {
                    Write-Host "$(Get-AiEngineName $config $name): предыдущие настройки уже восстановлены."; return
                }
                $service = Get-AiService $backend.service
                $wasRunning = $null -ne $service -and [string]$service.Status -eq 'Running'
                Stop-AiBackend $config $name
                Restore-AiServiceXml $backend
                if ($wasRunning) { Start-AiBackend $config $name }
                Write-Host "$(Get-AiEngineName $config $name): предыдущие настройки восстановлены."
            }
        }
        'context' {
            if ($rest.Count -gt 2) { throw 'Использование: ai context [движок] [128k]' }
            $name = $config.default_backend; $value = $null
            if ($rest.Count -eq 2) { $name = $rest[0]; $value = $rest[1] }
            elseif ($rest.Count -eq 1) {
                if ($config.backends.Contains($rest[0])) { $name = $rest[0] }
                elseif ($rest[0] -match '^\d') { $value = $rest[0] }
                else { $null = Get-AiBackend $config $rest[0] }
            }
            $backend = Get-AiBackend $config $name
            if ($null -eq $value) {
                Write-Host "$(Get-AiEngineName $config $name): контекст $(Get-AiConfiguredContext $backend) токенов. Фактический — в ai status."
            } else {
                $tokens = ConvertTo-AiContext $value
                Assert-AiAdministrator
                Invoke-AiLocked {
                    $preparedXml = New-AiContextXml $backend $tokens
                    Save-AiServiceXml $backend $preparedXml
                    Write-Host "$(Get-AiEngineName $config $name): контекст $tokens токенов сохранён для следующего запуска. Применить сейчас: ai restart $(Get-AiCliName $config $name)"
                }
            }
        }
        'engine' {
            $usage = 'Использование: ai engine [versions | install | update | remove] [prismml | ollama] [версия]'
            if (-not $rest.Count) {
                foreach ($engine in Get-AiEngineOverview $ConfigPath) {
                    if (-not $engine.installed) { Write-Host "$($engine.label): не установлен · установить: ai engine install $($engine.cli)"; continue }
                    $version = if ($engine.version) { "версия $($engine.version)" } elseif ($engine.executable) { "установлен вне OmniBox: $($engine.executable)" } else { 'версия неизвестна' }
                    $state = if ($engine.state -eq 'Missing') { ' · служба не найдена' } else { '' }
                    Write-Host "$($engine.label): $version$state"
                    if (@($engine.versions).Count -gt 1) { Write-Host "  скачанные версии: $(@($engine.versions) -join ', ')" }
                }
                return
            }
            if ($rest[0] -notin @('versions', 'install', 'update', 'remove') -or $rest.Count -lt 2 -or $rest.Count -gt 3 -or ($rest[0] -in @('versions', 'update', 'remove') -and $rest.Count -gt 2)) { throw $usage }
            $type = if ($config.backends.Contains($rest[1])) { $config.backends[$rest[1]].type } else { ConvertTo-AiEngineType $rest[1] }
            $name = @($config.backends.Keys | Where-Object { $config.backends[$_].type -eq $type }) | Select-Object -First 1
            $label = (Get-AiEngineKind $type).label
            $version = if ($rest.Count -eq 3) { $rest[2] } else { '' }
            if ($rest[0] -eq 'versions') {
                $current = if ($name) { @(Get-AiEngineOverview $ConfigPath | Where-Object type -eq $type)[0].version } else { '' }
                foreach ($release in @(Get-AiEngineReleases $type | Select-Object -First 15)) {
                    $marks = @(); if ($release.prerelease) { $marks += 'предварительная' }; if ($release.tag -ceq $current) { $marks += 'установлена' }
                    Write-Host ("{0} · {1}{2}" -f $release.tag, ([string]$release.published).Substring(0, [Math]::Min(10, ([string]$release.published).Length)), $(if ($marks.Count) { " ($($marks -join ', '))" } else { '' }))
                }
                return
            }
            Assert-AiAdministrator
            if ($rest[0] -eq 'install' -and -not $name) { Write-Host (Install-AiEngine $ConfigPath $type $version); return }
            if (-not $name) { throw "$label не установлен. Установить: ai engine install $((Get-AiEngineKind $type).name)" }
            if ($rest[0] -eq 'remove') { Write-Host (Uninstall-AiEngine $ConfigPath $name); return }
            Write-Host (Set-AiEngineVersion $ConfigPath $name $version)
        }
        'storage' {
            if ($rest.Count -gt 1 -or ($rest.Count -and $rest[0] -ne 'clean')) { throw 'Использование: ai storage [clean]' }
            Import-Module (Join-Path $PSScriptRoot 'Storage.Helpers.psm1')
            $root = Split-Path ([IO.Path]::GetFullPath($ConfigPath)) -Parent
            $mb = { param($Bytes) '{0} МБ' -f [math]::Round($Bytes / 1MB, 1).ToString([Globalization.CultureInfo]::InvariantCulture) }
            if ($rest.Count) {
                Assert-AiAdministrator
                $result = Invoke-AiStorageCleanup $root $config
                Write-Host "Удалено файлов: $($result.removed), освобождено $(& $mb $result.freed_bytes)."
            }
            $usage = Get-AiStorageUsage $root $config
            $keep = if ($usage.log_retention_days) { "хранятся $($usage.log_retention_days) дн." } else { 'срок хранения не ограничен' }
            Write-Host "Журналы: $(& $mb $usage.logs_bytes) (файлов: $($usage.log_files)), $keep"
            if ($usage.logs_over_limit) { Write-Host "Журналы больше порога $($usage.log_warn_mb) МБ. Очистить устаревшие: ai storage clean" }
            Write-Host "Резервные копии: $(& $mb $usage.backups_bytes) · обновления: $(& $mb $usage.updates_bytes) · временные файлы: $(& $mb $usage.temp_bytes)"
            $gb = { param($Bytes) '{0} ГБ' -f [math]::Round($Bytes / 1GB, 1).ToString([Globalization.CultureInfo]::InvariantCulture) }
            $disk = Get-AiDiskOverview $ConfigPath
            foreach ($model in $disk.models) { Write-Host "Модели $($model.engine): $(& $gb $model.bytes) (моделей: $($model.models))$(if ($model.path) { " · $($model.path)" })" }
            foreach ($drive in $disk.drives) {
                $roles = if (@($drive.roles).Count) { " · $(@($drive.roles) -join ', ')" } else { '' }
                Write-Host "Диск $($drive.name): занято $(& $gb $drive.used) из $(& $gb $drive.total), свободно $(& $gb $drive.free)$roles$(if ($drive.low) { ' · мало места' })"
            }
        }
        'status' {
            if ($rest.Count) { throw 'Использование: ai status' }
            $rows = @(Get-AiStatus $config)
            foreach ($row in $rows) { Format-AiStatusRow $config $row | ForEach-Object { Write-Host $_ } }
            Show-AiGpu
        }
        'power' {
            if ($rest.Count) { throw 'Использование: ai power' }
            Assert-AiAdministrator
            Show-AiPower
        }
        'logs' {
            if ($rest.Count -gt 3) { throw 'Использование: ai logs [движок] [N] [all|err|out|host]' }
            $name = if ($rest.Count -ge 1) { $rest[0] } else { $config.default_backend }
            $tail = if ($rest.Count -ge 2) { [int]$rest[1] } else { 60 }
            $stream = if ($rest.Count -ge 3) { $rest[2] } else { 'all' }
            Show-AiLogs $config $name $tail $stream
        }
        'endpoint' {
            if ($rest.Count -gt 1) { throw 'Использование: ai endpoint [движок]' }
            $name = if ($rest.Count) { $rest[0] } else { $config.default_backend }
            (Get-AiBackend $config $name).base_url.TrimEnd('/') + '/v1'
        }
        'config' {
            if ($rest.Count) { throw 'Использование: ai config' }
            Write-Host $ConfigPath
            Get-Content -LiteralPath $ConfigPath -Raw
        }
        'ollama' { Invoke-AiOllama $config $rest $ConfigPath }
        { $_ -in @('run', 'pull', 'push', 'list', 'ps', 'show', 'cp', 'rm', 'create') } {
            Invoke-AiOllama $config $Arguments $ConfigPath
        }
        default { throw "Неизвестная команда '$command'. Справка: ai help" }
    }
}

. (Join-Path $PSScriptRoot 'Panel.Engine.ps1')
. (Join-Path $PSScriptRoot 'Panel.Cli.ps1')
. (Join-Path $PSScriptRoot 'Telemetry.ps1')
. (Join-Path $PSScriptRoot 'Engine.Manager.ps1')
Export-ModuleMember -Function Invoke-Ai, Get-AiDashboard, Invoke-AiPanelAction, Get-AiTelemetry, Get-AiGatewayRuntime, Get-AiEngineOverview, Get-AiEngineReleases, Get-AiDiskOverview
