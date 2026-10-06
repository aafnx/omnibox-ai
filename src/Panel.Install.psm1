Set-StrictMode -Version Latest
Import-Module (Join-Path $PSScriptRoot 'Panel.Security.psm1') -Force

function Get-AiPanelInstallOptions {
    param([string]$Directory, [string]$HostName = '', [int]$Port = 18090, [string]$RemoteAddresses = 'LocalSubnet', [switch]$NonInteractive)
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
    if (-not $NonInteractive) {
        $value = Read-Host "IP или DNS-имя этого ПК для панели [$HostName]"; if ($value) { $HostName = $value.Trim() }
        $value = Read-Host "Порт веб-панели [$Port]"; if ($value) { $Port = [int]$value }
        $value = Read-Host "Разрешённые сети/адреса Windows Firewall [$RemoteAddresses]"; if ($value) { $RemoteAddresses = $value.Trim() }
    }
    if ($HostName -notmatch '^[a-zA-Z0-9][a-zA-Z0-9.-]*$' -or $HostName.Length -gt 253 -or $Port -lt 1024 -or $Port -gt 65535) { throw 'Для панели нужен IPv4/DNS-адрес без пути и порт 1024..65535.' }
    $addresses = @($RemoteAddresses -split ',' | ForEach-Object { $_.Trim() })
    foreach ($address in $addresses) { if ($address -ne 'LocalSubnet' -and $address -notmatch '^[0-9a-fA-F:.]+(?:/[0-9]{1,3})?$') { throw 'Firewall: LocalSubnet или IP/CIDR, разделённые запятыми.' } }
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
    return @{ schema_version = 1; port = $Port; allowed_hosts = @(@($HostName, '127.0.0.1', 'localhost', [Environment]::MachineName.ToLowerInvariant()) | Select-Object -Unique); credential = $credential; ssh_target = $ssh; remote_addresses = $addresses; generated_password = $generated }
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
    param([string]$Directory, [string]$PwshPath, $Config, $Options)
    $wrapper = Join-Path $Directory 'panel-service.exe'
    if (-not (Test-Path -LiteralPath $wrapper -PathType Leaf)) {
        $source = $null
        foreach ($b in $Config.backends.Values) {
            $candidate = [IO.Path]::ChangeExtension($b.service_config, '.exe')
            if (Test-Path -LiteralPath $candidate -PathType Leaf) { $source = $candidate; break }
        }
        if (-not $source) { throw 'Не найден существующий WinSW wrapper рядом с XML Bonsai/Ollama.' }
        Copy-Item -LiteralPath $source -Destination $wrapper
    }
    $service = Get-Service -Name 'omnibox-ai' -ErrorAction SilentlyContinue
    if ($null -ne $service -and [string]$service.Status -ne 'Stopped') {
        Stop-Service -Name 'omnibox-ai' -ErrorAction Stop
        $service.WaitForStatus('Stopped', [TimeSpan]::FromSeconds(30))
    }
    if (Get-NetTCPConnection -LocalPort $Options.port -State Listen -ErrorAction SilentlyContinue) { throw "Порт $($Options.port) занят другим процессом. Выбери другой порт панели." }
    $password = $Options.generated_password
    $saved = @{}; foreach ($key in $Options.Keys) { if ($key -ne 'generated_password') { $saved[$key] = $Options[$key] } }
    Set-Content -LiteralPath (Join-Path $Directory 'panel.json') -Value ($saved | ConvertTo-Json -Depth 8) -Encoding utf8
    $xml = New-AiPanelServiceXml $PwshPath $Directory
    $xml.Save((Join-Path $Directory 'panel-service.xml'))
    $null = New-Item -ItemType Directory -Path (Join-Path $Directory 'logs') -Force
    Protect-AiPanelDirectory $Directory
    if ($null -eq $service) { & $wrapper install; if ($LASTEXITCODE -ne 0) { throw 'Не удалось зарегистрировать службу веб-панели.' } }
    Set-Service -Name 'omnibox-ai' -StartupType Automatic
    $rule = Get-NetFirewallRule -Name 'omnibox-ai-panel' -ErrorAction SilentlyContinue
    if ($null -ne $rule) { Remove-NetFirewallRule -Name 'omnibox-ai-panel' }
    $null = New-NetFirewallRule -Name 'omnibox-ai-panel' -DisplayName 'OmniBox AI panel' -Direction Inbound -Action Allow -Enabled True -Protocol TCP -LocalPort $Options.port -Profile Any -RemoteAddress $Options.remote_addresses
    Start-Service -Name 'omnibox-ai' -ErrorAction Stop
    $hostName = $Options.allowed_hosts[0]
    $ready = $false
    for ($i = 0; $i -lt 30; $i++) {
        try { $null = Invoke-WebRequest -Uri "http://127.0.0.1:$($Options.port)/" -TimeoutSec 2; $ready = $true; break } catch { Start-Sleep -Milliseconds 500 }
    }
    if (-not $ready) { throw "Панель не ответила. Журналы: $Directory\logs\panel-service.err.log и wrapper.log" }
    Write-Host "Веб-панель: http://${hostName}:$($Options.port)"
    if ($password) { Write-Host "Сгенерированный пароль панели: $password"; Write-Host 'Сохрани пароль в Bitwarden: открытым текстом он нигде не сохраняется.' }
    else { Write-Host 'Пароль панели: выбранный при первой установке.' }
    Write-Host 'Панель запускается с Windows. ИИ-движки сами не запускаются.'
}

Export-ModuleMember -Function Get-AiPanelInstallOptions, Install-AiPanelService, New-AiPanelServiceXml
