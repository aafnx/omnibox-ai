#requires -Version 7.4
[CmdletBinding()]
param([string]$OutputPath = (Join-Path (Split-Path $PSScriptRoot -Parent) 'dist/install-omnibox-ai.ps1'))
$ErrorActionPreference = 'Stop'
$root = Split-Path $PSScriptRoot -Parent
$package = Import-PowerShellDataFile -LiteralPath (Join-Path $root 'package.psd1')
$version = (Get-Content -LiteralPath (Join-Path $root 'VERSION') -Raw).Trim()
if ($version -notmatch '^\d+\.\d+\.\d+$') { throw 'VERSION must contain a single x.y.z version.' }
$files = @($package.Files) + @($package.InstallerFiles)

function ConvertTo-AiPackageBytes {
    # Same bytes from any checkout: CRLF for Windows scripts, LF for everything else.
    param([string]$Path, [string[]]$CrlfExtensions)
    $text = [Text.UTF8Encoding]::new($false, $true).GetString([IO.File]::ReadAllBytes($Path))
    if ($text.Contains([char]0)) { throw "Package file is not text: $Path" }
    $text = $text.Replace("`r`n", "`n")
    if ([IO.Path]::GetExtension($Path).ToLowerInvariant() -in $CrlfExtensions) { $text = $text.Replace("`n", "`r`n") }
    return ,[Text.UTF8Encoding]::new($false).GetBytes($text)
}

$memory = [IO.MemoryStream]::new()
try {
    $archive = [IO.Compression.ZipArchive]::new($memory, [IO.Compression.ZipArchiveMode]::Create, $true)
    try {
        foreach ($file in $files) {
            $entry = $archive.CreateEntry($file, [IO.Compression.CompressionLevel]::Optimal)
            $entry.LastWriteTime = [DateTimeOffset]::new(2026, 10, 5, 0, 0, 0, [TimeSpan]::Zero)
            $output = $entry.Open()
            try {
                $bytes = ConvertTo-AiPackageBytes (Join-Path $root $file) $package.CrlfExtensions
                $output.Write($bytes, 0, $bytes.Length)
            } finally { $output.Dispose() }
        }
    } finally { $archive.Dispose() }
    $bytes = $memory.ToArray()
} finally { $memory.Dispose() }
$sha = [Convert]::ToHexString([Security.Cryptography.SHA256]::HashData($bytes)).ToLowerInvariant()
$template = (Get-Content -LiteralPath (Join-Path $PSScriptRoot 'setup.template.ps1') -Raw) -replace "`r?`n", "`r`n"
$generated = $template.Replace('__VERSION__', $version).Replace('__PAYLOAD_SHA256__', $sha).Replace('__PAYLOAD_BASE64__', [Convert]::ToBase64String($bytes))
$fullOutput = [IO.Path]::GetFullPath($OutputPath)
$null = New-Item -ItemType Directory -Path (Split-Path $fullOutput -Parent) -Force
Set-Content -LiteralPath $fullOutput -Value $generated -Encoding utf8BOM -NoNewline
Write-Host "Built: $fullOutput ($($bytes.Length) bytes embedded, SHA256 $sha)"
