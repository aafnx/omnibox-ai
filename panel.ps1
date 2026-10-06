#requires -Version 7.4
[CmdletBinding()]
param([string]$PanelConfigPath = (Join-Path $PSScriptRoot 'panel.json'))
$ErrorActionPreference = 'Stop'
$global:LASTEXITCODE = 0
Import-Module (Join-Path $PSScriptRoot 'src/Panel.Server.psm1') -Force
Start-OmniBoxPanel -Root $PSScriptRoot -PanelConfigPath $PanelConfigPath
