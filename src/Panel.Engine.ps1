# Dot-sourced by OmniBox.Ai.psm1; uses its service, XML, rollback and mutex adapters.
function Get-AiArgumentValue {
    param([string[]]$Tokens, [string[]]$Names, $Default = $null)
    $value = $Default
    for ($i = 0; $i -lt $Tokens.Count; $i++) {
        if ($Tokens[$i] -in $Names -and $i + 1 -lt $Tokens.Count) { $value = $Tokens[++$i].Trim('"') }
        foreach ($name in $Names) { if ($Tokens[$i].StartsWith("$name=")) { $value = $Tokens[$i].Substring($name.Length + 1).Trim('"') } }
    }
    return $value
}

function Set-AiArgumentValue {
    param([Xml.XmlDocument]$Xml, [string[]]$Names, [string]$Value)
    $tokens = @(Get-AiPrismArguments $Xml)
    $next = [Collections.Generic.List[string]]::new()
    for ($i = 0; $i -lt $tokens.Count; $i++) {
        if ($tokens[$i] -in $Names) {
            if ($i + 1 -ge $tokens.Count) { throw "Нет значения у $($tokens[$i])." }; $i++; continue
        }
        $matched = $false
        foreach ($name in $Names) { if ($tokens[$i].StartsWith("$name=")) { $matched = $true } }
        if (-not $matched) { $next.Add($tokens[$i]) }
    }
    if ($Value.Contains('"') -or $Value.Contains("`n") -or $Value.Contains("`r")) { throw 'Кавычки и переносы строк в параметрах запрещены.' }
    $next.Add($Names[0]); $next.Add($(if ($Value -match '\s') { '"' + $Value + '"' } else { $Value }))
    $Xml.SelectSingleNode('/service/arguments').InnerText = $next -join ' '
}

# Drops valued arguments (with their value or as name=value) and bare flags.
function Remove-AiArgument {
    param([Xml.XmlDocument]$Xml, [string[]]$Names, [string[]]$Flags = @())
    $tokens = @(Get-AiPrismArguments $Xml)
    $next = [Collections.Generic.List[string]]::new()
    for ($i = 0; $i -lt $tokens.Count; $i++) {
        if ($tokens[$i] -in $Names) { $i++; continue }
        if ($tokens[$i] -in $Flags) { continue }
        $matched = $false
        foreach ($name in $Names) { if ($tokens[$i].StartsWith("$name=")) { $matched = $true } }
        if (-not $matched) { $next.Add($tokens[$i]) }
    }
    $Xml.SelectSingleNode('/service/arguments').InnerText = $next -join ' '
}

# Optional llama-server arguments: an empty value removes the argument so the
# binary's own default applies and older builds without the flag still start.
$script:AiPrismOptions = [ordered]@{
    reasoning         = @{ names = @('--reasoning','-rea'); values = @('on','off','auto') }
    reasoning_effort  = @{ names = @('--reasoning-effort'); values = @('minimal','low','medium','high','xhigh','max') }
    reasoning_budget  = @{ names = @('--reasoning-budget'); min = -1; max = 1000000 }
    temperature       = @{ names = @('--temp','--temperature'); min = 0; max = 2; decimal = $true }
    top_k             = @{ names = @('--top-k'); min = 0; max = 1000 }
    top_p             = @{ names = @('--top-p'); min = 0; max = 1; decimal = $true }
    min_p             = @{ names = @('--min-p'); min = 0; max = 1; decimal = $true }
    repeat_penalty    = @{ names = @('--repeat-penalty'); min = 0; max = 2; decimal = $true }
    presence_penalty  = @{ names = @('--presence-penalty'); min = -2; max = 2; decimal = $true }
    frequency_penalty = @{ names = @('--frequency-penalty'); min = -2; max = 2; decimal = $true }
    max_tokens        = @{ names = @('-n','--predict','--n-predict'); min = -1; max = 1000000 }
    seed              = @{ names = @('-s','--seed'); min = -1; max = 4294967295 }
    threads           = @{ names = @('-t','--threads'); min = 1; max = 256 }
    load_mode         = @{ names = @('--load-mode','-lm'); values = @('auto','none','mmap','mlock','mmap+mlock'); flags = @('--mlock','--mmap','--no-mmap') }
}
$script:AiCacheTypes = @('f16','bf16','q8_0','q5_1','q5_0','q4_1','iq4_nl','q4_0')
$script:AiKvOffloadFlags = @('--no-kv-offload','-nkvo','--kv-offload','-kvo')

function Get-AiGgufQuant {
    param([string]$Name)
    if ($Name -match '(?i)(?:^|[-_.])((?:UD-)?(?:I?Q\d_[A-Z0-9_]+?|Q\d_\d|T?Q\d_\d|PQ\d_\d|PTQ\d(?:_\d)?|MXFP4|NVFP4|BF16|F16|F32))(?:[-_.](?:\d{5}-of-\d{5}\.)?gguf$|\.gguf$)') { return $Matches[1].ToUpperInvariant() }
    return ''
}

