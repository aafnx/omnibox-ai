#requires -Version 7.4
param([string]$Root)
$ErrorActionPreference = 'Stop'
Import-Module (Join-Path (Split-Path $PSScriptRoot -Parent) 'src/Panel.Server.psm1') -Force
Start-OmniBoxPanel -Root $Root -EngineModulePath (Join-Path $PSScriptRoot 'mock-panel-engine.psm1')
