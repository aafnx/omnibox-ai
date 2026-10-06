Set-StrictMode -Version Latest
# Reuse shared dependencies: forcing a nested reload removes their caller-scope exports.
Import-Module (Join-Path $PSScriptRoot 'Service.Install.psm1')

# Lock order is fixed everywhere: install, then control. A thread that already owns a
# named mutex acquires it again immediately, so the web worker may take both locks and
# then run the installer in the same thread without a deadlock.
$script:InstallMutexName = 'Global\omnibox-ai-install-v1'
$script:ControlMutexName = 'Global\omnibox-ai-control-v1'
$script:JournalName = '.omnibox-ai-transaction.json'

function Enter-AiMutex {
    param([string]$Name, [int]$TimeoutSeconds, [string]$Busy)
    $mutex = [Threading.Mutex]::new($false, $Name)
    $acquired = $false
    try { $acquired = $mutex.WaitOne([TimeSpan]::FromSeconds($TimeoutSeconds)) }
    catch [Threading.AbandonedMutexException] { $acquired = $true }
    if (-not $acquired) { $mutex.Dispose(); throw $Busy }
    return $mutex
}

function Enter-AiInstallLock {
    param([int]$TimeoutSeconds = 5)
    $mutex = Enter-AiMutex $script:InstallMutexName $TimeoutSeconds 'Уже выполняется другая установка или обновление omnibox-ai (CLI или веб-панель). Дождитесь её завершения и повторите.'
    return @{ install = $mutex; control = $null }
}

function Enter-AiControlLock {
    # Blocks ai/panel engine control (start, stop, context, model) until the lock is released.
    param($Lock, [int]$TimeoutSeconds = 120)
    if ($null -ne $Lock.control) { return }
    $Lock.control = Enter-AiMutex $script:ControlMutexName $TimeoutSeconds 'Управление серверами ещё выполняется (ai или веб-панель). Установка не начата; повторите после завершения операции.'
}

function Exit-AiInstallLock {
    param($Lock)
    if ($null -eq $Lock) { return }
    foreach ($key in @('control', 'install')) {
        if ($null -ne $Lock[$key]) { try { $Lock[$key].ReleaseMutex() } finally { $Lock[$key].Dispose(); $Lock[$key] = $null } }
    }
}

function Get-AiBytesSha256 {
    param([string]$Path)
    if (-not [IO.File]::Exists($Path)) { return $null }
    return [Convert]::ToHexString([Security.Cryptography.SHA256]::HashData([IO.File]::ReadAllBytes($Path))).ToLowerInvariant()
}

function ConvertTo-AiCanonicalValue {
    # Key order and JSON formatting are not settings; nested dictionaries are sorted.
    param($Value)
    if ($Value -is [Collections.IDictionary]) {
        $sorted = [ordered]@{}
        foreach ($key in @($Value.Keys | Sort-Object { [string]$_ } -CaseSensitive)) { $sorted[[string]$key] = ConvertTo-AiCanonicalValue $Value[$key] }
        return $sorted
    }
    if ($Value -is [Collections.IList]) { return ,@($Value | ForEach-Object { ConvertTo-AiCanonicalValue $_ }) }
    return $Value
}

function ConvertTo-AiCanonicalJson {
    param($Value)
    return (ConvertTo-Json -InputObject (ConvertTo-AiCanonicalValue $Value) -Depth 30 -Compress)
}

