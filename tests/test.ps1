#requires -Version 7.4
# Self-contained tests: no Pester, no Windows services, GPU, network, or model required.
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$repo = Split-Path $PSScriptRoot -Parent
$modulePath = Join-Path $repo 'src/OmniBox.Ai.psm1'
$example = Join-Path $repo 'config.example.json'
$version = (Get-Content -LiteralPath (Join-Path $repo 'VERSION') -Raw).Trim()
$manifest = Import-PowerShellDataFile -LiteralPath (Join-Path $repo 'package.psd1')
$script:passed = 0
$script:failed = 0
$testRoot = Join-Path ([IO.Path]::GetTempPath()) ('omnibox-ai-tests-' + [Guid]::NewGuid().ToString('N'))
$null = New-Item -ItemType Directory -Path $testRoot

function Assert-Equal($Actual, $Expected) {
    if ($Actual -cne $Expected) { throw "Expected <$Expected>, got <$Actual>" }
}
function Assert-True($Condition, $Message = 'Assertion failed') { if (-not $Condition) { throw $Message } }
function Assert-Throws([scriptblock]$Action, [string]$Pattern) {
    $caught = $null
    try { & $Action } catch { $caught = $_.Exception.Message }
    if (-not $caught -or $caught -notlike $Pattern) { throw "Expected error <$Pattern>, got <$caught>" }
}
function Test([string]$Name, [scriptblock]$Action) {
    try { & $Action; $script:passed++; Write-Host "PASS $Name" }
    catch { $script:failed++; [Console]::Error.WriteLine("FAIL $Name : $($_.Exception.Message)") }
}
function New-TestModule {
    Remove-Module OmniBox.Ai -ErrorAction SilentlyContinue
    $module = Import-Module $modulePath -Force -PassThru
    & $module {
        $script:calls = [Collections.Generic.List[string]]::new()
        $script:services = @{
            bonsai = [pscustomobject]@{ Status = 'Stopped'; StartType = 'Manual' }
            ollama = [pscustomobject]@{ Status = 'Running'; StartType = 'Manual' }
        }
        $script:failStop = ''
        $script:sleeping = $false
        $script:realSingleModelLimits=(Get-Command Set-AiSingleModelLimits).ScriptBlock
        # The sample Windows paths are fictional on the test host. Real temporary
        # XML fixtures still execute the production policy, including validation.
        function script:Set-AiSingleModelLimits {param($Config,$Name,$Service)if(Test-Path -LiteralPath $Config.backends[$Name].service_config){& $script:realSingleModelLimits $Config $Name $Service}}
        function script:Assert-AiAdministrator { $script:calls.Add('admin') }
        function script:Invoke-AiLocked { param([scriptblock]$Action); $script:calls.Add('lock'); & $Action }
        function script:Get-AiService { param($Name); return $script:services[$Name] }
        function script:Start-AiService { param($Name); $script:calls.Add("start:$Name"); $script:services[$Name].Status = 'Running' }
        function script:Stop-AiService {
            param($Name, $TimeoutSeconds)
            $script:calls.Add("stop:$Name")
            if ($script:failStop -eq $Name) { throw 'mock stop failure' }
            $script:services[$Name].Status = 'Stopped'
        }
        function script:Invoke-AiProbe {
            param($Backend, $Path)
            $script:calls.Add("probe:$($Backend.service):$Path")
            switch ($Path) {
                '/health' { [pscustomobject]@{ status = 'ok' } }
                '/props' { [pscustomobject]@{ is_sleeping = $script:sleeping; default_generation_settings = [pscustomobject]@{ n_ctx = 196608 }; model_path = 'bonsai-2-27b-pq2_0.gguf' } }
                '/api/version' { [pscustomobject]@{ version = '0.35.1' } }
                '/api/ps' { [pscustomobject]@{ models = @() } }
            }
        }
        function script:Show-AiGpu { $script:calls.Add('gpu') }
    }
    return $module
}
function Get-Calls($Module) { @(& $Module { $script:calls.ToArray() }) }
function Save-TestConfig($Config) {
    $path = Join-Path $testRoot ([Guid]::NewGuid().ToString('N') + '.json')
    $Config | ConvertTo-Json -Depth 10 | Set-Content -LiteralPath $path -Encoding utf8
    return $path
}
function New-ContextFixture([string]$Arguments = '-m "C:\ai\models\-c 77 model.gguf" --port 18080 -c 196608 -ctk q8_0 --sleep-idle-seconds 1800') {
    $config = Get-Content $example -Raw | ConvertFrom-Json -AsHashtable
    $bonsaiPath = Join-Path $testRoot ([Guid]::NewGuid().ToString('N') + '-bonsai.xml')
    $ollamaPath = Join-Path $testRoot ([Guid]::NewGuid().ToString('N') + '-ollama.xml')
    $argumentText = [Security.SecurityElement]::Escape($Arguments)
    "<service><id>bonsai</id><arguments>$argumentText</arguments><startmode>Manual</startmode><logpath>C:\ai\logs\bonsai</logpath></service>" | Set-Content -LiteralPath $bonsaiPath
    '<service><id>ollama</id><env name="OLLAMA_CONTEXT_LENGTH" value="32768"/><env name="OLLAMA_KEEP_ALIVE" value="30m"/><startmode>Manual</startmode></service>' | Set-Content -LiteralPath $ollamaPath
    $config.backends.bonsai.service_config = $bonsaiPath
    $config.backends.ollama.service_config = $ollamaPath
    return @{ path = (Save-TestConfig $config); config = $config; bonsai = $bonsaiPath; ollama = $ollamaPath }
}

function New-WebModelFixture {
    $dir = Join-Path $testRoot ('models with spaces-' + [Guid]::NewGuid().ToString('N'))
    $null = New-Item -ItemType Directory -Path $dir
    foreach ($name in @('bonsai-pq2.gguf','other-model.gguf','mmproj-bonsai.gguf')) { [IO.File]::WriteAllBytes((Join-Path $dir $name), [Text.Encoding]::ASCII.GetBytes('GGUFfixture')) }
    $args = '-m "' + (Join-Path $dir 'bonsai-pq2.gguf') + '" --alias bonsai-2-27b -c 196608 -ngl 99 -fa on -ctk q8_0 -ctv q8_0 -b 512 -ub 128 --sleep-idle-seconds 1800 --port 18080 --jinja'
    return New-ContextFixture $args
}