function Get-AiXmlEnv {
    param([Xml.XmlDocument]$Xml, [string]$Name, [string]$Default)
    $nodes = @($Xml.SelectNodes('/service/env') | Where-Object { $_.GetAttribute('name') -eq $Name })
    if ($nodes.Count -gt 1) { throw "Повторяется $Name в XML." }
    if ($nodes.Count) { return $nodes[0].GetAttribute('value') }; return $Default
}

function Set-AiXmlEnv {
    param([Xml.XmlDocument]$Xml, [string]$Name, [string]$Value)
    $nodes = @($Xml.SelectNodes('/service/env') | Where-Object { $_.GetAttribute('name') -eq $Name })
    if ($nodes.Count -gt 1) { throw "Повторяется $Name в XML." }
    $node = if ($nodes.Count) { $nodes[0] } else { $Xml.CreateElement('env') }
    $node.SetAttribute('name', $Name); $node.SetAttribute('value', $Value)
    if (-not $nodes.Count) { $null = $Xml.DocumentElement.AppendChild($node) }
}

function Get-AiLaunchSettings {
    param($Backend)
    $xml = Read-AiServiceXml $Backend
    $settings = @{ context = [string](Get-AiConfiguredContext $Backend); idle_minutes = 30;placement='auto' }
    if ($Backend.type -eq 'prism') {
        $tokens = @(Get-AiPrismArguments $xml)
        $settings.model = Get-AiArgumentValue $tokens @('-m','--model') ''
        $settings.alias = Get-AiArgumentValue $tokens @('--alias') ([IO.Path]::GetFileNameWithoutExtension($settings.model))
        $settings.gpu_layers = Get-AiArgumentValue $tokens @('-ngl','--gpu-layers','--n-gpu-layers') '99'
        $settings.placement=if([int]$settings.gpu_layers -eq 0){'cpu'}elseif([int]$settings.gpu_layers -ge 99){'gpu'}else{'hybrid'}
        $settings.cache_key = Get-AiArgumentValue $tokens @('-ctk','--cache-type-k') 'f16'
        $settings.cache_value = Get-AiArgumentValue $tokens @('-ctv','--cache-type-v') 'f16'
        $settings.flash_attention = Get-AiArgumentValue $tokens @('-fa','--flash-attn') 'auto'
        $settings.batch = Get-AiArgumentValue $tokens @('-b','--batch-size') '512'
        $settings.ubatch = Get-AiArgumentValue $tokens @('-ub','--ubatch-size') '128'
        # --cpu-moe keeps every expert in RAM; 999 layers means the same for any model.
        $settings.n_cpu_moe = if ($tokens | Where-Object { $_ -in @('--cpu-moe','-cmoe') }) { '999' } else { Get-AiArgumentValue $tokens @('--n-cpu-moe','-ncmoe') '0' }
        $settings.idle_minutes = [math]::Round([double](Get-AiArgumentValue $tokens @('--sleep-idle-seconds') '-1') / 60, 2)
        foreach ($key in $script:AiPrismOptions.Keys) { $settings[$key] = Get-AiArgumentValue $tokens $script:AiPrismOptions[$key].names '' }
        $settings.kv_offload = if ($tokens | Where-Object { $_ -in @('--no-kv-offload','-nkvo') }) { 'off' } else { 'on' }
    } else {
        $settings.placement=Get-AiXmlEnv $xml 'OMNIBOX_PLACEMENT' 'auto'
        $settings.keep_alive = Get-AiXmlEnv $xml 'OLLAMA_KEEP_ALIVE' '5m'
        if ($settings.keep_alive -match '^(\d+)m$') { $settings.idle_minutes = [int]$Matches[1] }
        elseif ($settings.keep_alive -match '^(\d+)s?$') { $settings.idle_minutes = [math]::Round([double]$Matches[1] / 60, 2) }
        else { $settings.idle_minutes = $null }
    }
    return $settings
}

# GGUF files live next to the selected model; a fresh engine without one uses models_directory.
function Get-AiPrismModelDirectory {
    param($Backend)
    $settings = Get-AiLaunchSettings $Backend
    if ($settings.model) { return Split-Path ([IO.Path]::GetFullPath($settings.model)) -Parent }
    if ($Backend.Contains('models_directory') -and $Backend.models_directory) { return [string]$Backend.models_directory }
    return ''
}

function Get-AiPrismModels {
    param($Backend)
    $directory = Get-AiPrismModelDirectory $Backend
    if (-not $directory) { return @() }
    if (-not (Test-Path -LiteralPath $directory -PathType Container)) { return @() }
    $files = @(Get-ChildItem -LiteralPath $directory -File -Filter '*.gguf' | Where-Object { $_.Name -notmatch '(?i)mmproj|projector' -and -not $_.LinkType })
    # A split model (name-00001-of-00003.gguf ...) is one model: llama-server loads it from the first part.
    return @($files | Where-Object { $_.Name -notmatch '-(?!00001)\d{5}-of-\d{5}\.gguf$' } | Sort-Object Name | ForEach-Object {
        $item = @{ name = $_.Name; id = $_.FullName; bytes = $_.Length; quant = Get-AiGgufQuant $_.Name; parts = 1 }
        if ($_.Name -match '^(?<base>.+)-00001-of-(?<total>\d{5})\.gguf$') {
            $pattern = '^' + [regex]::Escape($Matches.base) + '-\d{5}-of-' + $Matches.total + '\.gguf$'
            $group = @($files | Where-Object { $_.Name -match $pattern })
            $item.parts = $group.Count; $item.bytes = [long]($group | Measure-Object -Property Length -Sum).Sum
        }
        $item
    })
}

