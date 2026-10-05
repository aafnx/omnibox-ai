Set-StrictMode -Version Latest

function Read-AiConfig {
    param([string]$Path)
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        throw "Нет конфигурации: $Path. Запусти install.ps1 или создай config.json из config.example.json."
    }
    $config = Get-Content -LiteralPath $Path -Raw | ConvertFrom-Json -AsHashtable
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
    }
    return $config
}

function Get-AiBackend {
    param($Config, [string]$Name)
    if (-not $Name) { $Name = $Config.default_backend }
    if (-not $Config.backends.Contains($Name)) {
        throw "Неизвестный сервер '$Name'. Доступны: $($Config.backends.Keys -join ', ')."
    }
    return $Config.backends[$Name]
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
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw "Нет XML службы: $path" }
    $settings = [Xml.XmlReaderSettings]::new()
    $settings.DtdProcessing = [Xml.DtdProcessing]::Prohibit
    $settings.XmlResolver = $null
    $reader = [Xml.XmlReader]::Create($path, $settings)
    try {
        $xml = [Xml.XmlDocument]::new()
        $xml.PreserveWhitespace = $true
        $xml.Load($reader)
    } finally { $reader.Dispose() }
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
    $directory = Split-Path $path -Parent
    $suffix = (Get-Date -Format 'yyyyMMdd-HHmmss') + '-' + [Guid]::NewGuid().ToString('N')
    $backup = "$path.backup-$suffix"
    $temp = Join-Path $directory ('.omnibox-ai-xml-' + [Guid]::NewGuid().ToString('N') + '.tmp')
    try {
        $settings = [Xml.XmlWriterSettings]::new()
        $settings.Encoding = [Text.UTF8Encoding]::new($false)
        $writer = [Xml.XmlWriter]::Create($temp, $settings)
        try { $Xml.Save($writer) } finally { $writer.Dispose() }
        # Replace atomically and keep the original XML as backup; all paths are same-volume.
        [IO.File]::Replace($temp, $path, $backup)
        Write-Host "Резервная копия XML: $backup"
    } finally {
        if (Test-Path -LiteralPath $temp) { Remove-Item -LiteralPath $temp -Force }
    }
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
        } elseif ($arg.StartsWith('-') -or $named) { throw "Неизвестный аргумент '$arg'. Использование: ai start/restart [backend] [--context 128k]" }
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
    Invoke-RestMethod -Uri ($Backend.base_url.TrimEnd('/') + $Path) -TimeoutSec 2 -ErrorAction Stop
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
    if ($null -eq $service) { Write-Host "$Name`: служба не установлена, пропущено."; return }
    if ([string]$service.Status -eq 'Stopped') { Write-Host "$Name`: уже остановлен."; return }
    Stop-AiService -Name $backend.service -TimeoutSeconds $Config.stop_timeout_seconds
    Write-Host "$Name`: остановлен."
}

function Wait-AiReady {
    param($Config, [string]$Name)
    $backend = Get-AiBackend $Config $Name
    $watch = [Diagnostics.Stopwatch]::StartNew()
    while ($watch.Elapsed.TotalSeconds -lt $Config.startup_timeout_seconds) {
        $service = Get-AiService $backend.service
        if ($null -eq $service -or [string]$service.Status -eq 'Stopped') {
            throw "$Name`: служба завершилась. Посмотри ai logs $Name."
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
    throw "$Name`: API не готов за $($Config.startup_timeout_seconds) с. Служба не остановлена автоматически; проверь ai status и ai logs $Name."
}

function Start-AiBackend {
    param($Config, [string]$Name)
    if (-not $Name) { $Name = $Config.default_backend }
    $backend = Get-AiBackend $Config $Name
    # Check the target before disturbing any other backend.
    $service = Get-AiService $backend.service
    if ($null -eq $service) { throw "Служба '$($backend.service)' не установлена. ai не устанавливает движки." }
    if ([string]$service.StartType -eq 'Disabled') { throw "Служба '$($backend.service)' отключена. Другие серверы не остановлены." }
    if ($Config.exclusive) {
        foreach ($other in $Config.backends.Keys) {
            if ($other -ne $Name) { Stop-AiBackend $Config $other }
        }
    }
    if ([string]$service.Status -notin @('Running', 'StartPending')) { Start-AiService $backend.service }
    Write-Host "$Name`: ожидание API..."
    Wait-AiReady $Config $Name
    Write-Host "$Name`: API готов — $($backend.base_url)/v1"
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
                }
            } catch { $row.API = 'Unavailable' }
        }
        [pscustomobject]$row
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
    $streams = if ($Stream -eq 'all') { @('err', 'out', 'wrapper') } else { @($Stream) }
    if ($Stream -notin @('all', 'err', 'out', 'wrapper')) { throw 'Журнал: all, err, out или wrapper.' }
    foreach ($item in $streams) {
        $path = Join-Path $backend.log_directory "$($backend.log_prefix).$item.log"
        Write-Host "`n--- $path ---"
        if (Test-Path -LiteralPath $path -PathType Leaf) { Get-Content -LiteralPath $path -Tail $Tail }
        else { Write-Host 'Журнал пока отсутствует.' }
    }
}

