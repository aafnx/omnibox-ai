#requires -Version 7.4
[CmdletBinding()]
param([string]$OutputPath = (Join-Path (Split-Path $PSScriptRoot -Parent) 'dist/install-omnibox-ai.ps1'))
$ErrorActionPreference = 'Stop'
$root = Split-Path $PSScriptRoot -Parent
$files = @('ai.ps1', 'ai.cmd', 'panel.ps1', 'src/OmniBox.Ai.psm1', 'src/Panel.Engine.ps1', 'src/Panel.Cli.ps1', 'src/Telemetry.ps1', 'src/Inference.Stats.cs', 'src/Gateway.Helpers.psm1', 'src/OpenAi.Proxy.cs', 'src/Model.Catalog.psm1', 'src/Panel.Server.psm1', 'src/Panel.Security.psm1', 'src/Panel.Install.psm1', 'src/Install.Helpers.psm1', 'src/Setup.Helpers.psm1', 'web/index.html', 'web/style.css', 'web/app.js', 'config.example.json', 'install.ps1', 'README.md', 'docs/commands.md', 'docs/architecture.md', 'docs/install.md', 'docs/web-panel.md', 'docs/reverse-proxy.md', 'docs/model-api.md', 'docs/models.md', 'docs/monitoring.md')
$memory = [IO.MemoryStream]::new()
try {
    $archive = [IO.Compression.ZipArchive]::new($memory, [IO.Compression.ZipArchiveMode]::Create, $true)
    try {
        foreach ($file in $files) {
            $entry = $archive.CreateEntry($file, [IO.Compression.CompressionLevel]::Optimal)
            $entry.LastWriteTime = [DateTimeOffset]::new(2026, 10, 5, 0, 0, 0, [TimeSpan]::Zero)
            $output = $entry.Open()
            try {
                $bytes = [IO.File]::ReadAllBytes((Join-Path $root $file))
                $output.Write($bytes, 0, $bytes.Length)
            } finally { $output.Dispose() }
        }
    } finally { $archive.Dispose() }
    $bytes = $memory.ToArray()
} finally { $memory.Dispose() }
$sha = [Convert]::ToHexString([Security.Cryptography.SHA256]::HashData($bytes)).ToLowerInvariant()
$template = Get-Content -LiteralPath (Join-Path $PSScriptRoot 'setup.template.ps1') -Raw
$generated = $template.Replace('__PAYLOAD_SHA256__', $sha).Replace('__PAYLOAD_BASE64__', [Convert]::ToBase64String($bytes))
$fullOutput = [IO.Path]::GetFullPath($OutputPath)
$null = New-Item -ItemType Directory -Path (Split-Path $fullOutput -Parent) -Force
Set-Content -LiteralPath $fullOutput -Value $generated -Encoding utf8BOM -NoNewline
Write-Host "Built: $fullOutput ($($bytes.Length) bytes embedded, SHA256 $sha)"