try {
    Test 'all PowerShell files parse' {
        foreach ($file in Get-ChildItem -LiteralPath $repo -Recurse -File | Where-Object Extension -in @('.ps1', '.psm1')) {
            $tokens = $null; $errors = $null
            $null = [Management.Automation.Language.Parser]::ParseFile($file.FullName, [ref]$tokens, [ref]$errors)
            Assert-Equal $errors.Count 0
        }
    }
    Test 'help and version need no config' {
        $m = New-TestModule
        Assert-True ((Invoke-Ai -Arguments @() -ConfigPath 'missing') -like '*OmniBox AI*')
        Assert-Equal (Invoke-Ai -Arguments @('version') -ConfigPath 'missing') $version
        Assert-Equal @(Get-Calls $m).Count 0
    }
    Test 'example config and endpoint' {
        $m = New-TestModule
        Assert-Equal (Invoke-Ai -Arguments @('endpoint') -ConfigPath $example) 'http://127.0.0.1:18080/v1'
        Assert-Equal (Invoke-Ai -Arguments @('endpoint', 'ollama') -ConfigPath $example) 'http://127.0.0.1:11434/v1'
    }
    Test 'missing config fails clearly' {
        $null = New-TestModule
        Assert-Throws { Invoke-Ai -Arguments @('status') -ConfigPath 'missing.json' } '*Нет конфигурации*'
    }
    Test 'unknown backend does not mutate services' {
        $m = New-TestModule
        Assert-Throws { Invoke-Ai -Arguments @('start', 'other') -ConfigPath $example } '*Неизвестный сервер*'
        Assert-Equal @(Get-Calls $m).Count 0
    }
    Test 'unknown command fails rather than forwarding a typo' {
        $m = New-TestModule
        Assert-Throws { Invoke-Ai -Arguments @('strat') -ConfigPath $example } '*Неизвестная команда*'
        Assert-Equal @(Get-Calls $m).Count 0
    }
    Test 'default start stops Ollama before loading Bonsai' {
        $m = New-TestModule
        Invoke-Ai -Arguments @('start') -ConfigPath $example
        Assert-Equal ((Get-Calls $m) -join ',') 'admin,lock,stop:ollama,start:bonsai,probe:bonsai:/health'
    }
    Test 'idempotent start does not restart loaded backend' {
        $m = New-TestModule
        & $m { $script:services.bonsai.Status = 'Running'; $script:services.ollama.Status = 'Stopped' }
        Invoke-Ai -Arguments @('start', 'bonsai') -ConfigPath $example
        Assert-True (-not ((Get-Calls $m) -like 'start:*'))
    }
    Test 'Ollama starts and only version API is required' {
        $m = New-TestModule
        & $m { $script:services.bonsai.Status = 'Running'; $script:services.ollama.Status = 'Stopped' }
        Invoke-Ai -Arguments @('start', 'ollama') -ConfigPath $example
        Assert-Equal ((Get-Calls $m) -join ',') 'admin,lock,stop:bonsai,start:ollama,probe:ollama:/api/version'
    }
    Test 'missing target leaves the other server alone' {
        $m = New-TestModule
        & $m { $script:services.Remove('bonsai') }
        Assert-Throws { Invoke-Ai -Arguments @('start', 'bonsai') -ConfigPath $example } '*не установлена*'
        Assert-True (-not ((Get-Calls $m) -like 'stop:*'))
    }
    Test 'disabled target leaves the other server alone' {
        $m = New-TestModule
        & $m { $script:services.bonsai.StartType = 'Disabled' }
        Assert-Throws { Invoke-Ai -Arguments @('start') -ConfigPath $example } '*отключена*'
        Assert-True (-not ((Get-Calls $m) -like 'stop:*'))
    }
    Test 'failed stop prevents competing start' {
        $m = New-TestModule
        & $m { $script:failStop = 'ollama' }
        Assert-Throws { Invoke-Ai -Arguments @('start') -ConfigPath $example } '*mock stop failure*'
        Assert-True (-not ((Get-Calls $m) -like 'start:*'))
    }
    Test 'stop all stops both services' {
        $m = New-TestModule
        & $m { $script:services.bonsai.Status = 'Running' }
        Invoke-Ai -Arguments @('stop') -ConfigPath $example
        Assert-Equal @((Get-Calls $m) | Where-Object { $_ -like 'stop:*' }).Count 2
    }
    Test 'stop all attempts both even after one fails' {
        $m = New-TestModule
        & $m { $script:services.bonsai.Status = 'Running'; $script:failStop = 'bonsai' }
        Assert-Throws { Invoke-Ai -Arguments @('stop') -ConfigPath $example } '*mock stop failure*'
        Assert-Equal @((Get-Calls $m) | Where-Object { $_ -like 'stop:*' }).Count 2
    }
    Test 'stop tolerates absent optional backend' {
        $m = New-TestModule
        & $m { $script:services.Remove('bonsai') }
        Invoke-Ai -Arguments @('stop') -ConfigPath $example
        Assert-True ((Get-Calls $m) -contains 'stop:ollama')
    }
    Test 'restart orders stop before start' {
        $m = New-TestModule
        & $m { $script:services.bonsai.Status = 'Running'; $script:services.ollama.Status = 'Stopped' }
        Invoke-Ai -Arguments @('restart', 'bonsai') -ConfigPath $example
        Assert-Equal ((Get-Calls $m) -join ',') 'admin,lock,stop:bonsai,start:bonsai,probe:bonsai:/health'
    }
    Test 'status does not start stopped services' {
        $m = New-TestModule
        & $m { $script:services.ollama.Status = 'Stopped' }
        $null = Invoke-Ai -Arguments @('status') -ConfigPath $example
        Assert-Equal ((Get-Calls $m) -join ',') 'gpu'
    }
    Test 'sleep status uses props and does not wake the model' {
        $m = New-TestModule
        & $m { $script:services.bonsai.Status = 'Running'; $script:services.ollama.Status = 'Stopped'; $script:sleeping = $true }
        $rows = & $m { param($p); Get-AiStatus (Read-AiConfig $p) } $example
        $bonsai = $rows | Where-Object Backend -eq 'bonsai'
        Assert-Equal $bonsai.Sleeping $true
        Assert-Equal $bonsai.Context 196608
        Assert-Equal ((Get-Calls $m) -join ',') 'probe:bonsai:/props'
    }
    Test 'unexpected backend shutdown is reported' {
        $m = New-TestModule
        Assert-Throws { & $m { param($p); Wait-AiReady (Read-AiConfig $p) 'bonsai' } $example } '*служба завершилась*'
    }
    Test 'extra management args rejected before mutation' {
        $m = New-TestModule
        Assert-Throws { Invoke-Ai -Arguments @('start', 'bonsai', '--bad') -ConfigPath $example } '*Использование*'
        Assert-Equal @(Get-Calls $m).Count 0
    }
    Test 'single-engine policy also stops another backend in legacy exclusive false config' {
        $m = New-TestModule
        $cfg = Get-Content $example -Raw | ConvertFrom-Json -AsHashtable
        $cfg.exclusive = $false
        $path = Save-TestConfig $cfg
        Invoke-Ai -Arguments @('start') -ConfigPath $path
        Assert-True ((Get-Calls $m) -contains 'stop:ollama')
    }
    Test 'non-local endpoint rejected' {
        $null = New-TestModule
        $cfg = Get-Content $example -Raw | ConvertFrom-Json -AsHashtable
        $cfg.backends.bonsai.base_url = 'http://192.168.1.77:18080'
        $path = Save-TestConfig $cfg
        Assert-Throws { Invoke-Ai -Arguments @('status') -ConfigPath $path } '*локальным HTTP*'
    }
    Test 'wildcard service names rejected' {
        $null = New-TestModule
        $cfg = Get-Content $example -Raw | ConvertFrom-Json -AsHashtable
        $cfg.backends.bonsai.service = '*'
        $path = Save-TestConfig $cfg
        Assert-Throws { Invoke-Ai -Arguments @('stop') -ConfigPath $path } '*Некорректное имя службы*'
    }
    Test 'duplicate service targets rejected' {
        $null = New-TestModule
        $cfg = Get-Content $example -Raw | ConvertFrom-Json -AsHashtable
        $cfg.backends.ollama.service = 'BONSAI'
        $path = Save-TestConfig $cfg
        Assert-Throws { Invoke-Ai -Arguments @('stop') -ConfigPath $path } '*одной службой*'
    }
    Test 'native Ollama args, environment, output and exit code preserved' {
        $m = New-TestModule
        $fixture = Join-Path $PSScriptRoot 'native-fixture.ps1'
        $cfg = Get-Content $example -Raw | ConvertFrom-Json -AsHashtable
        $cfg.backends.ollama.executable = Join-Path $PSHOME $(if ($IsWindows) { 'pwsh.exe' } else { 'pwsh' })
        $cfg.backends.ollama.service_config = Join-Path $testRoot 'missing.xml'
        $path = Save-TestConfig $cfg
        $previous = $env:OLLAMA_HOST
        try {
            $env:OLLAMA_HOST = 'http://wrong:9999'
            $output = Invoke-Ai -Arguments @('ollama', '-NoProfile', '-File', $fixture, '--flag', 'два слова', 'x;y', '') -ConfigPath $path
            Assert-Equal $global:LASTEXITCODE 7
            $decoded = $output | ConvertFrom-Json
            Assert-Equal $decoded.host 'http://127.0.0.1:11434'
            Assert-Equal ($decoded.arguments -join '|') '--flag|два слова|x;y|'
            Assert-Equal $env:OLLAMA_HOST 'http://wrong:9999'
            Assert-Equal @(Get-Calls $m).Count 0
        } finally { $env:OLLAMA_HOST = $previous; $global:LASTEXITCODE = 0 }
    }
    Test 'Ollama shortcuts forward the original arguments' {
        $m = New-TestModule
        & $m { function script:Invoke-AiOllama { param($Config, $NativeArguments); $script:calls.Add('native:' + ($NativeArguments -join '|')) } }
        Invoke-Ai -Arguments @('run', 'some-model', 'два слова') -ConfigPath $example
        Assert-Equal ((Get-Calls $m) -join ',') 'native:run|some-model|два слова'
    }
    Test 'logs support filenames with spaces and do not mutate' {
        $m = New-TestModule
        $dir = Join-Path $testRoot 'logs with spaces'
        $null = New-Item -ItemType Directory -Path $dir -Force
        Set-Content -LiteralPath (Join-Path $dir 'bonsai-service.err.log') -Value @('first', 'last')
        $cfg = Get-Content $example -Raw | ConvertFrom-Json -AsHashtable
        $cfg.backends.bonsai.log_directory = $dir
        $path = Save-TestConfig $cfg
        Assert-Equal (Invoke-Ai -Arguments @('logs', 'bonsai', '1', 'err') -ConfigPath $path) 'last'
        Assert-Equal @(Get-Calls $m).Count 0
    }
    Test 'bad log length rejected' {
        $null = New-TestModule
        Assert-Throws { Invoke-Ai -Arguments @('logs', 'bonsai', '0') -ConfigPath $example } '*Число строк*'
    }
    Test 'unsupported schema rejected' {
        $null = New-TestModule
        $cfg = Get-Content $example -Raw | ConvertFrom-Json -AsHashtable
        $cfg.schema_version = 2
        $path = Save-TestConfig $cfg
        Assert-Throws { Invoke-Ai -Arguments @('status') -ConfigPath $path } '*schema_version*'
    }
    Test 'fractional timeout rejected' {
        $null = New-TestModule
        $cfg = Get-Content $example -Raw | ConvertFrom-Json -AsHashtable
        $cfg.startup_timeout_seconds = 1.5
        $path = Save-TestConfig $cfg
        Assert-Throws { Invoke-Ai -Arguments @('status') -ConfigPath $path } '*целым числом*'
    }
    Test 'malformed API path rejected' {
        $null = New-TestModule
        $cfg = Get-Content $example -Raw | ConvertFrom-Json -AsHashtable
        $cfg.backends.bonsai.base_url = 'http://127.0.0.1:18080/v1'
        $path = Save-TestConfig $cfg
        Assert-Throws { Invoke-Ai -Arguments @('status') -ConfigPath $path } '*локальным HTTP*'
    }
    Test 'stopped status tolerates missing services' {
        $m = New-TestModule
        & $m { $script:services.Clear() }
        $rows = @(& $m { param($p); Get-AiStatus (Read-AiConfig $p) } $example)
        Assert-Equal $rows.Count 2
        Assert-True (@($rows | Where-Object Service -ne 'Missing').Count -eq 0)
        Assert-Equal @(Get-Calls $m).Count 0
    }
    Test 'real mutex releases after exceptions' {
        Remove-Module OmniBox.Ai -ErrorAction SilentlyContinue
        $m = Import-Module $modulePath -Force -PassThru
        Assert-Throws { & $m { Invoke-AiLocked { throw 'inside lock' } } } '*inside lock*'
        Assert-Equal (& $m { Invoke-AiLocked { 'released' } }) 'released'
    }
    Test 'CLI entry point preserves native exit code and XML model directory' {
        $null = New-TestModule
        $cfg = Get-Content $example -Raw | ConvertFrom-Json -AsHashtable
        $exe = Join-Path $PSHOME $(if ($IsWindows) { 'pwsh.exe' } else { 'pwsh' })
        $cfg.backends.ollama.executable = $exe
        $xmlPath = Join-Path $testRoot 'ollama-service.xml'
        '<service><env name="OLLAMA_MODELS" value="models with spaces"/></service>' | Set-Content $xmlPath
        $cfg.backends.ollama.service_config = $xmlPath
        $path = Save-TestConfig $cfg
        $previousConfig = $env:OMNIBOX_AI_CONFIG
        $previousModels = $env:OLLAMA_MODELS
        try {
            $env:OMNIBOX_AI_CONFIG = $path
            $env:OLLAMA_MODELS = 'old models'
            $output = & $exe -NoLogo -NoProfile -File (Join-Path $repo 'ai.ps1') ollama -NoProfile -File (Join-Path $PSScriptRoot 'native-fixture.ps1') '--version' 'два слова'
            Assert-Equal $global:LASTEXITCODE 7
            $decoded = $output | ConvertFrom-Json
            Assert-Equal $decoded.models 'models with spaces'
            Assert-Equal ($decoded.arguments -join '|') '--version|два слова'
            Assert-Equal $env:OLLAMA_MODELS 'old models'
        } finally {
            $env:OMNIBOX_AI_CONFIG = $previousConfig
            $env:OLLAMA_MODELS = $previousModels
            $global:LASTEXITCODE = 0
        }
    }
    Test 'context accepts tokens and binary K suffix' {
        $m = New-TestModule
        Assert-Equal (& $m { ConvertTo-AiContext '192k' }) 196608
        Assert-Equal (& $m { ConvertTo-AiContext '128K' }) 131072
        Assert-Equal (& $m { ConvertTo-AiContext '32768' }) 32768
        Assert-Throws { & $m { ConvertTo-AiContext '0' } } '*от 1024*'
        Assert-Throws { & $m { ConvertTo-AiContext '2m' } } '*от 1024*'
        Assert-Throws { & $m { ConvertTo-AiContext '1.5k' } } '*Контекст*'
    }
    Test 'context read has no service/admin side effects' {
        $m = New-TestModule
        $fixture = New-ContextFixture
        Invoke-Ai -Arguments @('context', 'bonsai') -ConfigPath $fixture.path
        Assert-Equal (& $m { param($b); Get-AiConfiguredContext $b } $fixture.config.backends.bonsai) '196608'
        Assert-Equal @(Get-Calls $m).Count 0
    }
    Test 'context save creates exact backup and preserves other arguments' {
        $m = New-TestModule
        $fixture = New-ContextFixture
        $original = Get-Content $fixture.bonsai -Raw
        Invoke-Ai -Arguments @('context', 'bonsai', '128k') -ConfigPath $fixture.path
        $xml = [xml](Get-Content $fixture.bonsai -Raw)
        Assert-True ($xml.service.arguments -like '*"C:\ai\models\-c 77 model.gguf"*')
        Assert-True ($xml.service.arguments -like '*--sleep-idle-seconds 1800*')
        Assert-True ($xml.service.arguments -like '*-ctk q8_0*')
        Assert-True ($xml.service.arguments -like '*-c 131072')
        Assert-Equal $xml.service.startmode 'Manual'
        $backup = @(Get-ChildItem -LiteralPath ($fixture.bonsai + '.ai-previous'))
        Assert-Equal $backup.Count 1
        Assert-Equal (Get-Content $backup[0].FullName -Raw) $original
        Assert-Equal ((Get-Calls $m) -join ',') 'admin,lock'
    }
    Test 'Ollama context changes default env but preserves idle' {
        $m = New-TestModule
        $fixture = New-ContextFixture
        Invoke-Ai -Arguments @('context', 'ollama', '64k') -ConfigPath $fixture.path
        Assert-Equal (& $m { param($b); Get-AiConfiguredContext $b } $fixture.config.backends.ollama) '65536'
        $xml = [xml](Get-Content $fixture.ollama -Raw)
        Assert-Equal (($xml.service.env | Where-Object name -eq 'OLLAMA_KEEP_ALIVE').value) '30m'
        Assert-Equal ((Get-Calls $m) -join ',') 'admin,lock'
    }
    Test 'Ollama context env is added if missing' {
        $m = New-TestModule
        $fixture = New-ContextFixture
        '<service><id>ollama</id><env name="OLLAMA_KEEP_ALIVE" value="30m"/></service>' | Set-Content $fixture.ollama
        Invoke-Ai -Arguments @('context', 'ollama', '32k') -ConfigPath $fixture.path
        Assert-Equal (& $m { param($b); Get-AiConfiguredContext $b } $fixture.config.backends.ollama) '32768'
    }
    Test 'start context saves before launch' {
        $m = New-TestModule
        $fixture = New-ContextFixture
        Invoke-Ai -Arguments @('start', 'bonsai', '--context', '128k') -ConfigPath $fixture.path
        Assert-Equal (& $m { param($b); Get-AiConfiguredContext $b } $fixture.config.backends.bonsai) '131072'
        Assert-Equal ((Get-Calls $m) -join ',') 'admin,lock,stop:ollama,start:bonsai,probe:bonsai:/health'
    }
    Test 'start context refuses an already running model without XML changes' {
        $m = New-TestModule
        & $m { $script:services.bonsai.Status = 'Running' }
        $fixture = New-ContextFixture
        $old = Get-Content $fixture.bonsai -Raw
        Assert-Throws { Invoke-Ai -Arguments @('start', '--context=128k') -ConfigPath $fixture.path } '*ai restart*'
        Assert-Equal (Get-Content $fixture.bonsai -Raw) $old
        Assert-True (-not ((Get-Calls $m) -like 'stop:*'))
    }
    Test 'restart context stops then writes then reloads' {
        $m = New-TestModule
        & $m { $script:services.bonsai.Status = 'Running'; $script:services.ollama.Status = 'Stopped' }
        $fixture = New-ContextFixture
        Invoke-Ai -Arguments @('restart', 'bonsai', '--context=64k') -ConfigPath $fixture.path
        Assert-Equal (& $m { param($b); Get-AiConfiguredContext $b } $fixture.config.backends.bonsai) '65536'
        Assert-Equal ((Get-Calls $m) -join ',') 'admin,lock,stop:bonsai,start:bonsai,probe:bonsai:/health'
    }
    Test 'invalid context never stops a service' {
        $m = New-TestModule
        Assert-Throws { Invoke-Ai -Arguments @('restart', '--context', '999m') -ConfigPath $example } '*от 1024*'
        Assert-Equal @(Get-Calls $m).Count 0
    }
    Test 'invalid XML is checked before stopping a working server' {
        $m = New-TestModule
        & $m { $script:services.bonsai.Status = 'Running' }
        $fixture = New-ContextFixture '-m model.gguf -c nonsense'
        Assert-Throws { Invoke-Ai -Arguments @('restart', '--context', '128k') -ConfigPath $fixture.path } '*Некорректный параметр*'
        Assert-True (-not ((Get-Calls $m) -like 'stop:*'))
    }
    Test 'XML service identity mismatch prevents writes' {
        $m = New-TestModule
        $fixture = New-ContextFixture
        '<service><id>another</id><arguments>-c 1234</arguments></service>' | Set-Content $fixture.bonsai
        Assert-Throws { Invoke-Ai -Arguments @('context', '128k') -ConfigPath $fixture.path } '*не принадлежит*'
    }
    Test 'duplicate context flags are normalized, quoted paths are preserved' {
        $m = New-TestModule
        $fixture = New-ContextFixture '-m "path -c 12.gguf" -c 32768 --ctx-size=65536 --port 18080'
        Invoke-Ai -Arguments @('context', '192k') -ConfigPath $fixture.path
        $xml = [xml](Get-Content $fixture.bonsai -Raw)
        Assert-Equal $xml.service.arguments '-m "path -c 12.gguf" --port 18080 -c 196608'
    }
    Test 'PATH profile update preserves prompt and is idempotent' {
        Import-Module (Join-Path $repo 'src/Install.Helpers.psm1') -Force
        $profile = Join-Path $testRoot 'profiles/profile.ps1'
        $null = New-Item -ItemType Directory -Path (Split-Path $profile -Parent)
        $prompt = 'function prompt { "my prompt λ" }'
        Set-Content -LiteralPath $profile -Value $prompt -NoNewline
        $backups = Join-Path $testRoot 'profile-backups'
        Update-AiPowerShellProfile $profile 'C:\tools\ai' $backups
        $first = Get-Content $profile -Raw
        Assert-True ($first.StartsWith($prompt))
        $tokens = $null; $errors = $null
        $null = [Management.Automation.Language.Parser]::ParseFile($profile, [ref]$tokens, [ref]$errors)
        Assert-Equal $errors.Count 0
        Update-AiPowerShellProfile $profile 'C:\tools\ai' $backups
        Assert-Equal (Get-Content $profile -Raw) $first
        Assert-Equal @(Get-ChildItem $backups).Count 1
        Assert-Equal (Get-Content @(Get-ChildItem $backups)[0].FullName -Raw) $prompt
    }
    Test 'PATH profile update modifies only owned marker block' {
        Import-Module (Join-Path $repo 'src/Install.Helpers.psm1') -Force
        $profile = Join-Path $testRoot 'profile-other.ps1'
        $backups = Join-Path $testRoot 'profile-other-backups'
        Update-AiPowerShellProfile $profile 'C:\tools\ai' $backups
        Add-Content -LiteralPath $profile -Value 'function prompt { "keep" }'
        Update-AiPowerShellProfile $profile 'C:\tools\ai2' $backups
        $text = Get-Content $profile -Raw
        Assert-True ($text -like '*C:\tools\ai2*')
        Assert-True ($text -like '*function prompt { "keep" }*')
        Assert-Equal ([regex]::Matches($text, [regex]::Escape('# >>> omnibox-ai PATH >>>')).Count) 1
    }
    Test 'unapplied context edits keep the original restore point' {
        $m = New-TestModule; $f = New-ContextFixture
        $original = Get-Content $f.bonsai -Raw
        Invoke-Ai @('context','bonsai','128k') $f.path
        Invoke-Ai @('context','bonsai','64k') $f.path
        Assert-Equal (Get-Content ($f.bonsai + '.ai-previous') -Raw) $original
        Assert-True (& $m { param($b) (Read-AiRecoveryState $b).pending } $f.config.backends.bonsai)
    }
    Test 'successful start commits pending context for next restore point' {
        $m = New-TestModule; $f = New-ContextFixture
        Invoke-Ai @('start','--context','128k') $f.path
        Assert-True (-not (& $m { param($b) (Read-AiRecoveryState $b).pending } $f.config.backends.bonsai))
        $working = Get-Content $f.bonsai -Raw
        Invoke-Ai @('context','64k') $f.path
        Assert-Equal (Get-Content ($f.bonsai + '.ai-previous') -Raw) $working
    }
    Test 'failed new context rolls back without waking a previously stopped server' {
        $m = New-TestModule; $f = New-ContextFixture
        $original = Get-Content $f.bonsai -Raw
        & $m { function script:Wait-AiReady { param($Config,$Name) throw 'mock load OOM' } }
        Assert-Throws { Invoke-Ai @('start','--context','128k') $f.path } '*оставлен остановленным*'
        Assert-Equal (Get-Content $f.bonsai -Raw) $original
        Assert-Equal (& $m { [string]$script:services.bonsai.Status }) 'Stopped'
        Assert-Equal @((Get-Calls $m) | Where-Object { $_ -eq 'start:bonsai' }).Count 1
    }
    Test 'failed restart restores and restarts the formerly running configuration' {
        $m = New-TestModule; $f = New-ContextFixture
        $original = Get-Content $f.bonsai -Raw
        & $m {
            $script:services.bonsai.Status = 'Running'
            function script:Wait-AiReady {
                param($Config,$Name)
                $value = Get-AiConfiguredContext (Get-AiBackend $Config $Name)
                $script:calls.Add("ready:$value")
                if ($value -eq '131072') { throw 'mock load OOM' }
            }
        }
        Assert-Throws { Invoke-Ai @('restart','--context','128k') $f.path } '*снова запущен*'
        Assert-Equal (& $m {param($b)Get-AiConfiguredContext $b} $f.config.backends.bonsai) '196608'
        Assert-Equal (& $m {param($b)Get-AiArgumentValue @(Get-AiPrismArguments (Read-AiServiceXml $b)) @('-np','--parallel')} $f.config.backends.bonsai) '1'
        Assert-Equal (& $m { [string]$script:services.bonsai.Status }) 'Running'
        Assert-Equal @((Get-Calls $m) | Where-Object { $_ -eq 'start:bonsai' }).Count 2
        Assert-True ((Get-Calls $m) -contains 'ready:196608')
    }
    Test 'pending context cannot be committed by probing an already running model' {
        $m = New-TestModule; $f = New-ContextFixture
        & $m { $script:services.bonsai.Status = 'Running' }
        Invoke-Ai @('context','128k') $f.path
        Assert-Throws { Invoke-Ai @('start') $f.path } '*ai restart*'
        Assert-True (-not ((Get-Calls $m) -like 'stop:*'))
        Assert-True (& $m { param($b) (Read-AiRecoveryState $b).pending } $f.config.backends.bonsai)
    }
    Test 'failed stop prevents overwriting XML during rollback' {
        $m = New-TestModule; $f = New-ContextFixture
        Invoke-Ai @('context','128k') $f.path
        $pendingXml = Get-Content $f.bonsai -Raw
        & $m { $script:services.bonsai.Status = 'Running'; $script:failStop = 'bonsai' }
        Assert-Throws { Invoke-Ai @('restart') $f.path } '*mock stop failure*'
        Assert-Equal (Get-Content $f.bonsai -Raw) $pendingXml
        Assert-True (-not ((Get-Calls $m) -like 'start:*'))
    }
    Test 'manual restore of a stopped backend does not load the model' {
        $m = New-TestModule; $f = New-ContextFixture
        $original = Get-Content $f.bonsai -Raw
        Invoke-Ai @('context','128k') $f.path
        Invoke-Ai @('restore','bonsai') $f.path
        Assert-Equal (Get-Content $f.bonsai -Raw) $original
        Assert-True (-not ((Get-Calls $m) -like 'start:*'))
        Invoke-Ai @('restore','bonsai') $f.path
        Assert-True (-not ((Get-Calls $m) -like 'stop:*'))
    }
    Test 'manual restore of a running backend restarts restored settings' {
        $m = New-TestModule; $f = New-ContextFixture
        Invoke-Ai @('start','--context','128k') $f.path
        & $m { $script:calls.Clear() }
        Invoke-Ai @('restore') $f.path
        Assert-Equal (& $m { param($b) Get-AiConfiguredContext $b } $f.config.backends.bonsai) '196608'
        Assert-Equal ((Get-Calls $m) -join ',') 'admin,lock,stop:bonsai,start:bonsai,probe:bonsai:/health'
    }
    Test 'external XML modifications block restore before touching services' {
        $m = New-TestModule; $f = New-ContextFixture
        Invoke-Ai @('context','128k') $f.path
        Add-Content $f.bonsai '<!-- external edit -->'
        $changed = Get-Content $f.bonsai -Raw
        Assert-Throws { Invoke-Ai @('restore') $f.path } '*изменены вне ai*'
        Assert-Equal (Get-Content $f.bonsai -Raw) $changed
        Assert-True (-not ((Get-Calls $m) -like 'stop:*'))
    }
    Test 'damaged restore point blocks context changes' {
        $m = New-TestModule; $f = New-ContextFixture
        Invoke-Ai @('context','128k') $f.path
        Add-Content ($f.bonsai + '.ai-previous') 'broken'
        $pendingXml = Get-Content $f.bonsai -Raw
        Assert-Throws { Invoke-Ai @('context','64k') $f.path } '*изменены вне ai*'
        Assert-Equal (Get-Content $f.bonsai -Raw) $pendingXml
    }
    Test 'failed recovery-state write undoes all context file changes' {
        $m = New-TestModule; $f = New-ContextFixture
        $original = Get-Content $f.bonsai -Raw
        & $m { function script:Write-AiRecoveryState { param($Backend,$State) throw 'mock disk error' } }
        Assert-Throws { Invoke-Ai @('context','128k') $f.path } '*прежние файлы восстановлены*'
        Assert-Equal (Get-Content $f.bonsai -Raw) $original
        Assert-True (-not (Test-Path ($f.bonsai + '.ai-previous')))
        Assert-True (-not (Test-Path ($f.bonsai + '.ai-state.json')))
        Assert-Equal @(Get-ChildItem -Path ($f.bonsai + '.tmp-*')).Count 0
    }
    Test 'restore without history fails clearly' {
        $m = New-TestModule; $f = New-ContextFixture
        Assert-Throws { Invoke-Ai @('restore') $f.path } '*Нет предыдущих настроек*'
        Assert-True (-not ((Get-Calls $m) -like 'stop:*'))
    }
    Test 'power diagnostics are read-only and never touch AI services' {
        $m = New-TestModule
        & $m { function script:Show-AiPower { $script:calls.Add('power') } }
        Invoke-Ai @('power') $example
        Assert-Equal ((Get-Calls $m) -join ',') 'admin,power'
    }
    Test 'setup wizard defaults preserve context and do not start services' {
        Import-Module (Join-Path $repo 'src/Setup.Helpers.psm1') -Force
        $c = Get-Content $example -Raw | ConvertFrom-Json -AsHashtable
        $result = Get-AiSetupOptions -ConfigLoader { param($d) $c } -ContextReader { param($b) '196608' } -ReadAnswer { param($p) '' }
        Assert-Equal $result.directory 'C:\tools\ai'
        Assert-Equal $result.backend 'bonsai'
        Assert-True ($null -eq $result.context)
        Assert-True (-not $result.start)
    }
    Test 'setup wizard accepts backend context and explicit start' {
        $c = Get-Content $example -Raw | ConvertFrom-Json -AsHashtable
        $script:answers = [Collections.Generic.Queue[string]]::new([string[]]@('C:\tools\ai','ollama','64k','y'))
        $result = Get-AiSetupOptions -ConfigLoader { param($d) $c } -ContextReader { param($b) '32768' } -ReadAnswer { param($p) $script:answers.Dequeue() }
        Assert-Equal $result.backend 'ollama'
        Assert-Equal $result.context 65536
        Assert-True $result.start
    }
    Test 'setup noninteractive mode does not prompt and recognizes unchanged context' {
        $c = Get-Content $example -Raw | ConvertFrom-Json -AsHashtable
        $result = Get-AiSetupOptions -NonInteractive -Context 192k -ConfigLoader { param($d) $c } -ContextReader { param($b) '196608' } -ReadAnswer { throw 'must not prompt' }
        Assert-True ($null -eq $result.context)
        Assert-True (-not $result.start)
    }
    Test 'setup detects an existing installation without prompting' {
        $directory=Join-Path $testRoot 'setup-existing';$null=New-Item -ItemType Directory -Path $directory -Force
        Set-Content (Join-Path $directory '.omnibox-ai-install') 'omnibox-ai 0.9.0'
        $c=Get-Content $example -Raw|ConvertFrom-Json -AsHashtable
        $result=Get-AiSetupOptions -InstallDirectory $directory -ConfigLoader {param($d)$c} -ContextReader {param($b)'196608'} -ReadAnswer {throw 'must not prompt'}
        Assert-Equal $result.backend $c.default_backend
        Assert-True ($null -eq $result.context -and -not $result.start)
    }
    Test 'setup skips model questions after selecting an existing installation directory' {
        $directory=Join-Path $testRoot 'setup-existing'
        $c=Get-Content $example -Raw|ConvertFrom-Json -AsHashtable
        $script:setupPrompts=0
        $result=Get-AiSetupOptions -InstallDirectory (Join-Path $testRoot 'setup-new') -ConfigLoader {param($d)$c} -ContextReader {param($b)'196608'} -ReadAnswer {param($p)$script:setupPrompts++;if($script:setupPrompts -ne 1){throw 'unexpected question'};$directory}
        Assert-Equal $result.directory $directory
        Assert-Equal $script:setupPrompts 1
    }
    Test 'wizard accepts only English yes no answers and their abbreviations' {
        foreach ($a in @('y','yes','YES',' Y ')) { $answer=$a; Assert-True (Read-AiYesNo 'test' -ReadAnswer {param($p)$answer}) }
        foreach ($a in @('n','no','NO',' N ')) { $answer=$a; Assert-True (-not (Read-AiYesNo 'test' -ReadAnswer {param($p)$answer})) }
        Assert-Throws {Read-AiYesNo 'test' -ReadAnswer {param($p)'да'}} '*yes/no*'
    }
    Test 'wizard English prompt shows and respects the default' {
        Assert-True (Read-AiYesNo 'test' -Default $true -ReadAnswer {param($p) Assert-True ($p -like '*[[]Y/n[]]');''})
        Assert-True (-not (Read-AiYesNo 'test' -ReadAnswer {param($p) Assert-True ($p -like '*[[]y/N[]]');''}))
    }
    Test 'setup rejects invalid settings before installation' {
        $c = Get-Content $example -Raw | ConvertFrom-Json -AsHashtable
        Assert-Throws { Get-AiSetupOptions -NonInteractive -DefaultBackend other -ConfigLoader { param($d) $c } } '*Неизвестный сервер*'
        Assert-Throws { Get-AiSetupOptions -NonInteractive -Context 999m -ConfigLoader { param($d) $c } -ContextReader { param($b) '196608' } } '*от 1024*'
    }
    Test 'documented gh bootstrap parses, uses gh API and removes its temporary installer' {
        $doc=Get-Content (Join-Path $repo 'docs/install.md') -Raw
        $line=[regex]::Match($doc,'(?m)^& \{ \$ErrorActionPreference[^\r\n]+').Value
        Assert-True ($line.Length -gt 0)
        $bootstrap=[scriptblock]::Create($line)
        & {
            function gh {
                Assert-True (($args -join ' ').Contains('repos/aafnx/omnibox-ai/contents/dist/install-omnibox-ai.ps1?ref=main'))
                $global:LASTEXITCODE=0
                @{type='file';encoding='base64';content=[Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes('Write-Output "mock installer executed"'))}|ConvertTo-Json
            }
            $oldTemp=$env:TEMP;$env:TEMP=$testRoot
            try{Assert-Equal (& $bootstrap) 'mock installer executed';Assert-Equal @(Get-ChildItem $testRoot -Filter 'omnibox-ai-*.ps1').Count 0}finally{$env:TEMP=$oldTemp}
        }
    }
    Test 'bootstrap download failure leaves no token headers or temporary installer' {
        $u=Import-Module (Join-Path $repo 'src/Update.Helpers.psm1') -Force -PassThru
        & $u {
            function script:Read-Host {param($Prompt,[switch]$AsSecureString)ConvertTo-SecureString 'not-a-real-token' -AsPlainText -Force}
            function script:Invoke-RestMethod {param($Uri,$Headers,$MaximumRedirection,$TimeoutSec)$script:failedHeaders=$Headers;throw 'mock GitHub access denied'}
        }
        Assert-Throws {Get-AiInstallerBytes -UseToken} '*access denied*'
        Assert-Equal (& $u {$script:failedHeaders.Count}) 0
    }
    Test 'previously saved pending context also rolls back on next start failure' {
        $m = New-TestModule; $f = New-ContextFixture
        $original = Get-Content $f.bonsai -Raw
        Invoke-Ai @('context','128k') $f.path
        & $m { function script:Wait-AiReady { param($Config,$Name) throw 'mock OOM' } }
        Assert-Throws { Invoke-Ai @('start') $f.path } '*оставлен остановленным*'
        Assert-Equal (Get-Content $f.bonsai -Raw) $original
    }
    Test 'panel reads complete launch settings and local model catalog' {
        $m = New-TestModule; $f = New-WebModelFixture
        $b = $f.config.backends.bonsai
        $s = & $m { param($b) Get-AiLaunchSettings $b } $b
        Assert-Equal $s.context '196608'; Assert-Equal $s.idle_minutes 30
        Assert-Equal $s.gpu_layers '99'; Assert-Equal $s.cache_key 'q8_0'
        $models = @(& $m { param($b) Get-AiPrismModels $b } $b)
        Assert-Equal $models.Count 2
        Assert-True (-not (@($models | ForEach-Object name) -like '*mmproj*'))
    }
    Test 'panel save changes launch parameters without stopping running service' {
        $m = New-TestModule; $f = New-WebModelFixture
        & $m { $script:services.bonsai.Status = 'Running' }
        $model = Join-Path (Split-Path (& $m { param($b) (Get-AiLaunchSettings $b).model } $f.config.backends.bonsai) -Parent) 'other-model.gguf'
        $null = Invoke-AiPanelAction $f.path @{action='save';backend='bonsai';settings=@{model=$model;context='128k';idle_minutes=45;gpu_layers=80;cache_key='q4_0';cache_value='q4_0';flash_attention='on';batch=256;ubatch=64;alias='other-model'}}
        $s = & $m { param($b) Get-AiLaunchSettings $b } $f.config.backends.bonsai
        Assert-Equal $s.model $model; Assert-Equal $s.context '131072'; Assert-Equal $s.idle_minutes 45
        Assert-Equal $s.cache_key 'q4_0'; Assert-Equal $s.alias 'other-model'
        Assert-True ((Get-Content $f.bonsai -Raw) -like '*--port 18080 --jinja*')
        Assert-True (-not ((Get-Calls $m) -like 'stop:*'))
    }
    Test 'panel rejects arbitrary model paths and settings before touching services' {
        $m = New-TestModule; $f = New-WebModelFixture
        Assert-Throws { Invoke-AiPanelAction $f.path @{action='apply';settings=@{model='/etc/passwd'}} } '*произвольные пути запрещены*'
        Assert-Throws { Invoke-AiPanelAction $f.path @{action='apply';settings=@{command='whoami'}} } '*Неизвестная настройка*'
        Assert-Throws { Invoke-AiPanelAction $f.path @{action='apply';settings=@{gpu_layers='99 --host 0.0.0.0'}} } '*gpu_layers*'
        Assert-Throws { Invoke-AiPanelAction $f.path @{action='apply';settings=@{batch=64;ubatch=128}} } '*ubatch*'
        Assert-True (-not ((Get-Calls $m) -like 'stop:*'))
    }
    Test 'panel apply uses automatic rollback for model and cache changes too' {
        $m = New-TestModule; $f = New-WebModelFixture
        $original = Get-Content $f.bonsai -Raw
        & $m { function script:Wait-AiReady { param($Config,$Name) throw 'mock incompatible cache' } }
        Assert-Throws { Invoke-AiPanelAction $f.path @{action='apply';settings=@{cache_key='q4_0'}} } '*Предыдущий XML восстановлен*'
        Assert-Equal (Get-Content $f.bonsai -Raw) $original
    }
    Test 'panel unload PrismML stops service and never wakes it' {
        $m = New-TestModule; $f = New-WebModelFixture
        & $m { $script:services.bonsai.Status = 'Running' }
        $null = Invoke-AiPanelAction $f.path @{action='unload';backend='bonsai'}
        Assert-True ((Get-Calls $m) -contains 'stop:bonsai')
        Assert-True (-not ((Get-Calls $m) -like 'start:*'))
    }
    Test 'Ollama UI defaults modify only context and idle env' {
        $m = New-TestModule; $f = New-WebModelFixture
        $null = Invoke-AiPanelAction $f.path @{action='save';backend='ollama';settings=@{context='64k';idle_minutes=15}}
        $s = & $m { param($b) Get-AiLaunchSettings $b } $f.config.backends.ollama
        Assert-Equal $s.context '65536'; Assert-Equal $s.keep_alive '15m'
        Assert-Throws { Invoke-AiPanelAction $f.path @{action='save';backend='ollama';settings=@{gpu_layers=99}} } '*Неизвестная настройка*'
    }
    Test 'Ollama model preload uses empty prompt and configured keep-alive' {
        $m = New-TestModule; $f = New-WebModelFixture
        & $m {
            function script:Invoke-AiProbe { param($Backend,$Path) if($Path -eq '/api/tags') { [pscustomobject]@{models=@([pscustomobject]@{name='test:latest'})} } elseif($Path -eq '/api/ps'){[pscustomobject]@{models=@()}} else { [pscustomobject]@{version='0.35.1'} } }
            function script:Invoke-AiModelRequest { param($Backend,$Path,$Body,$Timeout) $script:modelBody=$Body; $script:modelPath=$Path }
        }
        $null = Invoke-AiPanelAction $f.path @{action='load';backend='ollama';model='test:latest'}
        Assert-Equal (& $m {$script:modelBody.prompt}) ''
        Assert-Equal (& $m {$script:modelBody.keep_alive}) '30m'
        Assert-Equal (& $m {$script:modelBody.options.num_ctx}) 32768
        Assert-Equal (& $m {$script:modelPath}) '/api/generate'
    }
    Test 'Ollama unload releases every loaded model without stopping service' {
        $m = New-TestModule; $f = New-WebModelFixture
        & $m {
            $script:unloaded=[Collections.Generic.List[string]]::new()
            function script:Invoke-AiProbe { param($Backend,$Path) [pscustomobject]@{models=@([pscustomobject]@{name='one:latest'},[pscustomobject]@{name='two:latest'})} }
            function script:Invoke-AiModelRequest { param($Backend,$Path,$Body,$Timeout) if($Body.keep_alive -ne 0){throw 'must unload'}; $script:unloaded.Add($Body.model) }
        }
        $null = Invoke-AiPanelAction $f.path @{action='unload';backend='ollama'}
        Assert-Equal (& $m {$script:unloaded -join ','}) 'one:latest,two:latest'
        Assert-True (-not ((Get-Calls $m) -like 'stop:*'))
    }
    Test 'panel dashboard of stopped servers does not query API or GPU' {
        $m = New-TestModule; $f = New-WebModelFixture
        & $m {$script:services.ollama.Status='Stopped';function script:Get-AiGpuData {throw 'no GPU probe'}}
        $d = Get-AiDashboard $f.path
        Assert-Equal $d.backends.Count 2
        Assert-True (-not ((Get-Calls $m) -like 'probe:*'))
        Assert-Equal $d.gpu.Count 0
    }
    Test 'download validates URL and filename before accessing network' {
        $m = New-TestModule; $f = New-WebModelFixture
        Assert-Throws { Invoke-AiPanelAction $f.path @{action='download';url='http://127.0.0.1/private.gguf';filename='model.gguf'} } '*HTTPS-ссылка*'
        Assert-Throws { Invoke-AiPanelAction $f.path @{action='download';url='https://huggingface.co/o/r/resolve/main/x.gguf';filename='../model.gguf'} } '*Имя GGUF*'
        Assert-Throws { Invoke-AiPanelAction $f.path @{action='pull';backend='ollama';model='x; Stop-Service bonsai'} } '*Некорректное имя*'
    }
    Test 'GGUF download checks magic and never overwrites existing model' {
        $m = New-TestModule; $f = New-WebModelFixture
        & $m { function script:Invoke-WebRequest {param($Uri,$OutFile,$MaximumRedirection,$TimeoutSec) [IO.File]::WriteAllBytes($OutFile,[Text.Encoding]::ASCII.GetBytes('GGUFdownload'))} }
        $request=@{action='download';url='https://huggingface.co/o/r/resolve/main/x.gguf';filename='new-model.gguf'}
        $null=Invoke-AiPanelAction $f.path $request
        Assert-Throws { Invoke-AiPanelAction $f.path $request } '*перезапись моделей запрещена*'
        Assert-True (-not ((Get-Calls $m) -like 'start:*'))
    }
    Test 'bad downloaded GGUF is removed without adding a model' {
        $m = New-TestModule; $f = New-WebModelFixture
        & $m {function script:Invoke-WebRequest {param($Uri,$OutFile,$MaximumRedirection,$TimeoutSec) [IO.File]::WriteAllText($OutFile,'HTML error')}}
        Assert-Throws { Invoke-AiPanelAction $f.path @{action='download';url='https://huggingface.co/o/r/resolve/main/x.gguf';filename='bad-model.gguf'} } '*заголовка GGUF*'
        $dir = Split-Path (& $m {param($b)(Get-AiLaunchSettings $b).model} $f.config.backends.bonsai) -Parent
        Assert-True (-not (Test-Path (Join-Path $dir 'bad-model.gguf')))
        Assert-Equal @(Get-ChildItem -LiteralPath $dir -Filter '*.part').Count 0
    }
    Test 'panel password hashes are salted and checked securely' {
        Import-Module (Join-Path $repo 'src/Panel.Security.psm1') -Force
        $a=New-AiPanelPassword 'correct test password'; $b=New-AiPanelPassword 'correct test password'
        Assert-True ($a.hash -cne $b.hash)
        Assert-True (Test-AiPanelPassword 'correct test password' $a)
        Assert-True (-not (Test-AiPanelPassword 'incorrect password' $a))
        Assert-Throws {New-AiPanelPassword 'short'} '*12*'
    }
    Test 'panel origin check blocks cross-site actions' {
        Assert-True (Test-AiPanelOrigin 'http://127.0.0.1:18090' '127.0.0.1:18090')
        Assert-True (-not (Test-AiPanelOrigin 'http://evil.example' '127.0.0.1:18090'))
        Assert-True (-not (Test-AiPanelOrigin 'http://127.0.0.1:18090@evil.example' '127.0.0.1:18090'))
    }
    Test 'proxy config rejects incomplete HTTP path wildcard and CIDR setup' {
        foreach ($c in @(
            @{public_origin='https://ai.example.com';trusted_proxy_addresses=@()},
            @{public_origin='';trusted_proxy_addresses=@('192.168.1.2')},
            @{public_origin='http://ai.example.com';trusted_proxy_addresses=@('192.168.1.2')},
            @{public_origin='https://ai.example.com/control';trusted_proxy_addresses=@('192.168.1.2')},
            @{public_origin='https://ai.example.com';trusted_proxy_addresses=@('192.168.1.0/24')},
            @{public_origin='https://ai.example.com';trusted_proxy_addresses=@('*')}
        )) { Assert-Throws {Set-AiPanelProxyDefaults $c} '*' }
    }
    Test 'proxy defaults keep legacy configurations and normalize HTTPS origin' {
        $old=@{};Set-AiPanelProxyDefaults $old;Assert-Equal $old.public_origin '';Assert-Equal $old.trusted_proxy_addresses.Count 0
        $c=@{public_origin='https://ai.example.com:8443/';trusted_proxy_addresses=@('192.168.1.2')};Set-AiPanelProxyDefaults $c
        Assert-Equal $c.public_origin 'https://ai.example.com:8443'
        Assert-True (Test-AiPanelOrigin 'https://ai.example.com:8443' 'ai.example.com:8443' 'https')
    }
    Test 'trusted proxy headers reject client address chains and accept mapped IPv4' {
        $c=@{public_origin='https://ai.example.com';trusted_proxy_addresses=@('192.168.1.2')}
        $r=[pscustomobject]@{RemoteEndPoint=[pscustomobject]@{Address=[Net.IPAddress]::Parse('::ffff:192.168.1.2')};Url=[Uri]'http://localhost:18090';Headers=@{'X-Forwarded-Proto'='https';'X-Forwarded-Host'='ai.example.com';'X-Forwarded-For'='198.51.100.1, 198.51.100.2'}}
        $t=Get-AiPanelRequestContext $r $c;Assert-True $t.secure;Assert-Equal $t.client_address '192.168.1.2'
        $r.Headers['X-Forwarded-For']='198.51.100.3';Assert-Equal (Get-AiPanelRequestContext $r $c).client_address '198.51.100.3'
    }
    Test 'panel service runs UI independently with automatic startup' {
        Import-Module (Join-Path $repo 'src/Panel.Install.psm1') -Force
        $xml=New-AiPanelServiceXml 'C:\Program Files\PowerShell\7\pwsh.exe' $testRoot
        Assert-Equal $xml.service.id 'omnibox-ai'
        Assert-Equal $xml.service.startmode 'Automatic'
        Assert-Equal $xml.service.executable 'C:\Program Files\PowerShell\7\pwsh.exe'
        Assert-True ($xml.service.arguments -like '*panel.ps1*')
        Assert-True ($xml.service.arguments -notlike '*Start-Service*')
    }
    Test 'panel upgrade preserves proxy network port and password without prompting' {
        $directory=Join-Path $testRoot 'panel-upgrade';$null=New-Item -ItemType Directory $directory
        $credential=& (Get-Module Panel.Install) {New-AiPanelPassword 'upgrade test password'}
        $c=@{schema_version=1;port=18091;allowed_hosts=@('192.168.1.77','localhost');credential=$credential;ssh_target='user@server';remote_addresses=@('192.168.1.2');public_origin='https://ai.example.com';trusted_proxy_addresses=@('192.168.1.2')}
        $c|ConvertTo-Json -Depth 8|Set-Content (Join-Path $directory 'panel.json')
        $o=Get-AiPanelInstallOptions -Directory $directory -NonInteractive
        Assert-Equal $o.port 18091;Assert-Equal $o.public_origin 'https://ai.example.com';Assert-Equal $o.credential.hash $credential.hash
        Assert-Equal ($o.remote_addresses -join ',') '192.168.1.2';Assert-Equal ($o.trusted_proxy_addresses -join ',') '192.168.1.2'
        Assert-True ('ai.example.com' -in $o.allowed_hosts);Assert-Equal $o.generated_password ''
    }
    Test 'panel upgrade can disable proxy explicitly while preserving password' {
        $directory=Join-Path $testRoot 'panel-upgrade'
        $o=Get-AiPanelInstallOptions -Directory $directory -NonInteractive -PublicUrl '' -TrustedProxyAddresses ''
        Assert-Equal $o.public_origin '';Assert-Equal $o.trusted_proxy_addresses.Count 0
        Assert-True ('ai.example.com' -notin $o.allowed_hosts);Assert-Equal $o.port 18091
    }
    Test 'gateway installer defaults to the shared panel port and generates a key' {
        $o=Get-AiGatewayInstallOptions -Directory (Join-Path $testRoot 'new-api') -HostName '192.168.1.77' -NonInteractive
        Assert-True $o.enabled;Assert-True $o.require_key;Assert-Equal $o.port 18090;Assert-Equal $o.base_url 'http://192.168.1.77:18090/v1'
        Assert-True ($o.generated_key -match '^obai-[a-f0-9]{64}$');Assert-Equal $o.key_hash.Length 64
    }
    Test 'gateway installer advertises the existing NAS origin and allows an extra API port' {
        $o=Get-AiGatewayInstallOptions -Directory (Join-Path $testRoot 'nas-api') -HostName '192.168.1.77' -NonInteractive -PanelPort 18091 -PublicUrl 'https://llm.fnx.red'
        Assert-Equal $o.port 18091;Assert-Equal $o.base_url 'https://llm.fnx.red/v1'
        $o=Get-AiGatewayInstallOptions -Directory (Join-Path $testRoot 'extra-api') -HostName '192.168.1.77' -NonInteractive -Port 11435
        Assert-Equal $o.port 11435;Assert-Equal $o.base_url 'http://192.168.1.77:11435/v1'
    }
    Test 'gateway installer can leave network API disabled' {
        $o=Get-AiGatewayInstallOptions -Directory (Join-Path $testRoot 'no-api') -HostName '192.168.1.77' -NonInteractive -NoApi
        Assert-True (-not $o.enabled);Assert-True (-not $o.require_key);Assert-Equal $o.generated_key ''
    }
    Test 'gateway API keys are random and hash only configuration is sufficient' {
        Import-Module (Join-Path $repo 'src/Gateway.Helpers.psm1') -Force
        $a=New-AiApiKey;$b=New-AiApiKey;Assert-True ($a.key -cne $b.key);Assert-True ($a.hash -cne $b.hash)
        Assert-Equal $a.hash ([Convert]::ToHexString([Security.Cryptography.SHA256]::HashData([Text.Encoding]::UTF8.GetBytes($a.key))).ToLowerInvariant())
    }
    Test 'gateway validates client URL without accepting embedded credentials' {
        Assert-True (Test-AiClientBaseUrl 'https://llm.fnx.red/v1');Assert-True (Test-AiClientBaseUrl 'https://llm.fnx.red:11435/v1')
        foreach($url in @('https://llm.fnx.red','https://user:secret@llm.fnx.red/v1','file:///v1','http://host/v1?secret=x')){Assert-True (-not(Test-AiClientBaseUrl $url))}
    }
    Test 'gateway update rejects arbitrary targets and supports optional auth' {
        $a=New-AiApiKey;$c=@{schema_version=1;enabled=$true;port=11435;base_url='http://192.168.1.77:11435/v1';backend='bonsai';require_key=$true;key_hash=$a.hash}
        Assert-Throws {Update-AiGatewaySettings $c @{action='save';backend='http://other';base_url=$c.base_url;require_key=$false} @('bonsai','ollama')} '*известный движок*'
        $r=Update-AiGatewaySettings $c @{action='save';backend='ollama';base_url='https://llm.fnx.red/v1';require_key=$false} @('bonsai','ollama')
        Assert-Equal $r.config.backend 'ollama';Assert-True (-not $r.config.require_key);Assert-Equal $r.config.key_hash $a.hash
        $view=Get-AiGatewayView $c;Assert-True (-not $view.Contains('key_hash'))
    }
    Test 'gateway writes atomically and keeps original on invalid configuration' {
        $path=Join-Path $testRoot 'gateway.json';$pair=New-AiApiKey
        $c=@{schema_version=1;enabled=$true;port=11435;base_url='http://192.168.1.77:11435/v1';backend='bonsai';require_key=$true;key_hash=$pair.hash}
        Write-AiGatewayConfig $path $c;$original=Get-Content $path -Raw;$c.key_hash=''
        Assert-Throws {Write-AiGatewayConfig $path $c} '*hash API key*';Assert-Equal (Get-Content $path -Raw) $original
        Assert-True (-not $original.Contains($pair.key));Assert-Equal @(Get-ChildItem $testRoot -Filter 'gateway.json.tmp-*').Count 0
    }
    Test 'catalog search encodes user query and retains only valid repository links' {
        $cm=Import-Module (Join-Path $repo 'src/Model.Catalog.psm1') -Force -PassThru
        & $cm {function script:Invoke-AiHubApi {param($Path)$script:lastPath=$Path;@([pscustomobject]@{id='author/model-GGUF';downloads=10;gated=$false},[pscustomobject]@{id='../bad';downloads=1;gated=$false})}}
        $c=Search-AiModelCatalog 'uncensored & test';Assert-Equal $c.models.Count 1;Assert-Equal $c.models[0].url 'https://huggingface.co/author/model-GGUF'
        Assert-True ((& $cm {$script:lastPath}) -like '*uncensored%20%26%20test*')
        Assert-Throws {Search-AiModelCatalog 'x'} '*2 до 100*'
    }
    Test 'catalog files pin revision and label unsupported split and Prism formats' {
        $cm=Get-Module Model.Catalog
        & $cm {function script:Invoke-AiHubApi {param($Path)[pscustomobject]@{sha=('b'*40);siblings=@([pscustomobject]@{rfilename='model-PQ2_0.gguf';size=100;lfs=[pscustomobject]@{sha256=('a'*64)}},[pscustomobject]@{rfilename='model-00001-of-00002.gguf';size=50},[pscustomobject]@{rfilename='mmproj-model.gguf';size=50})}}}
        $c=Get-AiCatalogFiles 'author/model';Assert-Equal $c.files.Count 2;Assert-True $c.files[0].prism_only;Assert-True $c.files[1].split
        Assert-True ($c.files[0].url -like ('*/resolve/'+('b'*40)+'/*'));Assert-Equal $c.files[0].sha256 ('a'*64)
        Assert-Throws {Get-AiCatalogFiles '../secret'} '*author/model*'
    }
    Test 'OpenAI proxy source compiles under the installed PowerShell runtime' {
        if(-not ('OmniBox.OpenAiProxy' -as [type])){Add-Type -Path @((Join-Path $repo 'src/Inference.Stats.cs'),(Join-Path $repo 'src/OpenAi.Proxy.cs'))}
        Assert-Equal ([OmniBox.OpenAiProxy].FullName) 'OmniBox.OpenAiProxy'
    }
    Test 'access info exposes addresses but no hashes or keys' {
        $m=New-TestModule;$dir=Join-Path $testRoot 'panel-recovery';$null=New-Item -ItemType Directory $dir
        Import-Module (Join-Path $repo 'src/Panel.Security.psm1') -Force
        Import-Module (Join-Path $repo 'src/Gateway.Helpers.psm1') -Force
        $credential=New-AiPanelPassword 'old recovery password'
        $panel=@{schema_version=1;port=18090;allowed_hosts=@('192.168.1.77','localhost');credential=$credential;ssh_target='avoq@omnibox';public_origin='https://llm.fnx.red';trusted_proxy_addresses=@('192.168.1.9')}
        $panel|ConvertTo-Json -Depth 10|Set-Content (Join-Path $dir 'panel.json')
        $key=New-AiApiKey
        Write-AiGatewayConfig (Join-Path $dir 'gateway.json') @{schema_version=1;enabled=$true;port=18090;base_url='https://llm.fnx.red/v1';backend='bonsai';require_key=$true;key_hash=$key.hash}
        $info=& $m {param($Root)Get-AiAccessInfo $Root} $dir
        Assert-Equal $info.PanelLAN 'http://192.168.1.77:18090';Assert-Equal $info.OpenAIBaseURL 'https://llm.fnx.red/v1'
        $json=$info|ConvertTo-Json;Assert-True (-not $json.Contains($credential.hash));Assert-True (-not $json.Contains($key.hash));Assert-True (-not $json.Contains($key.key))
    }
    Test 'password reset invalidates old password and restarts only panel service' {
        $m=New-TestModule;$dir=Join-Path $testRoot 'panel-recovery'
        & $m {$script:services['omnibox-ai']=[pscustomobject]@{Status='Running';StartType='Automatic'}}
        & $m {param($Root)Set-AiPanelPassword $Root 'new recovery password'} $dir
        $c=& $m {param($Root)Read-AiPanelConfig (Join-Path $Root 'panel.json')} $dir
        Assert-True (& $m {param($c)Test-AiPanelPassword 'new recovery password' $c.credential} $c)
        Assert-True (-not(& $m {param($c)Test-AiPanelPassword 'old recovery password' $c.credential} $c))
        Assert-Equal ($c.trusted_proxy_addresses -join ',') '192.168.1.9';Assert-Equal $c.public_origin 'https://llm.fnx.red'
        Assert-Equal ((Get-Calls $m)-join ',') 'stop:omnibox-ai,start:omnibox-ai'
        $bytes=Get-Content (Join-Path $dir 'panel.json') -Raw;Assert-True (-not $bytes.Contains('new recovery password'))
    }
    Test 'invalid password leaves panel credential file unchanged' {
        $m=New-TestModule;$dir=Join-Path $testRoot 'panel-recovery';$old=Get-Content (Join-Path $dir 'panel.json') -Raw
        Assert-Throws {& $m {param($Root)Set-AiPanelPassword $Root 'short'} $dir} '*от 12*'
        Assert-Equal (Get-Content (Join-Path $dir 'panel.json') -Raw) $old;Assert-Equal @(Get-Calls $m).Count 0
        Assert-Throws {Invoke-Ai @('password','unexpected') (Join-Path $dir 'missing-config.json')} '*ai password reset*'
    }
    Test 'single model XML enforces one Prism slot and one Ollama runner' {
        $m=New-TestModule;$f=New-ContextFixture '-m model.gguf -np 4 --parallel=2 -c 196608'
        $x=& $m {param($b)New-AiSingleModelXml $b (Read-AiServiceXml $b)} $f.config.backends.bonsai
        Assert-Equal (& $m {param($x)Get-AiArgumentValue @(Get-AiPrismArguments $x) @('-np','--parallel')} $x) '1'
        $x=& $m {param($b)New-AiSingleModelXml $b (Read-AiServiceXml $b)} $f.config.backends.ollama
        Assert-Equal (& $m {param($x)Get-AiXmlEnv $x 'OLLAMA_MAX_LOADED_MODELS' ''} $x) '1'
        Assert-Equal (& $m {param($x)Get-AiXmlEnv $x 'OLLAMA_NUM_PARALLEL' ''} $x) '1'
        Assert-Equal (& $m {param($x)Get-AiXmlEnv $x 'OLLAMA_KEEP_ALIVE' ''} $x) '30m'
    }
    Test 'router configuration is rejected before stopping another engine' {
        $m=New-TestModule;$f=New-ContextFixture '-m model.gguf --models-dir C:\models -c 196608'
        Assert-Throws {Invoke-Ai @('start') $f.path} '*Router mode*'
        Assert-True (-not ((Get-Calls $m)-like 'stop:*'))
        Assert-Throws {Invoke-Ai @('ollama','serve') $f.path} '*ai start ollama*'
    }
    Test 'Ollama switching unloads previous model before loading and persists selected name' {
        $m=New-TestModule;$f=New-WebModelFixture
        & $m {
            $script:modelCalls=[Collections.Generic.List[string]]::new()
            function script:Invoke-AiProbe {param($Backend,$Path)switch($Path){
                '/api/tags'{[pscustomobject]@{models=@([pscustomobject]@{name='new:latest'})}}
                '/api/ps'{[pscustomobject]@{models=@([pscustomobject]@{name='old:latest'})}}
                default{[pscustomobject]@{version='0.35.1'}}}}
            function script:Invoke-AiModelRequest {param($Backend,$Path,$Body,$Timeout)$script:modelCalls.Add("$($Body.model):$($Body.keep_alive)")}
        }
        $null=Invoke-AiPanelAction $f.path @{action='load';backend='ollama';model='new:latest'}
        Assert-Equal (& $m {$script:modelCalls -join ','}) 'old:latest:0,new:latest:30m'
        $c=Get-Content $f.path -Raw|ConvertFrom-Json -AsHashtable;Assert-Equal $c.backends.ollama.active_model 'new:latest'
    }
    Test 'gateway rejects an unexpected loaded Ollama model and uses actual context' {
        $m=New-TestModule;$f=New-WebModelFixture
        $f.config.backends.ollama.active_model='selected';$path=Save-TestConfig $f.config
        & $m {
            $script:loadedName='other:latest'
            function script:Invoke-AiProbe {param($Backend,$Path)[pscustomobject]@{models=@([pscustomobject]@{name=$script:loadedName;context_length=65536})}}
        }
        $r=Get-AiGatewayRuntime $path 'ollama';Assert-True (-not $r.available);Assert-Equal $r.status 409
        & $m {$script:loadedName='selected:latest'}
        $r=Get-AiGatewayRuntime $path 'ollama';Assert-True $r.available;Assert-Equal $r.model 'selected:latest';Assert-Equal $r.context 65536
    }
    Test 'gateway and client card retain the active alias while new settings are pending' {
        $m=New-TestModule;$f=New-WebModelFixture
        & $m {$script:services.bonsai.Status='Running';$script:services.ollama.Status='Stopped'}
        $null=Invoke-AiPanelAction $f.path @{action='save';backend='bonsai';settings=@{alias='future-model';context='128k'}}
        $r=Get-AiGatewayRuntime $f.path 'bonsai';Assert-Equal $r.model 'bonsai-2-27b';Assert-Equal $r.context 196608
        $d=Get-AiDashboard $f.path;$b=$d.backends|Where-Object name -eq 'bonsai'
        Assert-Equal $b.selected_model 'bonsai-2-27b';Assert-Equal $b.settings.alias 'future-model';Assert-True $b.pending
    }
    Test 'telemetry of stopped engines performs no system or GPU sampling' {
        $m=New-TestModule;& $m {$script:services.ollama.Status='Stopped';function script:Get-AiSystemSample {throw 'must not sample'}}
        $t=Get-AiTelemetry $example;Assert-True (-not $t.enabled)
    }
    Test 'telemetry caches samples for concurrent visible clients' {
        $m=New-TestModule;& $m {$script:samples=0;function script:Get-AiSystemSample {$script:samples++;@{cpu_percent=25;ram_total_gb=32;ram_used_gb=16;cpu_temperature=$null;gpu=@()}}}
        $a=Get-AiTelemetry $example;$b=Get-AiTelemetry $example
        Assert-True $a.enabled;Assert-Equal $a.sample.cpu_percent 25;Assert-Equal $b.sample.ram_used_gb 16
        Assert-Equal (& $m {$script:samples}) 1
    }
    Test 'usage journal writes whitelisted metadata and reloads session totals' {
        $dir=Join-Path $testRoot 'usage-journal';[OmniBox.InferenceStats]::Configure($dir)
        $cancel=[Threading.CancellationTokenSource]::new()
        $r=[OmniBox.InferenceStats]::Begin('bonsai','bonsai-2-27b','/v1/chat/completions',196608,'private session identifier',$cancel)
        [OmniBox.InferenceStats]::Observe($r,'{"choices":[{"message":{"content":"private answer"}}],"usage":{"prompt_tokens":20,"completion_tokens":8,"total_tokens":28}}')
        [OmniBox.InferenceStats]::Finish($r,200,$true);$cancel.Dispose()
        $text=Get-Content (Join-Path $dir 'requests.jsonl') -Raw
        Assert-True (-not $text.Contains('private'));Assert-True (-not $text.Contains('choices'));Assert-True (-not $text.Contains('message'))
        [OmniBox.InferenceStats]::Configure($dir);$data=[OmniBox.InferenceStats]::Snapshot()|ConvertFrom-Json
        Assert-Equal $data.requests.Count 1;Assert-Equal $data.sessions[0].total_tokens 28;Assert-Equal $data.requests[0].context_remaining 196580
    }
    Test 'live progress updates token counters without counting SSE fragments as tokens' {
        [OmniBox.InferenceStats]::Configure((Join-Path $testRoot 'live-usage'))
        $cancel=[Threading.CancellationTokenSource]::new();$r=[OmniBox.InferenceStats]::Begin('bonsai','bonsai-2-27b','/v1/chat/completions',32768,'',$cancel)
        [OmniBox.InferenceStats]::Observe($r,'{"usage":null,"prompt_progress":{"total":40,"processed":30},"timings":{"predicted_n":3}}')
        [OmniBox.InferenceStats]::ObserveSlot('bonsai',9,32768)
        $s=[OmniBox.InferenceStats]::Snapshot()|ConvertFrom-Json;Assert-Equal $s.active.input_tokens 40;Assert-Equal $s.active.output_tokens 9;Assert-Equal $s.active.total_tokens 49
        [OmniBox.InferenceStats]::Finish($r,200,$true);$cancel.Dispose()
    }
    Test 'generation and control guards reject overlapping work but stop can cancel' {
        [OmniBox.InferenceStats]::Configure((Join-Path $testRoot 'busy-usage'))
        $cancel=[Threading.CancellationTokenSource]::new();$r=[OmniBox.InferenceStats]::Begin('bonsai','model','/v1/chat/completions',32768,'',$cancel)
        Assert-True ($null -eq [OmniBox.InferenceStats]::Begin('bonsai','model','/v1/chat/completions',32768,'',$cancel))
        Assert-True (-not [OmniBox.InferenceStats]::BeginControl($false));Assert-True ([OmniBox.InferenceStats]::BeginControl($true));Assert-True $cancel.IsCancellationRequested
        [OmniBox.InferenceStats]::Finish($r,504,$false);[OmniBox.InferenceStats]::EndControl();$cancel.Dispose()
    }
    Test 'request journal rotates bounded files and preserves unknown usage as null' {
        $dir=Join-Path $testRoot 'rotation-usage';[OmniBox.InferenceStats]::Configure($dir)
        [IO.File]::WriteAllText((Join-Path $dir 'requests.jsonl'),('x'*(4MB)))
        $cancel=[Threading.CancellationTokenSource]::new();$r=[OmniBox.InferenceStats]::Begin('bonsai','model','/v1/chat/completions',32768,'',$cancel)
        [OmniBox.InferenceStats]::Finish($r,502,$false);$cancel.Dispose()
        Assert-True (Test-Path (Join-Path $dir 'requests.jsonl.1'))
        $r=Get-Content (Join-Path $dir 'requests.jsonl') -Raw|ConvertFrom-Json
        Assert-True ($null -eq $r.input_tokens);Assert-True ($null -eq $r.output_tokens);Assert-True ($null -eq $r.total_tokens)
        Assert-Equal $r.state 'failed'
    }
    Test 'native service command quotes executable, script and compatible profile paths' {
        Import-Module (Join-Path $repo 'src/Service.Install.psm1') -Force
        $cmd=New-AiNativeServiceCommand '/program files/pwsh' '/tools/ai' '/models/service.xml'
        Assert-True ($cmd.StartsWith('"' + (Join-Path '/tools/ai' 'src/Service.Host.exe') + '"')) 'Host path is not the first quoted token'
        Assert-True ($cmd.Contains('-Profile "/models/service.xml"'))
        Assert-True (-not $cmd.Contains('winsw'))
        Assert-Throws {New-AiNativeServiceCommand '/bad"path' '/tools/ai' '/model.xml'} '*Invalid service path*'
        Add-Type -Path (Join-Path $repo 'src/Service.Host.cs')
        Assert-True (Test-AiOwnedServiceCommand $cmd '/program files/pwsh' '/tools/ai' '/models/service.xml')
        $legacy=New-AiLegacyServiceCommand '/program files/pwsh' '/tools/ai' '/models/service.xml'
        Assert-True (Test-AiOwnedServiceCommand $legacy '/program files/pwsh' '/tools/ai' '/models/service.xml')
        Assert-True (-not(Test-AiOwnedServiceCommand '"/other/service.exe" -Profile "/models/service.xml"' '/program files/pwsh' '/tools/ai' '/models/service.xml'))
    }
    Test 'service command changes check Windows provider result and preserved quoted paths' {
        Import-Module (Join-Path $repo 'src/Service.Install.psm1') -Force
        $m=Get-Module Service.Install
        & $m {
            $script:fixtureService=[pscustomobject]@{Name='fixture';PathName='old';StartMode='Manual'}
            $script:fixtureCode=0;$script:fixtureIgnore=$false;$script:fixtureMissing=$false
            function script:Get-CimInstance {[CmdletBinding()]param($ClassName,$Filter)if(-not $script:fixtureMissing){$script:fixtureService.PSObject.Copy()}}
            function script:Invoke-CimMethod {
                [CmdletBinding()]param($InputObject,$MethodName,$Arguments,$OperationTimeoutSec)
                if($MethodName -ne 'Change'){throw 'Wrong provider method'}
                if($script:fixtureCode -eq 0 -and -not $script:fixtureIgnore){$script:fixtureService.PathName=$Arguments.PathName;if($Arguments.ContainsKey('StartMode')){$script:fixtureService.StartMode=if($Arguments.StartMode -eq 'Automatic'){'Auto'}else{$Arguments.StartMode}}}
                [pscustomobject]@{ReturnValue=$script:fixtureCode}
            }
        }
        try {
            $command='"C:\program files\ai\src\Service.Host.exe" -Profile "C:\tools\ai\panel-service.xml"'
            Set-AiServiceCommand -Name fixture -Command $command -StartupType Automatic
            Assert-Equal (& $m {$script:fixtureService.PathName}) $command
            Assert-Equal (& $m {$script:fixtureService.StartMode}) 'Auto'
            & $m {$script:fixtureCode=2}
            Assert-Throws {Set-AiServiceCommand fixture 'replacement'} '*Win32_Service.Change code 2*'
            Assert-Equal (& $m {$script:fixtureService.PathName}) $command
            & $m {$script:fixtureCode=0;$script:fixtureIgnore=$true}
            Assert-Throws {Set-AiServiceCommand fixture 'replacement'} '*verification failed*'
            & $m {$script:fixtureMissing=$true}
            Assert-Throws {Set-AiServiceCommand fixture 'replacement'} '*does not exist*'
        } finally {& $m {Remove-Item Function:script:Get-CimInstance,Function:script:Invoke-CimMethod}}
    }
    Test 'product scripts never pass unsupported BinaryPathName to Set-Service' {
        foreach($file in Get-ChildItem $repo -Recurse -File | Where-Object {$_.Extension -in @('.ps1','.psm1') -and $_.FullName -notmatch '[/\\]tests[/\\]'}){
            $ast=[Management.Automation.Language.Parser]::ParseFile($file.FullName,[ref]$null,[ref]$null)
            $invalid=$ast.FindAll({param($node)$node -is [Management.Automation.Language.CommandAst] -and $node.GetCommandName() -eq 'Set-Service' -and @($node.CommandElements|Where-Object {$_ -is [Management.Automation.Language.CommandParameterAst] -and $_.ParameterName -eq 'BinaryPathName'}).Count -gt 0},$true)
            Assert-Equal @($invalid).Count 0
        }
    }
    Test 'Prism placement sets CPU, full GPU or explicit hybrid layers without losing context' {
        $f=New-ContextFixture;$m=New-TestModule;$b=$f.config.backends.bonsai
        $x=& $m {param($b)New-AiLaunchXml $b @{placement='cpu'}} $b
        Assert-Equal (& $m {param($x)Get-AiArgumentValue @(Get-AiPrismArguments $x) @('-ngl') ''} $x) '0'
        Assert-True ($x.service.arguments.Contains('--no-kv-offload'))
        $x=& $m {param($b)New-AiLaunchXml $b @{placement='gpu'}} $b
        Assert-Equal (& $m {param($x)Get-AiArgumentValue @(Get-AiPrismArguments $x) @('-ngl') ''} $x) '999'
        $x=& $m {param($b)New-AiLaunchXml $b @{placement='hybrid';gpu_layers='12'}} $b
        Assert-Equal (& $m {param($x)Get-AiArgumentValue @(Get-AiPrismArguments $x) @('-ngl') ''} $x) '12'
        Assert-Throws {& $m {param($b)New-AiLaunchXml $b @{placement='hybrid'}} $b} '*1 до 98*'
    }
    Test 'Ollama CPU placement disables GPU discovery and automatic placement restores prior masks' {
        $f=New-ContextFixture;$m=New-TestModule;$b=$f.config.backends.ollama
        $x=& $m {param($b)New-AiLaunchXml $b @{placement='cpu'}} $b
        Assert-Equal (& $m {param($x)Get-AiXmlEnv $x 'CUDA_VISIBLE_DEVICES' ''} $x) '-1'
        $x.Save($b.service_config)
        $x=& $m {param($b)New-AiLaunchXml $b @{placement='auto'}} $b
        Assert-True ($null -eq $x.SelectSingleNode("/service/env[@name='CUDA_VISIBLE_DEVICES']"))
        Assert-Equal (& $m {param($x)Get-AiXmlEnv $x 'OLLAMA_CONTEXT_LENGTH' ''} $x) '32768'
        Assert-Throws {& $m {param($b)New-AiLaunchXml $b @{placement='gpu'}} $b} '*Недопустимое*'
    }
    Test 'Ollama load applies pending CPU placement with a restart before loading' {
        $f=New-WebModelFixture;$m=New-TestModule
        $null=Invoke-AiPanelAction $f.path @{action='save';backend='ollama';settings=@{placement='cpu'}}
        & $m {
            function script:Invoke-AiProbe {param($Backend,$Path)switch($Path){
                '/api/tags'{[pscustomobject]@{models=@([pscustomobject]@{name='new:latest'})}}
                '/api/ps'{[pscustomobject]@{models=@()}}
                default{[pscustomobject]@{version='0.35.1'}}}}
            function script:Invoke-AiModelRequest {param($Backend,$Path,$Body,$Timeout)$script:cpuBody=$Body}
        }
        $null=Invoke-AiPanelAction $f.path @{action='load';backend='ollama';model='new:latest'}
        Assert-True ((& $m {$script:calls -join ','}).Contains('stop:ollama'))
        Assert-True ((& $m {$script:calls -join ','}).Contains('start:ollama'))
        Assert-Equal (& $m {$script:cpuBody.options.num_gpu}) 0
    }
    Test 'all brand vectors are valid original SVG assets in the package' {
        foreach($name in @('favicon.svg','brand-light.svg','brand-dark.svg')){
            $x=[xml](Get-Content (Join-Path $repo "web/$name") -Raw)
            Assert-Equal $x.DocumentElement.LocalName 'svg'
            Assert-True ($x.OuterXml.Contains('#7C6CFF') -or $x.OuterXml.Contains('#7c6cff'))
        }
    }
    Test 'updater uses gh credentials without asking for a token' {
        $u=Import-Module (Join-Path $repo 'src/Update.Helpers.psm1') -Force -PassThru
        $expected=[Text.Encoding]::UTF8.GetBytes("#requires -Version 7.4`n"+('#'*200))
        & $u {
            param($Encoded)
            $script:encoded=$Encoded
            function script:Get-Command {param($Name,$CommandType,$ErrorAction)if($Name -eq 'gh'){[pscustomobject]@{Source='fake-gh'}}}
            function script:Invoke-AiGhContents {param($Executable)$script:executable=$Executable;@{type='file';encoding='base64';content=$script:encoded}}
            function script:Read-Host {throw 'Unexpected token prompt'}
        } ([Convert]::ToBase64String($expected))
        $actual=Get-AiInstallerBytes
        Assert-Equal ([Convert]::ToBase64String($actual)) ([Convert]::ToBase64String($expected))
        Assert-Equal (& $u {$script:executable}) 'fake-gh'
    }
    Test 'updater token alternative clears headers and disposes its secure input' {
        $u=Import-Module (Join-Path $repo 'src/Update.Helpers.psm1') -Force -PassThru
        & $u {
            $script:secret=ConvertTo-SecureString 'fixture-read-token' -AsPlainText -Force
            function script:Read-Host {param($Prompt,[switch]$AsSecureString)$script:secret}
            function script:Invoke-RestMethod {param($Uri,$Headers,$MaximumRedirection,$TimeoutSec)$script:headers=$Headers;$script:hadAuth=$Headers.Authorization -eq 'Bearer fixture-read-token';@{type='file';encoding='base64';content=[Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes("#requires -Version 7.4`n"+('#'*200)))}}
        }
        $null=Get-AiInstallerBytes -UseToken
        Assert-True (& $u {$script:hadAuth})
        Assert-Equal (& $u {$script:headers.Count}) 0
        Assert-Throws {& $u {$script:secret.AppendChar([char]'x')}} '*disposed*'
    }
    Test 'updater refuses failed or malformed gh downloads before executing installer' {
        $u=Import-Module (Join-Path $repo 'src/Update.Helpers.psm1') -Force -PassThru
        & $u {
            function script:Get-Command {param($Name,$CommandType,$ErrorAction)[pscustomobject]@{Source='fake-gh'}}
            function script:Invoke-AiGhContents {param($Executable)throw 'download denied'}
            function script:Invoke-AiDownloadedInstaller {throw 'Should not execute installer'}
        }
        Assert-Throws {Invoke-AiUpdate '/installed/ai'} '*download denied*'
        & $u {function script:Invoke-AiGhContents {param($Executable)@{type='file';encoding='base64';content=[Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes('not an installer'))}}}
        Assert-Throws {Get-AiInstallerBytes} '*Некорректный установочный файл*'
    }
    Test 'updater executes exact installer check payload and deletes the temporary script' {
        $u=Import-Module (Join-Path $repo 'src/Update.Helpers.psm1') -Force -PassThru
        $before=@(Get-ChildItem ([IO.Path]::GetTempPath()) -Filter 'omnibox-ai-install-*.ps1').Count
        $output=Invoke-AiDownloadedInstaller -Bytes ([IO.File]::ReadAllBytes((Join-Path $repo 'dist/install-omnibox-ai.ps1'))) -CheckOnly
        Assert-True ($output -like "*$version payload OK*")
        Assert-Equal (@(Get-ChildItem ([IO.Path]::GetTempPath()) -Filter 'omnibox-ai-install-*.ps1').Count) $before
        Assert-Throws {Invoke-AiDownloadedInstaller -Bytes ([Text.Encoding]::UTF8.GetBytes("throw 'fixture installer error'"))} '*fixture installer error*'
        Assert-Equal (@(Get-ChildItem ([IO.Path]::GetTempPath()) -Filter 'omnibox-ai-install-*.ps1').Count) $before
    }
    Test 'ai update dispatch uses its installed directory and validates arguments' {
        $m=New-TestModule
        & $m {
            function script:Import-Module {param($Name,[switch]$Force)}
            function script:Invoke-AiUpdate {param($InstallDirectory,[switch]$UseToken,[string]$Version,[switch]$List)$script:updateRoot=$InstallDirectory;$script:updateToken=[bool]$UseToken;$script:updateVersion=$Version;$script:updateList=[bool]$List}
        }
        Invoke-Ai -Arguments @('update','--token') -ConfigPath 'missing'
        Assert-Equal (& $m {$script:updateRoot}) $repo
        Assert-True (& $m {$script:updateToken})
        Invoke-Ai -Arguments @('update','v0.9.4','--token') -ConfigPath 'missing'
        Assert-Equal (& $m {$script:updateVersion}) '0.9.4'
        & $m {$script:calls.Clear()}
        Invoke-Ai -Arguments @('update','--list') -ConfigPath 'missing'
        Assert-True (& $m {$script:updateList -and -not $script:updateToken -and -not $script:updateVersion}) 'List not dispatched'
        Assert-True (-not (& $m {$script:calls.Contains('admin')})) 'Listing versions must not require elevation'
        Assert-Throws {Invoke-Ai -Arguments @('update','--wrong') -ConfigPath 'missing'} '*ai update*'
        Assert-Throws {Invoke-Ai -Arguments @('update','0.9.4','0.9.5') -ConfigPath 'missing'} '*ai update*'
    }
    Test 'releases.json lists installable versions newest first, top entry is VERSION from main' {
        $u=Import-Module (Join-Path $repo 'src/Update.Helpers.psm1') -Force -PassThru
        $releases=@(ConvertTo-AiReleaseList (Get-Content -LiteralPath (Join-Path $repo 'releases.json') -Raw))
        Assert-Equal $releases[0].version $version
        Assert-True ($null -eq $releases[0].commit) 'The newest entry must install from main'
        Assert-True (@($releases|Where-Object {[version]$_.version -lt [version]'0.9.4'}).Count -eq 0) 'Versions before 0.9.4 cannot be installed over native services'
        foreach($r in $releases){Assert-True ($r.note.Length -gt 0) "No note for $($r.version)"}
        # Each older entry must point at a commit whose packaged installer is exactly that version.
        $shallow=(& git -C $repo rev-parse --is-shallow-repository 2>$null) -eq 'true'
        foreach($r in @($releases|Select-Object -Skip 1)){
            $type=& git -C $repo cat-file -t $r.commit 2>$null
            if($type -ne 'commit'){if($env:CI -or -not $shallow){throw "Commit $($r.commit) of $($r.version) is not in the repository history"};Write-Host "SKIP release commit check in shallow clone: $($r.version)";continue}
            $text=(& git -C $repo show "$($r.commit):dist/install-omnibox-ai.ps1") -join "`n"
            Assert-True ($text -match "OmniBox AI $([regex]::Escape($r.version)) payload OK") "Commit $($r.commit) does not contain the $($r.version) installer"
        }
        Assert-Throws {ConvertTo-AiReleaseList '{"schema_version":1,"releases":[{"version":"0.9.5","commit":null},{"version":"0.9.6","commit":"0123456789abcdef0123456789abcdef01234567"}]}'} '*от новой к старой*'
        Assert-Throws {ConvertTo-AiReleaseList '{"schema_version":1,"releases":[{"version":"0.9.6","commit":"main"}]}'} '*коммит*'
        Assert-Throws {ConvertTo-AiReleaseList '{"schema_version":1,"releases":[{"version":"0.9.6","commit":null},{"version":"0.9.5","commit":"../evil"}]}'} '*коммит*'
    }
    Test 'ai update installs the requested version from its release commit under the install lock' {
        $u=Import-Module (Join-Path $repo 'src/Update.Helpers.psm1') -Force -PassThru
        $dir=Join-Path $testRoot 'update-version';$null=New-Item -ItemType Directory -Path $dir -Force
        Set-Content -LiteralPath (Join-Path $dir '.omnibox-ai-install') -Value 'omnibox-ai 0.9.6'
        $installer={param($v)[Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes("#requires -Version 7.4`n# OmniBox AI $v payload OK`n"+('#'*200)))}
        $list='{"schema_version":1,"releases":[{"version":"0.9.6","commit":null,"note":"latest"},{"version":"0.9.5","commit":"bb763637e095b280069d49ff37cd71d86b9df504","note":"transactional"},{"version":"0.9.4","commit":"01727ebeb86c0e1088f1d9ec397f77f4df5ddb6c","note":"old"}]}'
        & $u {
            param($List,$Installer)
            $script:requests=[Collections.Generic.List[string]]::new();$script:list=$List;$script:makeInstaller=$Installer;$script:served=@{}
            function script:Get-Command {param($Name,$CommandType,$ErrorAction)[pscustomobject]@{Source='fake-gh'}}
            function script:Read-Host {throw 'Unexpected token prompt'}
            function script:Invoke-AiGhContents {
                param($Executable,$Path,$Ref)
                $script:requests.Add("$Path@$Ref")
                if($Path -eq 'releases.json'){return @{type='file';encoding='base64';content=[Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($script:list))}}
                $v=if($script:served.Contains($Ref)){$script:served[$Ref]}else{'0.9.6'}
                @{type='file';encoding='base64';content=(& $script:makeInstaller $v)}
            }
            function script:Invoke-AiDownloadedInstaller {
                param([byte[]]$Bytes,$InstallDirectory)
                # Another thread must not be able to take the install lock while the installer runs.
                $job=Start-ThreadJob {$m=[Threading.Mutex]::new($false,'Global\omnibox-ai-install-v1');try{$m.WaitOne(0)}finally{$m.Dispose()}}
                $script:lockFree=Receive-Job $job -Wait -AutoRemoveJob
                $script:ran=[regex]::Match([Text.Encoding]::UTF8.GetString($Bytes),'OmniBox AI (\S+) payload').Groups[1].Value
            }
        } $list $installer
        & $u {$script:served['01727ebeb86c0e1088f1d9ec397f77f4df5ddb6c']='0.9.4'}
        $output=Invoke-AiUpdate -InstallDirectory $dir -Version '0.9.4' 6>&1 | Out-String
        Assert-Equal (& $u {$script:ran}) '0.9.4'
        Assert-True (& $u {$script:requests -contains 'dist/install-omnibox-ai.ps1@01727ebeb86c0e1088f1d9ec397f77f4df5ddb6c'}) 'Release commit not used'
        Assert-True ($output -like '*Откат*0.9.6*0.9.4*' -and $output -like '*нет автоматического отката*') 'Downgrade not announced'
        Assert-Equal (& $u {$script:lockFree}) $false
        Import-Module (Join-Path $repo 'src/Install.Transaction.psm1')
        $again=Enter-AiInstallLock -TimeoutSeconds 1; Exit-AiInstallLock $again
        # Latest comes from main; the newest listed version is also served from main.
        & $u {$script:ran='';$script:requests.Clear()}
        $output=Invoke-AiUpdate -InstallDirectory $dir 6>&1 | Out-String
        Assert-True ((& $u {$script:requests.ToArray()}) -join ',' -eq 'dist/install-omnibox-ai.ps1@main') 'Latest update read something else'
        Assert-True ($output -like '*Переустановка*0.9.6*') 'Reinstall not announced'
        & $u {$script:ran='';$script:served['bb763637e095b280069d49ff37cd71d86b9df504']='0.9.3'}
        Assert-Throws {Invoke-AiUpdate -InstallDirectory $dir -Version '0.9.5'} '*0.9.3 вместо 0.9.5*'
        Assert-Throws {Invoke-AiUpdate -InstallDirectory $dir -Version '0.8.0'} '*недоступна*'
        Assert-Throws {Invoke-AiUpdate -InstallDirectory $dir -Version 'main'} '*x.y.z*'
        Assert-Equal (& $u {$script:ran}) ''
        $listing=Invoke-AiUpdate -InstallDirectory $dir -List 6>&1 | Out-String
        Assert-True ($listing -match '0\.9\.6\s+\(latest\)\s+— последняя, установлена') 'Latest/installed marks missing'
        Assert-True ($listing -match '0\.9\.4\s+\(old\)') 'Note missing'
        Assert-Equal (& $u {$script:ran}) ''
    }
    Test 'install settings compare values: formatting and documented migrations are not differences' {
        $t = Import-Module (Join-Path $repo 'src/Install.Transaction.psm1') -Force -PassThru
        $dir = Join-Path $testRoot 'settings-values'; $null = New-Item -ItemType Directory -Path (Join-Path $dir 'updates/auth') -Force
        $xmlPath = Join-Path $dir 'bonsai.xml'
        Set-Content -LiteralPath $xmlPath -Value '<service><id>bonsai</id><arguments>-m model.gguf  -c 32768</arguments><env name="A" value="1"/></service>'
        Set-Content -LiteralPath ($xmlPath + '.ai-previous') -Value '<service><id>bonsai</id></service>'
        Set-Content -LiteralPath ($xmlPath + '.ai-state.json') -Value '{"pending":false}'
        Set-Content -LiteralPath (Join-Path $dir 'updates/auth/github.dpapi') -Value 'encrypted'
        $config = [ordered]@{ schema_version = 1; default_backend = 'bonsai'; exclusive = $true; backends = [ordered]@{ bonsai = [ordered]@{ type = 'prism'; service = 'bonsai'; service_config = $xmlPath; active_model = 'm' } } }
        $config | ConvertTo-Json -Depth 8 | Set-Content (Join-Path $dir 'config.json')
        $credential = @{ salt = 's'; hash = 'h'; iterations = 210000 }
        @{ schema_version = 1; port = 18090; allowed_hosts = @('192.168.1.77', 'Custom.Example'); credential = $credential; ssh_target = 'u@h'; remote_addresses = @('LocalSubnet') } | ConvertTo-Json -Depth 5 | Set-Content (Join-Path $dir 'panel.json')
        @{ schema_version = 1; enabled = $true; port = 18090; base_url = 'https://llm.example/v1/'; backend = 'bonsai'; require_key = $true; key_hash = ('a' * 64) } | ConvertTo-Json | Set-Content (Join-Path $dir 'gateway.json')
        $before = Get-AiInstallSettings $dir
        $expected = Get-AiExpectedInstallSettings $before @{ 'allowed_hosts.extra' = @('localhost') }
        # Rewrite everything as the installer would: other key order and formatting, added defaults and runtime_path.
        $config.backends.bonsai.runtime_path = $xmlPath + '.runtime.json'
        $reordered = [ordered]@{ backends = $config.backends; exclusive = $true; default_backend = 'bonsai'; schema_version = 1 }
        [IO.File]::WriteAllText((Join-Path $dir 'config.json'), ($reordered | ConvertTo-Json -Depth 8 -Compress))
        @{ trusted_proxy_addresses = @(); public_origin = ''; remote_addresses = @('LocalSubnet'); ssh_target = 'u@h'; credential = @{ iterations = 210000; hash = 'h'; salt = 's' }; allowed_hosts = @('custom.example', '192.168.1.77', 'localhost'); port = 18090; schema_version = 1 } | ConvertTo-Json -Depth 5 | Set-Content (Join-Path $dir 'panel.json')
        @{ key_hash = ('a' * 64); require_key = $true; backend = 'bonsai'; base_url = 'https://llm.example/v1'; port = 18090; enabled = $true; schema_version = 1 } | ConvertTo-Json | Set-Content (Join-Path $dir 'gateway.json')
        Set-Content -LiteralPath $xmlPath -Value "<?xml version=`"1.0`"?>`n<service>`n  <id>bonsai</id>`n  <arguments>-m model.gguf -c 32768</arguments>`n  <env value=`"1`" name=`"A`" />`n</service>"
        Assert-Equal (@(Compare-AiInstallSettings $expected (Get-AiInstallSettings $dir)).Count) 0
        Assert-AiInstallSettings $expected $dir
    }
    Test 'install settings detect lost hosts, changed secrets, model settings and touched history' {
        $dir = Join-Path $testRoot 'settings-values'
        $base = Get-AiInstallSettings $dir
        $expected = Get-AiExpectedInstallSettings $base
        $cases = @(
            @{ file = 'panel.json'; change = { param($v) $v.allowed_hosts = @('192.168.1.77') }; pattern = '*allowed_hosts*custom.example*' },
            @{ file = 'panel.json'; change = { param($v) $v.credential.hash = 'other' }; pattern = '*panel.credential*' },
            @{ file = 'gateway.json'; change = { param($v) $v.key_hash = ('b' * 64) }; pattern = '*gateway.key_hash*' },
            @{ file = 'gateway.json'; change = { param($v) $v.require_key = $false }; pattern = '*gateway.require_key*' },
            @{ file = 'config.json'; change = { param($v) $v.backends.bonsai.active_model = 'other' }; pattern = '*config.backends*' }
        )
        foreach ($case in $cases) {
            $path = Join-Path $dir $case.file; $original = [IO.File]::ReadAllBytes($path)
            try {
                $value = Get-Content $path -Raw | ConvertFrom-Json -AsHashtable; & $case.change $value
                $value | ConvertTo-Json -Depth 8 | Set-Content $path
                Assert-Throws { Assert-AiInstallSettings $expected $dir } $case.pattern
            } finally { [IO.File]::WriteAllBytes($path, $original) }
        }
        $xmlPath = Join-Path $dir 'bonsai.xml'
        foreach ($path in @($xmlPath, ($xmlPath + '.ai-state.json'), (Join-Path $dir 'updates/auth/github.dpapi'))) {
            $original = [IO.File]::ReadAllBytes($path)
            try {
                if ($path -eq $xmlPath) { Set-Content $path '<service><id>bonsai</id><arguments>-m model.gguf -c 65536</arguments><env name="A" value="1"/></service>' }
                else { [IO.File]::AppendAllText($path, ' ') }
                Assert-Throws { Assert-AiInstallSettings $expected $dir } '*не пройдена*'
            } finally { [IO.File]::WriteAllBytes($path, $original) }
        }
        # Settings the user asked to change are expected to change.
        $port = Get-AiExpectedInstallSettings $base @{ 'panel.port' = 18091 }
        $panel = Get-Content (Join-Path $dir 'panel.json') -Raw | ConvertFrom-Json -AsHashtable; $panel.port = 18091
        $original = [IO.File]::ReadAllBytes((Join-Path $dir 'panel.json'))
        try { $panel | ConvertTo-Json -Depth 8 | Set-Content (Join-Path $dir 'panel.json'); Assert-AiInstallSettings $port $dir }
        finally { [IO.File]::WriteAllBytes((Join-Path $dir 'panel.json'), $original) }
    }
    Test 'install lock refuses a second installer and is re-entrant for the owning thread' {
        $t = Import-Module (Join-Path $repo 'src/Install.Transaction.psm1') -Force -PassThru
        $exe = Join-Path $PSHOME $(if ($IsWindows) { 'pwsh.exe' } else { 'pwsh' })
        $ready = Join-Path $testRoot 'lock-ready'
        $holder = Start-Process -FilePath $exe -ArgumentList @('-NoLogo', '-NoProfile', '-Command', "`$m=[Threading.Mutex]::new(`$false,'Global\omnibox-ai-install-v1');`$null=`$m.WaitOne();Set-Content '$ready' x;Start-Sleep 30") -PassThru
        try {
            $deadline = [DateTime]::UtcNow.AddSeconds(30); while (-not (Test-Path $ready) -and [DateTime]::UtcNow -lt $deadline) { Start-Sleep -Milliseconds 100 }
            Assert-True (Test-Path $ready) 'Lock holder did not start'
            Assert-Throws { Enter-AiInstallLock -TimeoutSeconds 1 } '*Уже выполняется другая установка*'
        } finally { $holder.Kill(); $holder.WaitForExit() }
        $lock = Enter-AiInstallLock -TimeoutSeconds 5
        try {
            Enter-AiControlLock $lock -TimeoutSeconds 1
            # The web worker owns both locks and runs the installer in the same thread.
            $nested = Enter-AiInstallLock -TimeoutSeconds 1; Enter-AiControlLock $nested -TimeoutSeconds 1; Exit-AiInstallLock $nested
            $m = New-TestModule; & $m { function script:Assert-AiAdministrator {} }
            Assert-Equal (& $m { Invoke-AiLocked { 'control inside install' } }) 'control inside install'
        } finally { Exit-AiInstallLock $lock }
        $again = Enter-AiInstallLock -TimeoutSeconds 1; Exit-AiInstallLock $again
    }
    Test 'VERSION is the single source of the application version' {
        Assert-True ($version -match '^\d+\.\d+\.\d+$') 'VERSION must be x.y.z'
        $sources = @($manifest.Files + $manifest.InstallerFiles + @('scripts/setup.template.ps1', 'scripts/build-setup.ps1') | Where-Object { $_ -match '\.(ps1|psm1|psd1|cs|js|html)$' })
        foreach ($file in $sources) {
            $text = Get-Content -LiteralPath (Join-Path $repo $file) -Raw
            Assert-True ($text -notmatch [regex]::Escape("'$version'")) "Hard-coded version in $file"
            Assert-True ($text -notmatch 'OmniBox AI \d+\.\d+\.\d+|omnibox-ai \d+\.\d+\.\d+') "Hard-coded version text in $file"
        }
        Assert-True ((Get-Content -LiteralPath (Join-Path $repo 'CHANGELOG.md') -Raw) -match ('(?m)^## ' + [regex]::Escape($version) + ' ')) 'CHANGELOG has no entry for VERSION'
        Assert-True ((Get-Content -LiteralPath (Join-Path $repo 'README.md') -Raw).Contains("**$version**")) 'README states another version'
    }
    Test 'self-contained package matches every current source file' {
        $setup = Join-Path $repo 'dist/install-omnibox-ai.ps1'
        $text = Get-Content $setup -Raw
        $encoded = [regex]::Match($text, '(?m)^\$payload = ''([^'']+)''').Groups[1].Value
        $sha = [regex]::Match($text, '(?m)^\$expectedSha = ''([a-f0-9]{64})''').Groups[1].Value
        Assert-True ($encoded.Length -gt 0)
        # The generated wrapper is exactly the current template with this version and payload.
        $template = (Get-Content -LiteralPath (Join-Path $repo 'scripts/setup.template.ps1') -Raw) -replace "`r?`n", "`r`n"
        $expected = $template.Replace('__VERSION__', $version).Replace('__PAYLOAD_SHA256__', $sha).Replace('__PAYLOAD_BASE64__', $encoded)
        Assert-True (($text -replace "`r?`n", "`r`n") -ceq $expected) 'dist was not built from the current template/VERSION: run scripts/build-setup.ps1'
        $memory = [IO.MemoryStream]::new([Convert]::FromBase64String($encoded))
        $archive = [IO.Compression.ZipArchive]::new($memory, [IO.Compression.ZipArchiveMode]::Read)
        try {
            $names = @($archive.Entries | ForEach-Object FullName)
            Assert-Equal ($names -join ',') ((@($manifest.Files) + @($manifest.InstallerFiles)) -join ',')
            foreach ($entry in $archive.Entries) {
                Assert-True ($entry.FullName -notmatch '(^/|\.\.)')
                $reader = [IO.StreamReader]::new($entry.Open(), [Text.UTF8Encoding]::new($false), $false)
                try { $embedded = $reader.ReadToEnd() } finally { $reader.Dispose() }
                $current = (Get-Content -LiteralPath (Join-Path $repo $entry.FullName) -Raw).Replace("`r`n", "`n")
                if ([IO.Path]::GetExtension($entry.FullName) -in $manifest.CrlfExtensions) { $current = $current.Replace("`n", "`r`n") }
                Assert-True ($embedded -ceq $current) "Package differs from source: $($entry.FullName). Run scripts/build-setup.ps1"
            }
        } finally { $archive.Dispose(); $memory.Dispose() }
        $exe = Join-Path $PSHOME $(if ($IsWindows) { 'pwsh.exe' } else { 'pwsh' })
        $output = & $exe -NoLogo -NoProfile -File $setup -CheckOnly
        Assert-Equal $global:LASTEXITCODE 0
        Assert-True ($output -like "*$version payload OK*")
    }
    Test 'package build is independent of checkout line endings' {
        $copy = Join-Path $testRoot 'eol-checkout'
        foreach ($file in @($manifest.Files) + @($manifest.InstallerFiles) + @('scripts/build-setup.ps1', 'scripts/setup.template.ps1')) {
            $target = Join-Path $copy $file
            $null = New-Item -ItemType Directory -Path (Split-Path $target -Parent) -Force
            # Opposite line endings from the packaged form must produce the same payload content.
            $content = (Get-Content -LiteralPath (Join-Path $repo $file) -Raw).Replace("`r`n", "`n")
            if ([IO.Path]::GetExtension($file) -notin $manifest.CrlfExtensions) { $content = $content.Replace("`n", "`r`n") }
            [IO.File]::WriteAllText($target, $content, [Text.UTF8Encoding]::new($false))
        }
        $exe = Join-Path $PSHOME $(if ($IsWindows) { 'pwsh.exe' } else { 'pwsh' })
        $built = Join-Path $copy 'dist/install-omnibox-ai.ps1'
        $null = & $exe -NoLogo -NoProfile -File (Join-Path $copy 'scripts/build-setup.ps1') -OutputPath $built
        Assert-Equal $global:LASTEXITCODE 0
        $pattern = '(?m)^\$payload = ''([^'']+)'''
        $read = {
            param([string]$Setup)
            $bytes = [Convert]::FromBase64String([regex]::Match((Get-Content -LiteralPath $Setup -Raw), $pattern).Groups[1].Value)
            $zip = [IO.Compression.ZipArchive]::new([IO.MemoryStream]::new($bytes))
            try { @($zip.Entries | ForEach-Object { $r = [IO.StreamReader]::new($_.Open()); try { $_.FullName + '=' + $r.ReadToEnd() } finally { $r.Dispose() } }) -join "`0" }
            finally { $zip.Dispose() }
        }
        Assert-True ((& $read $built) -ceq (& $read (Join-Path $repo 'dist/install-omnibox-ai.ps1'))) 'Payload content depends on checkout line endings'
    }
    Test 'self-contained package corruption is rejected before installation' {
        $text = Get-Content (Join-Path $repo 'dist/install-omnibox-ai.ps1') -Raw
        $encoded = [regex]::Match($text, '(?m)^\$payload = ''([^'']+)''').Groups[1].Value
        $changed = $(if ($encoded[0] -eq 'A') { 'B' } else { 'A' }) + $encoded.Substring(1)
        $tampered = Join-Path $testRoot 'tampered-setup.ps1'
        $text.Replace($encoded, $changed) | Set-Content -LiteralPath $tampered -Encoding utf8BOM
        $exe = Join-Path $PSHOME $(if ($IsWindows) { 'pwsh.exe' } else { 'pwsh' })
        $null = & $exe -NoLogo -NoProfile -File $tampered -CheckOnly 2>$null
        Assert-Equal $global:LASTEXITCODE 1
        $global:LASTEXITCODE = 0
    }
} finally {
    Remove-Module OmniBox.Ai -ErrorAction SilentlyContinue
    # Only this test's generated, validated GUID directory is removed.
    if ((Split-Path $testRoot -Leaf) -match '^omnibox-ai-tests-[a-f0-9]{32}$') {
        Remove-Item -LiteralPath $testRoot -Recurse -Force
    }
}
Write-Host "Tests: $script:passed passed, $script:failed failed"
if ($script:failed) { exit 1 }
exit 0
