#requires -Version 7.4
$ErrorActionPreference='Stop'
$root=Split-Path $PSScriptRoot -Parent
$source=Join-Path $root 'src/Service.Host.cs'
$output=Join-Path $root 'src/Service.Host.exe'
if($IsWindows){
    $compiler=Join-Path $env:WINDIR 'Microsoft.NET/Framework64/v4.0.30319/csc.exe'
    if(-not(Test-Path -LiteralPath $compiler)){$compiler=Join-Path $env:WINDIR 'Microsoft.NET/Framework/v4.0.30319/csc.exe'}
    if(-not(Test-Path -LiteralPath $compiler)){throw 'Windows .NET Framework compiler is unavailable.'}
    $diagnostics=@(& $compiler /nologo /target:exe /platform:anycpu /optimize+ "/out:$output" /reference:System.Xml.dll $source 2>&1)
    if($LASTEXITCODE -ne 0 -or -not(Test-Path -LiteralPath $output)){throw ('Cannot compile Windows service executable.'+"`n"+($diagnostics -join "`n"))}
    # Compiler warnings are diagnostics, not function return values (the stage path is returned by the caller).
    foreach($line in $diagnostics){Write-Verbose ([string]$line)}
}else{
    # CI syntax/PE validation only; Windows uses the inbox Framework compiler above.
    $null=Add-Type -TypeDefinition 'public class OmniBoxCompilerBootstrap {}'
    $tree=[Microsoft.CodeAnalysis.CSharp.CSharpSyntaxTree]::ParseText([IO.File]::ReadAllText($source),[Microsoft.CodeAnalysis.CSharp.CSharpParseOptions]::new([Microsoft.CodeAnalysis.CSharp.LanguageVersion]::CSharp5))
    $paths=[AppContext]::GetData('TRUSTED_PLATFORM_ASSEMBLIES') -split [IO.Path]::PathSeparator
    $references=[Microsoft.CodeAnalysis.MetadataReference[]]@($paths|ForEach-Object{[Microsoft.CodeAnalysis.MetadataReference]::CreateFromFile($_)})
    $options=[Microsoft.CodeAnalysis.CSharp.CSharpCompilationOptions]::new([Microsoft.CodeAnalysis.OutputKind]::ConsoleApplication)
    $compilation=[Microsoft.CodeAnalysis.CSharp.CSharpCompilation]::Create('Service.Host',[Microsoft.CodeAnalysis.SyntaxTree[]]@($tree),$references,$options)
    $stream=[IO.File]::Create($output)
    try{$result=$compilation.Emit($stream);if(-not $result.Success){throw (($result.Diagnostics|ForEach-Object ToString) -join "`n")}}finally{$stream.Dispose()}
}
