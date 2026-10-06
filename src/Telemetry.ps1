# Read only, called on demand by the visible panel. No background sampler.
function Read-AiCpuTemperature {
    if(-not $IsWindows){return $null}
    foreach($namespace in @('root/LibreHardwareMonitor','root/OpenHardwareMonitor')){
        try {
            $sensors=@(Get-CimInstance -Namespace $namespace -ClassName Sensor -Filter "SensorType='Temperature'" -OperationTimeoutSec 1 -ErrorAction Stop)
            $values=@($sensors|Where-Object{($_.Identifier -match '/(amd|intel)?cpu/' -or $_.Name -match '(?i)CPU.*(Package|Tctl|Tdie)|CPU Core') -and $_.Value -gt 0 -and $_.Value -lt 120}|ForEach-Object{[double]$_.Value})
            if($values.Count){return [math]::Round(($values|Measure-Object -Maximum).Maximum,1)}
        }catch{}
    }
    return $null
}

function Get-AiSystemSample {
    $sample=@{timestamp=[DateTime]::UtcNow.ToString('o');cpu_percent=$null;ram_total_gb=$null;ram_used_gb=$null;ram_percent=$null;cpu_temperature=$null;gpu=@();errors=@()}
    if($IsWindows){
        try{
            $cpu=Get-CimInstance -ClassName Win32_PerfFormattedData_PerfOS_Processor -Filter "Name='_Total'" -OperationTimeoutSec 2 -ErrorAction Stop
            $sample.cpu_percent=[math]::Min(100,[math]::Max(0,[double]$cpu.PercentProcessorTime))
            $os=Get-CimInstance -ClassName Win32_OperatingSystem -OperationTimeoutSec 2 -ErrorAction Stop
            $total=[double]$os.TotalVisibleMemorySize;$used=$total-[double]$os.FreePhysicalMemory
            $sample.ram_total_gb=[math]::Round($total/1MB,2);$sample.ram_used_gb=[math]::Round($used/1MB,2);$sample.ram_percent=[math]::Round($used/$total*100,1)
        }catch{$sample.errors+= 'CPU/RAM: источник недоступен.'}
        $sample.cpu_temperature=Read-AiCpuTemperature
    }
    $sample.gpu=@(Get-AiGpuData)
    return $sample
}

function Get-AiTelemetry {
    param([string]$ConfigPath)
    $config=Read-AiConfig $ConfigPath
    $running=@($config.backends.Values|Where-Object{$s=Get-AiService $_.service;$null -ne $s -and [string]$s.Status -in @('Running','StartPending')})
    if(-not $running.Count){return @{enabled=$false;timestamp=[DateTime]::UtcNow.ToString('o');reason='Серверы остановлены; опрос ресурсов выключен.'}}
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
