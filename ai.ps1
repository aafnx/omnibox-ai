#requires -Version 7.4
# Deliberately no param block: keep arbitrary Ollama flags in $args.
$ErrorActionPreference = 'Stop'
try {
    Import-Module (Join-Path $PSScriptRoot 'src/OmniBox.Ai.psm1') -Force
    $configPath = if ($env:OMNIBOX_AI_CONFIG) { $env:OMNIBOX_AI_CONFIG } else { Join-Path $PSScriptRoot 'config.json' }
    $global:LASTEXITCODE = 0
    Invoke-Ai -Arguments @($args) -ConfigPath $configPath
    exit $global:LASTEXITCODE
} catch {
    [Console]::Error.WriteLine('ai: ' + $_.Exception.Message)
    exit 1
}
