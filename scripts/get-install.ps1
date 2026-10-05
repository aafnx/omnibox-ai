#requires -Version 7.4
# Run on OmniBox in an elevated PowerShell SSH session. Token: repository Contents Read only.
$ErrorActionPreference = 'Stop'
$token = Read-Host 'GitHub token (omnibox-ai: Contents Read)' -AsSecureString
$headers = @{}
$file = Join-Path $env:TEMP ('omnibox-ai-install-' + [Guid]::NewGuid().ToString('N') + '.ps1')
try {
    $headers.Authorization = 'Bearer ' + ([pscredential]::new('github', $token)).GetNetworkCredential().Password
    $headers.Accept = 'application/vnd.github+json'
    $package = Invoke-RestMethod -Uri 'https://api.github.com/repos/aafnx/omnibox-ai/contents/dist/install-omnibox-ai.ps1?ref=main' -Headers $headers -MaximumRedirection 0 -TimeoutSec 60
    if ($package.type -ne 'file' -or $package.encoding -ne 'base64') { throw 'GitHub не вернул установочный файл.' }
    [IO.File]::WriteAllBytes($file, [Convert]::FromBase64String($package.content))
    $headers.Clear()
    & $file
} finally {
    $headers.Clear()
    $token.Dispose()
    if ([IO.File]::Exists($file)) { [IO.File]::Delete($file) }
}