function New-AiLaunchXml {
    param($Backend, [System.Collections.IDictionary]$Settings)
    $allowed = @('context','idle_minutes','placement')
    if ($Backend.type -eq 'prism') { $allowed += @('model','alias','gpu_layers','n_cpu_moe','cache_key','cache_value','flash_attention','batch','ubatch','kv_offload') + @($script:AiPrismOptions.Keys) }
    foreach ($key in $Settings.Keys) { if ($key -notin $allowed) { throw "Неизвестная настройка: $key" } }
    $xml = if ($Settings.Contains('context')) { New-AiContextXml $Backend (ConvertTo-AiContext ([string]$Settings.context)) } else { Read-AiServiceXml $Backend }
    if ($Settings.Contains('placement')) {
        $placement=[string]$Settings.placement
        $values=if($Backend.type -eq 'prism'){@('cpu','gpu','hybrid','auto')}else{@('cpu','auto')}
        if($placement -notin $values){throw 'Недопустимое размещение модели.'}
        if($Backend.type -eq 'ollama'){
            $oldPlacement=Get-AiXmlEnv $xml 'OMNIBOX_PLACEMENT' 'auto'
            $masks=@('CUDA_VISIBLE_DEVICES','ROCR_VISIBLE_DEVICES','HIP_VISIBLE_DEVICES','GGML_VK_VISIBLE_DEVICES')
            if($placement -eq 'cpu' -and $oldPlacement -ne 'cpu'){
                $previous=@{};foreach($key in $masks){$node=$xml.SelectSingleNode("/service/env[@name='$key']");$previous[$key]=if($null -ne $node){$node.GetAttribute('value')}else{$null}}
                Set-AiXmlEnv $xml 'OMNIBOX_PREVIOUS_GPU_ENV' ($previous|ConvertTo-Json -Compress)
            }
            if($placement -eq 'cpu'){foreach($key in $masks){Set-AiXmlEnv $xml $key '-1'}}
            elseif($oldPlacement -eq 'cpu'){
                $previous=Get-AiXmlEnv $xml 'OMNIBOX_PREVIOUS_GPU_ENV' '{}';$previous=$previous|ConvertFrom-Json -AsHashtable
                foreach($key in $masks){if($previous.Contains($key) -and $null -ne $previous[$key]){Set-AiXmlEnv $xml $key ([string]$previous[$key])}else{$node=$xml.SelectSingleNode("/service/env[@name='$key']");if($null -ne $node){$null=$xml.DocumentElement.RemoveChild($node)}}}
            }
            Set-AiXmlEnv $xml 'OMNIBOX_PLACEMENT' $placement
        }
    }
    if ($Settings.Contains('idle_minutes')) {
        $idle = [string]$Settings.idle_minutes
        if ($idle -notmatch '^\d+$' -or [long]$idle -lt 1 -or [long]$idle -gt 1440) { throw 'Простой должен быть от 1 до 1440 минут.' }
        if ($Backend.type -eq 'prism') { Set-AiArgumentValue $xml @('--sleep-idle-seconds') ([string]([int]$idle * 60)) }
        else { Set-AiXmlEnv $xml 'OLLAMA_KEEP_ALIVE' "${idle}m" }
    }
    if ($Backend.type -eq 'prism') {
        if ($Settings.Contains('model')) {
            $known = @(Get-AiPrismModels $Backend)
            $model = [string]$Settings.model
            if ($model -notin @($known | ForEach-Object { $_.id })) { throw 'Выбери существующий GGUF из каталога моделей; произвольные пути запрещены.' }
            Set-AiArgumentValue $xml @('-m','--model') $model
        }
        if ($Settings.Contains('alias')) {
            if ([string]$Settings.alias -notmatch '^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,79}$') { throw 'Имя API-модели: латиница, цифры, точка, дефис или подчёркивание, до 80 символов.' }
            Set-AiArgumentValue $xml @('--alias') ([string]$Settings.alias)
        }
        foreach ($key in @('gpu_layers','batch','ubatch')) {
            if ($Settings.Contains($key)) {
                $value = [string]$Settings[$key]; $min = if ($key -eq 'gpu_layers') { 0 } else { 1 }; $max = if ($key -eq 'gpu_layers') { 999 } else { 8192 }
                if ($value -notmatch '^\d+$' -or [long]$value -lt $min -or [long]$value -gt $max) { throw "$key должен быть от $min до $max." }
                $names = switch ($key) { 'gpu_layers' { @('-ngl','--gpu-layers','--n-gpu-layers') } 'batch' { @('-b','--batch-size') } 'ubatch' { @('-ub','--ubatch-size') } }
                Set-AiArgumentValue $xml $names $value
            }
        }
        if ($Settings.Contains('n_cpu_moe')) {
            $value = [string]$Settings.n_cpu_moe
            if ($value -notmatch '^\d+$' -or [long]$value -gt 999) { throw 'Слои MoE в RAM: от 0 до 999.' }
            Remove-AiArgument $xml @('--n-cpu-moe','-ncmoe') @('--cpu-moe','-cmoe')
            if ([int]$value -gt 0) { Set-AiArgumentValue $xml @('--n-cpu-moe','-ncmoe') ([string][int]$value) }
        }
        foreach ($key in @('cache_key','cache_value')) {
            if ($Settings.Contains($key)) {
                if ($Settings[$key] -cnotin $script:AiCacheTypes) { throw "Кеш: $($script:AiCacheTypes -join ', ')." }
                $names = if ($key -eq 'cache_key') { @('-ctk','--cache-type-k') } else { @('-ctv','--cache-type-v') }
                Set-AiArgumentValue $xml $names ([string]$Settings[$key])
            }
        }
        if ($Settings.Contains('flash_attention')) {
            if ($Settings.flash_attention -notin @('on','off','auto')) { throw 'Flash Attention: on, off или auto.' }
            Set-AiArgumentValue $xml @('-fa','--flash-attn') ([string]$Settings.flash_attention)
        }
        if($Settings.Contains('placement')){
            if($Settings.placement -eq 'cpu'){Set-AiArgumentValue $xml @('-ngl','--gpu-layers','--n-gpu-layers') '0';Remove-AiArgument $xml @('--n-cpu-moe','-ncmoe') @('--cpu-moe','-cmoe');$xml.service.arguments=(@(Get-AiPrismArguments $xml)|Where-Object{$_ -ne '--no-kv-offload'}) -join ' ';$xml.service.arguments+=' --no-kv-offload'}
            elseif($Settings.placement -in @('gpu','auto')){Set-AiArgumentValue $xml @('-ngl','--gpu-layers','--n-gpu-layers') '999'}
            if($Settings.placement -ne 'cpu'){$xml.service.arguments=(@(Get-AiPrismArguments $xml)|Where-Object{$_ -ne '--no-kv-offload'}) -join ' '}
            if($Settings.placement -eq 'hybrid' -and (-not $Settings.Contains('gpu_layers') -or [int]$Settings.gpu_layers -lt 1 -or [int]$Settings.gpu_layers -ge 99)){throw 'Для RAM + VRAM укажи от 1 до 98 слоёв GPU.'}
        }
        foreach ($key in $script:AiPrismOptions.Keys) {
            if (-not $Settings.Contains($key)) { continue }
            $option = $script:AiPrismOptions[$key]; $value = ([string]$Settings[$key]).Trim()
            if ($value) {
                if ($option.Contains('values')) {
                    if ($value -cnotin $option.values) { throw "${key}: допустимо $($option.values -join ', ') или пусто." }
                } else {
                    $pattern = if ($option.Contains('decimal')) { '^-?\d{1,7}(\.\d{1,4})?$' } else { '^-?\d{1,10}$' }
                    if ($value -notmatch $pattern -or [double]::Parse($value, [Globalization.CultureInfo]::InvariantCulture) -lt $option.min -or [double]::Parse($value, [Globalization.CultureInfo]::InvariantCulture) -gt $option.max) { throw "${key}: от $($option.min) до $($option.max) или пусто." }
                }
            }
            Remove-AiArgument $xml $option.names $(if ($option.Contains('flags')) { $option.flags } else { @() })
            if ($value) { Set-AiArgumentValue $xml $option.names $value }
        }
        # CPU placement always keeps the KV cache in RAM; otherwise honour the explicit choice.
        $cpuPlacement = [string](Get-AiArgumentValue @(Get-AiPrismArguments $xml) @('-ngl','--gpu-layers','--n-gpu-layers') '99') -eq '0'
        if ($Settings.Contains('kv_offload') -and -not $cpuPlacement) {
            if ([string]$Settings.kv_offload -notin @('on','off')) { throw 'KV-кеш на GPU: on или off.' }
            Remove-AiArgument $xml @() $script:AiKvOffloadFlags
            if ($Settings.kv_offload -eq 'off') { $xml.service.arguments = [string]$xml.service.arguments + ' --no-kv-offload' }
        }
        $tokens = @(Get-AiPrismArguments $xml)
        if ([int](Get-AiArgumentValue $tokens @('-ub','--ubatch-size') '128') -gt [int](Get-AiArgumentValue $tokens @('-b','--batch-size') '512')) { throw 'ubatch не может быть больше batch.' }
    }
    return ,$xml
}

