Set-StrictMode -Version Latest

# Disk housekeeping for files OmniBox AI writes itself. Cleanup runs only on panel start,
# at install and on user request; it never runs on a timer and only touches known name patterns.

$script:StorageDefaults = [ordered]@{ log_retention_days = 30; log_warn_mb = 200; request_log = 'metadata' }
$script:LogStreams = '(?:out|err|host|wrapper|bootstrap)'

function Read-AiStorageSettings {
    param([string]$Root)
    $settings = [ordered]@{} + $script:StorageDefaults
    $path = Join-Path $Root 'storage.json'
    if (Test-Path -LiteralPath $path -PathType Leaf) {
        try { $saved = Get-Content -LiteralPath $path -Raw | ConvertFrom-Json -AsHashtable } catch { $saved = $null }
        if ($saved -is [Collections.IDictionary]) {
            foreach ($key in @($script:StorageDefaults.Keys)) {
                if ($saved.Contains($key)) { try { $settings[$key] = Test-AiStorageValue $key $saved[$key] } catch { } }
            }
        }
    }
    return $settings
}

function Test-AiStorageValue {
    param([string]$Key, $Value)
    $text = [string]$Value
    if ($Key -eq 'request_log') {
        if ($text -cnotin @('metadata', 'content')) { throw 'Журнал запросов: metadata или content.' }
        return $text
    }
    if ($text -notmatch '^\d{1,6}$') { throw 'Срок хранения и порог объёма — целые числа.' }
    $number = [int]$text
    if ($Key -eq 'log_retention_days' -and $number -gt 365) { throw 'Срок хранения журналов: от 0 (не удалять) до 365 дней.' }
    if ($Key -eq 'log_warn_mb' -and ($number -lt 10 -or $number -gt 100000)) { throw 'Порог объёма журналов: от 10 до 100000 МБ.' }
    return $number
}

function Save-AiStorageSettings {
    param([string]$Root, [Collections.IDictionary]$Changes)
    $settings = Read-AiStorageSettings $Root
    foreach ($key in $Changes.Keys) {
        if ($key -notin @($script:StorageDefaults.Keys)) { throw "Неизвестная настройка хранения: $key" }
        $settings[$key] = Test-AiStorageValue $key $Changes[$key]
    }
    $value = [ordered]@{ schema_version = 1 } + $settings
    $path = Join-Path $Root 'storage.json'
    $temporary = "$path.tmp-$([Guid]::NewGuid().ToString('N'))"
    try {
        [IO.File]::WriteAllText($temporary, ($value | ConvertTo-Json), [Text.UTF8Encoding]::new($false))
        Move-Item -LiteralPath $temporary -Destination $path -Force
    } finally { if (Test-Path -LiteralPath $temporary) { Remove-Item -LiteralPath $temporary -Force } }
    return $settings
}

function Get-AiMatchingFiles {
    param([string]$Directory, [string]$Pattern)
    if (-not $Directory -or -not (Test-Path -LiteralPath $Directory -PathType Container)) { return @() }
    return @(Get-ChildItem -LiteralPath $Directory -File -ErrorAction SilentlyContinue | Where-Object { $_.Name -match $Pattern -and -not $_.LinkType })
}

# Every log file of the panel, the request journal and each engine, including rotated and legacy WinSW files.
function Get-AiLogFiles {
    param([string]$Root, $Config)
    $files = [Collections.Generic.Dictionary[string, IO.FileInfo]]::new([StringComparer]::OrdinalIgnoreCase)
    $add = { param($items) foreach ($item in $items) { $files[$item.FullName] = $item } }
    $streamFile = "^[A-Za-z0-9_.-]+\.$script:LogStreams(?:\.\d+)?\.log(?:\.\d+|\.old)?$"
    & $add (Get-AiMatchingFiles (Join-Path $Root 'logs') $streamFile)
    & $add (Get-AiMatchingFiles (Join-Path $Root 'logs/requests') '^(?:requests|content)\.jsonl(?:\.\d+)?$')
    if ($null -ne $Config) {
        foreach ($backend in $Config.backends.Values) {
            # Indexers: a partial config (missing keys) must not throw under strict mode.
            $profilePath = [string]$backend['service_config']
            $prefixes = @([string]$backend['log_prefix'])
            $directories = @([string]$backend['log_directory'])
            if ($profilePath) {
                $prefixes += [IO.Path]::GetFileNameWithoutExtension($profilePath)
                # Service.Host writes its bootstrap line next to the profile before it reads <logpath>.
                $directories += Join-Path (Split-Path $profilePath -Parent) 'logs'
            }
            foreach ($directory in $directories) {
                foreach ($prefix in ($prefixes | Where-Object { $_ } | Select-Object -Unique)) {
                    & $add (Get-AiMatchingFiles $directory ("^$([regex]::Escape($prefix))\.$script:LogStreams(?:\.\d+)?\.log(?:\.\d+|\.old)?$"))
                }
            }
        }
    }
    return @($files.Values)
}