function Invoke-AiOllama {
    param($Config, [string[]]$NativeArguments)
    $matches = @($Config.backends.Values | Where-Object type -eq 'ollama')
    if ($matches.Count -ne 1) { throw 'Для передачи команд нужен ровно один backend с type=ollama.' }
    $exe = $matches[0].executable
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

function Show-AiHelp {
    @'
OmniBox AI 0.2.0 — управление локальными серверами

ai start [backend] [--context 128k]  Запуск; по умолчанию bonsai. Другой остановится.
ai stop [bonsai|ollama]        Остановка; без имени — все настроенные серверы.
ai restart [backend] [--context 128k] Перезапуск, при необходимости с новым контекстом.
ai context [backend] [128k]   Просмотр/сохранение контекста для следующего запуска.
ai status                    Службы, API, модель, контекст, сон и видеопамять.
ai logs [backend] [N] [stream] Последние N строк; stream: all, err, out, wrapper.
ai endpoint [backend]         OpenAI-совместимый адрес (с localhost самого ПК).
ai config                    Путь и содержимое конфигурации.
ai ollama [любые аргументы]   Передача в Ollama; без запуска/переключения служб.
ai run/pull/list/ps/show/...  Сокращения команд Ollama, не команды Bonsai.
ai version                   Версия ai.
ai help                      Эта справка.

Перед игрой: ai stop. Статус и журналы не загружают модель.
Контекст: 32k=32768, 128k=131072, 192k=196608 токенов.
ai context bonsai 128k — сохранить, без перезапуска работающего сервера.
ai restart bonsai --context 128k — сохранить и применить сейчас.
ai start --context не меняет уже работающую модель: используй restart.
У Ollama это default OLLAMA_CONTEXT_LENGTH; модель/API могут переопределить num_ctx.
Автозапуск и таймер простоя не меняются. Перед изменением XML создаётся backup.
'@
}

function Invoke-Ai {
    param([AllowEmptyCollection()][string[]]$Arguments = @(), [string]$ConfigPath)
    $command = if ($Arguments.Count) { $Arguments[0].ToLowerInvariant() } else { 'help' }
    [string[]]$rest = @(if ($Arguments.Count -gt 1) { $Arguments[1..($Arguments.Count - 1)] })
    if ($command -in @('help', '--help', '-h')) { Show-AiHelp; return }
    if ($command -in @('version', '--version')) { '0.2.0'; return }
    $config = Read-AiConfig $ConfigPath
    switch ($command) {
        { $_ -in @('start', 'stop', 'restart') } {
            if ($command -eq 'stop') {
                if ($rest.Count -gt 1) { throw 'Использование: ai stop [backend]' }
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
                switch ($command) {
                    'start' {
                        if ($null -ne $preparedXml) { Save-AiServiceXml $backend $preparedXml }
                        Start-AiBackend $config $name
                    }
                    'restart' {
                        Stop-AiBackend $config $name
                        if ($null -ne $preparedXml) { Save-AiServiceXml $backend $preparedXml }
                        Start-AiBackend $config $name
                    }
                    'stop' {
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
        }
        'context' {
            if ($rest.Count -gt 2) { throw 'Использование: ai context [backend] [128k]' }
            $name = $config.default_backend; $value = $null
            if ($rest.Count -eq 2) { $name = $rest[0]; $value = $rest[1] }
            elseif ($rest.Count -eq 1) {
                if ($config.backends.Contains($rest[0])) { $name = $rest[0] }
                elseif ($rest[0] -match '^\d') { $value = $rest[0] }
                else { throw "Неизвестный сервер '$($rest[0])'." }
            }
            $backend = Get-AiBackend $config $name
            if ($null -eq $value) {
                Write-Host "$name`: настроенный контекст = $(Get-AiConfiguredContext $backend) токенов. Фактический: ai status / ai ollama ps."
            } else {
                $tokens = ConvertTo-AiContext $value
                Assert-AiAdministrator
                Invoke-AiLocked {
                    $preparedXml = New-AiContextXml $backend $tokens
                    Save-AiServiceXml $backend $preparedXml
                    Write-Host "$name`: сохранено $tokens токенов для следующего запуска. Работающий сервер не перезапущен. Применить сейчас: ai restart $name"
                }
            }
        }
        'status' {
            if ($rest.Count) { throw 'Использование: ai status' }
            $rows = @(Get-AiStatus $config)
            $rows | Format-Table Backend, Engine, Service, Startup, API, Context, Sleeping -AutoSize | Out-Host
            foreach ($row in $rows) { Write-Host "$($row.Backend) model: $($row.Model)" }
            Show-AiGpu
        }
        'logs' {
            if ($rest.Count -gt 3) { throw 'Использование: ai logs [backend] [N] [all|err|out|wrapper]' }
            $name = if ($rest.Count -ge 1) { $rest[0] } else { $config.default_backend }
            $tail = if ($rest.Count -ge 2) { [int]$rest[1] } else { 60 }
            $stream = if ($rest.Count -ge 3) { $rest[2] } else { 'all' }
            Show-AiLogs $config $name $tail $stream
        }
        'endpoint' {
            if ($rest.Count -gt 1) { throw 'Использование: ai endpoint [backend]' }
            $name = if ($rest.Count) { $rest[0] } else { $config.default_backend }
            (Get-AiBackend $config $name).base_url.TrimEnd('/') + '/v1'
        }
        'config' {
            if ($rest.Count) { throw 'Использование: ai config' }
            Write-Host $ConfigPath
            Get-Content -LiteralPath $ConfigPath -Raw
        }
        'ollama' { Invoke-AiOllama $config $rest }
        { $_ -in @('run', 'pull', 'push', 'list', 'ps', 'show', 'cp', 'rm', 'create') } {
            Invoke-AiOllama $config $Arguments
        }
        default { throw "Неизвестная команда '$command'. Справка: ai help" }
    }
}

Export-ModuleMember -Function Invoke-Ai
