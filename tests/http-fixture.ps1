#requires -Version 7.4
param([string]$Root, [switch]$ProductionEngine)
$ErrorActionPreference = 'Stop'
$server = Import-Module (Join-Path (Split-Path $PSScriptRoot -Parent) 'src/Panel.Server.psm1') -Force -PassThru
if (-not $IsWindows) {
    # Only test fixtures replace DPAPI on Linux; the installed Windows panel always uses DPAPI.
    & $server {
      $sessions = Get-Module Panel.Sessions
      & $sessions {
        function script:Protect-AiPanelSessions { param([byte[]]$Bytes) return ,([byte[]]$Bytes.Clone()) }
        function script:Unprotect-AiPanelSessions { param([byte[]]$Bytes) return ,([byte[]]$Bytes.Clone()) }
      }
    }
}
$engine = if ($ProductionEngine) { Join-Path $PSScriptRoot 'production-panel-engine.psm1' } else { Join-Path $PSScriptRoot 'mock-panel-engine.psm1' }
Start-OmniBoxPanel -Root $Root -EngineModulePath $engine -CatalogModulePath (Join-Path $PSScriptRoot 'mock-catalog.psm1')
