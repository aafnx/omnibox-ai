#requires -Version 7.4
Set-StrictMode -Version Latest
$script:Repository='aafnx/omnibox-ai'

function Get-AiUpdateGh {
    $cmd=Get-Command gh -CommandType Application -ErrorAction SilentlyContinue|Select-Object -First 1
    if($null -ne $cmd){return $cmd.Source}
    if($IsWindows){$path=Join-Path $env:ProgramFiles 'GitHub CLI/gh.exe';if(Test-Path -LiteralPath $path){return $path}}
    return ''
}
function Invoke-AiUpdateGh {
    param([string]$Executable,[string[]]$Arguments,[string]$ConfigDirectory='',[string]$Token='')
    $info=[Diagnostics.ProcessStartInfo]::new();$info.FileName=$Executable
    $info.UseShellExecute=$false;$info.CreateNoWindow=$true;$info.RedirectStandardOutput=$true;$info.RedirectStandardError=$true;$info.RedirectStandardInput=$true
    foreach($arg in $Arguments){$info.ArgumentList.Add($arg)}
    if($ConfigDirectory){$info.Environment['GH_CONFIG_DIR']=$ConfigDirectory;foreach($name in @('GH_TOKEN','GITHUB_TOKEN')){$null=$info.Environment.Remove($name)}}
    if($Token){$info.Environment['GH_TOKEN']=$Token;$info.Environment.Remove('GITHUB_TOKEN')|Out-Null}
    $p=[Diagnostics.Process]::new();$p.StartInfo=$info;$started=$false
    try{
        $started=$p.Start();$p.StandardInput.Close();$out=$p.StandardOutput.ReadToEndAsync();$err=$p.StandardError.ReadToEndAsync()
        if(-not $p.WaitForExit(60000)){$p.Kill($true);throw 'GitHub не ответил за 60 секунд.'}
        $value=$out.GetAwaiter().GetResult();$null=$err.GetAwaiter().GetResult()
        if($p.ExitCode -ne 0){throw 'Нет доступа к приватному aafnx/omnibox-ai. Войдите через gh или используйте токен с Contents: Read.'}
        return $value.Trim()
    }finally{$info.Environment.Remove('GH_TOKEN')|Out-Null;if($started -and -not $p.HasExited){try{$p.Kill($true)}catch{}};$p.Dispose()}
}
function Protect-AiUpdateDirectory {
    param([string]$Directory)
    if(-not $IsWindows){throw 'Хранилище авторизации предназначено для Windows.'}
    $null=New-Item -ItemType Directory -Path $Directory -Force
    $acl=[Security.AccessControl.DirectorySecurity]::new();$acl.SetAccessRuleProtection($true,$false)
    foreach($sid in @('S-1-5-18','S-1-5-32-544')){
        $acl.AddAccessRule([Security.AccessControl.FileSystemAccessRule]::new([Security.Principal.SecurityIdentifier]::new($sid),'FullControl','ContainerInherit,ObjectInherit','None','Allow'))
    }
    Set-Acl -LiteralPath $Directory -AclObject $acl
}
function Save-AiUpdateCredential {
    param([string]$Root,[string]$Token)
    if(-not $Token -or $Token.Length -gt 4096){throw 'Некорректная авторизация GitHub.'}
    $dir=Join-Path $Root 'updates/auth';Protect-AiUpdateDirectory $dir
    $plain=[Text.Encoding]::UTF8.GetBytes($Token)
    try{
        $cipher=[Security.Cryptography.ProtectedData]::Protect($plain,$null,[Security.Cryptography.DataProtectionScope]::LocalMachine)
        [IO.File]::WriteAllBytes((Join-Path $dir 'github.dpapi'),$cipher)
    }finally{[Array]::Clear($plain,0,$plain.Length)}
}
function Read-AiUpdateCredential {
    param([string]$Root)
    $file=Join-Path $Root 'updates/auth/github.dpapi'
    if(-not (Test-Path -LiteralPath $file)){return ''}
    $plain=[Security.Cryptography.ProtectedData]::Unprotect([IO.File]::ReadAllBytes($file),$null,[Security.Cryptography.DataProtectionScope]::LocalMachine)
    try{return [Text.Encoding]::UTF8.GetString($plain)}finally{[Array]::Clear($plain,0,$plain.Length)}
}
function Sync-AiUpdateCredential {
    param([string]$Root)
    $gh=Get-AiUpdateGh;if(-not $gh){return $false}
    try{
        $token=Invoke-AiUpdateGh $gh @('auth','token','--hostname','github.com')
        Save-AiUpdateCredential $Root $token
        return $true
    }catch{return $false}finally{$token=$null}
}
function Get-AiWebInstaller {
    param([string]$Root,[string]$Token='')
    $gh=Get-AiUpdateGh
    if(-not $Token){$Token=Read-AiUpdateCredential $Root}
    $headers=@{}
    try{
        if($gh){
            # Only the child process gets GH_TOKEN. No global environment or command argument contains it.
            $json=Invoke-AiUpdateGh $gh @('api','--hostname','github.com','repos/aafnx/omnibox-ai/contents/dist/install-omnibox-ai.ps1?ref=main','--header','Accept: application/vnd.github+json') -Token $Token
            $package=$json|ConvertFrom-Json -AsHashtable
        }elseif($Token){
            $headers=@{Authorization=('Bearer '+$Token);Accept='application/vnd.github+json'}
            $package=Invoke-RestMethod -Uri 'https://api.github.com/repos/aafnx/omnibox-ai/contents/dist/install-omnibox-ai.ps1?ref=main' -Headers $headers -TimeoutSec 60 -MaximumRedirection 0
            $package=$package|ConvertTo-Json -Depth 10|ConvertFrom-Json -AsHashtable
        }else{throw 'Войдите через gh или введите токен.'}
        if($package.type -ne 'file' -or $package.encoding -ne 'base64'){throw 'GitHub не вернул установочный файл.'}
        $bytes=[Convert]::FromBase64String($package.content)
        $text=[Text.Encoding]::UTF8.GetString($bytes).TrimStart([char]0xfeff)
        if($bytes.Length -lt 100 -or $bytes.Length -gt 2MB -or -not $text.StartsWith('#requires -Version 7.4')){throw 'Некорректный установочный файл.'}
        $version=[regex]::Match($text,'OmniBox AI (\d+\.\d+\.\d+) payload OK').Groups[1].Value
        $sha=[regex]::Match($text,'(?m)^\$expectedSha = ''([a-f0-9]{64})''').Groups[1].Value
        $payload=[regex]::Match($text,'(?m)^\$payload = ''([A-Za-z0-9+/=]+)''').Groups[1].Value
        if(-not $version -or -not $sha -or -not $payload){throw 'Неизвестный формат пакета обновления.'}
        $actual=[Convert]::ToHexString([Security.Cryptography.SHA256]::HashData([Convert]::FromBase64String($payload))).ToLowerInvariant()
        if($actual -cne $sha){throw 'Пакет обновления повреждён: SHA256 не совпадает.'}
        return @{bytes=$bytes;version=$version;sha256=[Convert]::ToHexString([Security.Cryptography.SHA256]::HashData($bytes)).ToLowerInvariant()}
    }catch{throw 'Не удалось получить пакет из GitHub. Проверьте подключение, авторизацию и доступ к aafnx/omnibox-ai.'}
    finally{$headers.Clear();$Token=$null}
}
function Invoke-AiWebGhLogin {
    param([string]$Root,$State)
    $gh=Get-AiUpdateGh;if(-not $gh){throw 'Установите GitHub CLI на ПК: winget install --id GitHub.cli -e. Затем перезапустите панель.'}
    $auth=Join-Path $Root 'updates/auth'
    # Recover protected temporary profiles left by an interrupted panel/device login.
    foreach($old in @(Get-ChildItem -LiteralPath $auth -Directory -ErrorAction SilentlyContinue|Where-Object{$_.Name -match '^login-[a-f0-9]{32}$' -and $_.LastWriteTimeUtc -lt [DateTime]::UtcNow.AddMinutes(-20)})){
        Remove-Item -LiteralPath $old.FullName -Recurse -Force -ErrorAction SilentlyContinue
    }
    $config=Join-Path $Root ('updates/auth/login-'+[Guid]::NewGuid().ToString('N'));Protect-AiUpdateDirectory $config
    $info=[Diagnostics.ProcessStartInfo]::new();$info.FileName=$gh;$info.UseShellExecute=$false;$info.CreateNoWindow=$true
    $info.RedirectStandardInput=$true;$info.RedirectStandardOutput=$true;$info.RedirectStandardError=$true
    foreach($arg in @('auth','login','--hostname','github.com','--git-protocol','https','--web')){$info.ArgumentList.Add($arg)}
    foreach($name in @('GH_TOKEN','GITHUB_TOKEN')){$null=$info.Environment.Remove($name)}
    $info.Environment['GH_CONFIG_DIR']=$config;$info.Environment['NO_COLOR']='1';$info.Environment['GH_PROMPT_DISABLED']='1'
    $p=[Diagnostics.Process]::new();$p.StartInfo=$info;$started=$false
    try{
        $started=$p.Start();$p.StandardInput.Close();$out=$p.StandardOutput.ReadToEndAsync();$line=$p.StandardError.ReadLineAsync();$deadline=[DateTime]::UtcNow.AddMinutes(15)
        while(-not $p.HasExited -or $line.IsCompleted){
            if($State.cancel -or [DateTime]::UtcNow -gt $deadline){throw 'Вход отменён или истёк срок кода. Повторите вход.'}
            if($line.IsCompleted){
                $value=$line.GetAwaiter().GetResult();if($null -eq $value){break}
                if($value -match 'one-time code:\s*([A-Z0-9]{4}-[A-Z0-9]{4})'){$State.code=$Matches[1];$State.message='Откройте github.com/login/device и подтвердите код.'}
                $line=$p.StandardError.ReadLineAsync()
            }else{Start-Sleep -Milliseconds 200}
        }
        if(-not $p.WaitForExit(1000) -or $p.ExitCode -ne 0){throw 'GitHub CLI не завершил вход. Повторите вход или используйте токен.'}
        $null=$out.GetAwaiter().GetResult()
        $token=Invoke-AiUpdateGh $gh @('auth','token','--hostname','github.com') -ConfigDirectory $config
        $package=Get-AiWebInstaller $Root $token
        if($State.cancel){throw 'Вход отменён.'}
        Save-AiUpdateCredential $Root $token
        return $package
    }finally{
        if($started -and -not $p.HasExited){try{$p.Kill($true)}catch{}};$p.Dispose();$token=$null;$State.code=''
        # Remove this temporary gh login from the SYSTEM credential store as well.
        try{$null=Invoke-AiUpdateGh $gh @('auth','logout','--hostname','github.com') -ConfigDirectory $config}catch{}
        Remove-Item -LiteralPath $config -Recurse -Force -ErrorAction SilentlyContinue
    }
}
function New-AiWebUpdateState {
    param([string]$Root)
    return @{root=$Root;job=$null;shared=[hashtable]::Synchronized(@{phase='idle';message='Нажмите «Проверить обновления».';code='';cancel=$false;latest='';file='';sha256=''});control=$false}
}
function Get-AiWebUpdateStatus {
    param($Manager)
    if($null -ne $Manager.job -and $Manager.job.State -in @('Completed','Failed','Stopped')){
        $null=Receive-Job $Manager.job -ErrorAction SilentlyContinue -InformationAction SilentlyContinue
        if($Manager.shared.phase -in @('checking','login')){$Manager.shared.phase='failed';$Manager.shared.message='Операция прервана. Повторите проверку.'}
        Remove-Job $Manager.job -Force;$Manager.job=$null
    }
    $current='0.9.4';$marker=Join-Path $Manager.root '.omnibox-ai-install'
    if(Test-Path -LiteralPath $marker){$match=[regex]::Match((Get-Content -LiteralPath $marker -Raw),'\d+\.\d+\.\d+');if($match.Success){$current=$match.Value}}
    $result=@{current=$current;latest=$Manager.shared.latest;phase=$Manager.shared.phase;message=$Manager.shared.message;code=$Manager.shared.code;gh_available=[bool](Get-AiUpdateGh);authenticated=(Test-Path -LiteralPath (Join-Path $Manager.root 'updates/auth/github.dpapi'))}
    $file=Join-Path $Manager.root 'updates/result.json'
    if(Test-Path -LiteralPath $file){
        try{
            $worker=Get-Content -LiteralPath $file -Raw|ConvertFrom-Json -AsHashtable
            if($worker.phase -eq 'installing' -and $IsWindows -and $worker.service -match '^omnibox-ai-update-[a-f0-9]{8}$' -and ([DateTime]::UtcNow-[DateTime]::Parse($worker.timestamp)).TotalSeconds -gt 30){
                $service=Get-Service -Name $worker.service -ErrorAction SilentlyContinue
                if($null -eq $service -or $service.Status -notin @('Running','StartPending')){$worker.phase='failed';$worker.message='Процесс обновления прерван. Проверьте службы и повторите проверку.'}
            }
            if($worker.phase -in @('installing','done','failed')){
                if($worker.phase -eq 'installing' -or $Manager.shared.phase -eq 'installing' -or $Manager.shared.phase -eq 'idle'){
                    $result.phase=$worker.phase;$result.message=$worker.message
                    if($worker.Contains('version')){$result.latest=$worker.version}
                }
            }
        }catch{}
    }
    if($Manager.control -and $result.phase -in @('done','failed')){[OmniBox.InferenceStats]::EndControl();$Manager.control=$false}
    return $result
}
function Start-AiWebUpdateAction {
    param($Manager,$Body,$Operations)
    if(-not $Body.Contains('action') -or $Body.action -isnot [string] -or $Body.action -notin @('check','login','token','cancel','forget','apply')){throw 'Неизвестная операция обновления.'}
    $keys=if($Body.action -eq 'token'){@('action','token')}else{@('action')}
    if(@($Body.Keys|Where-Object{$_ -notin $keys}).Count){throw 'Неизвестный параметр обновления.'}
    $status=Get-AiWebUpdateStatus $Manager
    if($Body.action -eq 'cancel'){
        if($status.phase -eq 'installing'){throw 'Установка уже выполняется.'}
        $Manager.shared.cancel=$true;$Manager.shared.message='Отменяем операцию. Завершение текущего запроса к GitHub может занять до 60 секунд.';return (Get-AiWebUpdateStatus $Manager)
    }
    if($status.phase -in @('checking','login','installing')){throw 'Обновление или вход уже выполняется.'}
    if($Body.action -eq 'forget'){
        Remove-Item -LiteralPath (Join-Path $Manager.root 'updates/auth/github.dpapi') -Force -ErrorAction SilentlyContinue
        $Manager.shared.phase='forgotten';$Manager.shared.message='Доступ панели к GitHub удалён.';return (Get-AiWebUpdateStatus $Manager)
    }
    if($Body.action -eq 'apply'){
        if($Manager.shared.phase -ne 'ready'){throw 'Сначала проверьте и скачайте новую версию.'}
        if(@($Operations.Values|Where-Object{$null -ne $_.job -and $_.job.State -notin @('Completed','Failed','Stopped')}).Count){throw 'Дождитесь завершения операций сервера и скачивания моделей.'}
        if(-not [OmniBox.InferenceStats]::BeginControl($false)){throw 'Модель занята генерацией или другой операцией. Дождитесь завершения.'}
        try{Start-AiWebUpdateWorker $Manager.root $Manager.shared.file $Manager.shared.sha256 $Manager.shared.latest;$Manager.control=$true;$Manager.shared.phase='installing';$Manager.shared.message='Установка. Панель перезапустится; модель будет остановлена.'}
        catch{[OmniBox.InferenceStats]::EndControl();throw}
        return (Get-AiWebUpdateStatus $Manager)
    }
    $token='';if($Body.action -eq 'token'){
        if(-not $Body.Contains('token') -or $Body.token -isnot [string] -or $Body.token.Length -lt 10 -or $Body.token.Length -gt 4096 -or $Body.token -match '[\s\x00-\x1f]'){throw 'Введите корректный GitHub token.'}
        $token=$Body.token;$Body.Remove('token')
    }
    $Manager.shared.phase=if($Body.action -eq 'login'){'login'}else{'checking'};$Manager.shared.message='Получаем доступ и пакет обновления…';$Manager.shared.cancel=$false;$Manager.shared.code=''
    $Manager.shared.file='';$Manager.shared.latest=''
    $modulePath=Join-Path $PSScriptRoot 'Web.Update.psm1'
    try{$Manager.job=Start-ThreadJob -ArgumentList $modulePath,$Manager.root,$Manager.shared,[string]$Body.action,$token,$status.current -ScriptBlock {
        param($Module,$Root,$State,$Action,$Token,$Current)
        $ErrorActionPreference='Stop';Import-Module $Module -Force
        try{
            $package=if($Action -eq 'login'){Invoke-AiWebGhLogin $Root $State}else{Get-AiWebInstaller $Root $Token}
            if($State.cancel){throw 'Проверка отменена.'}
            $State.latest=$package.version
            if([version]$package.version -le [version]$Current){$State.phase='current';$State.message='Установлена актуальная версия.';return}
            $dir=Join-Path $Root 'updates/download';$null=New-Item -ItemType Directory -Path $dir -Force
            $file=Join-Path $dir 'installer.ps1';[IO.File]::WriteAllBytes($file,$package.bytes)
            $State.file=$file;$State.sha256=$package.sha256;$State.phase='ready';$State.message='Пакет проверен и готов к установке.'
        }catch{$State.phase='failed';$State.message=$_.Exception.Message}
        finally{$Token=$null;$State.code=''}
    }
    }catch{$Manager.shared.phase='failed';$Manager.shared.message='Не удалось запустить проверку обновления.';throw}
    finally{$token=$null}
    return (Get-AiWebUpdateStatus $Manager)
}
function Start-AiWebUpdateWorker {
    param([string]$Root,[string]$Installer,[string]$Sha256,[string]$Version)
    if(-not $IsWindows){throw 'Обновление служб доступно только на Windows.'}
    if(-not (Test-Path -LiteralPath (Join-Path $Root '.omnibox-ai-install'))){throw 'Нет маркера установленного приложения.'}
    $bytes=[IO.File]::ReadAllBytes($Installer)
    if([Convert]::ToHexString([Security.Cryptography.SHA256]::HashData($bytes)).ToLowerInvariant() -cne $Sha256){throw 'Скачанный установщик изменился. Повторите проверку.'}
    $pwsh=(Get-Process -Id $PID).Path
    Import-Module (Join-Path $Root 'src/Service.Install.psm1') -Force
    $old=@(Get-ChildItem -LiteralPath (Join-Path $Root 'updates') -Directory -ErrorAction SilentlyContinue|Where-Object Name -match '^run-[a-f0-9]{32}$'|Sort-Object LastWriteTimeUtc -Descending)
    foreach($item in @($old|Select-Object -Skip 2)){
        $oldName='omnibox-ai-update-'+$item.Name.Substring(4,8)
        $oldService=Get-Service $oldName -ErrorAction SilentlyContinue
        if($null -ne $oldService -and $oldService.Status -eq 'Stopped'){
            $info=Get-CimInstance Win32_Service -Filter "Name='$oldName'" -ErrorAction SilentlyContinue
            if($null -ne $info -and (Test-AiOwnedServiceCommand $info.PathName $pwsh $item.FullName (Join-Path $item.FullName 'update.xml'))){Remove-Service $oldName -ErrorAction SilentlyContinue;$oldService=Get-Service $oldName -ErrorAction SilentlyContinue}
        }
        if($null -eq $oldService){Remove-Item -LiteralPath $item.FullName -Recurse -Force -ErrorAction SilentlyContinue}
    }
    $id=[Guid]::NewGuid().ToString('N');$name='omnibox-ai-update-'+$id.Substring(0,8)
    $stage=Join-Path $Root ('updates/run-'+$id);$null=New-Item -ItemType Directory -Path (Join-Path $stage 'src') -Force
    Copy-Item -LiteralPath (Join-Path $Root 'src/Service.Host.exe') -Destination (Join-Path $stage 'src')
    Copy-Item -LiteralPath (Join-Path $Root 'scripts/update-worker.ps1') -Destination $stage
    [IO.File]::WriteAllBytes((Join-Path $stage 'installer.ps1'),$bytes)
    $profile=Join-Path $stage 'update.xml';$x=[xml]'<service><id/><executable/><arguments/><workingdirectory/><logpath/></service>'
    $x.service.id=$name;$x.service.executable=$pwsh
    $x.service.arguments='-NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File "'+(Join-Path $stage 'update-worker.ps1')+'" -Root "'+$Root+'" -ServiceName "'+$name+'" -Version "'+$Version+'"'
    $x.service.workingdirectory=$stage;$x.service.logpath=Join-Path $stage 'logs';$x.Save($profile)
    # A durable marker prevents duplicate installation after a panel restart.
    $resultPath=Join-Path $Root 'updates/result.json'
    $result=@{phase='installing';message='Запускаем службу обновления…';version=$Version;service=$name;timestamp=[DateTime]::UtcNow.ToString('o')}|ConvertTo-Json -Compress
    [IO.File]::WriteAllText($resultPath,$result,[Text.Encoding]::UTF8)
    try{
        $null=New-Service -Name $name -BinaryPathName (New-AiNativeServiceCommand $pwsh $stage $profile) -StartupType Manual
        Start-Service $name -ErrorAction Stop
    }catch{Remove-Item -LiteralPath $resultPath -Force -ErrorAction SilentlyContinue;Remove-Service $name -ErrorAction SilentlyContinue;Remove-Item -LiteralPath $stage -Recurse -Force -ErrorAction SilentlyContinue;throw 'Не удалось запустить отдельную службу обновления.'}
}
function Stop-AiWebUpdateManager {
    param($Manager)
    $Manager.shared.cancel=$true
    if($null -ne $Manager.job){Stop-Job $Manager.job;Remove-Job $Manager.job -Force}
    if($Manager.control){[OmniBox.InferenceStats]::EndControl()}
}
Export-ModuleMember -Function New-AiWebUpdateState,Get-AiWebUpdateStatus,Start-AiWebUpdateAction,Stop-AiWebUpdateManager,Sync-AiUpdateCredential,Get-AiWebInstaller,Invoke-AiWebGhLogin
