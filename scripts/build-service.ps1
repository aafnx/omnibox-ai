#requires -Version 7.4
$ErrorActionPreference='Stop'
$root=Split-Path $PSScriptRoot -Parent
Add-Type -Path (Join-Path $root 'src/Service.Host.cs') -OutputAssembly (Join-Path $root 'src/Service.Host.dll')
