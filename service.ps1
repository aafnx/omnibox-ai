#requires -Version 7.4
param([Parameter(Mandatory)][string]$Profile)
$ErrorActionPreference='Stop'
try {
Add-Type -Path (Join-Path $PSScriptRoot 'src/Service.Host.dll')
$settings=[Xml.XmlReaderSettings]::new();$settings.DtdProcessing=[Xml.DtdProcessing]::Prohibit;$settings.XmlResolver=$null
$reader=[Xml.XmlReader]::Create($Profile,$settings)
try{$xml=[Xml.XmlDocument]::new();$xml.Load($reader)}finally{$reader.Dispose()}
$base=Split-Path ([IO.Path]::GetFullPath($Profile)) -Parent
$expand={param($v) [Environment]::ExpandEnvironmentVariables(([string]$v).Replace('%BASE%',$base))}
$environment=[Collections.Generic.Dictionary[string,string]]::new()
foreach($e in $xml.SelectNodes('/service/env')){$environment[$e.GetAttribute('name')]=& $expand $e.GetAttribute('value')}
[OmniBox.ServiceHost]::Run([string]$xml.service.id,(& $expand $xml.service.executable),(& $expand $xml.service.arguments),(& $expand $xml.service.workingdirectory),(& $expand $xml.service.logpath),[IO.Path]::GetFileNameWithoutExtension($Profile),($Profile+'.runtime.json'),$environment)

} catch {
    # Failures before the SCM callback (assembly/XML/interop) also need a durable log.
    try {
        $logDirectory = Join-Path (Split-Path ([IO.Path]::GetFullPath($Profile)) -Parent) 'logs'
        $null = New-Item -ItemType Directory -Path $logDirectory -Force
        $logFile = Join-Path $logDirectory ([IO.Path]::GetFileNameWithoutExtension($Profile) + '.bootstrap.log')
        $message = [DateTime]::UtcNow.ToString('o') + ' ' + $_.Exception.ToString() + "`n" + $_.ScriptStackTrace + "`n"
        [IO.File]::AppendAllText($logFile, $message, [Text.UTF8Encoding]::new($false))
    } catch { }
    throw
}
