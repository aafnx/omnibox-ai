Set-StrictMode -Version Latest

function Send-AiHttpResponse {
    param($Context, [int]$Status, $Value, [string]$ContentType = 'application/json; charset=utf-8')
    $response = $Context.Response
    $response.StatusCode = $Status
    $response.ContentType = $ContentType
    $response.Headers['Cache-Control'] = 'no-store'
    $response.Headers['X-Content-Type-Options'] = 'nosniff'
    $response.Headers['X-Frame-Options'] = 'DENY'
    $response.Headers['Referrer-Policy'] = 'no-referrer'
    $response.Headers['Content-Security-Policy'] = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"
    $text = if ($ContentType.StartsWith('application/json')) { ConvertTo-Json -InputObject $Value -Depth 20 -Compress } else { [string]$Value }
    $bytes = [Text.Encoding]::UTF8.GetBytes($text)
    $response.ContentLength64 = $bytes.Length
    try { $response.OutputStream.Write($bytes, 0, $bytes.Length) } finally { $response.Close() }
}

function Read-AiHttpJson {
    param($Request)
    if ($Request.ContentType -notmatch '^application/json(?:\s*;|$)' -or $Request.ContentLength64 -lt 0 -or $Request.ContentLength64 -gt 16384) { throw 'Нужен JSON-запрос до 16 КиБ с Content-Length.' }
    $buffer = [byte[]]::new([int]$Request.ContentLength64)
    $offset = 0
    while ($offset -lt $buffer.Length) {
        $read = $Request.InputStream.Read($buffer, $offset, $buffer.Length - $offset)
        if ($read -eq 0) { throw 'Неполный запрос.' }; $offset += $read
    }
    $body = [Text.Encoding]::UTF8.GetString($buffer) | ConvertFrom-Json -AsHashtable -Depth 12
    if ($body -isnot [System.Collections.IDictionary]) { throw 'Нужен JSON-объект.' }
    return $body
}

function Update-AiPanelOperations {
    param($Operations)
    foreach ($operation in @($Operations.Values)) {
        if ($null -ne $operation.job -and $operation.job.State -in @('Completed','Failed','Stopped')) {
            $output = @(Receive-Job $operation.job -ErrorAction SilentlyContinue -InformationAction SilentlyContinue)
            $result = $output | Where-Object { $_ -is [System.Collections.IDictionary] -and $_.Contains('ok') } | Select-Object -Last 1
            $operation.state = if ($null -ne $result -and $result.ok) { 'done' } else { 'failed' }
            $operation.message = if ($null -ne $result) { [string]$result.message } else { 'Операция прервана; проверь состояние и журнал.' }
            $operation.message = $operation.message.Substring(0, [math]::Min(12000, $operation.message.Length))
            Remove-Job $operation.job -Force
            $operation.job = $null
        }
    }
    $finished = @($Operations.Values | Where-Object state -in @('done','failed') | Sort-Object created -Descending)
    foreach ($old in @($finished | Select-Object -Skip 20)) { $Operations.Remove($old.id) }
    return @($Operations.Values | Sort-Object created -Descending | ForEach-Object { @{ id = $_.id; action = $_.action; backend = $_.backend; state = $_.state; message = $_.message } })
}

function Start-AiPanelOperation {
    param($Operations, [string]$EngineModulePath, [string]$ConfigPath, $Body)
    $category = if ($Body.action -in @('pull','download')) { 'download' } else { 'control' }
    if (@($Operations.Values | Where-Object { $_.category -eq $category -and $null -ne $_.job -and $_.job.State -notin @('Completed','Failed','Stopped') }).Count) { throw 'Операция этого типа ещё выполняется. Дождись результата.' }
    # Body travels as structured data. There is no shell, command text, or eval endpoint.
    $job = Start-ThreadJob -ArgumentList $EngineModulePath, $ConfigPath, $Body -ScriptBlock {
        param($EnginePath, $Config, $Request)
        $ErrorActionPreference = 'Stop'
        $global:LASTEXITCODE = 0
        try {
            Import-Module $EnginePath -Force
            $messages = @(Invoke-AiPanelAction -ConfigPath $Config -Request $Request)
            @{ ok = $true; message = ($messages | ForEach-Object { [string]$_ }) -join "`n" }
        } catch { @{ ok = $false; message = $_.Exception.Message } }
    }
    $id = [Guid]::NewGuid().ToString('N')
    $backend = if ($Body.Contains('backend')) { [string]$Body.backend } else { '' }
    $Operations[$id] = @{ id = $id; action = [string]$Body.action; backend = $backend; category = $category; state = 'running'; message = 'Выполняется…'; job = $job; created = [DateTime]::UtcNow }
    return @{ id = $id }
}

