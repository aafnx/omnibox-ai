#requires -Version 7.4
# Run on the Windows PC in elevated PowerShell; use existing gh login when available.
[CmdletBinding()]
param([string]$InstallDirectory='', [switch]$UseToken)
$ErrorActionPreference='Stop'
Import-Module (Join-Path (Split-Path $PSScriptRoot -Parent) 'src/Update.Helpers.psm1') -Force
Invoke-AiUpdate -InstallDirectory $InstallDirectory -UseToken:$UseToken