function Get-AiXmlCanonical {
    # Element names, sorted attributes and whitespace-collapsed text: formatting is ignored,
    # arguments, environment variables and paths are compared by value.
    param([string]$Path)
    $settings = [Xml.XmlReaderSettings]::new(); $settings.DtdProcessing = [Xml.DtdProcessing]::Prohibit; $settings.XmlResolver = $null
    $settings.IgnoreComments = $true; $settings.IgnoreWhitespace = $true
    $reader = [Xml.XmlReader]::Create($Path, $settings)
    try { $xml = [Xml.XmlDocument]::new(); $xml.Load($reader) } finally { $reader.Dispose() }
    $lines = [Collections.Generic.List[string]]::new()
    $walk = {
        param($Node, [string]$Prefix)
        $path = $Prefix + '/' + $Node.Name
        $attributes = @($Node.Attributes | Sort-Object Name -CaseSensitive | ForEach-Object { $_.Name + '=' + $_.Value }) -join ' '
        $text = @($Node.ChildNodes | Where-Object { $_.NodeType -in @('Text', 'CDATA') } | ForEach-Object { $_.Value }) -join ''
        $lines.Add($path + '[' + $attributes + ']' + ([regex]::Replace($text, '\s+', ' ').Trim()))
        foreach ($child in @($Node.ChildNodes | Where-Object { $_.NodeType -eq 'Element' })) { & $walk $child $path }
    }
    & $walk $xml.DocumentElement ''
    return ($lines -join "`n")
}

function Read-AiJsonFile {
    param([string]$Path)
    if (-not [IO.File]::Exists($Path)) { return $null }
    return (Get-Content -LiteralPath $Path -Raw | ConvertFrom-Json -AsHashtable)
}

function Get-AiInstallSettings {
    <#
    Normalized user settings of an installation. Values, not files: formatting, key order and
    documented migrations are not differences. Recovery history and the delegated gh credential
    are not settings the installer may touch, so they are compared byte for byte.
    #>
    param([string]$Directory)
    $result = [ordered]@{ config = $null; panel = $null; allowed_hosts = $null; gateway = $null; profiles = [ordered]@{}; immutable = [ordered]@{} }
    $config = Read-AiJsonFile (Join-Path $Directory 'config.json')
    if ($null -ne $config) {
        $copy = ConvertFrom-Json -InputObject (ConvertTo-Json -InputObject $config -Depth 30) -AsHashtable
        # Migration rule: the installer derives runtime_path from service_config (checked separately).
        if ($copy.Contains('backends') -and $copy.backends -is [Collections.IDictionary]) {
            foreach ($name in @($copy.backends.Keys)) {
                $backend = $copy.backends[$name]
                if ($backend -isnot [Collections.IDictionary]) { continue }
                $null = $backend.Remove('runtime_path')
                $profile = [string]$backend['service_config']
                if (-not $profile) { continue }
                # A profile that is not valid XML is still a setting: compare its bytes instead.
                $result.profiles[$name] = if (-not [IO.File]::Exists($profile)) { '<missing>' } else { try { Get-AiXmlCanonical $profile } catch { '<unparsed:' + (Get-AiBytesSha256 $profile) + '>' } }
                foreach ($suffix in @('.ai-previous', '.ai-state.json')) { $result.immutable[$profile + $suffix] = Get-AiBytesSha256 ($profile + $suffix) }
            }
        }
        $result.config = ConvertTo-AiCanonicalJson $copy
    }
    $panel = Read-AiJsonFile (Join-Path $Directory 'panel.json')
    if ($null -ne $panel) {
        # Migration rule: older panel.json has no proxy keys; their defaults are empty.
        $values = [ordered]@{}
        foreach ($key in @('schema_version', 'port', 'credential', 'ssh_target', 'remote_addresses')) { $values[$key] = $(if ($panel.Contains($key)) { $panel[$key] } else { $null }) }
        # Migration rule: the panel stores the origin as scheme://host[:port] (Set-AiPanelProxyDefaults).
        $origin = $(if ($panel.Contains('public_origin') -and $null -ne $panel.public_origin) { [string]$panel.public_origin } else { '' })
        $uri = $null; if ($origin -and [Uri]::TryCreate($origin, [UriKind]::Absolute, [ref]$uri)) { $origin = $uri.GetLeftPart([UriPartial]::Authority) }
        $values.public_origin = $origin
        $values.trusted_proxy_addresses = @($(if ($panel.Contains('trusted_proxy_addresses') -and $null -ne $panel.trusted_proxy_addresses) { $panel.trusted_proxy_addresses }))
        $values.remote_addresses = @($values.remote_addresses)
        $result.panel = ConvertTo-AiCanonicalJson $values
        $result.allowed_hosts = @($(if ($panel.Contains('allowed_hosts')) { @($panel.allowed_hosts | ForEach-Object { ([string]$_).ToLowerInvariant() }) }))
    }
    $gateway = Read-AiJsonFile (Join-Path $Directory 'gateway.json')
    if ($null -ne $gateway) {
        $values = [ordered]@{}
        foreach ($key in @('schema_version', 'enabled', 'port', 'base_url', 'backend', 'require_key', 'key_hash')) { $values[$key] = $(if ($gateway.Contains($key)) { $gateway[$key] } else { $null }) }
        # Migration rule: the installer stores Base URL without a trailing slash.
        if ($values.base_url -is [string]) { $values.base_url = $values.base_url.TrimEnd('/') }
        $result.gateway = ConvertTo-AiCanonicalJson $values
    }
    $result.immutable[(Join-Path $Directory 'updates/auth/github.dpapi')] = Get-AiBytesSha256 (Join-Path $Directory 'updates/auth/github.dpapi')
    return $result
}