function Invoke-AiModelRequest {
    param($Backend, [string]$Path, $Body, [int]$Timeout = 180, [ValidateSet('Post','Delete')][string]$Method = 'Post')
    $headers=@{};if($Backend.Contains('upstream_api_key') -and $Backend.upstream_api_key){$headers.Authorization='Bearer '+$Backend.upstream_api_key}
    Invoke-RestMethod -Uri ($Backend.base_url.TrimEnd('/') + $Path) -Headers $headers -Method $Method -ContentType 'application/json; charset=utf-8' -Body ($Body | ConvertTo-Json -Depth 10 -Compress) -TimeoutSec $Timeout -ErrorAction Stop
}

function Get-AiGpuData {
    # A new ThreadJob has no LASTEXITCODE until it runs its first native command.
    $exitVariable = Get-Variable LASTEXITCODE -Scope Global -ErrorAction SilentlyContinue
    $savedExit = if ($null -ne $exitVariable) { $exitVariable.Value } else { $null }
    try {
        $exe = Get-Command nvidia-smi -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
        if ($null -eq $exe) { return @() }
        $lines = @(& $exe.Source --query-gpu=name,memory.total,memory.used,memory.free,utilization.gpu,temperature.gpu,power.draw,fan.speed --format=csv,noheader,nounits 2>$null)
        if ($LASTEXITCODE -ne 0) { return @() }
        return @($lines | ForEach-Object {
            $parts = $_ -split ',\s*'
            if ($parts.Count -ge 4) {
                $row=@{ name = $parts[0]; total = [int]$parts[1]; used = [int]$parts[2]; free = [int]$parts[3];utilization=$null;temperature=$null;power_watts=$null;fan_percent=$null }
                foreach($pair in @(@('utilization',4),@('temperature',5),@('power_watts',6),@('fan_percent',7))){$n=0.0;if($parts.Count -gt $pair[1] -and [double]::TryParse($parts[$pair[1]],[Globalization.NumberStyles]::Float,[Globalization.CultureInfo]::InvariantCulture,[ref]$n)){$row[$pair[0]]=$n}}
                $row
            }
        })
    } finally {
        if ($null -ne $exitVariable) { $global:LASTEXITCODE = $savedExit }
        else { Remove-Variable LASTEXITCODE -Scope Global -ErrorAction SilentlyContinue }
    }
}

