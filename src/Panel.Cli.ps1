# Dot-sourced by OmniBox.Ai.psm1. Never print password hashes or API keys.
function Get-AiAccessInfo {
    param([string]$Root)
    Import-Module (Join-Path $PSScriptRoot 'Panel.Security.psm1') -Force
    Import-Module (Join-Path $PSScriptRoot 'Gateway.Helpers.psm1') -Force
    $panel = Read-AiPanelConfig (Join-Path $Root 'panel.json')
    $gateway = Read-AiGatewayConfig (Join-Path $Root 'gateway.json')
    $hosts = @($panel.allowed_hosts | Where-Object { $_ -notin @('localhost','127.0.0.1') })
    [pscustomobject]@{
        PanelLAN = "http://$(if($hosts.Count){$hosts[0]}else{'127.0.0.1'}):$($panel.port)"
        PanelHTTPS = $panel.public_origin
        OpenAIBaseURL = $(if($gateway.enabled){$gateway.base_url}else{'Disabled'})
        TrustedProxy = $panel.trusted_proxy_addresses -join ', '
        APIKey = $(if(-not $gateway.enabled){'Disabled'}elseif($gateway.require_key){'Required; use saved key or generate a new one in the panel'}else{'Not required'})
        Password = 'Stored as a hash; reset with ai password reset'
    }
}

function Restart-AiPanelService {
    $service = Get-AiService 'omnibox-ai'
    if ($null -ne $service -and [string]$service.Status -ne 'Stopped') {
        Stop-AiService 'omnibox-ai' 30
        Start-AiService 'omnibox-ai'
    }
}

function Set-AiPanelPassword {
    param([string]$Root, [string]$Password)
    Import-Module (Join-Path $PSScriptRoot 'Panel.Security.psm1') -Force
    $path = Join-Path $Root 'panel.json'
    $config = Read-AiPanelConfig $path
    $config.credential = New-AiPanelPassword $Password
    $temp = $path + '.tmp-' + [Guid]::NewGuid().ToString('N')
    try {
        [IO.File]::WriteAllText($temp, ($config | ConvertTo-Json -Depth 10), [Text.UTF8Encoding]::new($false))
        $null = Read-AiPanelConfig $temp
        [IO.File]::Move($temp, $path, $true)
    } finally { if([IO.File]::Exists($temp)){[IO.File]::Delete($temp)} }
    Restart-AiPanelService
}

function Invoke-AiPanelCli {
    param([string[]]$Arguments, [string]$Root)
    $command = if($Arguments.Count){$Arguments[0]}else{'info'}
    switch($command){
        'info' {
            if($Arguments.Count -gt 1){throw 'Использование: ai access / ai panel info'}
            Get-AiAccessInfo $Root | Format-List | Out-Host
        }
        'reset-password' {
            if($Arguments.Count -gt 2 -or ($Arguments.Count -eq 2 -and $Arguments[1] -ne '--generate')){throw 'ai panel reset-password [--generate]'}
            Assert-AiAdministrator
            $secret=$null;$plain=$null;$generated=$false
            try {
                if($Arguments.Count -eq 1){$secret=Read-Host 'New panel password (12+ characters; Enter to generate)' -AsSecureString}
                if($null -ne $secret -and $secret.Length){$plain=([pscredential]::new('panel',$secret)).GetNetworkCredential().Password}
                else{$plain=[Convert]::ToHexString([Security.Cryptography.RandomNumberGenerator]::GetBytes(16)).ToLowerInvariant();$generated=$true}
                Invoke-AiLocked { Set-AiPanelPassword $Root $plain }
                Write-Host 'Пароль панели заменён. Сессии входа сброшены; ИИ-движки не перезапускались.'
                if($generated){Write-Host "New panel password (save now): $plain"}
            } finally { $plain=$null;if($null -ne $secret){$secret.Dispose()} }
        }
        default { throw 'Команды: ai access, ai password reset, ai panel info, ai panel reset-password [--generate]' }
    }
}
