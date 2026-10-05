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
        Assert-Equal (Invoke-Ai -Arguments @('version') -ConfigPath 'missing') '0.1.0'
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
