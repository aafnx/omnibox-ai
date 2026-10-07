Set-StrictMode -Version Latest

$script:AiSharedReadTimeoutMs = 2000

function Read-AiSharedText {
    param([string]$Path)
    # Status reads the old snapshot without blocking an atomic writer.
    # Windows can briefly hide or lock the path during ReplaceFile, and a scanner may hold the new file
    # longer than a few retries; retry only these transient errors, by time rather than by count.
    $watch = [Diagnostics.Stopwatch]::StartNew()
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
            $locked = ($cause -is [IO.IOException] -and $code -in @(32, 33)) -or ($cause -is [UnauthorizedAccessException] -and $code -eq 5)
            # A path hidden by ReplaceFile reappears at once; a file that is really missing must fail fast.
            $hidden = $cause -is [IO.IOException] -and $code -in @(2, 3)
            $limit = if ($locked) { $script:AiSharedReadTimeoutMs } elseif ($hidden) { 200 } else { 0 }
            if (-not $IsWindows -or $watch.ElapsedMilliseconds -ge $limit) { throw }
            [Threading.Thread]::Sleep($delay)
            $delay = [Math]::Min($delay * 2, 100)
        } finally {
            if ($null -ne $reader) { $reader.Dispose() }
            elseif ($null -ne $stream) { $stream.Dispose() }
        }
    }
}

Export-ModuleMember -Function Read-AiSharedText
