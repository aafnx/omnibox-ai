# Dot-sourced by OmniBox.Ai.psm1: install, update, switch and remove AI engine runtimes
# (PrismML llama-server, Ollama) from their public GitHub releases.
# Versions live in <install>\engines\<type>\<tag>; a profile points at one of them.
# Network access happens only on a user action; nothing here runs on a timer.

$script:AiEngineKinds = @{
    prism = @{
        label = 'PrismML'; repo = 'PrismML-Eng/llama.cpp'; name = 'prismml'; port = 18080; exe = 'llama-server.exe'
        # CUDA 13 builds support Blackwell (RTX 50); CUDA 12 is the fallback. The cudart archive carries the CUDA DLLs.
        asset_sets = @(
            @('^llama-.+-bin-win-cuda-13\.\d+-x64\.zip$', '^cudart-llama-bin-win-cuda-13\.\d+-x64\.zip$'),
            @('^llama-.+-bin-win-cuda-12\.\d+-x64\.zip$', '^cudart-llama-bin-win-cuda-12\.\d+-x64\.zip$')
        )
    }
    ollama = @{
        label = 'Ollama'; repo = 'ollama/ollama'; name = 'ollama'; port = 11434; exe = 'ollama.exe'
        asset_sets = @(, @('^ollama-windows-amd64\.zip$'))
    }
}
$script:AiEngineAssetLimit = 4GB
Add-Type -AssemblyName System.IO.Compression.FileSystem

function Get-AiEngineKind {
    param([string]$Type)
    if (-not $script:AiEngineKinds.Contains($Type)) { throw "Неизвестный движок: $Type. Доступны: prismml, ollama." }
    return $script:AiEngineKinds[$Type]
}

# prismml/prism and ollama, as typed in the CLI and sent by the panel.
function ConvertTo-AiEngineType {
    param([string]$Value)
    switch ($Value) { { $_ -in @('prismml', 'prism') } { return 'prism' } 'ollama' { return 'ollama' } }
    throw "Неизвестный движок '$Value'. Доступны: prismml, ollama."
}

function Get-AiInstallRoot { param([string]$ConfigPath) Split-Path ([IO.Path]::GetFullPath($ConfigPath)) -Parent }
function Get-AiEnginesRoot { param([string]$ConfigPath) Join-Path (Get-AiInstallRoot $ConfigPath) 'engines' }

function Assert-AiEngineTag {
    param([string]$Tag)
    if ($Tag -notmatch '^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$') { throw 'Версия движка: латиница, цифры, точка, дефис, до 64 символов.' }
}

# Seams for tests: GitHub API, file download, binary check and Windows services.
function Invoke-AiGitHubReleases {
    param([string]$Repository)
    $headers = @{ Accept = 'application/vnd.github+json'; 'User-Agent' = 'omnibox-ai'; 'X-GitHub-Api-Version' = '2022-11-28' }
    return @(Invoke-RestMethod -Uri "https://api.github.com/repos/$Repository/releases?per_page=30" -Headers $headers -TimeoutSec 30 -MaximumRedirection 0)
}
function Invoke-AiEngineDownload {
    param([string]$Uri, [string]$OutFile)
    # The progress display slows multi-gigabyte downloads considerably.
    $ProgressPreference = 'SilentlyContinue'
    Invoke-WebRequest -Uri $Uri -OutFile $OutFile -MaximumRedirection 5 -TimeoutSec 7200 -Headers @{ 'User-Agent' = 'omnibox-ai' }
}
function Test-AiEngineBinary {
    param([string]$Executable)
    $info = [Diagnostics.ProcessStartInfo]::new($Executable, '--version')
    $info.UseShellExecute = $false; $info.RedirectStandardOutput = $true; $info.RedirectStandardError = $true; $info.CreateNoWindow = $true
    $info.WorkingDirectory = Split-Path $Executable -Parent
    $process = [Diagnostics.Process]::Start($info)
    try {
        $output = $process.StandardOutput.ReadToEndAsync(); $errors = $process.StandardError.ReadToEndAsync()
        if (-not $process.WaitForExit(60000)) { $process.Kill($true); throw 'Движок не ответил на --version за 60 с.' }
        $text = $output.Result + $errors.Result
        # A missing CUDA or runtime DLL stops the process before it prints its version.
        if ($text -notmatch '(?i)version') { throw "Движок не запустился: $($text.Trim() | Select-Object -First 1)" }
    } finally { $process.Dispose() }
}
function New-AiEngineService {
    param([string]$Name, [string]$Command, [string]$DisplayName)
    $null = New-Service -Name $Name -BinaryPathName $Command -StartupType Manual -DisplayName $DisplayName
}
function Remove-AiEngineService { param([string]$Name) Remove-Service -Name $Name -ErrorAction Stop }
function Get-AiServiceCommandLine {
    param([string]$Name)
    $service = Get-CimInstance -ClassName Win32_Service -Filter "Name='$Name'" -ErrorAction SilentlyContinue
    if ($null -eq $service) { return $null }
    return [string]$service.PathName
}