function Get-AiDirectorySize {
    param([string]$Path, [string[]]$Exclude = @())
    if (-not (Test-Path -LiteralPath $Path -PathType Container)) { return 0L }
    $total = 0L
    foreach ($file in Get-ChildItem -LiteralPath $Path -File -Recurse -Force -ErrorAction SilentlyContinue) {
        $relative = [IO.Path]::GetRelativePath($Path, $file.FullName)
        if (@($Exclude | Where-Object { $relative -like "$_*" }).Count) { continue }
        $total += $file.Length
    }
    return $total
}

# Leftovers of installer runs that were killed before their own cleanup.
function Get-AiStaleTempItems {
    param([TimeSpan]$MinimumAge = [TimeSpan]::FromDays(1))
    $temp = [IO.Path]::GetTempPath()
    if (-not (Test-Path -LiteralPath $temp -PathType Container)) { return @() }
    $limit = [DateTime]::UtcNow - $MinimumAge
    return @(Get-ChildItem -LiteralPath $temp -Force -ErrorAction SilentlyContinue | Where-Object {
        $_.Name -match '^omnibox-ai-(?:package|runtime)-[0-9a-f]{32}$|^omnibox-ai-install-[0-9a-f]{32}\.ps1$' -and -not $_.LinkType -and $_.LastWriteTimeUtc -lt $limit
    })
}

function Get-AiStorageUsage {
    param([string]$Root, $Config)
    $settings = Read-AiStorageSettings $Root
    $logs = @(Get-AiLogFiles $Root $Config)
    $logBytes = 0L; foreach ($file in $logs) { $logBytes += $file.Length }
    $tempBytes = 0L
    foreach ($item in Get-AiStaleTempItems ([TimeSpan]::Zero)) {
        $tempBytes += if ($item -is [IO.DirectoryInfo]) { Get-AiDirectorySize $item.FullName } else { $item.Length }
    }
    $contentBytes = 0L; foreach ($file in @(Get-AiMatchingFiles (Join-Path $Root 'logs/requests') '^content\.jsonl(?:\.\d+)?$')) { $contentBytes += $file.Length }
    $usage = [ordered]@{
        logs_bytes = $logBytes; log_files = $logs.Count; content_bytes = $contentBytes; request_log = $settings.request_log
        backups_bytes = Get-AiDirectorySize (Join-Path $Root 'backups')
        updates_bytes = Get-AiDirectorySize (Join-Path $Root 'updates') @('auth')
        engines_bytes = Get-AiDirectorySize (Join-Path $Root 'engines') @('logs')
        temp_bytes = $tempBytes
        log_retention_days = $settings.log_retention_days; log_warn_mb = $settings.log_warn_mb
    }
    $usage.logs_over_limit = $logBytes -gt ([long]$settings.log_warn_mb * 1MB)
    return $usage
}

