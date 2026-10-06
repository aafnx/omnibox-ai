#requires -Version 7.4
param([Parameter(Mandatory)][string]$Root,[Parameter(Mandatory)][string]$ServiceName,[Parameter(Mandatory)][string]$Version)
$ErrorActionPreference='Stop'
$global:LASTEXITCODE=0
$statusPath=Join-Path $Root 'updates/result.json'
function Write-UpdateResult([string]$Phase,[string]$Message){
    $value=@{phase=$Phase;message=$Message;version=$Version;service=$ServiceName;timestamp=[DateTime]::UtcNow.ToString('o')}|ConvertTo-Json -Compress
    $tmp=$statusPath+'.tmp';[IO.File]::WriteAllText($tmp,$value,[Text.Encoding]::UTF8);[IO.File]::Move($tmp,$statusPath,$true)
}
$snapshot=Join-Path $PSScriptRoot 'previous';$saved=$false;$serviceSnapshot=@()
$mutex=[Threading.Mutex]::new($false,'Global\omnibox-ai-control-v1');$locked=$false
try{
    try{$locked=$mutex.WaitOne(60000)}catch [Threading.AbandonedMutexException]{$locked=$true}
    if(-not $locked){throw 'Server control operation still running.'}
    Write-UpdateResult 'installing' 'Создаём резервную копию. Панель перезапустится; серверы будут остановлены.'
    $null=New-Item -ItemType Directory -Path $snapshot
    # Copy the installation, not model weights, request logs, earlier backups or credentials.
    foreach($entry in @('ai.ps1','ai.cmd','panel.ps1','panel-service.xml','service.ps1','src','scripts','web','docs','README.md','config.example.json','config.json','panel.json','gateway.json','.omnibox-ai-install')){
        $file=Join-Path $Root $entry
        if(Test-Path -LiteralPath $file){Copy-Item -LiteralPath $file -Destination $snapshot -Recurse -Force}
    }
    Import-Module (Join-Path $snapshot 'src/Service.Install.psm1') -Force
    $previousConfig=Get-Content -LiteralPath (Join-Path $snapshot 'config.json') -Raw|ConvertFrom-Json -AsHashtable
    $profiles=@{'omnibox-ai'=(Join-Path $Root 'panel-service.xml')}
    foreach($backend in $previousConfig.backends.Values){if($backend.service -notmatch '^[A-Za-z0-9_.-]+$'){throw 'Invalid backend service.'};$profiles[$backend.service]=$backend.service_config}
    $pwsh=(Get-Process -Id $PID).Path
    foreach($name in $profiles.Keys){
        $svc=Get-CimInstance Win32_Service -Filter "Name='$name'"
        if($null -ne $svc){
            if(-not(Test-AiOwnedServiceCommand $svc.PathName $pwsh $Root $profiles[$name])){throw 'Unexpected service command before update.'}
            $startup=switch($svc.StartMode){'Auto'{'Automatic'} 'Manual'{'Manual'} 'Disabled'{'Disabled'} default{throw 'Unexpected service start mode.'}}
            $serviceSnapshot+=@{name=$name;command=$svc.PathName;startup=$startup;profile=$profiles[$name]}
        }
    }
    $saved=$true
    & (Join-Path $PSScriptRoot 'installer.ps1') -InstallDirectory $Root -NonInteractive -NoProfileUpdate
    if($global:LASTEXITCODE -ne 0){throw 'Installer failed.'}
    if((Get-Service 'omnibox-ai' -ErrorAction Stop).Status -ne 'Running'){throw 'Panel did not restart.'}
    Write-UpdateResult 'done' 'Обновление завершено. Войдите в панель повторно. Модель можно запустить вручную.'
}catch{
    $restored=$false
    if($saved){
        try{
            $service=Get-CimInstance Win32_Service -Filter "Name='omnibox-ai'"
            $hostProcess=if($null -ne $service -and $service.ProcessId){Get-Process -Id $service.ProcessId -ErrorAction SilentlyContinue}else{$null}
            Stop-Service 'omnibox-ai' -ErrorAction SilentlyContinue
            if($null -ne $hostProcess -and -not $hostProcess.WaitForExit(30000)){throw 'Panel host still running.'}
            # A failed installation may have stopped the panel before stopping every backend.
            # Only stop services whose command exactly belongs to this installation.
            Import-Module (Join-Path $snapshot 'src/Service.Install.psm1') -Force
            $pwsh=(Get-Process -Id $PID).Path
            foreach($entry in $serviceSnapshot){
                $svc=Get-CimInstance Win32_Service -Filter "Name='$($entry.name)'"
                if($null -ne $svc -and $svc.State -ne 'Stopped'){
                    if(-not(Test-AiOwnedServiceCommand $svc.PathName $pwsh $Root $entry.profile)){throw 'Unexpected service command during recovery.'}
                    $process=Get-Process -Id $svc.ProcessId -ErrorAction SilentlyContinue
                    Stop-Service -Name $entry.name -ErrorAction Stop
                    if($null -ne $process -and -not $process.WaitForExit(30000)){throw 'Service host still running.'}
                }
            }
            foreach($item in Get-ChildItem -LiteralPath $snapshot -Force){Copy-Item -LiteralPath $item.FullName -Destination $Root -Recurse -Force}
            foreach($entry in $serviceSnapshot){Set-AiServiceCommand -Name $entry.name -Command $entry.command -StartupType $entry.startup}
            Start-Service 'omnibox-ai' -ErrorAction Stop
            $restored=$true
        }catch{}
    }
    Write-UpdateResult 'failed' $(if($restored){'Установка не завершилась. Предыдущие файлы восстановлены, панель запущена. Повторите проверку.'}else{'Установка не завершилась. Проверьте службу omnibox-ai и резервную копию в updates. Серверы не запускаются автоматически.'})
}finally{
    # SCM owns this process, independently of the panel job. Deletion takes effect on exit.
    Remove-Service -Name $ServiceName -ErrorAction SilentlyContinue
    if($locked){$mutex.ReleaseMutex()};$mutex.Dispose()
}