# Releases with a matching Windows build, newest first. Drafts are skipped.
function Get-AiEngineReleases {
    param([string]$Type)
    $kind = Get-AiEngineKind $Type
    $result = @()
    foreach ($release in Invoke-AiGitHubReleases $kind.repo) {
        if ($release.draft -or [string]$release.tag_name -notmatch '^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$') { continue }
        $assets = @($release.assets)
        foreach ($set in $kind.asset_sets) {
            $chosen = @()
            foreach ($pattern in $set) {
                $asset = @($assets | Where-Object { [string]$_.name -match $pattern } | Select-Object -First 1)
                if (-not $asset.Count) { $chosen = $null; break }
                $digest = if ($asset[0].PSObject.Properties['digest'] -and [string]$asset[0].digest -match '^sha256:([a-f0-9]{64})$') { $Matches[1] } else { '' }
                $chosen += @{ name = [string]$asset[0].name; url = [string]$asset[0].browser_download_url; size = [long]$asset[0].size; sha256 = $digest }
            }
            if ($null -ne $chosen) {
                $result += @{ tag = [string]$release.tag_name; published = [string]$release.published_at; prerelease = [bool]$release.prerelease; assets = $chosen }
                break
            }
        }
    }
    return $result
}

function Find-AiEngineRelease {
    param([string]$Type, [string]$Tag)
    $releases = @(Get-AiEngineReleases $Type)
    if (-not $Tag) {
        $latest = @($releases | Where-Object { -not $_.prerelease } | Select-Object -First 1)
        if (-not $latest.Count) { throw "Не найден выпуск $((Get-AiEngineKind $Type).label) со сборкой для Windows." }
        return $latest[0]
    }
    Assert-AiEngineTag $Tag
    $match = @($releases | Where-Object { $_.tag -ceq $Tag })
    if (-not $match.Count) { throw "Версия $Tag не найдена среди последних выпусков $((Get-AiEngineKind $Type).label) со сборкой для Windows." }
    return $match[0]
}

function Get-AiEngineVersionDirectory {
    param([string]$ConfigPath, [string]$Type, [string]$Tag)
    return Join-Path (Join-Path (Get-AiEnginesRoot $ConfigPath) $Type) $Tag
}

# Installed versions: directories with a manifest written after a verified install.
function Get-AiInstalledEngineVersions {
    param([string]$ConfigPath, [string]$Type)
    $directory = Join-Path (Get-AiEnginesRoot $ConfigPath) $Type
    if (-not (Test-Path -LiteralPath $directory -PathType Container)) { return @() }
    $result = @()
    foreach ($item in Get-ChildItem -LiteralPath $directory -Directory -ErrorAction SilentlyContinue) {
        $manifest = Join-Path $item.FullName '.omnibox-engine.json'
        if ($item.LinkType -or -not (Test-Path -LiteralPath $manifest -PathType Leaf)) { continue }
        try { $data = Get-Content -LiteralPath $manifest -Raw | ConvertFrom-Json -AsHashtable } catch { continue }
        $exe = Join-Path $item.FullName ([string]$data.executable)
        if (-not (Test-Path -LiteralPath $exe -PathType Leaf)) { continue }
        $result += @{ tag = $item.Name; executable = $exe; installed = [string]$data.installed; directory = $item.FullName }
    }
    return @($result | Sort-Object { $_.installed } -Descending)
}