function Get-AiDashboard {
    param([string]$ConfigPath, [switch]$IncludeGpu)
    $config = Read-AiConfig $ConfigPath
    $statuses = @(Get-AiStatus $config)
    $rows = @()
    foreach ($name in ($config.backends.Keys | Sort-Object)) {
        $b = $config.backends[$name]; $status = $statuses | Where-Object Backend -eq $name | Select-Object -First 1
        $settings = $null; $models = @(); $errors = @(); $pending = $false; $restore = $false
        try {
            $settings = Get-AiLaunchSettings $b
            $history = Read-AiRecoveryState $b
            $pending = $null -ne $history -and $history.pending
            $restore = $null -ne $history
        } catch { $errors += $_.Exception.Message }
        if ($b.type -eq 'prism') { try { $models = @(Get-AiPrismModels $b) } catch { $errors += $_.Exception.Message } }
        elseif ($status.Service -eq 'Running') {
            try { $tags = Invoke-AiProbe $b '/api/tags'; $models = @($tags.models | ForEach-Object { @{ id = $_.name; name = $_.name; bytes = $_.size } }) }
            catch { $errors += $_.Exception.Message }
        }
        $selectedModel=if($b.type -eq 'prism' -and $null -ne $settings){$settings.alias}elseif($b.Contains('active_model')){$b.active_model}else{''}
        if($b.type -eq 'prism' -and $pending -and $status.Service -eq 'Running'){
            try{$previous=[xml](Get-Content -LiteralPath ($b.service_config+'.ai-previous') -Raw);$selectedModel=Get-AiArgumentValue @(Get-AiPrismArguments $previous) @('--alias') ([IO.Path]::GetFileNameWithoutExtension($status.Model))}catch{}
        }
        $residency=@{mode='unknown';ram_bytes=$null;vram_bytes=$null;gpu_layers=$null;total_layers=$null;cpu_moe_layers=$null}
        $idle=@{expires_at=$null;estimated=$false;busy=$false;state='stopped'}
        if($status.Service -eq 'Running'){
            $idle.state=if($status.Sleeping){'unloaded'}else{'unknown'}
            if('OmniBox.InferenceStats' -as [type]){$idle.busy=[OmniBox.InferenceStats]::IsActive($name)}
            if($b.type -eq 'ollama'){
                try{$loaded=@((Invoke-AiProbe $b '/api/ps').models);if($loaded.Count -eq 1){
                    $m=$loaded[0];$idle.state='loaded';$idle.expires_at=$m.expires_at
                    if($m.PSObject.Properties['size_vram'] -and $m.PSObject.Properties['size']){
                        $residency.vram_bytes=[long]$m.size_vram;$residency.ram_bytes=[math]::Max(0,[long]$m.size-[long]$m.size_vram)
                        $residency.mode=if($residency.vram_bytes -eq 0){'ram'}elseif($residency.ram_bytes -eq 0){'vram'}else{'hybrid'}
                    }
                }else{$idle.state='unloaded'}}catch{}
            }elseif(-not $status.Sleeping){
                $runtimePath=$b.service_config+'.runtime.json'
                try{
                    $runtime=Get-Content -LiteralPath $runtimePath -Raw|ConvertFrom-Json -AsHashtable
                    if($runtime.running){
                        if($settings.placement -eq 'cpu' -and -not $pending){$residency.mode='ram'}
                        if($null -ne $runtime.gpu_layers -and $null -ne $runtime.total_layers){$residency.gpu_layers=$runtime.gpu_layers;$residency.total_layers=$runtime.total_layers;$residency.mode=if($runtime.gpu_layers -eq 0){'ram'}elseif($runtime.gpu_layers -ge $runtime.total_layers){'vram'}else{'hybrid'}}
                        # Offload logs count expert layers as GPU layers even when --n-cpu-moe keeps their weights in RAM.
                        if(-not $pending -and $null -ne $settings -and [string]$settings.n_cpu_moe -match '^\d+$' -and [int]$settings.n_cpu_moe -gt 0 -and $residency.mode -ne 'ram'){$residency.cpu_moe_layers=[int]$settings.n_cpu_moe;$residency.mode='hybrid'}
                        $anchor=[DateTimeOffset]::Parse($runtime.started)
                        if('OmniBox.InferenceStats' -as [type]){$last=[OmniBox.InferenceStats]::LastActivity($name);if($last -and [DateTimeOffset]::Parse($last) -gt $anchor){$anchor=[DateTimeOffset]::Parse($last)}}
                        if($null -ne $settings -and $settings.idle_minutes -gt 0){$idle.expires_at=$anchor.AddMinutes($settings.idle_minutes).ToString('o');$idle.estimated=$true;$idle.state='loaded'}
                    }
                }catch{}
            }
        }
        $rows += @{ name = $name; type = $b.type; service = $status.Service; startup = $status.Startup; api = $status.API; active_context = $status.Context; sleeping = $status.Sleeping; loaded_model = $status.Model; selected_model=$selectedModel; settings = $settings; models = $models; pending = $pending; can_restore = $restore; errors = $errors; residency=$residency;idle=$idle;endpoint = $b.base_url.TrimEnd('/') + '/v1' }
    }
    $gpu = @(); if ($IncludeGpu) { $gpu = @(Get-AiGpuData) }
    return @{ version = $script:AiVersion; default_backend = $config.default_backend; exclusive = $true; backends = $rows; gpu = $gpu; computer = [Environment]::MachineName }
}

