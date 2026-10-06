Set-StrictMode -Version Latest
# Reuse shared dependencies: forcing a nested reload removes their caller-scope exports.
Import-Module (Join-Path $PSScriptRoot 'Panel.Security.psm1')
Import-Module (Join-Path $PSScriptRoot 'Setup.Helpers.psm1')
Import-Module (Join-Path $PSScriptRoot 'Gateway.Helpers.psm1')
Import-Module (Join-Path $PSScriptRoot 'Service.Install.psm1')

function Get-AiGatewayInstallOptions {
    param([string]$Directory, [string]$HostName, [string]$Backend = 'bonsai', [switch]$NonInteractive, [int]$Port = 18090, [string]$BaseUrl = '', [switch]$NoApi, [int]$PanelPort = 18090, [string]$PublicUrl = '')
    $path = Join-Path $Directory 'gateway.json'
    $exists = Test-Path -LiteralPath $path -PathType Leaf
    $c = Read-AiGatewayConfig $path
    $enabled = if ($exists) { $c.enabled } else { $true }
    if ($NoApi) { $enabled = $false }
    if (-not $PSBoundParameters.ContainsKey('Port')) { $Port=if($exists){$c.port}else{$PanelPort} }
    if (-not $BaseUrl) { $BaseUrl=if($exists){$c.base_url}elseif($PublicUrl){$PublicUrl.TrimEnd('/')+'/v1'}else{"http://${HostName}:$Port/v1"} }
    $require = if($exists){$c.require_key}else{$enabled}
    if (-not $NonInteractive) {
        $enabled=Read-AiYesNo -Prompt 'Включить сетевой OpenAI API для Jan/Pi?' -Default $enabled
        if($enabled){
            Write-Host "OpenAI API доступен на порту панели $PanelPort по пути /v1. Другой порт добавляет отдельный вход."
            $value=Read-Host "Порт OpenAI API [$Port]";if($value){$Port=[int]$value;if(-not $exists -and -not $PublicUrl -and -not $PSBoundParameters.ContainsKey('BaseUrl')){$BaseUrl="http://${HostName}:$Port/v1"}}
            $value=Read-Host "OpenAI Base URL для клиентов [$BaseUrl]";if($value){$BaseUrl=$value.Trim()}
            $require=Read-AiYesNo -Prompt 'Использовать API key?' -Default $require
        }
    }
    if(-not $enabled -and -not $exists){$require=$false}
    $hash=if($exists){$c.key_hash}else{''};$generated=''
    if($require -and -not $hash){$pair=New-AiApiKey;$hash=$pair.hash;$generated=$pair.key}
    $result=@{schema_version=1;enabled=[bool]$enabled;port=$Port;base_url=$BaseUrl.TrimEnd('/');backend=$(if($exists){$c.backend}else{$Backend});require_key=[bool]$require;key_hash=$hash;generated_key=$generated}
    if($Port -lt 1024 -or $Port -gt 65535 -or -not(Test-AiClientBaseUrl $result.base_url)){throw 'Для OpenAI API нужен порт 1024..65535 и HTTP/HTTPS Base URL с /v1.'}
    return $result
}