function Set-AiCanonicalProperty {
    param([string]$Json, [string]$Key, $Value)
    $values = ConvertFrom-Json -InputObject $Json -AsHashtable
    $values[$Key] = $Value
    return ConvertTo-AiCanonicalJson $values
}

function Get-AiExpectedInstallSettings {
    <#
    Expected values after installation: the values before installation, changed only by
    explicit migration rules and by settings the user requested (wizard or parameters).
    Overrides use 'config.default_backend', 'panel.<key>', 'gateway.<key>' and
    'allowed_hosts.extra' (hosts the installer may add; existing hosts are never removed).
    #>
    param($Before, [hashtable]$Overrides = @{})
    $expected = [ordered]@{}
    foreach ($key in $Before.Keys) { $expected[$key] = $Before[$key] }
    $expected.profiles = [ordered]@{}; foreach ($key in $Before.profiles.Keys) { $expected.profiles[$key] = $Before.profiles[$key] }
    $expected.immutable = [ordered]@{}; foreach ($key in $Before.immutable.Keys) { $expected.immutable[$key] = $Before.immutable[$key] }
    $expected.allowed_hosts_extra = @()
    foreach ($name in $Overrides.Keys) {
        $value = $Overrides[$name]
        if ($name -eq 'allowed_hosts.extra') { $expected.allowed_hosts_extra = @($value | ForEach-Object { ([string]$_).ToLowerInvariant() }); continue }
        $section, $key = $name -split '\.', 2
        if ($section -notin @('config', 'panel', 'gateway')) { throw "Unknown settings override: $name" }
        if ($null -eq $expected[$section]) { continue }
        $expected[$section] = Set-AiCanonicalProperty $expected[$section] $key $value
    }
    return $expected
}

function Compare-AiInstallSettings {
    # Returns readable differences; values themselves are not printed (they include hashes).
    param($Expected, $Actual)
    $differences = [Collections.Generic.List[string]]::new()
    foreach ($section in @('config', 'panel', 'gateway')) {
        if ($null -eq $Expected[$section]) { continue }
        if ($null -eq $Actual[$section]) { $differences.Add("$section.json отсутствует"); continue }
        $want = ConvertFrom-Json -InputObject $Expected[$section] -AsHashtable
        $have = ConvertFrom-Json -InputObject $Actual[$section] -AsHashtable
        foreach ($key in @(@($want.Keys) + @($have.Keys) | Select-Object -Unique)) {
            if ((ConvertTo-AiCanonicalJson $want[$key]) -cne (ConvertTo-AiCanonicalJson $have[$key])) { $differences.Add("$section.$key") }
        }
    }
    if ($null -ne $Expected.allowed_hosts) {
        $have = @($Actual.allowed_hosts)
        foreach ($hostName in $Expected.allowed_hosts) { if ($hostName -notin $have) { $differences.Add("panel.allowed_hosts: удалён $hostName") } }
        foreach ($hostName in $have) { if ($hostName -notin $Expected.allowed_hosts -and $hostName -notin $Expected.allowed_hosts_extra) { $differences.Add("panel.allowed_hosts: лишний $hostName") } }
    }
    foreach ($name in $Expected.profiles.Keys) {
        if (-not $Actual.profiles.Contains($name) -or $Actual.profiles[$name] -cne $Expected.profiles[$name]) { $differences.Add("XML-профиль $name") }
    }
    foreach ($path in $Expected.immutable.Keys) {
        $have = if ($Actual.immutable.Contains($path)) { $Actual.immutable[$path] } else { Get-AiBytesSha256 $path }
        if ($have -cne $Expected.immutable[$path]) { $differences.Add("файл изменён: $path") }
    }
    return $differences.ToArray()
}