function Get-AiGatewayRuntime {
    param([string]$ConfigPath,[string]$Name)
    $config=Read-AiConfig $ConfigPath;$b=Get-AiBackend $config $Name
    $s=Get-AiService $b.service
    if($null -eq $s -or [string]$s.Status -ne 'Running'){return @{available=$false;reason='Selected engine is stopped.';status=503}}
    foreach($other in $config.backends.Keys){if($other -ne $Name){$o=Get-AiService $config.backends[$other].service;if($null -ne $o -and [string]$o.Status -notin @('Stopped')){return @{available=$false;reason='More than one engine is active. Stop the other engine first.';status=409}}}}
    $settings=Get-AiLaunchSettings $b
    $model=if($b.type -eq 'prism'){$settings.alias}elseif($b.Contains('active_model')){$b.active_model}else{''}
    $context=[int]$settings.context
    if($b.type -eq 'prism'){
        $props=Invoke-AiProbe $b '/props'
        if($props.PSObject.Properties['default_generation_settings'] -and [int]$props.default_generation_settings.n_ctx -gt 0){$context=[int]$props.default_generation_settings.n_ctx}
        $history=Read-AiRecoveryState $b
        if($null -ne $history -and $history.pending){$previous=[xml](Get-Content -LiteralPath ($b.service_config+'.ai-previous') -Raw);$model=Get-AiArgumentValue @(Get-AiPrismArguments $previous) @('--alias') ([IO.Path]::GetFileNameWithoutExtension($props.model_path))}
    }
    if($b.type -eq 'ollama'){
        $ps=Invoke-AiProbe $b '/api/ps';$loaded=@($ps.models)
        if($loaded.Count -gt 1){return @{available=$false;reason='Multiple models are loaded. Unload them in the panel first.';status=409}}
        if($loaded.Count -eq 1){
            $actual=[string]$loaded[0].name
            if($model -and $actual -ne $model -and $actual -ne "${model}:latest"){return @{available=$false;reason='A different model was loaded outside OmniBox AI. Select/load it in the panel.';status=409}}
            $model=$actual
            if($loaded[0].PSObject.Properties['context_length'] -and [int]$loaded[0].context_length -gt 0){$context=[int]$loaded[0].context_length}
        }
    }
    return @{available=$true;model=[string]$model;context=$context;type=$b.type;placement=$settings.placement}
}

function New-AiSingleModelXml {
    param($Backend,[Xml.XmlDocument]$Xml)
    if($Backend.type -eq 'prism'){
        $tokens=@(Get-AiPrismArguments $Xml)
        if(@($tokens|Where-Object{$_ -match '^--models-(dir|preset)(=|$)'}).Count){throw 'Router mode не поддерживается: настрой один файл GGUF.'}
        Set-AiArgumentValue $Xml @('-np','--parallel') '1'
    }else{
        Set-AiXmlEnv $Xml 'OLLAMA_MAX_LOADED_MODELS' '1'
        Set-AiXmlEnv $Xml 'OLLAMA_NUM_PARALLEL' '1'
    }
    return ,$Xml
}