# Downloads, verifies and unpacks one release into its version directory. Idempotent.
function Install-AiEngineRuntime {
    param([string]$ConfigPath, [string]$Type, [string]$Tag)
    $kind = Get-AiEngineKind $Type
    $release = Find-AiEngineRelease $Type $Tag
    $target = Get-AiEngineVersionDirectory $ConfigPath $Type $release.tag
    $existing = @(Get-AiInstalledEngineVersions $ConfigPath $Type | Where-Object { $_.tag -ceq $release.tag })
    if ($existing.Count) { return $existing[0] }
    $engines = Get-AiEnginesRoot $ConfigPath
    $stage = Join-Path $engines ('.staging-' + [Guid]::NewGuid().ToString('N'))
    try {
        $files = Join-Path $stage 'files'; $null = New-Item -ItemType Directory -Path $files -Force
        foreach ($asset in $release.assets) {
            if (-not $asset.url.StartsWith("https://github.com/$($kind.repo)/releases/download/", [StringComparison]::Ordinal)) { throw 'Ссылка на файл движка не указывает на выпуск GitHub.' }
            if ($asset.size -le 0 -or $asset.size -gt $script:AiEngineAssetLimit) { throw "Недопустимый размер файла $($asset.name)." }
            $zip = Join-Path $stage $asset.name
            Invoke-AiEngineDownload $asset.url $zip
            $size = (Get-Item -LiteralPath $zip).Length
            if ($size -ne $asset.size) { throw "Файл $($asset.name) скачан не полностью: $size из $($asset.size) байт." }
            if ($asset.sha256 -and (Get-FileHash -LiteralPath $zip -Algorithm SHA256).Hash -ine $asset.sha256) { throw "Контрольная сумма $($asset.name) не совпадает." }
            # .NET rejects archive entries that would escape the target directory.
            [IO.Compression.ZipFile]::ExtractToDirectory($zip, $files, $true)
            Remove-Item -LiteralPath $zip -Force
        }
        $exe = @(Get-ChildItem -LiteralPath $files -Recurse -File -Filter $kind.exe | Sort-Object { $_.FullName.Length } | Select-Object -First 1)
        if (-not $exe.Count) { throw "В выпуске $($release.tag) нет $($kind.exe)." }
        Test-AiEngineBinary $exe[0].FullName
        $relative = [IO.Path]::GetRelativePath($files, $exe[0].FullName)
        @{ schema_version = 1; type = $Type; tag = $release.tag; executable = $relative; installed = [DateTime]::UtcNow.ToString('o'); assets = @($release.assets | ForEach-Object { $_.name }) } |
            ConvertTo-Json | Set-Content -LiteralPath (Join-Path $files '.omnibox-engine.json') -Encoding utf8
        $null = New-Item -ItemType Directory -Path (Split-Path $target -Parent) -Force
        if (Test-Path -LiteralPath $target) { Remove-Item -LiteralPath $target -Recurse -Force }
        Move-Item -LiteralPath $files -Destination $target
    } finally { if (Test-Path -LiteralPath $stage) { Remove-Item -LiteralPath $stage -Recurse -Force -ErrorAction SilentlyContinue } }
    return @(Get-AiInstalledEngineVersions $ConfigPath $Type | Where-Object { $_.tag -ceq $release.tag })[0]
}

function Get-AiProfileExecutable {
    param($Xml, [string]$ProfilePath)
    $node = $Xml.SelectSingleNode('/service/executable')
    if ($null -eq $node) { return '' }
    $value = $node.InnerText.Replace('%BASE%', (Split-Path $ProfilePath -Parent))
    return [Environment]::ExpandEnvironmentVariables($value)
}