function Assert-AiInstallSettings {
    param($Expected, [string]$Directory, [string]$Context = 'после установки')
    $actual = Get-AiInstallSettings $Directory
    $differences = @(Compare-AiInstallSettings $Expected $actual)
    if ($differences.Count) { throw "Проверка сохранения настроек $Context не пройдена: $($differences -join '; ')" }
    $config = Read-AiJsonFile (Join-Path $Directory 'config.json')
    if ($null -ne $config) {
        foreach ($name in @($config.backends.Keys)) {
            $backend = $config.backends[$name]
            if ($backend.Contains('runtime_path') -and $backend.runtime_path -cne ($backend.service_config + '.runtime.json')) { throw "Проверка сохранения настроек $Context не пройдена: config.backends.$name.runtime_path" }
        }
    }
}

function Get-AiFirewallSnapshot {
    param([string[]]$Names)
    $rules = @()
    if (-not (Get-Command Get-NetFirewallRule -ErrorAction SilentlyContinue)) { return ,$rules }
    foreach ($name in $Names) {
        $rule = Get-NetFirewallRule -Name $name -ErrorAction SilentlyContinue | Select-Object -First 1
        if ($null -eq $rule) { $rules += @{ name = $name; exists = $false }; continue }
        $port = @($rule | Get-NetFirewallPortFilter -ErrorAction Stop)[0]
        $address = @($rule | Get-NetFirewallAddressFilter -ErrorAction Stop)[0]
        $rules += @{ name = $name; exists = $true; display = [string]$rule.DisplayName; enabled = [string]$rule.Enabled; protocol = [string]$port.Protocol; port = @($port.LocalPort | ForEach-Object { [string]$_ }); addresses = @($address.RemoteAddress | ForEach-Object { [string]$_ }) }
    }
    return ,$rules
}

function Restore-AiFirewallSnapshot {
    param($Rules)
    if (-not (Get-Command Get-NetFirewallRule -ErrorAction SilentlyContinue)) { return }
    foreach ($rule in @($Rules)) {
        if ($null -ne (Get-NetFirewallRule -Name $rule.name -ErrorAction SilentlyContinue)) { Remove-NetFirewallRule -Name $rule.name -ErrorAction Stop }
        if ($rule.exists) {
            $enabled = if ($rule.enabled -eq 'True') { 'True' } else { 'False' }
            $null = New-NetFirewallRule -Name $rule.name -DisplayName $rule.display -Direction Inbound -Action Allow -Enabled $enabled -Protocol $rule.protocol -LocalPort $rule.port -Profile Any -RemoteAddress $rule.addresses
        }
    }
}

