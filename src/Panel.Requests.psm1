Set-StrictMode -Version Latest
Import-Module (Join-Path $PSScriptRoot 'File.Read.psm1')

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
    $text = if ($Value -is [byte[]]) { '' } elseif ($ContentType.StartsWith('application/json')) { ConvertTo-Json -InputObject $Value -Depth 20 -Compress } else { [string]$Value }
    $bytes = if($Value -is [byte[]]){$Value}else{[Text.Encoding]::UTF8.GetBytes($text)}
    $response.ContentLength64 = $bytes.Length
    try { $response.OutputStream.Write($bytes, 0, $bytes.Length) } finally { $response.Close() }
}

function New-AiGatewayRuntimeCache {
    # Shared by ThreadJobs in this process. Every read/write below holds the same monitor.
    return @{ gate = [object]::new(); key = ''; expires = [DateTime]::MinValue; value = $null }
}

function Get-AiRuntimeCacheInput {
    param([string]$ConfigPath, [string]$Name)
    $text = Read-AiSharedText $ConfigPath
    $config = $text | ConvertFrom-Json -AsHashtable
    $parts = [Collections.Generic.List[string]]::new()
    $version = [OmniBox.InferenceStats]::RuntimeVersion
    $parts.Add($Name); $parts.Add($version.ToString())
    $parts.Add([Convert]::ToHexString([Security.Cryptography.SHA256]::HashData([Text.Encoding]::UTF8.GetBytes($text))))
    # Also notices profile changes made by the CLI in a different process.
    foreach ($backend in $config.backends.Values) {
        if (-not $backend.Contains('service_config')) { continue }
        foreach ($suffix in @('', '.ai-previous', '.ai-state.json')) {
            $path = [string]$backend.service_config + $suffix
            $file = [IO.FileInfo]::new($path)
            $parts.Add("$path|$($file.Exists)|$($file.LastWriteTimeUtc.Ticks)|$(if ($file.Exists) { $file.Length } else { 0 })")
        }
    }
    return @{ key = $parts -join "`n"; config = $config; version = $version }
}

function Get-AiCachedGatewayRuntime {
    param($Cache, [string]$ConfigPath, [string]$Name)
    [Threading.Monitor]::Enter($Cache.gate)
    try {
        if ([OmniBox.InferenceStats]::IsControlling) { return @{ runtime = @{ available = $false; status = 409; reason = 'A control operation is running.' }; backend = $null } }
        $snapshot = Get-AiRuntimeCacheInput $ConfigPath $Name
        if (-not $snapshot.config.backends.Contains($Name)) { return @{ runtime = @{ available = $false; status = 503; reason = 'Selected backend is unavailable.' }; backend = $null } }
        if ($Cache.key -eq $snapshot.key -and $Cache.expires -gt [DateTime]::UtcNow) { return $Cache.value }
        try { $runtime = Get-AiGatewayRuntime $ConfigPath $Name }
        catch { $runtime = @{ available = $false; status = 503; reason = 'Selected engine could not be checked. Retry after completion.' } }
        # A check started before a control operation must never repopulate the cache after it.
        if ([OmniBox.InferenceStats]::IsControlling -or (Get-AiRuntimeCacheInput $ConfigPath $Name).key -ne $snapshot.key) {
            return @{ runtime = @{ available = $false; status = 409; reason = 'Engine state changed during the check. Retry after completion.' }; backend = $null }
        }
        $Cache.value = @{ runtime = $runtime; backend = $snapshot.config.backends[$Name]; version = $snapshot.version }
        $Cache.key = $snapshot.key; $Cache.expires = [DateTime]::UtcNow.AddSeconds(1)
        return $Cache.value
    } finally { [Threading.Monitor]::Exit($Cache.gate) }
}

