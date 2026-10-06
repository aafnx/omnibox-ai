#requires -Version 7.4
# Self-contained tests: no Pester, no Windows services, GPU, network, or model required.
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$repo = Split-Path $PSScriptRoot -Parent
$modulePath = Join-Path $repo 'src/OmniBox.Ai.psm1'
$example = Join-Path $repo 'config.example.json'
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
        Assert-Equal (Invoke-Ai -Arguments @('version') -ConfigPath 'missing') '0.5.0'
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
    Test 'exclusive false leaves other backend running' {
        $m = New-TestModule
        $cfg = Get-Content $example -Raw | ConvertFrom-Json -AsHashtable
        $cfg.exclusive = $false
        $path = Save-TestConfig $cfg
        Invoke-Ai -Arguments @('start') -ConfigPath $path
        Assert-True (-not ((Get-Calls $m) -like 'stop:*'))
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
        Assert-Equal (Get-Content $f.bonsai -Raw) $original
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
    Test 'documented one-line bootstrap parses and removes its temporary installer' {
        $doc = Get-Content (Join-Path $repo 'docs/install.md') -Raw
        $line = [regex]::Match($doc, '(?m)^& \{ \$ErrorActionPreference[^\r\n]+').Value
        Assert-True ($line.Length -gt 0)
        $bootstrap = [scriptblock]::Create($line)
        & {
            $script:seenHeaders = $null
            function Read-Host { param($Prompt, [switch]$AsSecureString) ConvertTo-SecureString 'not-a-real-token' -AsPlainText -Force }
            function Invoke-RestMethod {
                param($Uri, $Headers, $MaximumRedirection, $TimeoutSec)
                Assert-Equal $Uri 'https://api.github.com/repos/aafnx/omnibox-ai/contents/dist/install-omnibox-ai.ps1?ref=main'
                Assert-Equal $MaximumRedirection 0
                Assert-True ($Headers.Authorization.StartsWith('Bearer '))
                $script:seenHeaders = $Headers
                [pscustomobject]@{ type = 'file'; encoding = 'base64'; content = [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes('Write-Output "mock installer executed"')) }
            }
            $oldTemp = $env:TEMP; $env:TEMP = $testRoot
            try {
                Assert-Equal (& $bootstrap) 'mock installer executed'
                Assert-Equal $script:seenHeaders.Count 0
                Assert-Equal @(Get-ChildItem $testRoot -Filter 'omnibox-ai-install-*.ps1').Count 0
            } finally { $env:TEMP = $oldTemp }
        }
    }
    Test 'bootstrap download failure leaves no token headers or temporary installer' {
        & {
            $global:omniboxTestHeaders = $null
            function Read-Host { param($Prompt, [switch]$AsSecureString) ConvertTo-SecureString 'not-a-real-token' -AsPlainText -Force }
            function Invoke-RestMethod { param($Uri, $Headers, $MaximumRedirection, $TimeoutSec) $global:omniboxTestHeaders = $Headers; throw 'mock GitHub access denied' }
            $oldTemp = $env:TEMP; $env:TEMP = $testRoot
            try {
                Assert-Throws { & (Join-Path $repo 'scripts/get-install.ps1') } '*access denied*'
                Assert-Equal $global:omniboxTestHeaders.Count 0
                Assert-Equal @(Get-ChildItem $testRoot -Filter 'omnibox-ai-install-*.ps1').Count 0
            } finally { $env:TEMP = $oldTemp; Remove-Variable omniboxTestHeaders -Scope Global }
        }
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
            function script:Invoke-AiProbe { param($Backend,$Path) if($Path -eq '/api/tags') { [pscustomobject]@{models=@([pscustomobject]@{name='test:latest'})} } else { [pscustomobject]@{version='0.35.1'} } }
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
    Test 'self-contained package matches every current source file' {
        $setup = Join-Path $repo 'dist/install-omnibox-ai.ps1'
        $text = Get-Content $setup -Raw
        $encoded = [regex]::Match($text, '(?m)^\$payload = ''([^'']+)''').Groups[1].Value
        Assert-True ($encoded.Length -gt 0)
        $memory = [IO.MemoryStream]::new([Convert]::FromBase64String($encoded))
        $archive = [IO.Compression.ZipArchive]::new($memory, [IO.Compression.ZipArchiveMode]::Read)
        try {
            Assert-Equal $archive.Entries.Count 21
            foreach ($entry in $archive.Entries) {
                Assert-True ($entry.FullName -notmatch '(^/|\.\.)')
                $reader = [IO.StreamReader]::new($entry.Open(), [Text.Encoding]::UTF8)
                try { $embedded = $reader.ReadToEnd() } finally { $reader.Dispose() }
                Assert-Equal $embedded (Get-Content -LiteralPath (Join-Path $repo $entry.FullName) -Raw)
            }
        } finally { $archive.Dispose(); $memory.Dispose() }
        $exe = Join-Path $PSHOME $(if ($IsWindows) { 'pwsh.exe' } else { 'pwsh' })
        $output = & $exe -NoLogo -NoProfile -File $setup -CheckOnly
        Assert-Equal $global:LASTEXITCODE 0
        Assert-True ($output -like '*payload OK*')
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