function Write-AiInstallJournal {
    param($Transaction)
    $value = [ordered]@{ schema_version = 1; state = $Transaction.state; snapshot = $Transaction.snapshot; started = $Transaction.started; steps = @($Transaction.steps); error = $Transaction.error; recovery = $Transaction.recovery }
    $json = ConvertTo-Json -InputObject $value -Depth 6
    foreach ($path in @((Join-Path $Transaction.snapshot 'journal.json'), $Transaction.journal)) {
        # The journal in the installation stays until the installation or its rollback is complete,
        # so an interrupted or incomplete rollback is retried by the next installer run.
        if ($path -eq $Transaction.journal -and $Transaction.state -in @('committed', 'rolled-back')) {
            if ([IO.File]::Exists($path)) { [IO.File]::Delete($path) }
            continue
        }
        $temp = $path + '.tmp'
        [IO.File]::WriteAllText($temp, $json, [Text.UTF8Encoding]::new($false))
        [IO.File]::Move($temp, $path, $true)
    }
}

function New-AiInstallTransaction {
    <#
    Takes a restorable snapshot before the first change: application files, settings,
    backend XML profiles with recovery history, service commands and start modes,
    firewall rules and normalized settings. The journal marks an unfinished installation.
    #>
    param([string]$Directory, [string[]]$Files, [Collections.IDictionary]$ServiceProfiles, [string[]]$ExternalFiles = @(), [bool]$PanelWasRunning, [string]$Version)
    $snapshot = Join-Path $Directory ('backups/' + (Get-Date -Format 'yyyyMMdd-HHmmss') + '-' + [Guid]::NewGuid().ToString('N').Substring(0, 8))
    $null = New-Item -ItemType Directory -Path (Join-Path $snapshot 'files'), (Join-Path $snapshot 'external') -Force
    $entries = @()
    foreach ($file in ($Files | Select-Object -Unique)) {
        $source = Join-Path $Directory $file
        $exists = [IO.File]::Exists($source)
        if ($exists) {
            $target = Join-Path (Join-Path $snapshot 'files') $file
            $null = New-Item -ItemType Directory -Path (Split-Path $target -Parent) -Force
            Copy-Item -LiteralPath $source -Destination $target
        }
        $entries += @{ path = $file; exists = $exists; sha256 = Get-AiBytesSha256 $source }
    }
    $external = @(); $index = 0
    foreach ($path in ($ExternalFiles | Where-Object { $_ } | Select-Object -Unique)) {
        $exists = [IO.File]::Exists($path); $copy = Join-Path $snapshot ('external/' + ($index++) + '.bin')
        if ($exists) { Copy-Item -LiteralPath $path -Destination $copy }
        $external += @{ path = $path; exists = $exists; copy = $copy; sha256 = Get-AiBytesSha256 $path }
    }
    $services = @()
    # Service name -> launch profile; the profile identifies commands that belong to this installation.
    foreach ($name in @($ServiceProfiles.Keys | Sort-Object)) {
        $profile = [string]$ServiceProfiles[$name]
        $service = Get-CimInstance Win32_Service -Filter "Name='$name'" -ErrorAction Stop
        if ($null -eq $service) { $services += @{ name = $name; profile = $profile; exists = $false }; continue }
        $startup = switch ([string]$service.StartMode) { 'Auto' { 'Automatic' } 'Manual' { 'Manual' } 'Disabled' { 'Disabled' } default { throw "Unexpected start mode of $name." } }
        $services += @{ name = $name; profile = $profile; exists = $true; command = [string]$service.PathName; startup = $startup; state = [string]$service.State }
    }
    $transaction = @{
        directory = $Directory; snapshot = $snapshot; journal = (Join-Path $Directory $script:JournalName); version = $Version
        started = [DateTime]::UtcNow.ToString('o'); state = 'running'; steps = [Collections.Generic.List[string]]::new(); error = ''; recovery = ''
        files = $entries; external = $external; services = $services; firewall = (Get-AiFirewallSnapshot @('omnibox-ai-panel', 'omnibox-ai-api'))
        panel_was_running = $PanelWasRunning; settings = (Get-AiInstallSettings $Directory)
    }
    $manifest = @{}; foreach ($key in @('directory', 'snapshot', 'version', 'files', 'external', 'services', 'firewall', 'panel_was_running')) { $manifest[$key] = $transaction[$key] }
    [IO.File]::WriteAllText((Join-Path $snapshot 'snapshot.json'), (ConvertTo-Json -InputObject $manifest -Depth 8), [Text.UTF8Encoding]::new($false))
    [IO.File]::WriteAllText((Join-Path $snapshot 'settings.json'), (ConvertTo-Json -InputObject $transaction.settings -Depth 8), [Text.UTF8Encoding]::new($false))
    Write-AiInstallJournal $transaction
    return $transaction
}

