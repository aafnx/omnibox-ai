Set-StrictMode -Version Latest

$script:AiSharedReadTimeoutMs = 5000

function Read-AiSharedText {
    param([string]$Path)
    # Status reads the old snapshot without blocking an atomic writer.
    # Windows briefly locks or hides the path during ReplaceFile; retry only these transient errors,
    # by time rather than by count.
    $watch = [Diagnostics.Stopwatch]::StartNew()
    $missingSince = -1L
    $delay = 5
    while ($true) {
        $stream = $null; $reader = $null
        try {
            $stream = [IO.File]::Open($Path, [IO.FileMode]::Open, [IO.FileAccess]::Read, ([IO.FileShare]::ReadWrite -bor [IO.FileShare]::Delete))
            $reader = [IO.StreamReader]::new($stream)
            return $reader.ReadToEnd()
        } catch {
            $cause = $_.Exception
            while ($null -ne $cause.InnerException) { $cause = $cause.InnerException }
            $code = $cause.HResult -band 0xffff
            # 5: a replaced file is delete-pending while an old reader still holds it.
            # Measured on Windows runners: a file being replaced stays locked for up to half a second.
            $locked = ($cause -is [IO.IOException] -and $code -in @(32, 33)) -or ($cause -is [UnauthorizedAccessException] -and $code -eq 5)
            $hidden = $cause -is [IO.IOException] -and $code -in @(2, 3)
            # ReplaceFile hides the path only for a moment, often right after a lock; a file that stays
            # missing for 200 ms is really missing and must fail fast.
            if ($hidden) { if ($missingSince -lt 0) { $missingSince = $watch.ElapsedMilliseconds } } else { $missingSince = -1L }
            $retry = $IsWindows -and ($locked -or $hidden) -and $watch.ElapsedMilliseconds -lt $script:AiSharedReadTimeoutMs -and
                (-not $hidden -or $watch.ElapsedMilliseconds - $missingSince -lt 200)
            if (-not $retry) { throw }
            [Threading.Thread]::Sleep($delay)
            $delay = [Math]::Min($delay * 2, 50)
        } finally {
            if ($null -ne $reader) { $reader.Dispose() }
            elseif ($null -ne $stream) { $stream.Dispose() }
        }
    }
}

# Puts a fully written temporary file in place of a settings file that status readers may hold open.
# Readers share Delete, so ReplaceFile succeeds under them; MoveFileEx with overwrite does not on Windows.
function Move-AiFileOver {
    param([string]$Source, [string]$Path)
    if ([IO.File]::Exists($Path)) { [IO.File]::Replace($Source, $Path, [System.Management.Automation.Language.NullString]::Value) }
    else { [IO.File]::Move($Source, $Path) }
}

Export-ModuleMember -Function Read-AiSharedText, Move-AiFileOver
