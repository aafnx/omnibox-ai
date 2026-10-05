#requires -Version 5.1
<#
.SYNOPSIS
Self-contained OmniBox AI installer. No GitHub token or Internet download required.
.EXAMPLE
& "$env:USERPROFILE\Downloads\install-omnibox-ai.ps1" -Remote
.EXAMPLE
& .\install-omnibox-ai.ps1
Run directly on OmniBox in an elevated SSH session.
#>
[CmdletBinding()]
param(
    [switch]$Remote,
    [string]$SshTarget = 'avoq@192.168.1.77',
    [string]$IdentityFile = '',
    [string]$InstallDirectory = 'C:\tools\ai',
    [switch]$CheckOnly
)

$ErrorActionPreference = 'Stop'
$global:LASTEXITCODE = 0
$PSNativeCommandArgumentPassing = 'Standard'
$expectedSha = '__PAYLOAD_SHA256__'
$payload = '__PAYLOAD_BASE64__'
$payloadBytes = [Convert]::FromBase64String($payload)
$hasher = [Security.Cryptography.SHA256]::Create()
try { $actualSha = [BitConverter]::ToString($hasher.ComputeHash($payloadBytes)).Replace('-', '').ToLowerInvariant() }
finally { $hasher.Dispose() }
if ($actualSha -cne $expectedSha) { throw 'Встроенный пакет повреждён: SHA256 не совпадает.' }
if ($CheckOnly) { Write-Output "OmniBox AI 0.2.0 payload OK ($($payloadBytes.Length) bytes)"; return }
if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) { throw 'Установщик предназначен для Windows.' }

if ($Remote) {
    if ($SshTarget -notmatch '^[a-zA-Z0-9_.-]+@[a-zA-Z0-9_.-]+$') { throw 'SshTarget: user@host, без пробелов и параметров.' }
    if (-not $IdentityFile) { $IdentityFile = Join-Path $env:USERPROFILE '.ssh\id_ed25519_omnibox' }
    if (-not (Test-Path -LiteralPath $IdentityFile -PathType Leaf)) { throw "Нет SSH-ключа: $IdentityFile" }
    $ssh = (Get-Command ssh.exe -CommandType Application -ErrorAction Stop).Source
    $scp = (Get-Command scp.exe -CommandType Application -ErrorAction Stop).Source
    $remoteName = 'omnibox-ai-setup-' + [Guid]::NewGuid().ToString('N') + '.ps1'
    Write-Host "Передача установщика на $SshTarget..."
    & $scp -i $IdentityFile $PSCommandPath "${SshTarget}:$remoteName"
    if ($LASTEXITCODE -ne 0) { throw 'Не удалось передать скрипт по SCP.' }
    $escapedDirectory = $InstallDirectory.Replace("'", "''")
    $remoteCommand = "`$ErrorActionPreference = 'Stop'; `$global:LASTEXITCODE = 0; try { `$file = Join-Path `$env:USERPROFILE '$remoteName'; & `$file -InstallDirectory '$escapedDirectory'; if (`$LASTEXITCODE -ne 0) { throw 'Установка не завершена' }; Remove-Item -LiteralPath `$file; & (Join-Path '$escapedDirectory' 'ai.ps1') version; exit `$LASTEXITCODE } catch { [Console]::Error.WriteLine(`$_.Exception.Message); exit 1 }"
    $encoded = [Convert]::ToBase64String([Text.Encoding]::Unicode.GetBytes($remoteCommand))
    & $ssh -i $IdentityFile $SshTarget "pwsh -NoLogo -NoProfile -ExecutionPolicy Bypass -OutputFormat Text -EncodedCommand $encoded"
    if ($LASTEXITCODE -ne 0) { throw 'Удалённая установка не завершена; сообщения выше содержат причину.' }
    Write-Host 'Установка завершена. Переподключись по SSH к OmniBox, затем ai help и ai status.'
    return
}

if ($PSVersionTable.PSVersion -lt [Version]'7.4') { throw 'Для локальной установки на OmniBox нужен PowerShell 7.4+. С ноутбука используй -Remote.' }

$temporaryDirectory = Join-Path $env:TEMP ('omnibox-ai-package-' + [Guid]::NewGuid().ToString('N'))
$null = New-Item -ItemType Directory -Path $temporaryDirectory
try {
    $zipPath = Join-Path $temporaryDirectory 'package.zip'
    [IO.File]::WriteAllBytes($zipPath, $payloadBytes)
    $sourcePath = Join-Path $temporaryDirectory 'source'
    Expand-Archive -LiteralPath $zipPath -DestinationPath $sourcePath
    & (Join-Path $sourcePath 'install.ps1') -InstallDirectory $InstallDirectory
    Write-Host 'Установка завершена. Доступные команды: ai help'
} finally {
    # Only our own, exact GUID directory, never a model directory or a workspace root.
    if ((Split-Path $temporaryDirectory -Leaf) -match '^omnibox-ai-package-[a-f0-9]{32}$') {
        Remove-Item -LiteralPath $temporaryDirectory -Recurse -Force
    }
}
