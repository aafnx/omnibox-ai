$ErrorActionPreference = 'Stop'
# Diagnostic only: measures how long Windows keeps a file unreadable while it is atomically replaced.
$repo = Split-Path $PSScriptRoot -Parent
$root = Join-Path ([IO.Path]::GetTempPath()) ('diag-' + [Guid]::NewGuid().ToString('N')); $null = New-Item -ItemType Directory $root
try { $mp = Get-MpComputerStatus -ErrorAction Stop; "Defender realtime: $($mp.RealTimeProtectionEnabled)" } catch { "Defender status unavailable: $($_.Exception.Message)" }
function Invoke-Scenario([string]$Name, [scriptblock]$Write) {
    $path = Join-Path $root "$Name.json"; [IO.File]::WriteAllText($path, ('{"a":' + ('1' * 2000) + '}'))
    $state = [hashtable]::Synchronized(@{ stop = $false; ok = 0; fails = [Collections.ArrayList]::Synchronized([Collections.ArrayList]::new()) })
    $job = Start-ThreadJob -ArgumentList $path, $state -ScriptBlock {
        param($Path, $State)
        $watch = [Diagnostics.Stopwatch]::StartNew(); $streakStart = -1.0
        while (-not $State.stop) {
            try {
                $s = [IO.File]::Open($Path, [IO.FileMode]::Open, [IO.FileAccess]::Read, ([IO.FileShare]::ReadWrite -bor [IO.FileShare]::Delete))
                $s.Dispose(); $State.ok++
                if ($streakStart -ge 0) { $null = $State.fails.Add(('streak {0:N1} ms' -f ($watch.Elapsed.TotalMilliseconds - $streakStart))); $streakStart = -1.0 }
            } catch {
                $c = $_.Exception; while ($c.InnerException) { $c = $c.InnerException }
                if ($streakStart -lt 0) { $streakStart = $watch.Elapsed.TotalMilliseconds; $null = $State.fails.Add("$($c.GetType().Name) $($c.HResult -band 0xffff)") }
            }
        }
        if ($streakStart -ge 0) { $null = $State.fails.Add(('open streak {0:N1} ms at stop' -f ($watch.Elapsed.TotalMilliseconds - $streakStart))) }
    }
    Start-Sleep -Milliseconds 200
    $bytes = [IO.File]::ReadAllBytes($path); $t = [Diagnostics.Stopwatch]::StartNew()
    for ($i = 0; $i -lt 400; $i++) { & $Write $path $bytes }
    $elapsed = $t.Elapsed.TotalMilliseconds
    $state.stop = $true; $job | Wait-Job | Receive-Job; Remove-Job $job
    $streaks = @($state.fails | Where-Object { $_ -like '*streak*' } | ForEach-Object { [double]($_ -replace '[^\d.,]', '' -replace ',', '') })
    $kinds = @($state.fails | Where-Object { $_ -notlike '*streak*' } | Group-Object | ForEach-Object { "$($_.Name) x$($_.Count)" })
    "{0}: writes {1:N0} ms, reads ok {2}, lock episodes {3}, max {4:N1} ms, kinds: {5}" -f $Name, $elapsed, $state.ok, $streaks.Count, (($streaks | Measure-Object -Maximum).Maximum), ($kinds -join '; ')
    $state.fails | Where-Object { $_ -like '*at stop*' }
}
$replace = { param($Path, $Bytes) $tmp = "$Path.tmp-$([Guid]::NewGuid().ToString('N'))"; [IO.File]::WriteAllBytes($tmp, $Bytes); [IO.File]::Replace($tmp, $Path, [System.Management.Automation.Language.NullString]::Value) }
$move = { param($Path, $Bytes) $tmp = "$Path.tmp-$([Guid]::NewGuid().ToString('N'))"; [IO.File]::WriteAllBytes($tmp, $Bytes); [IO.File]::Move($tmp, $Path, $true) }
foreach ($round in 1..2) { Invoke-Scenario "replace$round" $replace }
try { Invoke-Scenario 'move' $move } catch { "move writer fails with a reader open: $($_.Exception.InnerException.Message)" }
Remove-Item $root -Recurse -Force -ErrorAction SilentlyContinue
# The real reader with its retries, as the failing test uses it.
foreach ($round in 1..4) {
$path = Join-Path ([IO.Path]::GetTempPath()) ('diag-real-' + [Guid]::NewGuid().ToString('N') + '.json'); [IO.File]::WriteAllText($path, '{"a":1}')
$state = [hashtable]::Synchronized(@{ stop = $false; ok = 0; errors = [Collections.ArrayList]::Synchronized([Collections.ArrayList]::new()); slowest = 0.0 })
$job = Start-ThreadJob -ArgumentList (Join-Path $repo 'src/File.Read.psm1'), $path, $state -ScriptBlock {
    param($Module, $Path, $State)
    Import-Module $Module
    while (-not $State.stop) {
        $w = [Diagnostics.Stopwatch]::StartNew()
        try { $null = Read-AiSharedText $Path; $State.ok++ } catch { $c = $_.Exception; while ($c.InnerException) { $c = $c.InnerException }; $null = $State.errors.Add(('{0} {1} after {2:N0} ms' -f $c.GetType().Name, ($c.HResult -band 0xffff), $w.Elapsed.TotalMilliseconds)) }
        if ($w.Elapsed.TotalMilliseconds -gt $State.slowest) { $State.slowest = $w.Elapsed.TotalMilliseconds }
    }
}
Start-Sleep -Milliseconds 200
$bytes = [IO.File]::ReadAllBytes($path)
Import-Module (Join-Path $repo 'src/OmniBox.Ai.psm1')
$m = Get-Module OmniBox.Ai
for ($i = 0; $i -lt 400; $i++) { & $m { param($p, $b) Write-AiAtomicBytes $p $b } $path $bytes }
$state.stop = $true; $job | Wait-Job | Receive-Job; Remove-Job $job
"real reader: ok $($state.ok), slowest call {0:N0} ms, errors: {1}" -f $state.slowest, (($state.errors | Group-Object | ForEach-Object { "$($_.Name) x$($_.Count)" }) -join '; ')
}