function Get-AiPanelInstallOptions {
    param([string]$Directory, [string]$HostName = '', [int]$Port = 18090, [string]$RemoteAddresses = 'LocalSubnet', [switch]$NonInteractive,
        [string]$PublicUrl = '', [string]$TrustedProxyAddresses = '')
    $path = Join-Path $Directory 'panel.json'
    $existing = if (Test-Path -LiteralPath $path -PathType Leaf) { Read-AiPanelConfig $path } else { $null }
    if (-not $HostName) {
        if ($null -ne $existing) { $HostName = @($existing.allowed_hosts | Where-Object { $_ -notin @('127.0.0.1','localhost') })[0] }
        else {
            $address = Get-NetIPAddress -AddressFamily IPv4 -ErrorAction SilentlyContinue | Where-Object { $_.IPAddress -notmatch '^(127\.|169\.254\.)' -and $_.AddressState -eq 'Preferred' } | Sort-Object InterfaceMetric | Select-Object -First 1
            $HostName = if ($null -ne $address) { $address.IPAddress } else { [Environment]::MachineName.ToLowerInvariant() }
        }
    }
    if ($null -ne $existing -and -not $PSBoundParameters.ContainsKey('Port')) { $Port = [int]$existing.port }
    if ($null -ne $existing -and -not $PSBoundParameters.ContainsKey('RemoteAddresses')) { $RemoteAddresses = $existing.remote_addresses -join ',' }
    if ($null -ne $existing -and -not $PSBoundParameters.ContainsKey('PublicUrl')) { $PublicUrl = $existing.public_origin }
    if ($null -ne $existing -and -not $PSBoundParameters.ContainsKey('TrustedProxyAddresses')) { $TrustedProxyAddresses = $existing.trusted_proxy_addresses -join ',' }
    if (-not $NonInteractive) {
        $value = Read-Host "IP или DNS-имя этого ПК для панели [$HostName]"; if ($value) { $HostName = $value.Trim() }
        $value = Read-Host "Порт веб-панели [$Port]"; if ($value) { $Port = [int]$value }
        $value = Read-Host "Разрешённые сети/адреса Windows Firewall [$RemoteAddresses]"; if ($value) { $RemoteAddresses = $value.Trim() }
        $proxy = Read-AiYesNo -Prompt 'Настроить HTTPS-доступ к панели через reverse proxy на NAS?' -Default ([bool]$PublicUrl)
        if ($proxy) {
            $value = Read-Host "Внешний URL панели без пути [$PublicUrl]"; if ($value) { $PublicUrl = $value.Trim() }
            $value = Read-Host "IP NAS, который подключается к этому ПК [$TrustedProxyAddresses]"; if ($value) { $TrustedProxyAddresses = $value.Trim() }
        } else { $PublicUrl = ''; $TrustedProxyAddresses = '' }
    }
    if ($HostName -notmatch '^[a-zA-Z0-9][a-zA-Z0-9.-]*$' -or $HostName.Length -gt 253 -or $Port -lt 1024 -or $Port -gt 65535) { throw 'Для панели нужен IPv4/DNS-адрес без пути и порт 1024..65535.' }
    $addresses = @($RemoteAddresses -split ',' | ForEach-Object { $_.Trim() })
    foreach ($address in $addresses) { if ($address -ne 'LocalSubnet' -and $address -notmatch '^[0-9a-fA-F:.]+(?:/[0-9]{1,3})?$') { throw 'Firewall: LocalSubnet или IP/CIDR, разделённые запятыми.' } }
    $proxyOptions = @{ public_origin = $PublicUrl; trusted_proxy_addresses = @($TrustedProxyAddresses -split ',' | ForEach-Object { $_.Trim() } | Where-Object { $_ }) }
    Set-AiPanelProxyDefaults $proxyOptions
    $credential = if ($null -ne $existing) { $existing.credential } else { $null }
    $generated = ''
    if ($null -eq $credential) {
        $secret = if ($NonInteractive) { $null } else { Read-Host 'Пароль панели (от 12 символов; Enter — сгенерировать)' -AsSecureString }
        try {
            $plain = if ($null -ne $secret -and $secret.Length) { ([pscredential]::new('panel', $secret)).GetNetworkCredential().Password } else { $generated = [Convert]::ToHexString([Security.Cryptography.RandomNumberGenerator]::GetBytes(12)).ToLowerInvariant(); $generated }
            $credential = New-AiPanelPassword $plain
        } finally { $plain = $null; if ($null -ne $secret) { $secret.Dispose() } }
    }
    $ssh = if ($null -ne $existing) { $existing.ssh_target } else { "$($env:USERNAME)@$HostName" }
    $hosts = @($HostName, '127.0.0.1', 'localhost', [Environment]::MachineName.ToLowerInvariant())
    if ($null -ne $existing) { $hosts += @($existing.allowed_hosts) }
    if ($proxyOptions.public_origin) { $hosts += ([Uri]$proxyOptions.public_origin).Host }
    return @{ schema_version = 1; port = $Port; allowed_hosts = @($hosts | Select-Object -Unique); credential = $credential; ssh_target = $ssh; remote_addresses = $addresses; generated_password = $generated; public_origin = $proxyOptions.public_origin; trusted_proxy_addresses = $proxyOptions.trusted_proxy_addresses }
}

