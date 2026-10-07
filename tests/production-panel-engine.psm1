# Test-only adapter: production dashboard, settings, telemetry and GPU collector.
# Only Windows SCM, native discovery and CIM are substituted.
$script:engine = Import-Module (Join-Path (Split-Path $PSScriptRoot -Parent) 'src/OmniBox.Ai.psm1') -PassThru
& $script:engine {
    function script:Get-AiService { param($Name) [pscustomobject]@{ Status = 'Stopped'; StartType = 'Manual' } }
    function script:Get-Command { param($Name) if ($Name -ne 'nvidia-smi') { throw 'Unexpected native lookup' } }
    function script:Get-CimInstance { throw 'No real hardware queries in this fixture' }
}
function Get-AiDashboard {
    param([string]$ConfigPath, [switch]$IncludeGpu)
    & $script:engine { param($ConfigPath, $IncludeGpu) Get-AiDashboard $ConfigPath -IncludeGpu:$IncludeGpu } $ConfigPath ([bool]$IncludeGpu)
}
function Get-AiTelemetry {
    param([string]$ConfigPath, [switch]$Snapshot)
    & $script:engine { param($ConfigPath, $Snapshot) Get-AiTelemetry $ConfigPath -Snapshot:$Snapshot } $ConfigPath ([bool]$Snapshot)
}
Export-ModuleMember -Function Get-AiDashboard, Get-AiTelemetry
