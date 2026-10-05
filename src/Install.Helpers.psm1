Set-StrictMode -Version Latest
function Update-AiPowerShellProfile {
    param([string]$ProfilePath, [string]$InstallDirectory, [string]$BackupDirectory)
    $profileText = if (Test-Path -LiteralPath $ProfilePath) { Get-Content -LiteralPath $ProfilePath -Raw } else { '' }
    if ($null -eq $profileText) { $profileText = '' }
    $startMarker = '# >>> omnibox-ai PATH >>>'
    $endMarker = '# <<< omnibox-ai PATH <<<'
    $startCount = [regex]::Matches($profileText, [regex]::Escape($startMarker)).Count
    $endCount = [regex]::Matches($profileText, [regex]::Escape($endMarker)).Count
    if ($startCount -ne $endCount -or $startCount -gt 1 -or
        ($startCount -eq 1 -and $profileText.IndexOf($endMarker) -lt $profileText.IndexOf($startMarker))) {
        throw 'В профиле неоднозначные маркеры omnibox-ai; профиль не изменён.'
    }
    $escapedDirectory = $InstallDirectory.Replace("'", "''")
    $block = "$startMarker`r`nif (@(`$env:Path -split ';' | Where-Object { `$_.TrimEnd('\') -ieq '$escapedDirectory' }).Count -eq 0) { `$env:Path += ';$escapedDirectory' }`r`n$endMarker"
    $updated = if ($startCount) {
        $pattern = '(?s)' + [regex]::Escape($startMarker) + '.*?' + [regex]::Escape($endMarker)
        [regex]::Replace($profileText, $pattern, [Text.RegularExpressions.MatchEvaluator]{ param($match); $block })
    } else { $profileText.TrimEnd("`r", "`n") + "`r`n`r`n$block`r`n" }
    if ($updated -cne $profileText) {
        $null = New-Item -ItemType Directory -Path (Split-Path $ProfilePath -Parent) -Force
        if (Test-Path -LiteralPath $ProfilePath) {
            $null = New-Item -ItemType Directory -Path $BackupDirectory -Force
            $backup = Join-Path $BackupDirectory ('profile-' + (Get-Date -Format 'yyyyMMdd-HHmmss') + '-' + [Guid]::NewGuid().ToString('N') + '.ps1')
            Copy-Item -LiteralPath $ProfilePath -Destination $backup
            Write-Host "Резервная копия профиля: $backup"
        }
        Set-Content -LiteralPath $ProfilePath -Value $updated -Encoding utf8 -NoNewline
        Write-Host "Добавлен PATH-блок в профиль PowerShell: $ProfilePath"
    }
}
Export-ModuleMember -Function Update-AiPowerShellProfile
