#requires -Version 7.4
# Compatibility launcher for manual use; SCM registers Service.Host.exe directly.
param([Parameter(Mandatory)][string]$Profile)
$ErrorActionPreference='Stop'
& (Join-Path $PSScriptRoot 'src/Service.Host.exe') -Profile $Profile
if($LASTEXITCODE -ne 0){throw "Service.Host.exe exited with code $LASTEXITCODE"}