function Invoke-AiGatewayHttpRequest {
    param($Context, [string]$ConfigPath, [string]$GatewayPath, $Cache)
    $forwarded = $false
    try {
        $api = Read-AiGatewayConfig $GatewayPath
        # Recheck after the worker starts, including a key rotation while it was queued.
        if (-not [OmniBox.OpenAiProxy]::IsAuthorized($Context.Request, [bool]$api.require_key, [string]$api.key_hash)) {
            Send-AiHttpResponse $Context 401 @{ error = @{ message = 'Invalid or missing API key.'; type = 'omnibox_error' } }; return
        }
        if (-not $api.enabled) { Send-AiHttpResponse $Context 503 @{ error = @{ message = 'API is disabled.'; type = 'omnibox_error' } }; return }
        $view = Get-AiCachedGatewayRuntime $Cache $ConfigPath ([string]$api.backend)
        $runtime = $view.runtime
        if (-not $runtime.available) { Send-AiHttpResponse $Context ([int]$runtime.status) @{ error = @{ message = $runtime.reason; type = 'omnibox_error' } }; return }
        $backend = $view.backend
        $upstreamKey = if ($backend.Contains('upstream_api_key')) { [string]$backend.upstream_api_key } else { '' }
        $placement = if ($runtime.Contains('placement')) { [string]$runtime.placement } else { 'auto' }
        $null = [OmniBox.OpenAiProxy]::Handle($Context, [string]$backend.base_url, [bool]$api.require_key, [string]$api.key_hash, $upstreamKey, [string]$api.backend, [string]$runtime.model, [int]$runtime.context, [string]$runtime.type, $placement, [long]$view.version)
        $forwarded = $true
    } catch {
        try { Send-AiHttpResponse $Context 503 @{ error = @{ message = 'Backend configuration or state is unavailable.'; type = 'omnibox_error' } } } catch { }
    } finally { if (-not $forwarded) { $Context.Response.Close() } }
}

function Start-AiHttpRequestJob {
    param($Jobs, $Context, [string]$Mode, [string]$EngineModulePath, [string]$ConfigPath, [string]$GatewayPath, $Cache, [bool]$IncludeGpu = $false, $Operations = @())
    foreach ($entry in @($Jobs.ToArray())) {
        if ($entry.job.State -in @('Completed', 'Failed', 'Stopped')) {
            Remove-Job $entry.job -Force; $null = $Jobs.Remove($entry)
        }
    }
    $limit = if ($Mode -eq 'gateway') { 8 } else { 4 }
    if (@($Jobs | Where-Object mode -eq $Mode).Count -ge $limit) {
        $errorBody = if ($Mode -eq 'gateway') { @{ error = @{ message = 'Too many simultaneous requests. Retry after completion.'; type = 'omnibox_error' } } } else { @{ error = 'Слишком много запросов состояния. Повторите после завершения.' } }
        Send-AiHttpResponse $Context 429 $errorBody; return
    }
    $requestsModule = $PSCommandPath
    $gatewayModule = Join-Path $PSScriptRoot 'Gateway.Helpers.psm1'
    # Workers only validate runtime, then hand the context to the async proxy; a stream holds no PS worker.
    # Leave room for stop/control jobs even when all runtime and dashboard workers are busy.
    $job = Start-ThreadJob -ThrottleLimit 32 -ArgumentList $Context, $Mode, $EngineModulePath, $requestsModule, $gatewayModule, $ConfigPath, $GatewayPath, $Cache, $IncludeGpu, $Operations -ScriptBlock {
        param($Context, $Mode, $EngineModule, $RequestsModule, $GatewayModule, $ConfigPath, $GatewayPath, $Cache, $IncludeGpu, $Operations)
        $ErrorActionPreference = 'Stop'
        try {
            Import-Module $RequestsModule
            Import-Module $EngineModule
            if ($Mode -eq 'gateway') {
                Import-Module $GatewayModule
                Invoke-AiGatewayHttpRequest $Context $ConfigPath $GatewayPath $Cache
            } else {
                $data = Get-AiDashboard -ConfigPath $ConfigPath -IncludeGpu:$IncludeGpu
                $data.operations = $Operations
                foreach ($backend in $data.backends) {
                    if (($backend.service -eq 'Stopped' -or $backend.sleeping) -and -not [OmniBox.InferenceStats]::IsActive([string]$backend.name)) { [OmniBox.InferenceStats]::EndModelSession([string]$backend.name) }
                }
                Send-AiHttpResponse $Context 200 $data
            }
        } catch { try { Send-AiHttpResponse $Context 500 @{ error = 'Не удалось получить состояние сервера.' } } catch { $Context.Response.Close() } }
    }
    $Jobs.Add(@{ job = $job; mode = $Mode })
}

Export-ModuleMember -Function Send-AiHttpResponse, New-AiGatewayRuntimeCache, Get-AiCachedGatewayRuntime, Invoke-AiGatewayHttpRequest, Start-AiHttpRequestJob