function Set-AiSelectedModel {
    param([string]$ConfigPath,[string]$Name,[string]$Model)
    $c=Read-AiConfig $ConfigPath;$c.backends[$Name].active_model=$Model;$c.exclusive=$true
    Write-AiAtomicBytes $ConfigPath ([Text.UTF8Encoding]::new($false).GetBytes(($c|ConvertTo-Json -Depth 10)))
}

function Clear-AiOtherOllamaModels {
    param($Backend,[string]$KeepModel,[int]$Timeout)
    $ps=Invoke-AiProbe $Backend '/api/ps'
    foreach($model in @($ps.models)){
        if($model.name -ne $KeepModel){$null=Invoke-AiModelRequest $Backend '/api/generate' @{model=$model.name;prompt='';stream=$false;keep_alive=0} $Timeout}
    }
}

function Assert-AiModelName {
    param([string]$Name)
    if ($Name.Length -gt 200 -or $Name -notmatch '^[a-zA-Z0-9][a-zA-Z0-9_./:-]*$' -or $Name.Contains('..')) { throw 'Некорректное имя модели Ollama.' }
}

function Invoke-AiPanelAction {
    param([string]$ConfigPath, [System.Collections.IDictionary]$Request)
    $config = Read-AiConfig $ConfigPath
    foreach ($key in $Request.Keys) { if ($key -notin @('action','backend','settings','model','url','filename','sha256','engine','version')) { throw "Неизвестное поле: $key" } }
    $operationName = [string]$Request.action
    if ($operationName -notin @('start','stop','restart','stopall','save','apply','restore','load','unload','pull','download','engine-install','engine-update','engine-remove','model-delete')) { throw 'Неизвестная операция панели.' }
    if ($operationName -eq 'engine-install') { Assert-AiAdministrator; return Install-AiEngine $ConfigPath (ConvertTo-AiEngineType ([string]$Request.engine)) ([string]$Request.version) }
    $name = if ($Request.Contains('backend')) { [string]$Request.backend } else { $config.default_backend }
    $b = Get-AiBackend $config $name
    Assert-AiAdministrator
    if ($operationName -eq 'stopall') { Invoke-Ai @('stop') $ConfigPath; return 'Все настроенные ИИ-серверы остановлены.' }
    if ($operationName -in @('start','stop','restart','restore')) { Invoke-Ai @($operationName,$name) $ConfigPath; return "$name`: операция $operationName завершена." }
    if ($operationName -eq 'download') { return Save-AiPrismDownload $b $Request }
    if ($operationName -eq 'engine-update') { return Set-AiEngineVersion $ConfigPath $name ([string]$Request.version) }
    if ($operationName -eq 'engine-remove') { return Uninstall-AiEngine $ConfigPath $name }
    if ($operationName -eq 'model-delete') { return Remove-AiEngineModel $ConfigPath $name ([string]$Request.model) }
    if ($operationName -eq 'pull') {
        if ($b.type -ne 'ollama') { throw 'Скачивание по имени доступно для Ollama.' }
        Assert-AiModelName ([string]$Request.model)
        $service = Get-AiService $b.service
        if ($null -eq $service -or [string]$service.Status -ne 'Running') { throw 'Сначала запусти Ollama. Скачивание само сервер не запускает.' }
        $null = Invoke-AiModelRequest $b '/api/pull' @{ model = [string]$Request.model; stream = $false } 7200
        return "Модель $($Request.model) скачана."
    }
    Invoke-AiLocked {
        if ($operationName -in @('save','apply')) {
            if ($Request.settings -isnot [System.Collections.IDictionary]) { throw 'Нужен объект settings.' }
            $xml = New-AiLaunchXml $b $Request.settings
            if ($operationName -eq 'save') { Save-AiServiceXml $b $xml; return 'Настройки сохранены для следующего запуска; работающая модель не перезапущена.' }
            Invoke-AiBackendStart $config $name 'restart' $xml
            return 'Настройки применены. Сервер готов.'
        }
        if ($b.type -eq 'prism') {
            if ($operationName -eq 'unload') { Stop-AiBackend $config $name; return 'Модель выгружена, сервер остановлен. Новый запрос сам его не запустит.' }
            $xml = if ($Request.Contains('settings')) { New-AiLaunchXml $b $Request.settings } else { $null }
            $service = Get-AiService $b.service
            $verb = if ($null -ne $service -and [string]$service.Status -ne 'Stopped') { 'restart' } else { 'start' }
            Invoke-AiBackendStart $config $name $verb $xml
            return 'Модель загружена. API готов.'
        }
        if ($operationName -eq 'unload') {
            $service = Get-AiService $b.service
            if ($null -eq $service -or [string]$service.Status -ne 'Running') { return 'Ollama уже остановлена; загруженных моделей нет.' }
            $ps = Invoke-AiProbe $b '/api/ps'
            foreach ($model in @($ps.models)) { $null = Invoke-AiModelRequest $b '/api/generate' @{ model = $model.name; prompt = ''; stream = $false; keep_alive = 0 } $config.startup_timeout_seconds }
            return 'Все модели Ollama выгружены; сервер продолжает слушать API.'
        }
        Assert-AiModelName ([string]$Request.model)
        if ($operationName -eq 'load') {
            $prepared=if($Request.Contains('settings')){New-AiLaunchXml $b $Request.settings}else{$null}
            $history=Read-AiRecoveryState $b;$service=Get-AiService $b.service
            if($null -ne $prepared -or ($null -ne $history -and $history.pending)){
                $verb=if($null -ne $service -and $service.Status -ne 'Stopped'){'restart'}else{'start'}
                Invoke-AiBackendStart $config $name $verb $prepared
            }else{Start-AiBackend $config $name}
        }
        $service = Get-AiService $b.service
        if ($null -eq $service -or [string]$service.Status -ne 'Running') { throw 'Ollama остановлена.' }
        $tags = Invoke-AiProbe $b '/api/tags'
        if ([string]$Request.model -notin @($tags.models | ForEach-Object name)) { throw 'Модель не установлена. Сначала скачай её.' }
        Clear-AiOtherOllamaModels $b ([string]$Request.model) $config.startup_timeout_seconds
        $settings = Get-AiLaunchSettings $b
        $body = @{ model = [string]$Request.model; stream = $false; prompt = ''; keep_alive = $(if ($operationName -eq 'unload') { 0 } else { $settings.keep_alive }) }
        if ($operationName -eq 'load' -and $settings.context -match '^\d+$') { $body.options = @{ num_ctx = [int]$settings.context };if($settings.placement -eq 'cpu'){$body.options.num_gpu=0} }
        $null = Invoke-AiModelRequest $b '/api/generate' $body $config.startup_timeout_seconds
        Set-AiSelectedModel $ConfigPath $name ([string]$Request.model)
        return 'Модель загружена в память.'
    }
}

