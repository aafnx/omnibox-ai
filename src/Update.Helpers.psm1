#requires -Version 7.4
Set-StrictMode -Version Latest
function Invoke-AiGhContents {
    param([string]$Executable)
    $info=[Diagnostics.ProcessStartInfo]::new()
    $info.FileName=$Executable;$info.UseShellExecute=$false;$info.CreateNoWindow=$true
    $info.RedirectStandardOutput=$true;$info.RedirectStandardError=$true
    $info.StandardOutputEncoding=[Text.Encoding]::UTF8
    foreach($argument in @('api','--hostname','github.com','repos/aafnx/omnibox-ai/contents/dist/install-omnibox-ai.ps1?ref=main','--header','Accept: application/vnd.github+json')){$info.ArgumentList.Add($argument)}
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
function Get-AiInstallerBytes {
    param([switch]$UseToken)
    $gh=if(-not $UseToken){Get-Command gh -CommandType Application -ErrorAction SilentlyContinue|Select-Object -First 1}else{$null}
    if($null -ne $gh){
        Write-Host 'Скачиваем omnibox-ai через авторизацию GitHub CLI…'
        $package=Invoke-AiGhContents $gh.Source
    }else{
        $secret=Read-Host 'GitHub token (omnibox-ai: Contents Read)' -AsSecureString
        $headers=@{}
        try{
            $headers.Authorization='Bearer '+([pscredential]::new('github',$secret)).GetNetworkCredential().Password
            $headers.Accept='application/vnd.github+json'
            $package=Invoke-RestMethod -Uri 'https://api.github.com/repos/aafnx/omnibox-ai/contents/dist/install-omnibox-ai.ps1?ref=main' -Headers $headers -MaximumRedirection 0 -TimeoutSec 60
        }finally{$headers.Clear();$secret.Dispose()}
        $package=$package|ConvertTo-Json -Depth 10|ConvertFrom-Json -AsHashtable
    }
    if($package.type -ne 'file' -or $package.encoding -ne 'base64' -or -not $package.content){throw 'GitHub не вернул установочный файл; установка не запущена.'}
    $bytes=[Convert]::FromBase64String($package.content)
    if($bytes.Length -lt 100 -or $bytes.Length -gt 2MB -or -not ([Text.Encoding]::UTF8.GetString($bytes).TrimStart([char]0xfeff).StartsWith('#requires -Version 7.4'))){throw 'Некорректный установочный файл; установка не запущена.'}
    return ,$bytes
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
    param([string]$InstallDirectory,[switch]$UseToken)
    $bytes=Get-AiInstallerBytes -UseToken:$UseToken
    Invoke-AiDownloadedInstaller -Bytes $bytes -InstallDirectory $InstallDirectory
}
Export-ModuleMember -Function Get-AiInstallerBytes,Invoke-AiDownloadedInstaller,Invoke-AiUpdate