function Add-AiInstallStep {
    param($Transaction, [string]$Name)
    $Transaction.steps.Add($Name)
    Write-AiInstallJournal $Transaction
    # Test-only fault injection: a failure at a named step must restore the previous installation.
    if ($env:OMNIBOX_AI_INSTALL_FAIL_STEP -and $env:OMNIBOX_AI_INSTALL_FAIL_STEP -ceq $Name) { throw "Искусственный сбой установки на шаге $Name." }
}

function Complete-AiInstallTransaction {
    param($Transaction)
    $Transaction.state = 'committed'
    Write-AiInstallJournal $Transaction
}

function Get-AiInterruptedInstallTransaction {
    # A journal left behind means the previous installer died midway or could not finish its rollback.
    param([string]$Directory)
    $path = Join-Path $Directory $script:JournalName
    if (-not [IO.File]::Exists($path)) { return $null }
    $journal = Read-AiJsonFile $path
    if ($journal.state -notin @('running', 'rolling-back', 'rollback-failed')) { [IO.File]::Delete($path); return $null }
    $snapshot = [IO.Path]::GetFullPath([string]$journal.snapshot)
    $backups = [IO.Path]::GetFullPath((Join-Path $Directory 'backups')) + [IO.Path]::DirectorySeparatorChar
    if (-not $snapshot.StartsWith($backups, [StringComparison]::OrdinalIgnoreCase)) { throw 'Журнал прерванной установки указывает за пределы backups; восстановление отменено.' }
    $manifest = Read-AiJsonFile (Join-Path $snapshot 'snapshot.json')
    $settings = Read-AiJsonFile (Join-Path $snapshot 'settings.json')
    if ($null -eq $manifest -or $null -eq $settings) { throw "Снимок прерванной установки неполон: $snapshot" }
    $transaction = @{ journal = $path; state = 'running'; started = $journal.started; steps = [Collections.Generic.List[string]]::new(); error = 'Предыдущая установка прервана.'; recovery = '' }
    foreach ($key in $manifest.Keys) { $transaction[$key] = $manifest[$key] }
    foreach ($step in @($journal.steps)) { $transaction.steps.Add([string]$step) }
    $transaction.settings = $settings
    return $transaction
}

function Stop-AiOwnedService {
    param([string]$Name, [string[]]$Commands)
    $service = Get-CimInstance Win32_Service -Filter "Name='$Name'" -ErrorAction Stop
    if ($null -eq $service -or $service.State -eq 'Stopped') { return }
    if ([string]$service.PathName -notin $Commands) { throw "Служба $Name выполняет чужую команду; восстановление её не останавливает." }
    $process = if ($service.ProcessId) { Get-Process -Id $service.ProcessId -ErrorAction SilentlyContinue } else { $null }
    Stop-Service -Name $Name -NoWait -ErrorAction Stop
    (Get-Service -Name $Name).WaitForStatus('Stopped', [TimeSpan]::FromSeconds(30))
    if ($null -ne $process -and -not $process.WaitForExit(15000)) { throw "Процесс службы $Name не завершился." }
}

