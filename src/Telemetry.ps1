# Read only, called on demand by the visible panel. No background sampler.
function Get-AiSystemSample {
    $sample=@{timestamp=[DateTime]::UtcNow.ToString('o');cpu_percent=$null;ram_total_gb=$null;ram_used_gb=$null;ram_percent=$null;gpu=@();errors=@()}
    if($IsWindows){
        try{
            $cpu=Get-CimInstance -ClassName Win32_PerfFormattedData_PerfOS_Processor -Filter "Name='_Total'" -OperationTimeoutSec 2 -ErrorAction Stop
            $sample.cpu_percent=[math]::Min(100,[math]::Max(0,[double]$cpu.PercentProcessorTime))
            $os=Get-CimInstance -ClassName Win32_OperatingSystem -OperationTimeoutSec 2 -ErrorAction Stop
            $total=[double]$os.TotalVisibleMemorySize;$used=$total-[double]$os.FreePhysicalMemory
            $sample.ram_total_gb=[math]::Round($total/1MB,2);$sample.ram_used_gb=[math]::Round($used/1MB,2);$sample.ram_percent=[math]::Round($used/$total*100,1)
        }catch{$sample.errors+= 'CPU/RAM: источник недоступен.'}
    }
    $sample.gpu=@(Get-AiGpuData)
    return $sample
}

function Get-AiTelemetry {
    # -Snapshot: one sample the user asked for (panel opened or refreshed), even with every engine stopped.
    # Periodic polling stays limited to running engines.
    param([string]$ConfigPath,[switch]$Snapshot)
    $config=Read-AiConfig $ConfigPath
    $running=@($config.backends.Values|Where-Object{$s=Get-AiService $_.service;$null -ne $s -and [string]$s.Status -in @('Running','StartPending')})
    if(-not $running.Count -and -not $Snapshot){return @{enabled=$false;timestamp=[DateTime]::UtcNow.ToString('o');reason='Серверы остановлены; опрос ресурсов выключен.'}}
    if('OmniBox.InferenceStats' -as [type]){
        foreach($name in $config.backends.Keys){$b=$config.backends[$name];if($b.type -eq 'prism' -and [OmniBox.InferenceStats]::IsActive($name)){
            $requestId=[OmniBox.InferenceStats]::ActiveId($name)
            try{$slots=@(Invoke-AiProbe $b '/slots');foreach($slot in $slots){if($slot.is_processing -and $slot.PSObject.Properties['next_token']){[OmniBox.InferenceStats]::ObserveSlot($name,[long]$slot.next_token.n_decoded,[int]$slot.n_ctx,$requestId)}}}catch{}
        }}
    }
    if($null -eq $script:systemSample -or ([DateTime]::UtcNow-$script:systemSampleTime).TotalSeconds -ge 3){
        $script:systemSample=Get-AiSystemSample;$script:systemSampleTime=[DateTime]::UtcNow
    }
    return @{enabled=$true;sample=$script:systemSample}
}
$script:systemSample=$null;$script:systemSampleTime=[DateTime]::MinValue
