#requires -Version 7.4
Set-StrictMode -Version Latest
# Reuse shared dependencies: forcing a nested reload removes their caller-scope exports.
Import-Module (Join-Path $PSScriptRoot 'Install.Transaction.psm1')

$script:Releases = 'releases.json'
$script:Installer = 'dist/install-omnibox-ai.ps1'

function Assert-AiGitHubReference {
    # Only the two repository files and the main branch or a full commit SHA are ever requested.
    param([string]$Path, [string]$Ref)
    if ($Path -notin @($script:Releases, $script:Installer) -or $Ref -cnotmatch '^(main|[0-9a-f]{40})$') { throw 'Недопустимый запрос к GitHub.' }
}
function Invoke-AiGhContents {
    param([string]$Executable, [string]$Path = 'dist/install-omnibox-ai.ps1', [string]$Ref = 'main')
    Assert-AiGitHubReference $Path $Ref
    $info=[Diagnostics.ProcessStartInfo]::new()
    $info.FileName=$Executable;$info.UseShellExecute=$false;$info.CreateNoWindow=$true
    $info.RedirectStandardOutput=$true;$info.RedirectStandardError=$true
    $info.StandardOutputEncoding=[Text.Encoding]::UTF8
    foreach($argument in @('api','--hostname','github.com',"repos/aafnx/omnibox-ai/contents/$($Path)?ref=$Ref",'--header','Accept: application/vnd.github+json')){$info.ArgumentList.Add($argument)}
    $process=[Diagnostics.Process]::new();$process.StartInfo=$info
    try{
        if(-not $process.Start()){throw 'Не удалось запустить gh.'}
        $stdout=$process.StandardOutput.ReadToEndAsync();$stderr=$process.StandardError.ReadToEndAsync()
        if(-not $process.WaitForExit(60000)){$process.Kill($true);throw 'GitHub не ответил за 60 секунд; установка не запущена.'}
        $json=$stdout.GetAwaiter().GetResult();$null=$stderr.GetAwaiter().GetResult()
        if($process.ExitCode -ne 0){throw 'gh не смог скачать omnibox-ai. Проверь gh auth status --hostname github.com и доступ к aafnx/omnibox-ai. Для входа: gh auth login --hostname github.com --git-protocol https --web. Для другого способа: ai update --token.'}
        return $json|ConvertFrom-Json -AsHashtable
    }finally{$process.Dispose()}
}
function New-AiGitHubSource {
    # One source per command: gh when available, otherwise a token entered once and kept only in memory.
    param([switch]$UseToken)
    $gh=if(-not $UseToken){Get-Command gh -CommandType Application -ErrorAction SilentlyContinue|Select-Object -First 1}else{$null}
    if($null -ne $gh){Write-Host 'Используется авторизация GitHub CLI…';return @{gh=$gh.Source;secret=$null}}
    return @{gh=$null;secret=(Read-Host 'GitHub token (omnibox-ai: Contents Read)' -AsSecureString)}
}
function Close-AiGitHubSource {
    param($Source)
    if($null -ne $Source -and $null -ne $Source.secret){$Source.secret.Dispose()}
}
function Get-AiGitHubFile {
    param($Source, [string]$Path, [string]$Ref)
    Assert-AiGitHubReference $Path $Ref
    if($Source.gh){$package=Invoke-AiGhContents $Source.gh $Path $Ref}
    else{
        $headers=@{}
        try{
            $headers.Authorization='Bearer '+([pscredential]::new('github',$Source.secret)).GetNetworkCredential().Password
            $headers.Accept='application/vnd.github+json'
            $package=Invoke-RestMethod -Uri "https://api.github.com/repos/aafnx/omnibox-ai/contents/$($Path)?ref=$Ref" -Headers $headers -MaximumRedirection 0 -TimeoutSec 60
        }finally{$headers.Clear()}
        $package=$package|ConvertTo-Json -Depth 10|ConvertFrom-Json -AsHashtable
    }
    if($package.type -ne 'file' -or $package.encoding -ne 'base64' -or -not $package.content){throw 'GitHub не вернул ожидаемый файл; установка не запущена.'}
    return ,[Convert]::FromBase64String($package.content)
}
function Get-AiInstallerBytes {
    param([switch]$UseToken, $Source = $null, [string]$Ref = 'main')
    $own=$null -eq $Source
    if($own){$Source=New-AiGitHubSource -UseToken:$UseToken}
    try{$bytes=Get-AiGitHubFile $Source $script:Installer $Ref}finally{if($own){Close-AiGitHubSource $Source}}
    if($bytes.Length -lt 100 -or $bytes.Length -gt 2MB -or -not ([Text.Encoding]::UTF8.GetString($bytes).TrimStart([char]0xfeff).StartsWith('#requires -Version 7.4'))){throw 'Некорректный установочный файл; установка не запущена.'}
    return ,$bytes
}
function Get-AiInstallerVersion {
    param([byte[]]$Bytes)
    $match=[regex]::Match([Text.Encoding]::UTF8.GetString($Bytes),'OmniBox AI (\d+\.\d+\.\d+) payload OK')
    if(-not $match.Success){throw 'В установщике не указана версия; установка не запущена.'}
    return $match.Groups[1].Value
}
function ConvertTo-AiReleaseList {
    <#
    releases.json in main lists the versions that can be installed over the current one, newest
    first. The newest entry has commit null (main); every older one names the commit of its release.
    #>
    param([string]$Json)
    $value=$Json|ConvertFrom-Json -AsHashtable
    if($value -isnot [Collections.IDictionary] -or $value.schema_version -ne 1 -or $value.releases -isnot [array] -or $value.releases.Count -lt 1){throw 'Некорректный список версий releases.json.'}
    $result=@();$previous=$null
    for($i=0;$i -lt $value.releases.Count;$i++){
        $entry=$value.releases[$i]
        if($entry -isnot [Collections.IDictionary] -or $entry.version -isnot [string] -or $entry.version -notmatch '^\d+\.\d+\.\d+$'){throw 'Некорректная версия в releases.json.'}
        $commit=if($entry.Contains('commit')){$entry.commit}else{$null}
        if(($i -eq 0 -and $null -ne $commit) -or ($i -gt 0 -and ($commit -isnot [string] -or $commit -cnotmatch '^[0-9a-f]{40}$'))){throw "Некорректный коммит версии $($entry.version) в releases.json."}
        $note=if($entry.Contains('note')){[string]$entry.note}else{''}
        if($note.Length -gt 160 -or $note -match '[\x00-\x1f]'){throw "Некорректное примечание версии $($entry.version)."}
        if($null -ne $previous -and [version]$entry.version -ge [version]$previous){throw 'Версии в releases.json должны идти от новой к старой без повторов.'}
        $previous=$entry.version
        $result+=[pscustomobject]@{version=$entry.version;commit=$commit;date=$(if($entry.Contains('date')){[string]$entry.date}else{''});note=$note}
    }
    return $result
}
function Get-AiReleaseList {
    param($Source)
    $bytes=Get-AiGitHubFile $Source $script:Releases 'main'
    return @(ConvertTo-AiReleaseList ([Text.Encoding]::UTF8.GetString($bytes).TrimStart([char]0xfeff)))
}
function Get-AiInstalledVersion {
    param([string]$InstallDirectory)
    $marker=Join-Path $InstallDirectory '.omnibox-ai-install'
    if(-not (Test-Path -LiteralPath $marker -PathType Leaf)){return ''}
    $match=[regex]::Match((Get-Content -LiteralPath $marker -Raw),'\d+\.\d+\.\d+')
    return $(if($match.Success){$match.Value}else{''})
}
function Format-AiReleaseList {
    param($Releases, [string]$Installed)
    $lines=@('Доступные версии OmniBox AI:')
    $width=($Releases|ForEach-Object{$_.version.Length}|Measure-Object -Maximum).Maximum
    for($i=0;$i -lt $Releases.Count;$i++){
        $r=$Releases[$i];$marks=@()
        if($i -eq 0){$marks+='последняя'}
        if($r.version -eq $Installed){$marks+='установлена'}
        $line='  '+$r.version.PadRight($width)
        if($r.note){$line+="  ($($r.note))"}
        if($marks.Count){$line+='  — '+($marks -join ', ')}
        $lines+=$line
    }
    if($Installed -and -not @($Releases|Where-Object version -eq $Installed).Count){$lines+="Установлена версия $Installed, её нет в списке."}
    $lines+='Последняя версия: ai update. Конкретная версия (обновление или откат): ai update <версия>.'
    return $lines
}
function Invoke-AiDownloadedInstaller {
    param([byte[]]$Bytes,[string]$InstallDirectory='',[switch]$CheckOnly)
    $file=Join-Path ([IO.Path]::GetTempPath()) ('omnibox-ai-install-'+[Guid]::NewGuid().ToString('N')+'.ps1')
    try{
        [IO.File]::WriteAllBytes($file,$Bytes)
        $arguments=@{};if($InstallDirectory){$arguments.InstallDirectory=$InstallDirectory};if($CheckOnly){$arguments.CheckOnly=$true}
        & $file @arguments
    }finally{if([IO.File]::Exists($file)){[IO.File]::Delete($file)}}
}
function Invoke-AiUpdate {
    <#
    ai update             latest version from main
    ai update <x.y.z>     that version from releases.json: upgrade, reinstall or downgrade
    ai update --list      available versions with notes
    #>
    param([string]$InstallDirectory,[switch]$UseToken,[string]$Version='',[switch]$List)
    if($Version -and $Version -notmatch '^\d+\.\d+\.\d+$'){throw 'Версия указывается как x.y.z, например 0.9.5. Список версий: ai update --list'}
    $installed=Get-AiInstalledVersion $InstallDirectory
    $source=New-AiGitHubSource -UseToken:$UseToken
    try{
        if($List){Format-AiReleaseList (Get-AiReleaseList $source) $installed|ForEach-Object{Write-Host $_};return}
        $ref='main'
        if($Version){
            $release=@(Get-AiReleaseList $source|Where-Object version -eq $Version)
            if(-not $release.Count){throw "Версия $Version недоступна для установки. Список версий: ai update --list"}
            if($release[0].commit){$ref=$release[0].commit}
        }
        $bytes=Get-AiInstallerBytes -Source $source -Ref $ref
    }finally{Close-AiGitHubSource $source}
    $target=Get-AiInstallerVersion $bytes
    if($Version -and $target -ne $Version){throw "Скачанный установщик содержит версию $target вместо $Version; установка не запущена."}
    if(-not $installed){Write-Host "Установка OmniBox AI $target."}
    elseif([version]$target -gt [version]$installed){Write-Host "Обновление OmniBox AI: $installed → $target."}
    elseif([version]$target -lt [version]$installed){
        Write-Host "Откат OmniBox AI на более старую версию: $installed → $target. Настройки сохраняются; функции новых версий станут недоступны."
        if([version]$target -lt [version]'0.9.5'){Write-Host "В $target нет автоматического отката установки при сбое: резервная копия файлов будет в backups."}
    }else{Write-Host "Переустановка OmniBox AI $target."}
    # Same lock as every installer: nothing else installs between download and installation.
    # Current installers re-enter it in this thread; older ones run under its protection.
    $lock=Enter-AiInstallLock
    try{Invoke-AiDownloadedInstaller -Bytes $bytes -InstallDirectory $InstallDirectory}finally{Exit-AiInstallLock $lock}
}
Export-ModuleMember -Function Get-AiInstallerBytes,Get-AiInstallerVersion,ConvertTo-AiReleaseList,Format-AiReleaseList,Invoke-AiDownloadedInstaller,Invoke-AiUpdate
