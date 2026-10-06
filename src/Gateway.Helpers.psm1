Set-StrictMode -Version Latest

function New-AiApiKey {
    $key = 'obai-' + [Convert]::ToHexString([Security.Cryptography.RandomNumberGenerator]::GetBytes(32)).ToLowerInvariant()
    @{ key = $key; hash = [Convert]::ToHexString([Security.Cryptography.SHA256]::HashData([Text.Encoding]::UTF8.GetBytes($key))).ToLowerInvariant() }
}

function Test-AiClientBaseUrl {
    param([string]$Value)
    $uri = $null
    return [Uri]::TryCreate($Value, [UriKind]::Absolute, [ref]$uri) -and $uri.Scheme -in @('http','https') -and $uri.HostNameType -in @('Dns','IPv4') -and $uri.AbsolutePath -match '/v1/?$' -and -not $uri.UserInfo -and -not $uri.Query -and -not $uri.Fragment
}

function Read-AiGatewayConfig {
    param([string]$Path)
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { return @{schema_version=1;enabled=$false;port=11435;base_url='';backend='bonsai';require_key=$false;key_hash=''} }
    $c = Get-Content -LiteralPath $Path -Raw | ConvertFrom-Json -AsHashtable
    if ($c.schema_version -ne 1 -or $c.enabled -isnot [bool] -or $c.require_key -isnot [bool] -or $c.port -isnot [long] -and $c.port -isnot [int] -or $c.port -lt 1024 -or $c.port -gt 65535 -or $c.backend -notmatch '^[a-zA-Z0-9_.-]+$') { throw 'Некорректная gateway.json.' }
    if (-not (Test-AiClientBaseUrl $c.base_url)) { throw 'OpenAI Base URL: HTTP/HTTPS-адрес с /v1, без пароля/параметров.' }
    if ($c.key_hash -isnot [string] -or $c.key_hash -and $c.key_hash -notmatch '^[a-f0-9]{64}$' -or $c.require_key -and -not $c.key_hash) { throw 'Некорректный hash API key.' }
    return $c
}

function Write-AiGatewayConfig {
    param([string]$Path, $Config)
    $temp = $Path + '.tmp-' + [Guid]::NewGuid().ToString('N')
    try {
        [IO.File]::WriteAllText($temp, ($Config | ConvertTo-Json -Depth 6), [Text.UTF8Encoding]::new($false))
        $null = Read-AiGatewayConfig $temp
        [IO.File]::Move($temp, $Path, $true)
    } finally { if ([IO.File]::Exists($temp)) { [IO.File]::Delete($temp) } }
}

function Get-AiGatewayView {
    param($Config)
    return @{enabled=$Config.enabled;port=$Config.port;base_url=$Config.base_url;backend=$Config.backend;require_key=$Config.require_key;has_key=[bool]$Config.key_hash}
}

function Update-AiGatewaySettings {
    param($Config, $Body, [string[]]$Backends)
    foreach ($key in $Body.Keys) { if ($key -notin @('action','backend','base_url','require_key')) { throw "Неизвестная настройка API: $key" } }
    $key = ''
    switch ($Body.action) {
        'generate' { $pair=New-AiApiKey; $Config.key_hash=$pair.hash; $Config.require_key=$true; $key=$pair.key }
        'save' {
            if ($Body.backend -notin $Backends -or -not (Test-AiClientBaseUrl ([string]$Body.base_url)) -or $Body.require_key -isnot [bool]) { throw 'Нужны известный движок, OpenAI Base URL и require_key.' }
            if ($Body.require_key -and -not $Config.key_hash) { throw 'Сначала сгенерируй API key.' }
            $Config.backend=[string]$Body.backend;$Config.base_url=([string]$Body.base_url).TrimEnd('/');$Config.require_key=$Body.require_key
        }
        default { throw 'Настройки API: save или generate.' }
    }
    return @{ config=$Config; key=$key }
}

Export-ModuleMember -Function New-AiApiKey, Test-AiClientBaseUrl, Read-AiGatewayConfig, Write-AiGatewayConfig, Get-AiGatewayView, Update-AiGatewaySettings
