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

function Set-AiPanelProxyDefaults {
    param($Config)
    if (-not $Config.Contains('public_origin')) { $Config.public_origin = '' }
    if (-not $Config.Contains('trusted_proxy_addresses')) { $Config.trusted_proxy_addresses = @() }
    if ($Config.public_origin -isnot [string]) { throw 'public_origin: нужен HTTPS origin.' }
    $uri = $null
    if ($Config.public_origin -and (-not [Uri]::TryCreate($Config.public_origin, [UriKind]::Absolute, [ref]$uri) -or $uri.Scheme -ne 'https' -or $uri.HostNameType -notin @('Dns','IPv4') -or $uri.UserInfo -or $uri.AbsolutePath -ne '/' -or $uri.Query -or $uri.Fragment)) { throw 'public_origin: HTTPS-адрес без пути, например https://ai.example.com.' }
    if ($Config.trusted_proxy_addresses -isnot [array] -or $Config.trusted_proxy_addresses.Count -gt 20) { throw 'trusted_proxy_addresses: список IP NAS, до 20 адресов.' }
    foreach ($address in $Config.trusted_proxy_addresses) {
        $ip = $null
        if ($address -isnot [string] -or -not [Net.IPAddress]::TryParse($address, [ref]$ip)) { throw 'trusted_proxy_addresses: только конкретные IP, без CIDR/wildcard.' }
    }
    if ([bool]$Config.public_origin -ne ($Config.trusted_proxy_addresses.Count -gt 0)) { throw 'Reverse proxy: укажи public_origin и IP NAS вместе или оставь оба пустыми.' }
    if ($Config.public_origin) { $Config.public_origin = $uri.GetLeftPart([UriPartial]::Authority) }
}

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
    Set-AiPanelProxyDefaults $config
    return $config
}

function Test-AiPanelOrigin {
    param([string]$Origin, [string]$Authority, [string]$Scheme = 'http')
    if (-not $Origin) { return $true }
    $uri = $null
    return [Uri]::TryCreate($Origin, [UriKind]::Absolute, [ref]$uri) -and $uri.Scheme -eq $Scheme -and $uri.Authority -ieq $Authority -and $uri.AbsolutePath -eq '/' -and -not $uri.UserInfo -and -not $uri.Query -and -not $uri.Fragment
}

function Get-AiPanelRequestContext {
    param($Request, $Config)
    $remote = $Request.RemoteEndPoint.Address
    if ($remote.IsIPv4MappedToIPv6) { $remote = $remote.MapToIPv4() }
    $trusted = @($Config.trusted_proxy_addresses | Where-Object { [Net.IPAddress]::Parse($_).Equals($remote) }).Count -gt 0
    $result = @{ secure = $false; authority = $Request.Url.Authority; scheme = 'http'; client_address = $remote.ToString() }
    if (-not $trusted) { return $result }
    if ($Request.Headers['X-Forwarded-Proto'] -cne 'https') { throw 'Доверенный reverse proxy должен передавать X-Forwarded-Proto: https.' }
    $origin = [Uri]$Config.public_origin
    $forwardedHost = $Request.Headers['X-Forwarded-Host']
    if ($forwardedHost -and $forwardedHost -ine $origin.Authority) { throw 'X-Forwarded-Host не совпадает с public_origin.' }
    $result.secure = $true; $result.scheme = 'https'; $result.authority = $origin.Authority
    $client = $null
    # Require the proxy to overwrite this header with ONE address, never append an untrusted chain.
    if ([Net.IPAddress]::TryParse([string]$Request.Headers['X-Forwarded-For'], [ref]$client)) { $result.client_address = $client.ToString() }
    return $result
}

Export-ModuleMember -Function New-AiPanelPassword, Test-AiPanelPassword, New-AiPanelNonce, Read-AiPanelConfig, Test-AiPanelOrigin, Set-AiPanelProxyDefaults, Get-AiPanelRequestContext
