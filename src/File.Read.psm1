Set-StrictMode -Version Latest

function Read-AiSharedText {
    param([string]$Path)
    # Status workers may read the old snapshot while an atomic writer replaces the path.
    $stream = [IO.File]::Open($Path, [IO.FileMode]::Open, [IO.FileAccess]::Read, ([IO.FileShare]::ReadWrite -bor [IO.FileShare]::Delete))
    $reader = [IO.StreamReader]::new($stream)
    try { return $reader.ReadToEnd() } finally { $reader.Dispose() }
}

Export-ModuleMember -Function Read-AiSharedText