function New-AiPanelServiceXml {
    param([string]$PwshPath, [string]$Directory)
    $xml = [Xml.XmlDocument]::new()
    $xml.LoadXml('<service><id>omnibox-ai</id><name>OmniBox AI</name><description>AI server control panel</description><executable/><arguments/><workingdirectory/><startmode>Automatic</startmode><stoptimeout>30 sec</stoptimeout><stopparentprocessfirst>true</stopparentprocessfirst><logpath/><log mode="roll-by-size"><sizeThreshold>10240</sizeThreshold><keepFiles>3</keepFiles></log></service>')
    $xml.service.executable = $PwshPath
    $xml.service.arguments = '-NoLogo -NoProfile -ExecutionPolicy Bypass -File "' + (Join-Path $Directory 'panel.ps1') + '"'
    $xml.service.workingdirectory = $Directory
    $xml.service.logpath = Join-Path $Directory 'logs'
    return ,$xml
}

function Protect-AiPanelDirectory {
    param([string]$Directory)
    # The panel controls services as SYSTEM. Its code and credential must not be writable by untrusted local users.
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    try {
        $principals = @([Security.Principal.SecurityIdentifier]::new('S-1-5-18'), [Security.Principal.SecurityIdentifier]::new('S-1-5-32-544'), $identity.User)
        $items = @((Get-Item -LiteralPath $Directory)) + @(Get-ChildItem -LiteralPath $Directory -Recurse -Force)
        foreach ($item in $items) {
            if ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'Ссылки/junction внутри папки панели запрещены.' }
            $acl = if ($item.PSIsContainer) { [Security.AccessControl.DirectorySecurity]::new() } else { [Security.AccessControl.FileSecurity]::new() }
            $acl.SetAccessRuleProtection($true, $false)
            $acl.SetOwner($identity.User)
            foreach ($sid in $principals) {
                $rule = if ($item.PSIsContainer) { [Security.AccessControl.FileSystemAccessRule]::new($sid, 'FullControl', 'ContainerInherit,ObjectInherit', 'None', 'Allow') } else { [Security.AccessControl.FileSystemAccessRule]::new($sid, 'FullControl', 'Allow') }
                $acl.AddAccessRule($rule)
            }
            Set-Acl -LiteralPath $item.FullName -AclObject $acl
        }
    } finally { $identity.Dispose() }
}