# Deletes log files untouched for longer than the retention period and stale installer temp files.
# Files a running service is writing are recent, so they are never older than the period.
function Invoke-AiStorageCleanup {
    param([string]$Root, $Config)
    $settings = Read-AiStorageSettings $Root
    $items = @()
    if ($settings.log_retention_days -gt 0) {
        $limit = [DateTime]::UtcNow.AddDays(-$settings.log_retention_days)
        $items += @(Get-AiLogFiles $Root $Config | Where-Object { $_.LastWriteTimeUtc -lt $limit })
    }
    $items += @(Get-AiStaleTempItems)
    # Engine downloads unpack into engines\.staging-*; one left by a killed panel is dead after a day.
    $engines = Join-Path $Root 'engines'
    if (Test-Path -LiteralPath $engines -PathType Container) {
        $items += @(Get-ChildItem -LiteralPath $engines -Directory -Force -ErrorAction SilentlyContinue | Where-Object { $_.Name -match '^\.staging-[0-9a-f]{32}$' -and -not $_.LinkType -and $_.LastWriteTimeUtc -lt [DateTime]::UtcNow.AddDays(-1) })
    }
    $removed = 0; $freed = 0L
    foreach ($item in $items) {
        $size = if ($item -is [IO.DirectoryInfo]) { Get-AiDirectorySize $item.FullName } else { $item.Length }
        # A file still open by its writer stays until the next cleanup.
        try { Remove-Item -LiteralPath $item.FullName -Recurse -Force -ErrorAction Stop; $removed++; $freed += $size } catch { }
    }
    return [ordered]@{ removed = $removed; freed_bytes = $freed }
}

# Turning the content journal off also removes the request text already stored.
function Remove-AiRequestContent {
    param([string]$Root)
    $removed = 0
    foreach ($file in @(Get-AiMatchingFiles (Join-Path $Root 'logs/requests') '^content\.jsonl(?:\.\d+)?$')) {
        try { Remove-Item -LiteralPath $file.FullName -Force -ErrorAction Stop; $removed++ } catch { }
    }
    return $removed
}

# Keeps the newest install snapshots; never removes the one an unfinished transaction still needs.
function Remove-AiOldBackups {
    param([string]$Root, [int]$Keep = 3, [string[]]$Protect = @())
    $directory = Join-Path $Root 'backups'
    if (-not (Test-Path -LiteralPath $directory -PathType Container)) { return 0 }
    $protected = [Collections.Generic.HashSet[string]]::new([StringComparer]::OrdinalIgnoreCase)
    foreach ($path in $Protect) { if ($path) { $null = $protected.Add([IO.Path]::GetFullPath($path).TrimEnd('\', '/')) } }
    $journal = Join-Path $Root '.omnibox-ai-transaction.json'
    if (Test-Path -LiteralPath $journal -PathType Leaf) {
        try { $null = $protected.Add([IO.Path]::GetFullPath([string](Get-Content -LiteralPath $journal -Raw | ConvertFrom-Json).snapshot).TrimEnd('\', '/')) } catch { }
    }
    $removed = 0
    $groups = @(
        @(Get-ChildItem -LiteralPath $directory -Directory -ErrorAction SilentlyContinue | Where-Object { $_.Name -match '^\d{8}-\d{6}-[0-9a-f]{8}$' }),
        @(Get-ChildItem -LiteralPath $directory -Directory -ErrorAction SilentlyContinue | Where-Object { $_.Name -match '^services-[0-9a-f-]{32,36}$' }),
        # Settings kept by an engine removal whose rollback failed.
        @(Get-ChildItem -LiteralPath $directory -Directory -ErrorAction SilentlyContinue | Where-Object { $_.Name -match '^engine-remove-\d{8}-\d{6}-[0-9a-f]{8}$' }),
        @(Get-AiMatchingFiles (Join-Path $directory 'profiles') '^profile-.+\.ps1$')
    )
    foreach ($group in $groups) {
        foreach ($item in @($group | Where-Object { -not $_.LinkType } | Sort-Object LastWriteTimeUtc -Descending | Select-Object -Skip $Keep)) {
            if ($protected.Contains($item.FullName.TrimEnd('\', '/'))) { continue }
            try { Remove-Item -LiteralPath $item.FullName -Recurse -Force -ErrorAction Stop; $removed++ } catch { }
        }
    }
    return $removed
}

Export-ModuleMember -Function Read-AiStorageSettings, Save-AiStorageSettings, Remove-AiRequestContent, Get-AiLogFiles, Get-AiStorageUsage, Invoke-AiStorageCleanup, Remove-AiOldBackups, Get-AiStaleTempItems
