Set-StrictMode -Version Latest

function Get-AiPanelSessionKey {
    param([string]$Token)
    if ($Token -cnotmatch '^[a-f0-9]{64}$') { return '' }
    return [Convert]::ToHexString([Security.Cryptography.SHA256]::HashData([Text.Encoding]::UTF8.GetBytes($Token))).ToLowerInvariant()
}

function Get-AiPanelCredentialKey {
    param($Credential)
    $text = "$($Credential.salt)|$($Credential.hash)|$($Credential.iterations)"
    return [Convert]::ToHexString([Security.Cryptography.SHA256]::HashData([Text.Encoding]::UTF8.GetBytes($text))).ToLowerInvariant()
}

function Protect-AiPanelSessions {
    param([byte[]]$Bytes)
    return ,([Security.Cryptography.ProtectedData]::Protect($Bytes, $null, [Security.Cryptography.DataProtectionScope]::LocalMachine))
}

function Unprotect-AiPanelSessions {
    param([byte[]]$Bytes)
    return ,([Security.Cryptography.ProtectedData]::Unprotect($Bytes, $null, [Security.Cryptography.DataProtectionScope]::LocalMachine))
}

function Read-AiPanelSessions {
    param([string]$Path, $Credential)
    $plain = $null
    try {
        if (-not [IO.File]::Exists($Path) -or ([IO.FileInfo]::new($Path)).Length -gt 128KB) { return @{} }
        $plain = Unprotect-AiPanelSessions ([IO.File]::ReadAllBytes($Path))
        $data = [Text.Encoding]::UTF8.GetString($plain) | ConvertFrom-Json -AsHashtable
        if ($data.schema_version -ne 1 -or $data.credential -cne (Get-AiPanelCredentialKey $Credential) -or $data.sessions -isnot [Collections.IDictionary] -or $data.sessions.Count -gt 32) { return @{} }
        $sessions = @{}; $now = [DateTime]::UtcNow
        foreach ($key in $data.sessions.Keys) {
            $session = $data.sessions[$key]
            if ($key -cnotmatch '^[a-f0-9]{64}$' -or $session.csrf -cnotmatch '^[a-f0-9]{64}$' -or $session.secure -isnot [bool]) { return @{} }
            $expires = if ($session.expires -is [DateTime]) { $session.expires.ToUniversalTime() } else { [DateTimeOffset]::Parse([string]$session.expires).UtcDateTime }
            if ($expires -gt $now -and $expires -le $now.AddHours(8)) { $sessions[$key] = @{ csrf = $session.csrf; secure = $session.secure; expires = $expires } }
        }
        return $sessions
    } catch { return @{} }
    finally { if ($null -ne $plain) { [Array]::Clear($plain, 0, $plain.Length) } }
}

function Write-AiPanelSessions {
    param([string]$Path, $Credential, $Sessions)
    $saved = @{}; $now = [DateTime]::UtcNow
    foreach ($key in $Sessions.Keys) {
        $session = $Sessions[$key]
        if ($session.expires -gt $now) { $saved[$key] = @{ csrf = $session.csrf; secure = $session.secure; expires = $session.expires.ToUniversalTime().ToString('o') } }
    }
    $plain = [Text.Encoding]::UTF8.GetBytes((@{ schema_version = 1; credential = Get-AiPanelCredentialKey $Credential; sessions = $saved } | ConvertTo-Json -Depth 6 -Compress))
    $temporary = $Path + '.' + [Guid]::NewGuid().ToString('N') + '.tmp'
    try {
        # The installation directory restricts access to SYSTEM, administrators and its owner.
        $cipher = Protect-AiPanelSessions $plain
        [IO.File]::WriteAllBytes($temporary, $cipher)
        [IO.File]::Move($temporary, $Path, $true)
    } finally {
        [Array]::Clear($plain, 0, $plain.Length)
        if ([IO.File]::Exists($temporary)) { [IO.File]::Delete($temporary) }
    }
}

Export-ModuleMember -Function Get-AiPanelSessionKey, Read-AiPanelSessions, Write-AiPanelSessions
