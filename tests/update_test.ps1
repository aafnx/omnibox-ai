#requires -Version 7.4
$ErrorActionPreference='Stop'
Set-StrictMode -Version Latest
$root=Split-Path $PSScriptRoot -Parent
$version=(Get-Content -LiteralPath (Join-Path $root 'VERSION') -Raw).Trim()
$m=Import-Module (Join-Path $root 'src/Web.Update.psm1') -Force -PassThru
$dir=Join-Path ([IO.Path]::GetTempPath()) ('omnibox-update-tests-'+[Guid]::NewGuid().ToString('N'))
$null=New-Item -ItemType Directory -Path $dir
$script:passed=0
function Assert($condition,[string]$message){if(-not $condition){throw $message}}
function Test([string]$name,[scriptblock]$run){& $run;$script:passed++;Write-Host "PASS $name"}
try{
    $bytes=[IO.File]::ReadAllBytes((Join-Path $root 'dist/install-omnibox-ai.ps1'))
    & $m {param($b)
        $script:package=@{type='file';encoding='base64';content=[Convert]::ToBase64String($b)}
        $script:sentToken='';$script:saved=''
        function script:Get-AiUpdateGh {'test-gh'}
        function script:Read-AiUpdateCredential {param($Root)'delegated-secret'}
        function script:Invoke-AiUpdateGh {param($Executable,$Arguments,$ConfigDirectory,$Token)$script:sentToken=$Token;if($Arguments[0] -eq 'auth'){return 'delegated-secret'};return ($script:package|ConvertTo-Json -Compress)}
        function script:Save-AiUpdateCredential {param($Root,$Token)$script:saved=$Token}
    } $bytes
    Test 'web package uses delegated gh credential and validates version/payload without execution' {
        $p=Get-AiWebInstaller $dir
        Assert ($p.version -eq $version) 'Version missing'
        Assert ((& $m {$script:sentToken}) -eq 'delegated-secret') 'Credential not delegated'
        Assert ($p.bytes.Length -eq $bytes.Length) 'Package differs'
        Assert ($p.sha256 -eq [Convert]::ToHexString([Security.Cryptography.SHA256]::HashData($bytes)).ToLowerInvariant()) 'File hash differs'
    }
    Test 'one-use token takes precedence without persistence' {
        $null=Get-AiWebInstaller $dir 'one-use-secret'
        Assert ((& $m {$script:sentToken}) -eq 'one-use-secret') 'Wrong token'
        Assert ((& $m {$script:saved}) -eq '') 'Token persisted'
    }
    Test 'malformed package rejected with no secret in error' {
        & $m {$script:package.encoding='utf8'}
        $message='';try{Get-AiWebInstaller $dir 'hidden-secret'}catch{$message=$_.Exception.Message}
        Assert ($message -like '*Не удалось получить*' -and $message -notlike '*secret*') 'Unsafe error'
        & $m {$script:package.encoding='base64'}
    }
    Test 'corrupt embedded payload is rejected before execution' {
        & $m {param($b)
            $text=[Text.Encoding]::UTF8.GetString($b);$sha=[regex]::Match($text,'(?m)^\$expectedSha = ''([^'']+)''').Groups[1].Value
            $script:package.content=[Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($text.Replace($sha,('0'*64))))
        } $bytes
        $failed=$false;try{Get-AiWebInstaller $dir}catch{$failed=$true}
        Assert $failed 'Corrupt payload accepted'
    }
    Test 'update API refuses unknown keys, invalid token and installation without a checked package' {
        $state=New-AiWebUpdateState $dir
        foreach($body in @(@{action='exec'},@{action='check';url='https://evil'},@{action='token';token="bad`nsecret"},@{action='apply'})){
            $failed=$false;try{Start-AiWebUpdateAction $state $body @{}}catch{$failed=$true};Assert $failed 'Invalid request accepted'
        }
    }
    Test 'cancellation cannot interrupt installation and status does not reveal private state' {
        $state=New-AiWebUpdateState $dir;$state.shared.phase='installing';$state.shared.file='private-file';$state.shared.sha256='private-hash'
        $view=Get-AiWebUpdateStatus $state
        Assert (-not $view.Contains('file') -and -not $view.Contains('sha256') -and -not $view.Contains('token')) 'Private state exposed'
        $failed=$false;try{Start-AiWebUpdateAction $state @{action='cancel'} @{}}catch{$failed=$true};Assert $failed 'Installation cancelled'
    }
    Test 'busy model operation prevents installer handoff' {
        $state=New-AiWebUpdateState $dir;$state.shared.phase='ready'
        $busy=@{x=@{job=[pscustomobject]@{State='Running'}}}
        $failed=$false;try{Start-AiWebUpdateAction $state @{action='apply'} $busy}catch{$failed=$_.Exception.Message -like '*Дождитесь*'}
        Assert $failed 'Busy operation not protected'
    }
    Test 'worker is independent and does not load or overwrite its own runtime EXE' {
        $module=Get-Content (Join-Path $root 'src/Web.Update.psm1') -Raw
        $worker=Get-Content (Join-Path $root 'scripts/update-worker.ps1') -Raw
        Assert ($module.Contains("Join-Path `$stage 'src'") -and $module.Contains('New-Service -Name $name')) 'No isolated SCM worker'
        Assert ($worker.Contains('-NonInteractive -NoProfileUpdate') -and $worker.Contains("Global\omnibox-ai-control-v1")) 'Installer context/guard wrong'
        Assert ($worker.Contains('Remove-Service -Name $ServiceName') -and $worker.Contains("Write-UpdateResult 'failed'")) 'Cleanup/recovery missing'
    }
    Test 'installer imports individual modules and never imports an executable updater' {
        $installer=Get-Content (Join-Path $root 'install.ps1') -Raw
        foreach($line in $installer.Split("`n")|Where-Object {$_ -match '^\s*(?:\$aiModule = )?Import-Module'}){
            Assert ($line -notmatch "\.psm1',") "Invalid module Join-Path: $line"
            Assert ($line -notmatch 'update-worker|get-install|build-service') "Executable imported: $line"
        }
    }
    Test 'native updater stages its own EXE and fixed profile; modified installers never launch' {
        $fixture=Join-Path $dir 'installed';$null=New-Item -ItemType Directory -Path (Join-Path $fixture 'src'),(Join-Path $fixture 'scripts') -Force
        Copy-Item (Join-Path $root 'service.ps1') $fixture
        Copy-Item (Join-Path $root 'src/Service.Install.psm1') (Join-Path $fixture 'src')
        Copy-Item (Join-Path $root 'scripts/update-worker.ps1') (Join-Path $fixture 'scripts')
        Set-Content (Join-Path $fixture '.omnibox-ai-install') 'omnibox-ai 0.9.4'
        [IO.File]::WriteAllBytes((Join-Path $fixture 'src/Service.Host.exe'),[byte[]]@(1,2,3))
        $installer=Join-Path $fixture 'installer.ps1';[IO.File]::WriteAllBytes($installer,$bytes)
        $sha=[Convert]::ToHexString([Security.Cryptography.SHA256]::HashData($bytes)).ToLowerInvariant()
        & $m {
            Set-Variable IsWindows -Scope Script -Value $true
            $script:serviceCalls=[Collections.Generic.List[string]]::new();$script:failStart=$false
            function script:Get-Service {param($Name,$ErrorAction)return $null}
            function script:New-Service {param($Name,$BinaryPathName,$StartupType)$script:serviceCalls.Add('new:'+ $BinaryPathName)}
            function script:Start-Service {param($Name,$ErrorAction)if($script:failStart){throw 'Fixture failure'};$script:serviceCalls.Add('start:'+ $Name)}
            function script:Remove-Service {param($Name,$ErrorAction)$script:serviceCalls.Add('remove:'+ $Name)}
        }
        try{
            & $m {param($r,$f,$sha)Start-AiWebUpdateWorker $r $f $sha '9.9.9'} $fixture $installer $sha
            $stage=Get-ChildItem (Join-Path $fixture 'updates') -Directory|Where-Object Name -like 'run-*'
            Assert ((Get-Content (Join-Path $stage.FullName 'update.xml') -Raw) -match 'update-worker.ps1') 'Wrong worker profile'
            Assert ([IO.File]::ReadAllBytes((Join-Path $stage.FullName 'src/Service.Host.exe')).Length -eq 3) 'EXE not staged'
            $calls=@(& $m {$script:serviceCalls.ToArray()})
            Assert ($calls.Count -eq 2 -and $calls[0] -match 'updates[\\/]run-[a-f0-9]{32}[\\/]src[\\/]Service\.Host\.exe') 'SCM worker is not independent'
            Assert ((Get-Content (Join-Path $fixture 'updates/result.json') -Raw|ConvertFrom-Json).phase -eq 'installing') 'No durable guard'
            $failed=$false;try{& $m {param($r,$f)Start-AiWebUpdateWorker $r $f ('0'*64) '9.9.9'} $fixture $installer}catch{$failed=$true}
            Assert $failed 'Modified package accepted'
            Assert ((@(& $m {$script:serviceCalls.ToArray()})).Count -eq 2) 'Modified package launched a service'
            & $m {$script:failStart=$true}
            $failed=$false;try{& $m {param($r,$f,$sha)Start-AiWebUpdateWorker $r $f $sha '9.9.9'} $fixture $installer $sha}catch{$failed=$true}
            Assert $failed 'Failed SCM launch accepted'
            Assert (-not (Test-Path (Join-Path $fixture 'updates/result.json'))) 'Failed launch leaves permanent busy state'
        }finally{& $m {Remove-Variable IsWindows -Scope Script;Remove-Item Function:script:Get-Service,Function:script:New-Service,Function:script:Start-Service,Function:script:Remove-Service}}
    }
    Test 'active inference blocks update handoff and a failed handoff releases the guard' {
        if(-not ('OmniBox.InferenceStats' -as [type])){Add-Type -Path (Join-Path $root 'src/Inference.Stats.cs')}
        $state=New-AiWebUpdateState $dir;$state.shared.phase='ready'
        Assert ([OmniBox.InferenceStats]::BeginControl($false)) 'Cannot acquire fixture guard'
        try{
            $failed=$false;try{Start-AiWebUpdateAction $state @{action='apply'} @{}}catch{$failed=$_.Exception.Message -like '*Модель занята*'}
            Assert $failed 'Active control ignored'
        }finally{[OmniBox.InferenceStats]::EndControl()}
        $cancel=[Threading.CancellationTokenSource]::new()
        $record=[OmniBox.InferenceStats]::Begin('bonsai','model','/v1/chat/completions',32768,'',$cancel)
        Assert ($null -ne $record) 'Cannot create active generation'
        try{
            $failed=$false;try{Start-AiWebUpdateAction $state @{action='apply'} @{}}catch{$failed=$_.Exception.Message -like '*Модель занята*'}
            Assert $failed 'Active inference ignored'
            Assert (-not $cancel.IsCancellationRequested) 'Updating cancelled generation'
        }finally{[OmniBox.InferenceStats]::Finish($record,200,$true);$cancel.Dispose()}
        & $m {function script:Start-AiWebUpdateWorker {throw 'Fixture handoff failure'}}
        $failed=$false;try{Start-AiWebUpdateAction $state @{action='apply'} @{}}catch{$failed=$true}
        Assert $failed 'Handoff failure ignored'
        Assert ([OmniBox.InferenceStats]::BeginControl($false)) 'Guard leaked after failed handoff'
        [OmniBox.InferenceStats]::EndControl()
    }
    Import-Module (Join-Path $root 'src/Service.Install.psm1') -Force
    Test 'real worker script records success and restores application files after a failed installer' {
        $global:fixtureWorkerCalls=[Collections.Generic.List[string]]::new()
        function Get-Service {param($Name,$ErrorAction)[pscustomobject]@{Status='Running'}}
        function global:Get-CimInstance {param($ClassName,$Filter)if($Filter -eq "Name='omnibox-ai'"){return $global:fixtureWorkerService.PSObject.Copy()}}
        function Set-Service {[CmdletBinding()]param($Name,$StartupType)$global:fixtureWorkerCalls.Add('set:'+ $Name)}
        function global:Invoke-CimMethod {
            [CmdletBinding()]param($InputObject,$MethodName,$Arguments,$OperationTimeoutSec)
            if($MethodName -ne 'Change'){throw 'Unexpected CIM method'}
            $global:fixtureWorkerService.PathName=$Arguments.PathName
            if($Arguments.ContainsKey('StartMode')){$global:fixtureWorkerService.StartMode=if($Arguments.StartMode -eq 'Automatic'){'Auto'}else{$Arguments.StartMode}}
            $global:fixtureWorkerCalls.Add('change:'+ $InputObject.Name)
            [pscustomobject]@{ReturnValue=0}
        }
        function Stop-Service {param($Name,$ErrorAction)$global:fixtureWorkerCalls.Add('stop:'+ $Name)}
        function Start-Service {param($Name,$ErrorAction)$global:fixtureWorkerCalls.Add('start:'+ $Name)}
        function Remove-Service {param($Name,$ErrorAction)$global:fixtureWorkerCalls.Add('remove:'+ $Name)}
        try{
            foreach($scenario in @('success','failure','rolled-back','busy')){
                $failure=$scenario -ne 'success'
                $installed=Join-Path $dir ('worker-'+$scenario);$stage=Join-Path $installed 'updates/run-fixture'
                $null=New-Item -ItemType Directory -Path $stage,(Join-Path $installed 'src'),(Join-Path $installed 'models'),(Join-Path $installed 'updates/auth') -Force
                Copy-Item (Join-Path $root 'scripts/update-worker.ps1') $stage
                Copy-Item (Join-Path $root 'src/Service.Install.psm1') (Join-Path $installed 'src')
                Set-Content (Join-Path $installed 'ai.ps1') 'previous app'
                Set-Content (Join-Path $installed '.omnibox-ai-install') 'omnibox-ai 0.9.4'
                Set-Content (Join-Path $installed 'config.json') '{"backends":{}}'
                Set-Content (Join-Path $installed 'panel.json') '{"port":18090,"public_origin":"https://llm.fnx.red","credential":"fixture"}'
                Set-Content (Join-Path $installed 'gateway.json') '{"key_hash":"fixture-key-hash"}'
                Set-Content (Join-Path $installed 'models/model.gguf') 'untouched weights'
                Set-Content (Join-Path $installed 'updates/auth/github.dpapi') 'untouched encrypted credential'
                $originalCommand=New-AiLegacyServiceCommand ([Environment]::ProcessPath) $installed (Join-Path $installed 'panel-service.xml')
                $global:fixtureWorkerService=[pscustomobject]@{Name='omnibox-ai';ProcessId=0;State='Stopped';PathName=$originalCommand;StartMode='Auto'}
                $installer='param($InstallDirectory,[switch]$NonInteractive,[switch]$NoProfileUpdate); if(-not $NonInteractive -or -not $NoProfileUpdate){throw "Bad invocation"}; Set-Content (Join-Path $InstallDirectory "ai.ps1") "updated app"; Set-AiServiceCommand -Name omnibox-ai -Command (New-AiNativeServiceCommand ([Environment]::ProcessPath) $InstallDirectory (Join-Path $InstallDirectory "panel-service.xml")); '
                if($scenario -eq 'failure'){$installer+='throw "fixture installer failure secret must not appear in status"'}
                # A transactional installer restores everything itself and reports it; the worker must not restore again.
                if($scenario -eq 'rolled-back'){$installer='param($InstallDirectory,[switch]$NonInteractive,[switch]$NoProfileUpdate); $global:OmniBoxInstallResult=@{status="rolled-back"}; throw "fixture rollback secret must not appear in status"'}
                if($scenario -eq 'busy'){$installer='throw "installer must not run while another installation holds the lock"'}
                Set-Content (Join-Path $stage 'installer.ps1') $installer
                $holder=$null
                if($scenario -eq 'busy'){
                    $ready=Join-Path $installed 'lock-ready'
                    $holder=Start-Process -FilePath ([Environment]::ProcessPath) -ArgumentList @('-NoLogo','-NoProfile','-Command',"`$m=[Threading.Mutex]::new(`$false,'Global\omnibox-ai-install-v1');`$null=`$m.WaitOne();Set-Content '$ready' x;Start-Sleep 60") -PassThru
                    $deadline=[DateTime]::UtcNow.AddSeconds(30);while(-not(Test-Path $ready) -and [DateTime]::UtcNow -lt $deadline){Start-Sleep -Milliseconds 100}
                }
                $changesBefore=@($global:fixtureWorkerCalls|Where-Object {$_ -like 'change:*'}).Count
                try{& (Join-Path $stage 'update-worker.ps1') -Root $installed -ServiceName 'omnibox-ai-update-fixture' -Version '9.9.9'}
                finally{if($null -ne $holder){$holder.Kill();$holder.WaitForExit()}}
                $changes=@($global:fixtureWorkerCalls|Where-Object {$_ -like 'change:*'}).Count-$changesBefore
                $result=Get-Content (Join-Path $installed 'updates/result.json') -Raw|ConvertFrom-Json
                Assert ($result.phase -eq $(if($failure){'failed'}else{'done'})) 'Wrong worker outcome'
                Assert ((Get-Content (Join-Path $installed 'ai.ps1')).Trim() -eq $(if($failure){'previous app'}else{'updated app'})) 'Wrong restoration'
                if($failure){Assert ($global:fixtureWorkerService.PathName -eq $originalCommand) 'SCM command was not restored'}
                if($scenario -in @('rolled-back','busy')){Assert ($changes -eq 0) "Worker changed services after $scenario"}
                if($scenario -eq 'rolled-back'){Assert ($result.message -like '*восстановлены установщиком*') 'Installer rollback not reported'}
                if($scenario -eq 'busy'){Assert ($result.message -like '*Уже выполняется другая установка*') 'Busy installation not reported'}
                Assert ($result.message -notlike '*secret*') 'Raw error leaked'
                Assert ((Get-Content (Join-Path $installed 'gateway.json')) -like '*fixture-key-hash*') 'API key changed'
                Assert ((Get-Content (Join-Path $installed 'models/model.gguf')).Trim() -eq 'untouched weights') 'Model weights touched'
                Assert (-not (Test-Path (Join-Path $stage 'previous/models'))) 'Weights backed up with app'
                Assert ((Get-Content (Join-Path $installed 'updates/auth/github.dpapi')).Trim() -eq 'untouched encrypted credential') 'Credential changed'
            }
            Assert ($global:fixtureWorkerCalls.Contains('start:omnibox-ai')) 'Recovery never started panel'
            Assert ($global:fixtureWorkerCalls.Contains('remove:omnibox-ai-update-fixture')) 'Worker service was not removed'
        }finally{
            Remove-Item Function:Get-Service,Function:global:Get-CimInstance,Function:Stop-Service,Function:Start-Service,Function:Remove-Service,Function:Set-Service,Function:global:Invoke-CimMethod
            Remove-Variable fixtureWorkerCalls,fixtureWorkerService -Scope Global -ErrorAction SilentlyContinue
        }
    }
    # Real gh process, with test executable only: no network/login on GitHub.
    if(-not $IsWindows){
        $fake=Join-Path $dir 'gh';$fixture=Join-Path $dir 'contents.json'
        $package=@{type='file';encoding='base64';content=[Convert]::ToBase64String($bytes)}|ConvertTo-Json -Compress
        [IO.File]::WriteAllText($fixture,$package)
        $program=@'
#!/usr/bin/env python3
import sys,time
if sys.argv[1:3]==['auth','login']:
 print('! First copy your one-time code: ABCD-1234',file=sys.stderr,flush=True)
 time.sleep(.15)
 print('Authentication complete.',file=sys.stderr,flush=True)
elif sys.argv[1:3]==['auth','token']: print('fixture-token-not-real')
else: print(open(__file__+'.json').read())
'@
        [IO.File]::WriteAllText($fake,$program.Replace("`r`n","`n"),[Text.UTF8Encoding]::new($false));Copy-Item $fixture ($fake+'.json');& chmod +x $fake
        & $m {param($exe,$moduleFile)
            $script:realGh=$exe
            Remove-Item Function:script:Invoke-AiUpdateGh
            # Reload only the original process runner after removing its mock.
            $ast=[Management.Automation.Language.Parser]::ParseFile($moduleFile,[ref]$null,[ref]$null)
            $definition=$ast.Find({param($a)$a -is [Management.Automation.Language.FunctionDefinitionAst] -and $a.Name -eq 'Invoke-AiUpdateGh'},$true)
            Invoke-Expression $definition.Extent.Text.Replace('function Invoke-AiUpdateGh','function script:Invoke-AiUpdateGh')
            function script:Get-AiUpdateGh {$script:realGh}
            function script:Protect-AiUpdateDirectory {param($Directory)$null=New-Item -ItemType Directory -Path $Directory -Force}
        } $fake (Join-Path $root 'src/Web.Update.psm1')
        Test 'device code gh process completes, delegates credential and removes temporary gh profile' {
            $state=[hashtable]::Synchronized(@{cancel=$false;code='';message=''})
            $package=Invoke-AiWebGhLogin $dir $state
            Assert ($package.version -eq $version) 'Login did not download'
            Assert ((& $m {$script:saved}) -eq 'fixture-token-not-real') 'Login token not delegated'
            Assert ($state.code -eq '') 'Code not cleared'
            Assert (@(Get-ChildItem (Join-Path $dir 'updates/auth') -Directory).Count -eq 0) 'gh config leaked'
        }
        Test 'cancelled gh login kills its process and clears code/config' {
            $state=[hashtable]::Synchronized(@{cancel=$true;code='';message=''})
            $failed=$false;try{Invoke-AiWebGhLogin $dir $state}catch{$failed=$_.Exception.Message -like '*отменён*'}
            Assert $failed 'Cancellation not reported'
            Assert ($state.code -eq '') 'Code retained'
            Assert (@(Get-ChildItem (Join-Path $dir 'updates/auth') -Directory).Count -eq 0) 'Cancelled config leaked'
        }
    }
    Write-Host "Web update tests: $script:passed passed"
}finally{Remove-Item -LiteralPath $dir -Recurse -Force;Remove-Module Web.Update -ErrorAction SilentlyContinue}