function Install-AiPanelService {
    param([string]$Directory, [string]$PwshPath, $Config, $Options, $GatewayOptions)
    $service = Get-Service -Name 'omnibox-ai' -ErrorAction SilentlyContinue
    if ($null -ne $service -and [string]$service.Status -ne 'Stopped') {
        Stop-Service -Name 'omnibox-ai' -NoWait -ErrorAction Stop
        $service.WaitForStatus('Stopped', [TimeSpan]::FromSeconds(30))
    }
    if (Get-NetTCPConnection -LocalPort $Options.port -State Listen -ErrorAction SilentlyContinue) { throw "Порт $($Options.port) занят другим процессом. Выбери другой порт панели." }
    if($GatewayOptions.enabled -and $GatewayOptions.port -ne $Options.port -and (Get-NetTCPConnection -LocalPort $GatewayOptions.port -State Listen -ErrorAction SilentlyContinue)){throw "Порт OpenAI API $($GatewayOptions.port) занят."}
    $password = $Options.generated_password
    $saved = @{}; foreach ($key in $Options.Keys) { if ($key -ne 'generated_password') { $saved[$key] = $Options[$key] } }
    Set-Content -LiteralPath (Join-Path $Directory 'panel.json') -Value ($saved | ConvertTo-Json -Depth 8) -Encoding utf8
    $savedGateway=@{};foreach($key in $GatewayOptions.Keys){if($key -ne 'generated_key'){$savedGateway[$key]=$GatewayOptions[$key]}}
    Write-AiGatewayConfig (Join-Path $Directory 'gateway.json') $savedGateway
    $xml = New-AiPanelServiceXml $PwshPath $Directory
    $xml.Save((Join-Path $Directory 'panel-service.xml'))
    $null = New-Item -ItemType Directory -Path (Join-Path $Directory 'logs') -Force
    Protect-AiPanelDirectory $Directory
    $command=New-AiNativeServiceCommand $PwshPath $Directory (Join-Path $Directory 'panel-service.xml')
    if ($null -eq $service) { $null=New-Service -Name 'omnibox-ai' -BinaryPathName $command -StartupType Automatic -DisplayName 'OmniBox AI' }
    else { Set-Service -Name 'omnibox-ai' -BinaryPathName $command -ErrorAction Stop }
    Set-Service -Name 'omnibox-ai' -StartupType Automatic
    $rule = Get-NetFirewallRule -Name 'omnibox-ai-panel' -ErrorAction SilentlyContinue
    if ($null -ne $rule) { Remove-NetFirewallRule -Name 'omnibox-ai-panel' }
    $null = New-NetFirewallRule -Name 'omnibox-ai-panel' -DisplayName 'OmniBox AI panel' -Direction Inbound -Action Allow -Enabled True -Protocol TCP -LocalPort $Options.port -Profile Any -RemoteAddress $Options.remote_addresses
    Get-NetFirewallRule -Name 'omnibox-ai-api' -ErrorAction SilentlyContinue | Remove-NetFirewallRule
    if($GatewayOptions.enabled -and $GatewayOptions.port -ne $Options.port){$null=New-NetFirewallRule -Name 'omnibox-ai-api' -DisplayName 'OmniBox OpenAI API' -Direction Inbound -Action Allow -Enabled True -Protocol TCP -LocalPort $GatewayOptions.port -Profile Any -RemoteAddress $Options.remote_addresses}
    Start-Service -Name 'omnibox-ai' -ErrorAction Stop
    $hostName = $Options.allowed_hosts[0]
    $ready = $false
    for ($i = 0; $i -lt 30; $i++) {
        try { $null = Invoke-WebRequest -Uri "http://127.0.0.1:$($Options.port)/" -TimeoutSec 2; $ready = $true; break } catch { Start-Sleep -Milliseconds 500 }
    }
    if (-not $ready) { throw "Панель не ответила. Журналы: $Directory\logs\panel-service.err.log и panel-service.host.log" }
    Write-Host "Веб-панель: http://${hostName}:$($Options.port)"
    if ($Options.public_origin) { Write-Host "Через NAS после настройки proxy: $($Options.public_origin)" }
    if($GatewayOptions.enabled){Write-Host "OpenAI Base URL: $($GatewayOptions.base_url)";if($GatewayOptions.generated_key){Write-Host "API key (сохрани в Bitwarden): $($GatewayOptions.generated_key)"}}
    if ($password) { Write-Host "Сгенерированный пароль панели: $password"; Write-Host 'Сохрани пароль в Bitwarden: открытым текстом он нигде не сохраняется.' }
    else { Write-Host 'Пароль панели: выбранный при первой установке.' }
    Write-Host 'Панель запускается с Windows. ИИ-движки сами не запускаются.'
}

Export-ModuleMember -Function Get-AiPanelInstallOptions, Get-AiGatewayInstallOptions, Install-AiPanelService, New-AiPanelServiceXml
