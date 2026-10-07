Set-StrictMode -Version Latest

function Read-AiSharedText {
    param([string]$Path)
    # Status reads the old snapshot without blocking an atomic writer.
    # Windows can briefly hide the path during ReplaceFile; retry only these transient errors.
    for ($attempt = 0; $attempt -lt 10; $attempt++) {
        $stream = $null; $reader = $null
        try {
            $stream = [IO.File]::Open($Path, [IO.FileMode]::Open, [IO.FileAccess]::Read, ([IO.FileShare]::ReadWrite -bor [IO.FileShare]::Delete))
            $reader = [IO.StreamReader]::new($stream)
            return $reader.ReadToEnd()
        } catch {
            $cause = $_.Exception
            while ($null -ne $cause.InnerException) { $cause = $cause.InnerException }
            if (-not $IsWindows -or $cause -isnot [IO.IOException] -or ($cause.HResult -band 0xffff) -notin @(2, 3, 32, 33) -or $attempt -eq 9) { throw }
            [Threading.Thread]::Sleep(10)
        } finally {
            if ($null -ne $reader) { $reader.Dispose() }
            elseif ($null -ne $stream) { $stream.Dispose() }
        }
    }
}

Export-ModuleMember -Function Read-AiSharedText
