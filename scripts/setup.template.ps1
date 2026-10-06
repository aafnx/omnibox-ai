#requires -Version 7.4
<#
.SYNOPSIS
Self-contained interactive installer. Run on the Windows PC through SSH.
.EXAMPLE
& .\install-omnibox-ai.ps1
.EXAMPLE
& .\install-omnibox-ai.ps1 -NonInteractive -DefaultBackend bonsai -Context 192k
#>
[CmdletBinding()]
param([string]$InstallDirectory = 'C:\tools\ai', [switch]$NonInteractive, [switch]$Reconfigure,
    [string]$DefaultBackend = '', [string]$Context = '', [switch]$StartServer,
    [switch]$NoProfileUpdate, [switch]$CheckOnly, [string]$PanelHost = '', [int]$PanelPort = 18090,
    [string]$PanelRemoteAddresses = 'LocalSubnet', [switch]$NoPanelSetup,
    [string]$PanelPublicUrl = '', [string]$PanelTrustedProxyAddresses = '', [int]$ApiPort = 18090, [string]$ApiBaseUrl = '', [switch]$NoApi)

$ErrorActionPreference = 'Stop'
$global:LASTEXITCODE = 0
$expectedSha = '__PAYLOAD_SHA256__'
$payload = '__PAYLOAD_BASE64__'
$payloadBytes = [Convert]::FromBase64String($payload)
$actualSha = [Convert]::ToHexString([Security.Cryptography.SHA256]::HashData($payloadBytes)).ToLowerInvariant()
if ($actualSha -cne $expectedSha) { throw 'Встроенный пакет повреждён: SHA256 не совпадает.' }
if ($CheckOnly) { Write-Output "OmniBox AI __VERSION__ payload OK ($($payloadBytes.Length) bytes)"; return }
if (-not $IsWindows) { throw 'Установщик предназначен для Windows.' }

$temporaryDirectory = Join-Path $env:TEMP ('omnibox-ai-package-' + [Guid]::NewGuid().ToString('N'))
$null = New-Item -ItemType Directory -Path $temporaryDirectory
try {
    $zipPath = Join-Path $temporaryDirectory 'package.zip'
    [IO.File]::WriteAllBytes($zipPath, $payloadBytes)
    $sourcePath = Join-Path $temporaryDirectory 'source'
    Expand-Archive -LiteralPath $zipPath -DestinationPath $sourcePath
    $installArgs = @{ InstallDirectory = $InstallDirectory; NonInteractive = $NonInteractive; Reconfigure = $Reconfigure; DefaultBackend = $DefaultBackend; Context = $Context; StartServer = $StartServer; NoProfileUpdate = $NoProfileUpdate; PanelHost = $PanelHost; NoPanelSetup = $NoPanelSetup }
    if ($PSBoundParameters.ContainsKey('PanelRemoteAddresses')) { $installArgs.PanelRemoteAddresses = $PanelRemoteAddresses }
    if ($PSBoundParameters.ContainsKey('PanelPort')) { $installArgs.PanelPort = $PanelPort }
    if ($PSBoundParameters.ContainsKey('PanelPublicUrl')) { $installArgs.PanelPublicUrl = $PanelPublicUrl }
    if ($PSBoundParameters.ContainsKey('PanelTrustedProxyAddresses')) { $installArgs.PanelTrustedProxyAddresses = $PanelTrustedProxyAddresses }
    $installArgs.NoApi=$NoApi
    if($PSBoundParameters.ContainsKey('ApiPort')){$installArgs.ApiPort=$ApiPort}
    if($PSBoundParameters.ContainsKey('ApiBaseUrl')){$installArgs.ApiBaseUrl=$ApiBaseUrl}
    & (Join-Path $sourcePath 'install.ps1') @installArgs
    Write-Host 'Установка завершена. Доступные команды: ai help'
} finally {
    if ((Split-Path $temporaryDirectory -Leaf) -match '^omnibox-ai-package-[a-f0-9]{32}$') {
        Remove-Item -LiteralPath $temporaryDirectory -Recurse -Force
    }
}