function Start-OmniBoxPanel {
    param([string]$Root, [string]$PanelConfigPath = (Join-Path $Root 'panel.json'), [string]$EngineModulePath = (Join-Path $Root 'src/OmniBox.Ai.psm1'), [string]$CatalogModulePath = (Join-Path $PSScriptRoot 'Model.Catalog.psm1'))
    Import-Module (Join-Path $PSScriptRoot 'Panel.Security.psm1') -Force
    Import-Module (Join-Path $PSScriptRoot 'Gateway.Helpers.psm1') -Force
    Import-Module $EngineModulePath -Force
    $panel = Read-AiPanelConfig $PanelConfigPath
    $configPath = Join-Path $Root 'config.json'
    $gatewayPath = Join-Path $Root 'gateway.json'
    $gateway = Read-AiGatewayConfig $gatewayPath
    $gatewayPort = $gateway.port
    $apiHosts = @($panel.allowed_hosts)
    if ($gateway.enabled) { $apiHosts = @(@($apiHosts + ([Uri]$gateway.base_url).Host) | Select-Object -Unique) }
    if ($gateway.enabled -and -not ('OmniBox.OpenAiProxy' -as [type])) { Add-Type -Path (Join-Path $PSScriptRoot 'OpenAi.Proxy.cs') }
    $listener = [Net.HttpListener]::new()
    foreach ($hostName in $panel.allowed_hosts) { $listener.Prefixes.Add("http://${hostName}:$($panel.port)/") }
    if ($gateway.enabled -and $gatewayPort -ne $panel.port) { foreach ($hostName in $apiHosts) { $listener.Prefixes.Add("http://${hostName}:$gatewayPort/") } }
    if ($IsWindows) {
        $listener.TimeoutManager.HeaderWait = [TimeSpan]::FromSeconds(10)
        $listener.TimeoutManager.EntityBody = [TimeSpan]::FromSeconds(10)
    }
    $sessions = @{}; $attempts = @{}; $operations = @{}; $catalog = @{}
    $assets = @{ '/' = @('web/index.html','text/html; charset=utf-8'); '/app.js' = @('web/app.js','text/javascript; charset=utf-8'); '/style.css' = @('web/style.css','text/css; charset=utf-8') }
    try {
        $listener.Start()
        Write-Host "OmniBox AI 0.6.0 panel: port $($panel.port)"
        while ($listener.IsListening) {
            # Blocking wait: no background polling, model probes, GPU queries, or wake timers.
            $context = $listener.GetContext()
            try {
                $request = $context.Request; $path = $request.Url.AbsolutePath; $method = $request.HttpMethod
                # Route /v1 on the panel port before panel session/CSRF checks. A NAS
                # can forward the entire domain to one upstream without port routing.
                $onPanelPort = $request.LocalEndPoint.Port -eq $panel.port
                $isApi = $gateway.enabled -and (($onPanelPort -and ($path -eq '/v1' -or $path.StartsWith('/v1/'))) -or (-not $onPanelPort -and $request.LocalEndPoint.Port -eq $gatewayPort))
                $hosts = if ($isApi) { $apiHosts } else { $panel.allowed_hosts }
                if ($request.Url.Host -notin $hosts) { Send-AiHttpResponse $context 403 @{ error = 'Host запрещён.' }; continue }
                if ($isApi) {
                    $apiConfig = Read-AiGatewayConfig $gatewayPath
                    $backends = Get-Content -LiteralPath $configPath -Raw | ConvertFrom-Json -AsHashtable
                    if (-not $apiConfig.enabled -or -not $backends.backends.Contains($apiConfig.backend)) { Send-AiHttpResponse $context 503 @{error=@{message='Selected backend is unavailable.';type='omnibox_error'}}; continue }
                    $b = $backends.backends[$apiConfig.backend]
                    $upstreamKey = if ($b.Contains('upstream_api_key')) { [string]$b.upstream_api_key } else { '' }
                    $null = [OmniBox.OpenAiProxy]::Handle($context, [string]$b.base_url, [bool]$apiConfig.require_key, [string]$apiConfig.key_hash, $upstreamKey)
                    continue
                }
                if ($method -notin @('GET','POST')) { Send-AiHttpResponse $context 405 @{ error = 'Метод запрещён.' }; continue }
                if ($method -eq 'GET' -and $assets.Contains($path)) {
                    $asset = $assets[$path]
                    Send-AiHttpResponse $context 200 (Get-Content -LiteralPath (Join-Path $Root $asset[0]) -Raw) $asset[1]; continue
                }
                $transport = Get-AiPanelRequestContext $request $panel
                if ($method -eq 'POST' -and -not (Test-AiPanelOrigin $request.Headers['Origin'] $transport.authority $transport.scheme)) { Send-AiHttpResponse $context 403 @{ error = 'Origin запрещён.' }; continue }
                if ($method -eq 'POST' -and $path -eq '/api/login') {
                    $ip = $transport.client_address; $now = [DateTime]::UtcNow
                    foreach ($key in @($attempts.Keys)) { if ($attempts[$key].until -lt $now) { $attempts.Remove($key) } }
                    if ($attempts.Contains($ip) -and $attempts[$ip].count -ge 6) { Send-AiHttpResponse $context 429 @{ error = 'Слишком много попыток. Повтори через 5 минут.' }; continue }
                    $body = Read-AiHttpJson $request
                    if ($body.Count -ne 1 -or -not $body.Contains('password') -or $body.password -isnot [string]) { throw 'Нужен пароль.' }
                    if (-not (Test-AiPanelPassword $body.password $panel.credential)) {
                        if (-not $attempts.Contains($ip)) {
                            if ($attempts.Count -ge 512) { Send-AiHttpResponse $context 429 @{ error = 'Повтори позже.' }; continue }
                            $attempts[$ip] = @{ count = 0; until = $now.AddMinutes(5) }
                        }
                        $attempts[$ip].count++; Send-AiHttpResponse $context 401 @{ error = 'Неверный пароль.' }; continue
                    }
                    $attempts.Remove($ip)
                    foreach ($key in @($sessions.Keys)) { if ($sessions[$key].expires -lt $now) { $sessions.Remove($key) } }
                    if ($sessions.Count -ge 32) { $old = $sessions.Keys | Sort-Object { $sessions[$_].expires } | Select-Object -First 1; $sessions.Remove($old) }
                    $id = New-AiPanelNonce; $csrf = New-AiPanelNonce
                    $sessions[$id] = @{ csrf = $csrf; expires = $now.AddHours(8); secure = $transport.secure }
                    $secureCookie = if ($transport.secure) { '; Secure' } else { '' }
                    $context.Response.Headers['Set-Cookie'] = "omnibox_session=$id; Path=/; HttpOnly; SameSite=Strict; Max-Age=28800$secureCookie"
                    Send-AiHttpResponse $context 200 @{ csrf = $csrf }; continue
                }
                $cookie = $request.Cookies['omnibox_session']
                $session = if ($null -ne $cookie -and $sessions.Contains($cookie.Value)) { $sessions[$cookie.Value] } else { $null }
                if ($null -eq $session -or $session.expires -lt [DateTime]::UtcNow -or ($session.secure -and -not $transport.secure)) { Send-AiHttpResponse $context 401 @{ error = 'Войди в панель.' }; continue }
                if ($method -eq 'POST' -and $request.Headers['X-Omnibox-CSRF'] -cne $session.csrf) { Send-AiHttpResponse $context 403 @{ error = 'CSRF-проверка не пройдена.' }; continue }
                switch ("$method $path") {
                    'GET /api/session' { Send-AiHttpResponse $context 200 @{ csrf = $session.csrf; ssh_target = $panel.ssh_target; port = $panel.port } }
                    'GET /api/gateway' { Send-AiHttpResponse $context 200 (Get-AiGatewayView (Read-AiGatewayConfig $gatewayPath)) }
                    'POST /api/catalog' {
                        foreach($id in @($catalog.Keys)){if($catalog[$id].created -lt [DateTime]::UtcNow.AddMinutes(-5) -and $catalog[$id].job.State -in @('Completed','Failed','Stopped')){Remove-Job $catalog[$id].job -Force;$catalog.Remove($id)}}
                        if(@($catalog.Values|Where-Object{$_.job.State -notin @('Completed','Failed','Stopped')}).Count -or $catalog.Count -ge 20){throw 'Предыдущий поиск ещё выполняется. Повтори позже.'}
                        $body=Read-AiHttpJson $request
                        if($body.Count -ne 2 -or $body.mode -notin @('search','files') -or -not $body.Contains('value') -or $body.value -isnot [string] -or $body.value.Length -gt 100){throw 'Поиск: mode search/files и value до 100 символов.'}
                        $job=Start-ThreadJob -ArgumentList $CatalogModulePath,$body -ScriptBlock {param($Module,$Body) try{Import-Module $Module -Force;$data=if($Body.mode -eq 'search'){Search-AiModelCatalog $Body.value}else{Get-AiCatalogFiles $Body.value};@{ok=$true;data=$data}}catch{@{ok=$false;error=$_.Exception.Message}}}
                        $id=[Guid]::NewGuid().ToString('N');$catalog[$id]=@{job=$job;created=[DateTime]::UtcNow};Send-AiHttpResponse $context 202 @{id=$id}
                    }
                    'GET /api/catalog' {
                        $id=$request.QueryString['id'];if(-not $catalog.Contains($id)){throw 'Неизвестный поиск.'}
                        $entry=$catalog[$id]
                        if($entry.job.State -notin @('Completed','Failed','Stopped')){Send-AiHttpResponse $context 200 @{state='running'};break}
                        $result=@(Receive-Job $entry.job -Keep -ErrorAction SilentlyContinue)|Where-Object{$_ -is [System.Collections.IDictionary] -and $_.Contains('ok')}|Select-Object -Last 1
                        if($null -eq $result -or -not $result.ok){$errorText=if($null -ne $result){$result.error}else{'Поиск прерван.'};Send-AiHttpResponse $context 200 @{state='failed';error=$errorText};break}
                        Send-AiHttpResponse $context 200 @{state='done';data=$result.data}
                    }
                    'POST /api/gateway' {
                        $body = Read-AiHttpJson $request
                        $c = Read-AiGatewayConfig $gatewayPath
                        if (-not $c.enabled) { throw 'Сетевой API ещё не установлен. Повтори установщик с включённым OpenAI API.' }
                        $backends = Get-Content -LiteralPath $configPath -Raw | ConvertFrom-Json -AsHashtable
                        $changed = Update-AiGatewaySettings $c $body @($backends.backends.Keys)
                        Write-AiGatewayConfig $gatewayPath $changed.config
                        Send-AiHttpResponse $context 200 @{ gateway=(Get-AiGatewayView $changed.config);key=$changed.key }
                    }
                    'POST /api/logout' { $sessions.Remove($cookie.Value); $secureCookie = if ($transport.secure) { '; Secure' } else { '' }; $context.Response.Headers['Set-Cookie'] = "omnibox_session=; Path=/; HttpOnly; SameSite=Strict; Max-Age=0$secureCookie"; Send-AiHttpResponse $context 200 @{ ok = $true } }
                    'GET /api/status' {
                        $data = Get-AiDashboard -ConfigPath $configPath -IncludeGpu:($request.QueryString['gpu'] -eq '1')
                        $data.operations = @(Update-AiPanelOperations $operations)
                        Send-AiHttpResponse $context 200 $data
                    }
                    'GET /api/logs' {
                        $config = Get-Content $configPath -Raw | ConvertFrom-Json -AsHashtable
                        $name = $request.QueryString['backend']
                        if (-not $config.backends.Contains($name)) { throw 'Неизвестный сервер.' }
                        $b = $config.backends[$name]; $logs = @()
                        foreach ($stream in @('err','out','wrapper')) {
                            $file = Join-Path $b.log_directory "$($b.log_prefix).$stream.log"
                            $logs += @{ stream = $stream; text = $(if (Test-Path -LiteralPath $file -PathType Leaf) { (Get-Content -LiteralPath $file -Tail 80) -join "`n" } else { 'Журнал пока отсутствует.' }) }
                        }
                        Send-AiHttpResponse $context 200 @{ logs = $logs }
                    }
                    'POST /api/actions' {
                        $null = Update-AiPanelOperations $operations
                        $body = Read-AiHttpJson $request
                        if ($body.action -notin @('start','stop','restart','stopall','save','apply','restore','load','unload','pull','download')) { throw 'Неизвестная операция.' }
                        $result = Start-AiPanelOperation $operations $EngineModulePath $configPath $body
                        Send-AiHttpResponse $context 202 $result
                    }
                    default { Send-AiHttpResponse $context 404 @{ error = 'Не найдено.' } }
                }
            } catch {
                try { Send-AiHttpResponse $context 400 @{ error = $_.Exception.Message } } catch { Write-Verbose 'Клиент отключился.' }
            }
        }
    } finally {
        $listener.Close()
        foreach ($operation in $operations.Values) { if ($null -ne $operation.job) { Stop-Job $operation.job; Remove-Job $operation.job -Force } }
        foreach($entry in $catalog.Values){Stop-Job $entry.job;Remove-Job $entry.job -Force}
    }
}

Export-ModuleMember -Function Start-OmniBoxPanel
