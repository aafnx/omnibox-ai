$ErrorActionPreference = 'Stop'
# Diagnostic only: the exact reader/writer pattern of the failing core test, with stack traces.
$repo = Split-Path $PSScriptRoot -Parent
$modulePath = Join-Path $repo 'src/OmniBox.Ai.psm1'
$root = Join-Path ([IO.Path]::GetTempPath()) ('diag-exact-' + [Guid]::NewGuid().ToString('N')); $null = New-Item -ItemType Directory $root
foreach ($round in 1..20) {
    $config = Get-Content (Join-Path $repo 'config.example.json') -Raw | ConvertFrom-Json -AsHashtable
    $xmlPath = Join-Path $root ([Guid]::NewGuid().ToString('N') + '-bonsai.xml')
    '<service><id>bonsai</id><arguments>-m "C:\m\a.gguf" --port 18080 -c 196608</arguments><startmode>Manual</startmode><logpath>C:\ai\logs\bonsai</logpath></service>' | Set-Content -LiteralPath $xmlPath
    $config.backends.bonsai.service_config = $xmlPath
    $configPath = Join-Path $root ([Guid]::NewGuid().ToString('N') + '.json'); $config | ConvertTo-Json -Depth 10 | Set-Content -LiteralPath $configPath
    $state = [hashtable]::Synchronized(@{ stop = $false; ready = $false; count = 0; error = $null })
    $job = Start-ThreadJob -ArgumentList $modulePath, $configPath, $state -ScriptBlock {
        param($ModulePath, $ConfigPath, $State)
        $ErrorActionPreference = 'Stop'
        $engine = Import-Module $ModulePath -PassThru
        try {
            & $engine { param($ConfigPath, $State)
                while (-not $State.stop) { $w = [Diagnostics.Stopwatch]::StartNew(); $c = Read-AiConfig $ConfigPath; $null = Read-AiServiceXml $c.backends.bonsai; $State.count++; $State.ready = $true } } $ConfigPath $State
        } catch { $State.error = "$($_.Exception.GetType().Name): $($_.Exception.Message)`n$($_.ScriptStackTrace)`nFileRead module: $((Get-Module File.Read).Path)" }
    }
    while (-not $state.ready -and -not $state.error) { Start-Sleep -Milliseconds 20 }
    Remove-Module OmniBox.Ai -ErrorAction SilentlyContinue
    $m = Import-Module $modulePath -Force -PassThru
    $configBytes = [IO.File]::ReadAllBytes($configPath); $xmlBytes = [IO.File]::ReadAllBytes($xmlPath)
    $writeError = $null
    try { for ($i = 0; $i -lt 200; $i++) { & $m { param($P, $B) Write-AiAtomicBytes $P $B } $configPath $configBytes; & $m { param($P, $B) Write-AiAtomicBytes $P $B } $xmlPath $xmlBytes } } catch { $writeError = $_.Exception.Message }
    $state.stop = $true; $job | Wait-Job | Receive-Job; Remove-Job $job
    "round ${round}: reads $($state.count); writer error: $writeError; reader error: $($state.error)"
}