function Undo-AiInstallTransaction {
    <#
    Restores the snapshot: stops services of this installation, puts back every file and
    service command, removes what the failed installation created, restores firewall rules,
    verifies settings by value and starts the panel if it was running. Engines stay stopped.
    #>
    param($Transaction, [string]$PwshPath, [string]$Reason)
    $errors = [Collections.Generic.List[string]]::new()
    $Transaction.state = 'rolling-back'; $Transaction.error = $Reason
    $directory = $Transaction.directory
    # Only commands of this installation (before or after the failed change) are stopped or removed.
    $owned = @{}
    foreach ($service in @($Transaction.services)) {
        $owned[$service.name] = @((New-AiNativeServiceCommand $PwshPath $directory $service.profile)) + $(if ($service.exists) { @([string]$service.command) } else { @() })
        try { Stop-AiOwnedService $service.name $owned[$service.name] } catch { $errors.Add($_.Exception.Message) }
    }
    foreach ($entry in @($Transaction.files)) {
        try {
            $target = Join-Path $directory $entry.path
            if ($entry.exists) {
                if ((Get-AiBytesSha256 $target) -cne $entry.sha256) {
                    $null = New-Item -ItemType Directory -Path (Split-Path $target -Parent) -Force
                    Copy-Item -LiteralPath (Join-Path (Join-Path $Transaction.snapshot 'files') $entry.path) -Destination $target -Force
                }
            } elseif ([IO.File]::Exists($target)) { Remove-Item -LiteralPath $target -Force }
        } catch { $errors.Add("$($entry.path): $($_.Exception.Message)") }
    }
    foreach ($entry in @($Transaction.external)) {
        try {
            if ($entry.exists) { if ((Get-AiBytesSha256 $entry.path) -cne $entry.sha256) { Copy-Item -LiteralPath $entry.copy -Destination $entry.path -Force } }
            elseif ([IO.File]::Exists($entry.path)) { Remove-Item -LiteralPath $entry.path -Force }
        } catch { $errors.Add("$($entry.path): $($_.Exception.Message)") }
    }
    foreach ($service in @($Transaction.services)) {
        try {
            $current = Get-CimInstance Win32_Service -Filter "Name='$($service.name)'" -ErrorAction Stop
            if ($service.exists) {
                if ($null -eq $current) { $errors.Add("Служба $($service.name) удалена и не восстановлена автоматически."); continue }
                $mode = if ($current.StartMode -eq 'Auto') { 'Automatic' } else { [string]$current.StartMode }
                if ($current.PathName -cne $service.command -or $mode -ne $service.startup) { Set-AiServiceCommand -Name $service.name -Command $service.command -StartupType $service.startup }
            } elseif ($null -ne $current) {
                if ([string]$current.PathName -notin $owned[$service.name]) { $errors.Add("Служба $($service.name) создана не этой установкой."); continue }
                Remove-Service -Name $service.name -ErrorAction Stop
            }
        } catch { $errors.Add("служба $($service.name): $($_.Exception.Message)") }
    }
    try { Restore-AiFirewallSnapshot $Transaction.firewall } catch { $errors.Add("Firewall: $($_.Exception.Message)") }
    if (-not $errors.Count) {
        try { Assert-AiInstallSettings (Get-AiExpectedInstallSettings $Transaction.settings) $directory 'после отката' } catch { $errors.Add($_.Exception.Message) }
    }
    if ($Transaction.panel_was_running -and -not $errors.Count) {
        try { Start-Service -Name 'omnibox-ai' -ErrorAction Stop } catch { $errors.Add("Панель не запущена: $($_.Exception.Message)") }
    }
    $Transaction.state = if ($errors.Count) { 'rollback-failed' } else { 'rolled-back' }
    $Transaction.recovery = ($errors -join '; ')
    try { Write-AiInstallJournal $Transaction } catch { $errors.Add("Журнал: $($_.Exception.Message)") }
    return @{ restored = -not $errors.Count; errors = $errors.ToArray(); snapshot = $Transaction.snapshot }
}

Export-ModuleMember -Function Enter-AiInstallLock, Enter-AiControlLock, Exit-AiInstallLock, Get-AiInstallSettings, Get-AiExpectedInstallSettings, Compare-AiInstallSettings, Assert-AiInstallSettings, New-AiInstallTransaction, Add-AiInstallStep, Complete-AiInstallTransaction, Get-AiInterruptedInstallTransaction, Undo-AiInstallTransaction, Get-AiXmlCanonical
