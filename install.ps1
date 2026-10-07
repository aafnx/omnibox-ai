# OmniBox AI: installs or updates the program with one command. Runs in the built-in Windows
# PowerShell 5.1. It downloads a release, checks its SHA256 and starts "omnibox install", which
# asks the questions (Enter keeps the default) or, on an installed computer, updates it.
#
#   gh release download --repo aafnx/omnibox-ai --pattern install.ps1 --dir $env:TEMP --clobber
#   powershell -NoProfile -ExecutionPolicy Bypass -File $env:TEMP\install.ps1
param(
    [string]$Version = '',
    [switch]$Yes
)
$ErrorActionPreference = 'Stop'
$Repo = 'aafnx/omnibox-ai'
$Archive = 'omnibox-windows-amd64.zip'

function Test-Admin {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    return ([Security.Principal.WindowsPrincipal]$identity).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

# Release files through gh, which keeps its own credentials.
function Get-WithGh([string]$Tag, [string]$Directory) {
    $arguments = @('release', 'download')
    if ($Tag) { $arguments += $Tag }
    $arguments += @('--repo', $Repo, '--pattern', $Archive, '--pattern', 'SHA256SUMS', '--dir', $Directory, '--clobber')
    & gh @arguments
    if ($LASTEXITCODE -ne 0) { throw 'gh не скачал выпуск.' }
}

# Without gh: a GitHub token typed once, kept only in memory and never written anywhere.
function Get-WithToken([string]$Tag, [string]$Directory) {
    $secure = Read-Host 'GitHub token с доступом на чтение репозитория (ввод скрыт)' -AsSecureString
    $pointer = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secure)
    try {
        $token = [Runtime.InteropServices.Marshal]::PtrToStringBSTR($pointer)
        $headers = @{ Authorization = "Bearer $token"; Accept = 'application/vnd.github+json'; 'User-Agent' = 'omnibox-install' }
        $path = if ($Tag) { "tags/$Tag" } else { 'latest' }
        $release = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases/$path" -Headers $headers
        foreach ($name in @($Archive, 'SHA256SUMS')) {
            $asset = @($release.assets | Where-Object { $_.name -eq $name })
            if (-not $asset.Count) { throw "В выпуске нет файла $name." }
            # GitHub answers with a redirect to signed storage, which must not get the token.
            $request = [Net.HttpWebRequest]::Create($asset[0].url)
            $request.Headers['Authorization'] = "Bearer $token"
            $request.Accept = 'application/octet-stream'
            $request.UserAgent = 'omnibox-install'
            $request.AllowAutoRedirect = $false
            $response = $request.GetResponse()
            $location = $response.Headers['Location']
            $response.Close()
            if (-not $location) { throw "GitHub не выдал ссылку на $name." }
            Invoke-WebRequest -Uri $location -OutFile (Join-Path $Directory $name) -UseBasicParsing
        }
    } finally {
        [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($pointer)
        $token = $null
    }
}

if (-not (Test-Admin)) {
    Write-Host 'Запустите PowerShell от имени администратора и повторите команду.'
    exit 1
}
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
$tag = if ($Version) { "v$($Version.TrimStart('v'))" } else { '' }
$temporary = Join-Path $env:TEMP ('omnibox-install-' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $temporary | Out-Null
try {
    $gh = Get-Command gh -ErrorAction SilentlyContinue
    if ($gh) {
        & gh auth status *> $null
        if ($LASTEXITCODE -eq 0) { Get-WithGh $tag $temporary } else { $gh = $null }
    }
    if (-not $gh) { Get-WithToken $tag $temporary }
    $zip = Join-Path $temporary $Archive
    $expected = $null
    foreach ($line in Get-Content (Join-Path $temporary 'SHA256SUMS')) {
        if ($line -match "^([0-9a-f]{64}) +\*?$([regex]::Escape($Archive))$") { $expected = $Matches[1]; break }
    }
    if (-not $expected) { throw "В SHA256SUMS нет $Archive." }
    if ((Get-FileHash -Algorithm SHA256 -LiteralPath $zip).Hash -ne $expected.ToUpperInvariant()) { throw 'Контрольная сумма выпуска не совпадает; установка отменена.' }
    $package = Join-Path $temporary 'package'
    Expand-Archive -LiteralPath $zip -DestinationPath $package
    $arguments = @('install')
    if ($Yes) { $arguments += '--yes' }
    & (Join-Path $package 'omnibox.exe') @arguments
    exit $LASTEXITCODE
} catch {
    Write-Host "Установка не выполнена: $($_.Exception.Message)"
    exit 1
} finally {
    Remove-Item -LiteralPath $temporary -Recurse -Force -ErrorAction SilentlyContinue
}