# The managed version a path belongs to, or '' for an executable installed outside OmniBox.
function Get-AiEngineTagOf {
    param([string]$ConfigPath, [string]$Type, [string]$Executable)
    if (-not $Executable) { return '' }
    $base = (Join-Path (Get-AiEnginesRoot $ConfigPath) $Type).TrimEnd('\', '/') + [IO.Path]::DirectorySeparatorChar
    $full = [IO.Path]::GetFullPath($Executable)
    if (-not $full.StartsWith($base, [StringComparison]::OrdinalIgnoreCase)) { return '' }
    return $full.Substring($base.Length).Split([char[]]@('\', '/'))[0]
}

function Set-AiProfileRuntime {
    param($Xml, [string]$Executable)
    foreach ($pair in @(@('executable', $Executable), @('workingdirectory', (Split-Path $Executable -Parent)))) {
        $node = $Xml.SelectSingleNode("/service/$($pair[0])")
        if ($null -eq $node) { $node = $Xml.CreateElement($pair[0]); $null = $Xml.DocumentElement.AppendChild($node) }
        $node.InnerText = $pair[1]
    }
}

# Versions still referenced by a profile or its rollback copy stay; others are deleted.
function Remove-AiUnusedEngineVersions {
    param([string]$ConfigPath, [string]$Type)
    $config = Read-AiConfig $ConfigPath
    $used = [Collections.Generic.HashSet[string]]::new([StringComparer]::OrdinalIgnoreCase)
    foreach ($backend in @($config.backends.Values | Where-Object { $_.type -eq $Type })) {
        foreach ($path in @($backend.service_config, "$($backend.service_config).ai-previous")) {
            if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { continue }
            try { $xml = [xml](Get-Content -LiteralPath $path -Raw); $tag = Get-AiEngineTagOf $ConfigPath $Type (Get-AiProfileExecutable $xml $backend.service_config); if ($tag) { $null = $used.Add($tag) } } catch { }
        }
    }
    $removed = @()
    foreach ($version in Get-AiInstalledEngineVersions $ConfigPath $Type) {
        if ($used.Contains($version.tag)) { continue }
        try { Remove-Item -LiteralPath $version.directory -Recurse -Force -ErrorAction Stop; $removed += $version.tag } catch { }
    }
    return $removed
}

function Write-AiConfigFile {
    param([string]$ConfigPath, $Config)
    Write-AiAtomicBytes $ConfigPath ([Text.UTF8Encoding]::new($false).GetBytes(($Config | ConvertTo-Json -Depth 10)))
}

# A running engine restarts on a new runtime, which would cut an active generation. The panel
# blocks new generations and waits for the current one; the CLI runs in another process and
# cannot see the panel's generations, as with ai restart.
$script:AiGenerationWaitSeconds = 120
function Enter-AiGenerationGuard {
    if (-not ('OmniBox.InferenceStats' -as [type])) { return $false }
    $watch = [Diagnostics.Stopwatch]::StartNew()
    while (-not [OmniBox.InferenceStats]::ReserveControl()) {
        if ($watch.Elapsed.TotalSeconds -ge $script:AiGenerationWaitSeconds) { throw 'Выполняется другая операция управления. Новая версия скачана; повторите обновление после её завершения.' }
        Start-Sleep -Milliseconds 500
    }
    # New requests are refused from here on, so a steady stream cannot outlast the wait.
    while ([OmniBox.InferenceStats]::IsGenerating) {
        if ($watch.Elapsed.TotalSeconds -ge $script:AiGenerationWaitSeconds) {
            [OmniBox.InferenceStats]::EndControl()
            throw 'Модель занята генерацией. Новая версия скачана; повторите обновление после завершения запроса.'
        }
        Start-Sleep -Milliseconds 200
    }
    return $true
}
function Exit-AiGenerationGuard { if ('OmniBox.InferenceStats' -as [type]) { [OmniBox.InferenceStats]::EndControl() } }

function Get-AiEngineOverview {
    param([string]$ConfigPath)
    $config = Read-AiConfig $ConfigPath
    $result = @()
    foreach ($type in @('prism', 'ollama')) {
        $kind = Get-AiEngineKind $type
        $entry = [ordered]@{ type = $type; label = $kind.label; cli = $kind.name; installed = $false; restorable = [bool]($config.Contains('removed_engines') -and $config.removed_engines -is [Collections.IDictionary] -and $config.removed_engines.Contains($type)); name = $null; service = $null; state = 'NotInstalled'; version = ''; executable = ''; managed = $false; versions = @() }
        $names = @($config.backends.Keys | Where-Object { $config.backends[$_].type -eq $type })
        if ($names.Count) {
            $name = $names[0]; $backend = $config.backends[$name]
            $entry.installed = $true; $entry.name = $name; $entry.service = $backend.service
            $service = Get-AiService $backend.service
            $entry.state = if ($null -eq $service) { 'Missing' } else { [string]$service.Status }
            try {
                $entry.executable = Get-AiProfileExecutable (Read-AiServiceXml $backend) $backend.service_config
                $entry.version = Get-AiEngineTagOf $ConfigPath $type $entry.executable
                $entry.managed = [bool]$entry.version
            } catch { $entry.executable = '' }
        }
        $entry.versions = @(Get-AiInstalledEngineVersions $ConfigPath $type | ForEach-Object { $_.tag })
        $result += $entry
    }
    return $result
}

# Installs a release and points the engine at it. A running engine restarts on it and
# rolls back on failure; a stopped one stays stopped and checks the change at next start.
function Set-AiEngineVersion {
    param([string]$ConfigPath, [string]$Name, [string]$Tag)
    $config = Read-AiConfig $ConfigPath
    $backend = Get-AiBackend $config $Name
    $runtime = Install-AiEngineRuntime $ConfigPath $backend.type $Tag
    $label = Get-AiEngineName $config $Name
    # Taken after the long download and before the lock, so a waiting generation never holds the lock.
    # Taken whatever the service state: a parallel start may bring the engine up before the lock, and
    # while the guard is held no generation can begin, so the restart below never cuts one off.
    $guarded = Enter-AiGenerationGuard
    try {
        $message = Invoke-AiLocked {
            $config = Read-AiConfig $ConfigPath
            $backend = Get-AiBackend $config $Name
            $xml = Read-AiServiceXml $backend
            if ((Get-AiProfileExecutable $xml $backend.service_config) -ieq $runtime.executable) { return "$label уже использует версию $($runtime.tag)." }
            Set-AiProfileRuntime $xml $runtime.executable
            $service = Get-AiService $backend.service
            if ($null -ne $service -and [string]$service.Status -in @('Running', 'StartPending')) {
                Invoke-AiBackendStart $config $Name 'restart' $xml
                return "$label`: версия $($runtime.tag) запущена."
            }
            Save-AiServiceXml $backend $xml
            return "$label`: установлена версия $($runtime.tag). Движок остановлен; при ошибке следующего запуска вернётся прежняя версия."
        }
    } finally { if ($guarded) { Exit-AiGenerationGuard } }
    $null = Remove-AiUnusedEngineVersions $ConfigPath $backend.type
    return $message
}

function New-AiEngineProfileXml {
    param([string]$Type, [string]$Service, [string]$Executable, [string]$LogDirectory, [string]$Root)
    $xml = [Xml.XmlDocument]::new()
    $null = $xml.AppendChild($xml.CreateElement('service'))
    $add = { param($Tag, $Text) $node = $xml.CreateElement($Tag); $node.InnerText = $Text; $null = $xml.DocumentElement.AppendChild($node) }
    & $add 'id' $Service
    & $add 'executable' $Executable
    if ($Type -eq 'prism') {
        & $add 'arguments' "--host 127.0.0.1 --port $((Get-AiEngineKind $Type).port) -c 32768 -ngl 999 --jinja --sleep-idle-seconds 1800"
    } else { & $add 'arguments' 'serve' }
    & $add 'workingdirectory' (Split-Path $Executable -Parent)
    & $add 'logpath' $LogDirectory
    if ($Type -eq 'ollama') {
        $env = [ordered]@{ OLLAMA_HOST = "127.0.0.1:$((Get-AiEngineKind $Type).port)"; OLLAMA_MODELS = (Join-Path $Root 'models\ollama'); OLLAMA_KEEP_ALIVE = '30m'; OLLAMA_CONTEXT_LENGTH = '32768' }
        foreach ($key in $env.Keys) { $node = $xml.CreateElement('env'); $node.SetAttribute('name', $key); $node.SetAttribute('value', $env[$key]); $null = $xml.DocumentElement.AppendChild($node) }
    }
    return ,$xml
}

# Adds a new engine: runtime, profile, Windows service (manual start) and config entry.
function Install-AiEngine {
    param([string]$ConfigPath, [string]$Type, [string]$Tag)
    $kind = Get-AiEngineKind $Type
    $null = Assert-AiEngineInstallable $ConfigPath (Read-AiConfig $ConfigPath) $Type
    $runtime = Install-AiEngineRuntime $ConfigPath $Type $Tag
    $root = Get-AiInstallRoot $ConfigPath
    $engines = Get-AiEnginesRoot $ConfigPath
    Import-Module (Join-Path $PSScriptRoot 'Service.Install.psm1')
    return Invoke-AiLocked {
        # The download may take minutes; config.json is read again so concurrent changes survive.
        $config = Read-AiConfig $ConfigPath
        $plan = Assert-AiEngineInstallable $ConfigPath $config $Type
        $originalConfig = [IO.File]::ReadAllBytes($ConfigPath)
        $createdService = $false
        try {
            $entry = $plan.entry
            $logs = [string]$entry.log_directory
            $null = New-Item -ItemType Directory -Path $logs -Force
            if ($Type -eq 'prism') { if ($entry.Contains('models_directory')) { $null = New-Item -ItemType Directory -Path $entry.models_directory -Force } }
            else { $null = New-Item -ItemType Directory -Path (Join-Path $root 'models\ollama') -Force }
            # A profile kept from an earlier removal keeps the owner's settings; only the runtime changes.
            if (Test-Path -LiteralPath $entry.service_config -PathType Leaf) {
                $xml = Read-AiServiceXml @{ service = $entry.service; service_config = $entry.service_config }
                Set-AiProfileRuntime $xml $runtime.executable
            } else { $xml = New-AiEngineProfileXml $Type $entry.service $runtime.executable $logs $root }
            $stream = [IO.MemoryStream]::new()
            try { $xml.Save($stream); Write-AiAtomicBytes $entry.service_config $stream.ToArray() } finally { $stream.Dispose() }
            if ($Type -eq 'ollama') { $entry.executable = $runtime.executable }
            $entry.runtime_path = "$($entry.service_config).runtime.json"
            $config.backends[$plan.name] = $entry
            if ($config.Contains('removed_engines') -and $config.removed_engines.Contains($Type)) { $config.removed_engines.Remove($Type) }
            Write-AiConfigFile $ConfigPath $config
            $null = Read-AiConfig $ConfigPath
            # The same command the installer registers, so later updates recognise the service as ours.
            New-AiEngineService $entry.service (New-AiNativeServiceCommand ([Environment]::ProcessPath) $root $entry.service_config) "OmniBox AI · $($plan.name)"
            $createdService = $true
            $restored = if ($plan.restored) { ' Прежние настройки и каталог моделей восстановлены.' } else { '' }
            return "$($kind.label) $($runtime.tag) установлен.$restored Движок не запущен: выберите модель и запустите его в панели."
        } catch {
            $cause = $_.Exception.Message
            if ($createdService) { try { Remove-AiEngineService $entry.service } catch { } }
            Write-AiAtomicBytes $ConfigPath $originalConfig
            throw "Установка $($kind.label) не выполнена: $cause. Конфигурация не изменена."
        }
    }
}

# Where a new engine goes: the entry of an engine removed earlier (its profile, service name,
# models and logs) when its profile still exists, otherwise fresh paths under engines\.
function Assert-AiEngineInstallable {
    param([string]$ConfigPath, $Config, [string]$Type)
    $kind = Get-AiEngineKind $Type
    if (@($Config.backends.Values | Where-Object { $_.type -eq $Type }).Count) { throw "$($kind.label) уже установлен. Для другой версии используйте обновление." }
    $root = Get-AiInstallRoot $ConfigPath
    $engines = Join-Path $root 'engines'
    $previous = if ($Config.Contains('removed_engines') -and $Config.removed_engines -is [Collections.IDictionary] -and $Config.removed_engines.Contains($Type)) { $Config.removed_engines[$Type] } else { $null }
    if ($null -ne $previous -and $previous.entry -is [Collections.IDictionary] -and $previous.entry.service_config -and (Test-Path -LiteralPath $previous.entry.service_config -PathType Leaf)) {
        $name = [string]$previous.name; $entry = [ordered]@{}; foreach ($key in $previous.entry.Keys) { $entry[$key] = $previous.entry[$key] }
        $restored = $true
    } else {
        $name = $kind.name; $profilePath = Join-Path $engines "$name-service.xml"; $logs = Join-Path $engines 'logs'
        $entry = [ordered]@{ type = $Type; service = $name; base_url = "http://127.0.0.1:$($kind.port)"; service_config = $profilePath; log_directory = $logs; log_prefix = "$name-service" }
        if ($Type -eq 'prism') { $entry.models_directory = Join-Path $root 'models' }
        $restored = $false
    }
    if ($Config.backends.Contains($name)) { throw "Имя $name уже занято другим движком в config.json." }
    $port = ([Uri]$entry.base_url).Port
    if (@($Config.backends.Values | Where-Object { ([Uri]$_.base_url).Port -eq $port }).Count) { throw "Порт $port уже использует другой движок." }
    if ($null -ne (Get-AiService $entry.service)) { throw "Служба Windows '$($entry.service)' уже существует и не принадлежит OmniBox AI." }
    return @{ name = $name; entry = $entry; restored = $restored }
}

# Removes the service, the config entry and OmniBox-managed runtimes. Models and
# executables installed outside OmniBox stay on disk.
function Uninstall-AiEngine {
    param([string]$ConfigPath, [string]$Name)
    $config = Read-AiConfig $ConfigPath
    $backend = Get-AiBackend $config $Name
    $label = Get-AiEngineName $config $Name
    if ($config.backends.Count -lt 2) { throw 'Нельзя удалить единственный движок: панели нужен хотя бы один.' }
    $root = Get-AiInstallRoot $ConfigPath
    $gatewayPath = Join-Path $root 'gateway.json'
    Import-Module (Join-Path $PSScriptRoot 'Service.Install.psm1')
    Invoke-AiLocked {
        $config = Read-AiConfig $ConfigPath
        $backend = Get-AiBackend $config $Name
        if ($config.backends.Count -lt 2) { throw 'Нельзя удалить единственный движок: панели нужен хотя бы один.' }
        $service = Get-AiService $backend.service
        if ($null -ne $service) {
            $command = Get-AiServiceCommandLine $backend.service
            if ($null -ne $command -and -not (Test-AiOwnedServiceCommand $command ([Environment]::ProcessPath) $root $backend.service_config)) { throw "Служба '$($backend.service)' запускает другую программу; удаление отменено." }
            Stop-AiBackend $config $Name
        }
        # Settings are written first and restored if the service cannot be removed, so the
        # installation never ends with a registered engine that has no service or the reverse.
        $originals = [ordered]@{}
        foreach ($path in @($ConfigPath, $gatewayPath)) { if (Test-Path -LiteralPath $path -PathType Leaf) { $originals[$path] = [IO.File]::ReadAllBytes($path) } }
        # Copies on disk outlive a rollback that fails too, so the owner can still put the settings back.
        $saved = Join-Path (Join-Path $root 'backups') ('engine-remove-' + [DateTime]::UtcNow.ToString('yyyyMMdd-HHmmss') + '-' + [Guid]::NewGuid().ToString('N').Substring(0, 8))
        $null = New-Item -ItemType Directory -Path $saved -Force
        foreach ($path in $originals.Keys) { [IO.File]::WriteAllBytes((Join-Path $saved (Split-Path $path -Leaf)), $originals[$path]) }
        try {
            # Remember the removed engine so a reinstall gets its profile, service name, models and logs back.
            if (-not $config.Contains('removed_engines') -or $config.removed_engines -isnot [Collections.IDictionary]) { $config.removed_engines = [ordered]@{} }
            $config.removed_engines[$backend.type] = [ordered]@{ name = $Name; removed = [DateTime]::UtcNow.ToString('o'); entry = $backend }
            $config.backends.Remove($Name)
            if ($config.default_backend -eq $Name) { $config.default_backend = @($config.backends.Keys)[0] }
            Write-AiConfigFile $ConfigPath $config
            if ($originals.Contains($gatewayPath)) {
                $gateway = Get-Content -LiteralPath $gatewayPath -Raw | ConvertFrom-Json -AsHashtable
                if ($gateway.backend -eq $Name) { $gateway.backend = $config.default_backend; Write-AiAtomicBytes $gatewayPath ([Text.UTF8Encoding]::new($false).GetBytes(($gateway | ConvertTo-Json -Depth 10))) }
            }
            if ($null -ne $service) { Remove-AiEngineService $backend.service }
        } catch {
            $cause = $_.Exception.Message
            $failed = @()
            foreach ($path in $originals.Keys) { try { Write-AiAtomicBytes $path $originals[$path] } catch { $failed += "$(Split-Path $path -Leaf): $($_.Exception.Message)" } }
            if ($failed.Count) { throw "Удаление $label не выполнено: $cause. Восстановить настройки не удалось ($($failed -join '; ')). Прежние файлы сохранены в $saved; скопируйте их в $root. Движок остановлен." }
            Remove-Item -LiteralPath $saved -Recurse -Force -ErrorAction SilentlyContinue
            throw "Удаление $label не выполнено: $cause. Настройки восстановлены, движок остановлен."
        }
        Remove-Item -LiteralPath $saved -Recurse -Force -ErrorAction SilentlyContinue
        # Rollback history belongs to the removed service; the profile itself keeps settings for a reinstall.
        foreach ($suffix in @('.ai-previous', '.ai-state.json', '.runtime.json')) { $path = $backend.service_config + $suffix; if (Test-Path -LiteralPath $path -PathType Leaf) { Remove-Item -LiteralPath $path -Force -ErrorAction SilentlyContinue } }
    }
    $removed = @(Remove-AiUnusedEngineVersions $ConfigPath $backend.type)
    $kept = @()
    try {
        $exe = Get-AiProfileExecutable ([xml](Get-Content -LiteralPath $backend.service_config -Raw)) $backend.service_config
        if ($exe -and -not (Get-AiEngineTagOf $ConfigPath $backend.type $exe)) { $kept += Split-Path $exe -Parent }
    } catch { }
    $text = "$label удалён: служба и записи в настройках."
    if ($removed.Count) { $text += " Удалены версии: $($removed -join ', ')." }
    if ($kept.Count) { $text += " Файлы установки вне OmniBox оставлены: $($kept -join ', ')." }
    return "$text Модели и настройки остались на диске; повторная установка их восстановит."
}

# Deletes one model from disk (PrismML GGUF) or from Ollama. The model a profile still
# needs (current or rollback) is refused.
function Remove-AiEngineModel {
    param([string]$ConfigPath, [string]$Name, [string]$Model)
    $config = Read-AiConfig $ConfigPath
    $backend = Get-AiBackend $config $Name
    if ($backend.type -eq 'prism') {
        return Invoke-AiLocked {
            $file = @(Get-AiPrismModels $backend | Where-Object { $_.id -ceq $Model })
            if (-not $file.Count) { throw 'Модель не найдена в каталоге моделей.' }
            $parts = @(Get-AiGgufParts $file[0].id)
            $inUse = @((Get-AiLaunchSettings $backend).model)
            $previous = "$($backend.service_config).ai-previous"
            if (Test-Path -LiteralPath $previous -PathType Leaf) { try { $inUse += Get-AiArgumentValue @(Get-AiPrismArguments ([xml](Get-Content -LiteralPath $previous -Raw))) @('-m', '--model') '' } catch { } }
            # A split model is used through its first part but needs every part.
            foreach ($path in @($inUse | Where-Object { $_ })) {
                foreach ($part in @(Get-AiGgufParts ([IO.Path]::GetFullPath($path)))) {
                    if ($parts -icontains $part) { throw 'Эта модель выбрана в настройках или нужна для отката. Выберите другую модель и сохраните настройки.' }
                }
            }
            foreach ($part in $parts) { Remove-Item -LiteralPath $part -Force }
            $what = if ($parts.Count -gt 1) { " (частей: $($parts.Count))" } else { '' }
            return "Модель $($file[0].name) удалена$what, освобождено $([math]::Round($file[0].bytes / 1GB, 1)) ГБ."
        }
    }
    Assert-AiModelName $Model
    $service = Get-AiService $backend.service
    if ($null -eq $service -or [string]$service.Status -ne 'Running') { throw 'Запустите Ollama: модели удаляются через её API.' }
    $null = Invoke-AiModelRequest $backend '/api/delete' @{ model = $Model } 120 'Delete'
    Invoke-AiLocked {
        $config = Read-AiConfig $ConfigPath
        $current = $config.backends[$Name]
        if ($null -ne $current -and $current.Contains('active_model') -and $current.active_model -eq $Model) { $current.Remove('active_model'); Write-AiConfigFile $ConfigPath $config }
    }
    return "Модель $Model удалена из Ollama."
}

# All files of a split GGUF (name-00001-of-00003.gguf ...) or the file itself.
function Get-AiGgufParts {
    param([string]$Path)
    $name = [IO.Path]::GetFileName($Path)
    if ($name -notmatch '^(?<base>.+)-(?<part>\d{5})-of-(?<total>\d{5})\.gguf$') { return @($Path) }
    $directory = Split-Path $Path -Parent
    $base = $Matches.base; $total = $Matches.total
    $parts = @(Get-ChildItem -LiteralPath $directory -File -ErrorAction SilentlyContinue | Where-Object { $_.Name -match ('^' + [regex]::Escape($base) + '-\d{5}-of-' + $total + '\.gguf$') } | Sort-Object Name | ForEach-Object { $_.FullName })
    if (-not $parts.Count) { return @($Path) }
    return $parts
}

# Where each engine keeps its models. Ollama without OLLAMA_MODELS uses the profile of the
# account it runs as; the panel and the engines both run as SYSTEM, so that is this process' profile.
function Get-AiModelLocations {
    param($Config)
    $result = @()
    foreach ($name in @($Config.backends.Keys | Sort-Object)) {
        $backend = $Config.backends[$name]
        $path = ''
        try {
            if ($backend.type -eq 'prism') { $path = Get-AiPrismModelDirectory $backend }
            else {
                $path = Get-AiXmlEnv (Read-AiServiceXml $backend) 'OLLAMA_MODELS' ''
                if (-not $path) { $path = Join-Path ([Environment]::GetFolderPath('UserProfile')) '.ollama/models' }
            }
        } catch { $path = '' }
        $result += @{ name = $name; engine = Get-AiEngineName $Config $name; type = $backend.type; path = $path }
    }
    return $result
}

function Get-AiModelFolderSize {
    param([string]$Type, [string]$Path)
    $size = @{ bytes = 0L; models = 0 }
    if (-not $Path -or -not (Test-Path -LiteralPath $Path -PathType Container)) { return $size }
    if ($Type -eq 'prism') {
        foreach ($file in @(Get-ChildItem -LiteralPath $Path -File -Filter '*.gguf' -ErrorAction SilentlyContinue)) { $size.bytes += $file.Length; if ($file.Name -notmatch '(?i)mmproj|projector' -and $file.Name -notmatch '-(?!00001)\d{5}-of-\d{5}\.gguf$') { $size.models++ } }
        return $size
    }
    # Ollama: blobs hold the weights, one manifest per installed model tag.
    foreach ($file in @(Get-ChildItem -LiteralPath $Path -File -Recurse -Force -ErrorAction SilentlyContinue)) { $size.bytes += $file.Length }
    $manifests = Join-Path $Path 'manifests'
    if (Test-Path -LiteralPath $manifests -PathType Container) { $size.models = @(Get-ChildItem -LiteralPath $manifests -File -Recurse -ErrorAction SilentlyContinue).Count }
    return $size
}

# Fixed drives with total, used and free space, the roles they play for OmniBox AI and model sizes.
# Computed on request only (login, the Logs tab, ai storage).
function Get-AiDiskOverview {
    param([string]$ConfigPath)
    $config = Read-AiConfig $ConfigPath
    $models = @()
    foreach ($location in Get-AiModelLocations $config) {
        $size = Get-AiModelFolderSize $location.type $location.path
        $models += @{ name = $location.name; engine = $location.engine; path = $location.path; bytes = $size.bytes; models = $size.models }
    }
    $places = @(@{ role = 'OmniBox AI'; path = Get-AiInstallRoot $ConfigPath }) + @($models | Where-Object { $_.path } | ForEach-Object { @{ role = "модели $($_.engine)"; path = $_.path } })
    $drives = @()
    $all = @(Get-AiFixedDrives)
    # The deepest mount point containing the path is the drive that stores it.
    $ownerOf = { param($Path) $full = [IO.Path]::GetFullPath($Path); @($all | Where-Object { $full.StartsWith($_.root, [StringComparison]::OrdinalIgnoreCase) } | Sort-Object { $_.root.Length } -Descending | Select-Object -First 1) }
    # The download form shows the free space of the drive that holds the selected engine's models.
    foreach ($model in $models) { $owner = @(if ($model.path) { & $ownerOf $model.path }); $model.drive = if ($owner.Count) { $owner[0].name } else { $null } }
    foreach ($drive in $all) {
        $root = $drive.root
        $roles = @()
        foreach ($place in $places) {
            $owner = @(& $ownerOf $place.path)
            if ($owner.Count -and $owner[0].root -eq $root) { $roles += $place.role }
        }
        $used = $drive.total - $drive.free
        $low = $drive.free -lt 20GB -or $drive.free -lt ($drive.total * 0.1)
        $drives += [ordered]@{ name = $drive.name; label = $drive.label; total = $drive.total; free = $drive.free; used = $used; roles = $roles; low = [bool]($low -and $roles.Count) }
    }
    return @{ drives = $drives; models = $models }
}

function Get-AiFixedDrives {
    $result = @()
    foreach ($drive in [IO.DriveInfo]::GetDrives()) {
        try {
            if (-not $drive.IsReady -or $drive.DriveType -ne [IO.DriveType]::Fixed -or $drive.TotalSize -lt 1GB) { continue }
            $root = $drive.RootDirectory.FullName
            if (-not $root.EndsWith([IO.Path]::DirectorySeparatorChar)) { $root += [IO.Path]::DirectorySeparatorChar }
            $result += @{ name = $drive.Name.TrimEnd('\'); label = [string]$drive.VolumeLabel; root = $root; total = [long]$drive.TotalSize; free = [long]$drive.AvailableFreeSpace }
        } catch { }
    }
    return $result
}