function Save-AiPrismDownload {
    param($Backend, $Request)
    if ($Backend.type -ne 'prism') { throw 'GGUF скачивается для PrismML.' }
    $uri = $null
    if (-not [Uri]::TryCreate([string]$Request.url, [UriKind]::Absolute, [ref]$uri) -or $uri.Scheme -ne 'https' -or $uri.Host -cne 'huggingface.co' -or -not $uri.IsDefaultPort -or $uri.UserInfo -or $uri.Fragment -or
        $uri.AbsolutePath -notmatch '^/[^/]+/[^/]+/resolve/[^/]+/.+\.gguf$') { throw 'Нужна прямая HTTPS-ссылка huggingface.co/.../resolve/.../model.gguf.' }
    $filename = [string]$Request.filename
    if ($filename -notmatch '^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,159}\.gguf$' -or $filename.Contains('..') -or $filename -match '(?i)mmproj|projector') { throw 'Имя GGUF: латиница/цифры/дефис/подчёркивание, без пути и projector.' }
    $sha = if ($Request.Contains('sha256')) { [string]$Request.sha256 } else { '' }
    if ($sha -and $sha -notmatch '^[a-fA-F0-9]{64}$') { throw 'SHA256 должен содержать 64 hex-символа.' }
    $directory = Get-AiPrismModelDirectory $Backend
    if (-not $directory) { throw 'Не задан каталог моделей PrismML.' }
    $null = New-Item -ItemType Directory -Path $directory -Force
    $target = Join-Path $directory $filename
    if (Test-Path -LiteralPath $target) { throw 'Файл уже существует; перезапись моделей запрещена.' }
    # A download killed by a reboot or update leaves its .part file; one older than the download timeout is dead.
    Get-ChildItem -LiteralPath $directory -File -Force -ErrorAction SilentlyContinue | Where-Object { $_.Name -match '^\.download-[0-9a-f]{32}\.part$' -and $_.LastWriteTimeUtc -lt [DateTime]::UtcNow.AddHours(-3) } | Remove-Item -Force -ErrorAction SilentlyContinue
    $temporary = Join-Path $directory ('.download-' + [Guid]::NewGuid().ToString('N') + '.part')
    try {
        Invoke-WebRequest -Uri $uri.AbsoluteUri -OutFile $temporary -MaximumRedirection 10 -TimeoutSec 7200
        $stream = [IO.File]::OpenRead($temporary)
        try { $magic = [byte[]]::new(4); $read = $stream.Read($magic, 0, 4) } finally { $stream.Dispose() }
        if ($read -ne 4 -or [Text.Encoding]::ASCII.GetString($magic) -cne 'GGUF') { throw 'Скачанный файл не имеет заголовка GGUF.' }
        if ($sha -and (Get-FileHash -LiteralPath $temporary -Algorithm SHA256).Hash -ine $sha) { throw 'SHA256 скачанного GGUF не совпадает.' }
        [IO.File]::Move($temporary, $target)
        return "Скачано: $filename. Выбери модель в настройках; сама она не загружалась."
    } finally { if ([IO.File]::Exists($temporary)) { [IO.File]::Delete($temporary) } }
}
