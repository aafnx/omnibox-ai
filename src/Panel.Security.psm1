Set-StrictMode -Version Latest

function New-AiPanelPassword {
    param([string]$Password)
    if ($Password.Length -lt 12 -or $Password.Length -gt 256) { throw 'Пароль панели должен содержать от 12 до 256 символов.' }
    $salt = [Security.Cryptography.RandomNumberGenerator]::GetBytes(32)
    $hash = [Security.Cryptography.Rfc2898DeriveBytes]::Pbkdf2($Password, $salt, 210000, [Security.Cryptography.HashAlgorithmName]::SHA256, 32)
    return @{ salt = [Convert]::ToBase64String($salt); hash = [Convert]::ToBase64String($hash); iterations = 210000 }
}

function Test-AiPanelPassword {
    param([string]$Password, $Credential)
    if ($Password.Length -gt 256 -or $Password.Length -lt 1) { return $false }
    $actual = [Security.Cryptography.Rfc2898DeriveBytes]::Pbkdf2($Password, [Convert]::FromBase64String($Credential.salt), [int]$Credential.iterations, [Security.Cryptography.HashAlgorithmName]::SHA256, 32)
    return [Security.Cryptography.CryptographicOperations]::FixedTimeEquals($actual, [Convert]::FromBase64String($Credential.hash))
}

function New-AiPanelNonce { [Convert]::ToHexString([Security.Cryptography.RandomNumberGenerator]::GetBytes(32)).ToLowerInvariant() }

function Read-AiPanelConfig {
    param([string]$Path)
    $config = Get-Content -LiteralPath $Path -Raw | ConvertFrom-Json -AsHashtable
    if ($config.schema_version -ne 1 -or $config.port -isnot [long] -and $config.port -isnot [int] -or $config.port -lt 1024 -or $config.port -gt 65535) { throw 'Некорректная конфигурация панели или порт.' }
    if ($config.allowed_hosts -isnot [array] -or $config.allowed_hosts.Count -lt 1 -or $config.allowed_hosts.Count -gt 20) { throw 'Нужны allowed_hosts панели.' }
    foreach ($hostName in $config.allowed_hosts) {
        if ($hostName -notmatch '^[a-zA-Z0-9][a-zA-Z0-9.-]*$' -or $hostName.Length -gt 253) { throw 'allowed_hosts: IPv4 или DNS-имя без порта/пути.' }
    }
    if ($config.credential.iterations -lt 210000 -or $config.credential.iterations -gt 1000000 -or
        [Convert]::FromBase64String($config.credential.salt).Length -ne 32 -or [Convert]::FromBase64String($config.credential.hash).Length -ne 32) { throw 'Некорректный hash пароля панели.' }
    if ($config.ssh_target -notmatch '^[a-zA-Z0-9_.-]+@[a-zA-Z0-9_.-]+$') { throw 'ssh_target: user@host без параметров.' }
    return $config
}

function Test-AiPanelOrigin {
    param([string]$Origin, [string]$Authority)
    if (-not $Origin) { return $true }
    $uri = $null
    return [Uri]::TryCreate($Origin, [UriKind]::Absolute, [ref]$uri) -and $uri.Scheme -eq 'http' -and $uri.Authority -ieq $Authority -and $uri.AbsolutePath -eq '/' -and -not $uri.UserInfo -and -not $uri.Query -and -not $uri.Fragment
}

Export-ModuleMember -Function New-AiPanelPassword, Test-AiPanelPassword, New-AiPanelNonce, Read-AiPanelConfig, Test-AiPanelOrigin
