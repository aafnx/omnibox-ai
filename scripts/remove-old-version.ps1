# One-time removal of the PowerShell version of OmniBox AI (0.17.1 and older). Not part of the
# program: run it once on the PC after the Go version is installed, then forget it.
#
# It moves the old models into the new models directory first, then removes the old services,
# firewall rules, PATH entry, the PowerShell profile lines, engine folders set up for it, logs
# and the install directory. A folder whose models could not be moved is kept.
#
#   gh release download v0.18.0 --repo aafnx/omnibox-ai --pattern remove-old-version.ps1 --dir $env:TEMP --clobber
#   powershell -NoProfile -ExecutionPolicy Bypass -File $env:TEMP\remove-old-version.ps1
param(
    [string]$Root = '',
    [string]$DataDir = (Join-Path $env:ProgramData 'OmniBox AI'),
    [switch]$Yes
)
$ErrorActionPreference = 'Stop'

function Test-Admin {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    return ([Security.Principal.WindowsPrincipal]$identity).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

function Test-OldRoot([string]$Path) {
    return $Path -and (Test-Path -LiteralPath (Join-Path $Path 'config.json') -PathType Leaf) -and (Test-Path -LiteralPath (Join-Path $Path 'ai.ps1') -PathType Leaf)
}

function Test-Inside([string]$Path, [string]$Directory) {
    if (-not $Path -or -not $Directory) { return $false }
    $p = [IO.Path]::GetFullPath($Path).TrimEnd('\') + '\'
    $d = [IO.Path]::GetFullPath($Directory).TrimEnd('\') + '\'
    return $p.StartsWith($d, [StringComparison]::OrdinalIgnoreCase)
}

# Moves matching files of $From into $To, keeping relative paths; returns the files left behind.
function Move-Models([string]$From, [string]$To, [scriptblock]$Match) {
    $left = @()
    if (-not $From -or -not (Test-Path -LiteralPath $From -PathType Container)) { return $left }
    if ([IO.Path]::GetFullPath($From).TrimEnd('\') -ieq [IO.Path]::GetFullPath($To).TrimEnd('\')) { return $left }
    $base = [IO.Path]::GetFullPath($From).TrimEnd('\') + '\'
    foreach ($file in @(Get-ChildItem -LiteralPath $From -File -Recurse -Force -ErrorAction SilentlyContinue)) {
        $relative = $file.FullName.Substring($base.Length)
        if (-not (& $Match $relative)) { continue }
        $target = Join-Path $To $relative
        if (Test-Path -LiteralPath $target) { Write-Host "  уже есть в новом каталоге, оставлен: $($file.FullName)"; $left += $file.FullName; continue }
        try {
            New-Item -ItemType Directory -Path (Split-Path $target -Parent) -Force | Out-Null
            Move-Item -LiteralPath $file.FullName -Destination $target
            Write-Host "  перенесён: $relative"
        } catch { Write-Host "  не перенесён: $($file.FullName): $($_.Exception.Message)"; $left += $file.FullName }
    }
    return $left
}

function Remove-ProfileBlock([string]$Path) {
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { return }
    $text = [IO.File]::ReadAllText($Path)
    $start = '# >>> omnibox-ai PATH >>>'; $end = '# <<< omnibox-ai PATH <<<'
    $i = $text.IndexOf($start); $j = $text.IndexOf($end)
    if ($i -lt 0) { return }
    if ($j -lt $i) { Write-Host "Профиль $Path не изменён: маркеры omnibox-ai неоднозначны."; return }
    $updated = $text.Substring(0, $i).TrimEnd("`r", "`n") + $text.Substring($j + $end.Length)
    if ($updated.Trim()) { [IO.File]::WriteAllText($Path, $updated, (New-Object Text.UTF8Encoding $true)) } else { Remove-Item -LiteralPath $Path }
    Write-Host "Из профиля убрана строка PATH прежней версии: $Path"
}

if (-not (Test-Admin)) { Write-Host 'Запустите PowerShell от имени администратора и повторите команду.'; exit 1 }

# Models go where the Go version looks for them: its config.json, else the installer's defaults.
$prismTarget = Join-Path $DataDir 'models'
$ollamaTarget = Join-Path $DataDir 'models\ollama'
$newConfig = Join-Path $DataDir 'config.json'
if (Test-Path -LiteralPath $newConfig -PathType Leaf) {
    $engines = (Get-Content -LiteralPath $newConfig -Raw -Encoding UTF8 | ConvertFrom-Json).engines
    if ($engines -and $engines.prismml -and $engines.prismml.models_directory) { $prismTarget = [string]$engines.prismml.models_directory }
    if ($engines -and $engines.ollama -and $engines.ollama.models_directory) { $ollamaTarget = [string]$engines.ollama.models_directory }
} else {
    Write-Host 'Go-версия OmniBox AI не найдена. Сначала установите её (docs/install.md), затем запустите этот скрипт.'
    exit 1
}

# Where the old version lives: its panel service ran <root>\src\Service.Host.exe.
if (-not $Root) {
    $panel = Get-CimInstance -ClassName Win32_Service -Filter "Name='omnibox-ai'" -ErrorAction SilentlyContinue
    if ($panel -and $panel.PathName -match '^"([^"]+)"') { $candidate = Split-Path (Split-Path $Matches[1] -Parent) -Parent; if (Test-OldRoot $candidate) { $Root = $candidate } }
    if (-not $Root -and (Test-OldRoot 'C:\tools\ai')) { $Root = 'C:\tools\ai' }
}
$services = @('omnibox-ai')
$prismDirs = @(); $ollamaModels = ''; $engineDirs = @(); $logFiles = @(); $hasOllama = $false
if ($Root) {
    $config = Get-Content -LiteralPath (Join-Path $Root 'config.json') -Raw -Encoding UTF8 | ConvertFrom-Json
    foreach ($property in $config.backends.PSObject.Properties) {
        $b = $property.Value
        if ($b.service) { $services += [string]$b.service }
        $profilePath = [string]$b.service_config
        $profileDir = if ($profilePath) { Split-Path $profilePath -Parent } else { '' }
        $xml = $null
        if ($profilePath -and (Test-Path -LiteralPath $profilePath -PathType Leaf)) { try { $xml = [xml](Get-Content -LiteralPath $profilePath -Raw) } catch { } }
        $expand = { param($s) [Environment]::ExpandEnvironmentVariables(([string]$s).Replace('%BASE%', $profileDir)) }
        $exe = if ($xml -and $xml.service.executable) { & $expand $xml.service.executable } else { [string]$b.executable }
        if ($b.type -eq 'prism') {
            if ($b.models_directory) { $prismDirs += [string]$b.models_directory }
            if ($xml -and (& $expand $xml.service.arguments) -match '(?:^|\s)(?:-m|--model)\s+(?:"([^"]+)"|(\S+))') { $prismDirs += (Split-Path ($Matches[1] + $Matches[2]) -Parent) }
        } else {
            $hasOllama = $true
            if ($xml) { foreach ($e in @($xml.service.env)) { if ($e.name -eq 'OLLAMA_MODELS') { $ollamaModels = & $expand $e.value } } }
        }
        # An engine folder that holds the old profile was set up for OmniBox; a shared one stays.
        if ($exe -and $profileDir -and ((Split-Path $exe -Parent) -ieq $profileDir) -and -not (Test-Inside $profileDir $Root)) { $engineDirs += $profileDir }
        if ($b.log_directory -and $b.log_prefix -and -not (Test-Inside $b.log_directory $Root) -and (Test-Path -LiteralPath $b.log_directory)) {
            $logFiles += @(Get-ChildItem -LiteralPath $b.log_directory -File -Filter "$($b.log_prefix).*" -ErrorAction SilentlyContinue | ForEach-Object { $_.FullName })
        }
    }
}
if ($hasOllama -and -not $ollamaModels) { $ollamaModels = Join-Path $env:SystemRoot 'System32\config\systemprofile\.ollama\models' }
$services = @($services | Select-Object -Unique | Where-Object { Get-Service -Name $_ -ErrorAction SilentlyContinue })
$prismDirs = @($prismDirs | Where-Object { $_ } | Select-Object -Unique)
$engineDirs = @($engineDirs | Select-Object -Unique)

if (-not $Root -and -not $services.Count) { Write-Host 'PowerShell-версия OmniBox AI не найдена. Удалять нечего.'; exit 0 }
Write-Host 'Будет удалена PowerShell-версия OmniBox AI:'
if ($Root) { Write-Host "  каталог программы: $Root" }
if ($services.Count) { Write-Host "  службы: $($services -join ', ')" }
foreach ($d in $engineDirs) { Write-Host "  каталог движка: $d" }
if ($logFiles.Count) { Write-Host "  журналы вне каталога программы: $($logFiles.Count)" }
Write-Host '  правила Firewall, запись в PATH и строка в профиле PowerShell'
Write-Host 'Сначала будут перенесены модели:'
foreach ($d in $prismDirs) { Write-Host "  GGUF: $d -> $prismTarget" }
if ($ollamaModels) { Write-Host "  Ollama: $ollamaModels -> $ollamaTarget" }
if (-not $Yes) {
    $answer = Read-Host 'Продолжить? (y/N)'
    if ($answer -notmatch '^(y|yes|д|да)$') { Write-Host 'Отменено, ничего не изменено.'; exit 0 }
}

foreach ($name in $services) {
    try {
        Stop-Service -Name $name -Force -ErrorAction SilentlyContinue
        & sc.exe delete $name | Out-Null
        Write-Host "Служба $name удалена."
    } catch { Write-Host "Службу $name удалить не удалось: $($_.Exception.Message)" }
}

$left = @()
foreach ($d in $prismDirs) {
    Write-Host "Перенос GGUF из $d"
    $left += Move-Models $d $prismTarget { param($r) $r -notmatch '\\' -and $r -match '\.gguf$' }
}
if ($ollamaModels) {
    Write-Host "Перенос моделей Ollama из $ollamaModels"
    $left += Move-Models $ollamaModels $ollamaTarget { param($r) $r -match '^(blobs|manifests)\\' }
}

foreach ($rule in @('omnibox-ai-panel', 'omnibox-ai-api')) { Remove-NetFirewallRule -Name $rule -ErrorAction SilentlyContinue }
if ($Root) {
    $path = [Environment]::GetEnvironmentVariable('Path', 'User')
    if ($path) {
        $kept = @($path -split ';' | Where-Object { $_ -and ([Environment]::ExpandEnvironmentVariables($_).TrimEnd('\') -ine $Root.TrimEnd('\')) })
        [Environment]::SetEnvironmentVariable('Path', ($kept -join ';'), 'User')
    }
    $documents = [Environment]::GetFolderPath('MyDocuments')
    foreach ($p in @((Join-Path $documents 'PowerShell\profile.ps1'), (Join-Path $documents 'WindowsPowerShell\profile.ps1'))) { Remove-ProfileBlock $p }
}
foreach ($f in $logFiles) { Remove-Item -LiteralPath $f -Force -ErrorAction SilentlyContinue }
foreach ($dir in @($engineDirs + @($Root) | Where-Object { $_ })) {
    if (@($left | Where-Object { Test-Inside $_ $dir }).Count) { Write-Host "Каталог $dir оставлен: в нём остались модели, которые не удалось перенести."; continue }
    try { Remove-Item -LiteralPath $dir -Recurse -Force; Write-Host "Удалён каталог: $dir" }
    catch { Write-Host "Каталог $dir удалён не полностью: $($_.Exception.Message)" }
}
Write-Host ''
Write-Host 'Готово. PowerShell-версия удалена; модели — в новом каталоге моделей OmniBox AI.'
if ($left.Count) { Write-Host "Не перенесено файлов моделей: $($left.Count) — они остались на прежнем месте." }
