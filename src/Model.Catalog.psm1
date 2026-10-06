Set-StrictMode -Version Latest

function Invoke-AiHubApi {
    param([string]$Path)
    Invoke-RestMethod -Uri ('https://huggingface.co/api/models' + $Path) -TimeoutSec 20 -MaximumRedirection 0 -ErrorAction Stop
}

function Search-AiModelCatalog {
    param([string]$Query)
    if ($Query.Length -lt 2 -or $Query.Length -gt 100 -or $Query -match '[\x00-\x1f]') { throw 'Запрос поиска: от 2 до 100 символов.' }
    $items = @(Invoke-AiHubApi ('?search=' + [Uri]::EscapeDataString($Query) + '&filter=gguf&limit=12&sort=downloads&direction=-1&full=true'))
    $rows = @()
    foreach ($item in $items) {
        $id = if($item.PSObject.Properties['id']){[string]$item.id}else{[string]$item.modelId}
        if($id -notmatch '^[a-zA-Z0-9_.-]+/[a-zA-Z0-9_.-]+$' -or $id.Contains('..')){continue}
        $downloads=if($item.PSObject.Properties['downloads']){$item.downloads}else{0}
        $gated=if($item.PSObject.Properties['gated']){[bool]$item.gated}else{$false}
        $rows+=@{id=$id;name=$id;url="https://huggingface.co/$id";downloads=$downloads;gated=$gated}
    }
    return @{models=$rows;source='Hugging Face';query=$Query}
}

function Get-AiCatalogFiles {
    param([string]$Repository)
    if($Repository -notmatch '^[a-zA-Z0-9_.-]+/[a-zA-Z0-9_.-]+$' -or $Repository.Contains('..')){throw 'Нужен Hugging Face repo в формате author/model.'}
    $info=Invoke-AiHubApi ('/' + $Repository + '?blobs=true')
    $revision=if($info.PSObject.Properties['sha'] -and $info.sha -match '^[a-f0-9]{40}$'){$info.sha}else{'main'}
    $files=@()
    foreach($file in @($info.siblings)){
        $name=[string]$file.rfilename
        if($name -notmatch '\.gguf$' -or $name -match '(?i)mmproj|projector' -or $name.Contains('..') -or $name.StartsWith('/')){continue}
        $bytes=if($file.PSObject.Properties['size']){$file.size}else{$null}
        $hash='';if($file.PSObject.Properties['lfs'] -and $file.lfs -and $file.lfs.PSObject.Properties['sha256']){$hash=[string]$file.lfs.sha256}
        $basename=[IO.Path]::GetFileName($name)
        $filename=([regex]::Replace(($Repository.Replace('/','-')+'-'+$basename),'[^a-zA-Z0-9_.-]','-'))
        if($filename.Length -gt 165){$filename=$filename.Substring(0,150)+'.gguf'}
        $encoded=($name -split '/' | ForEach-Object{[Uri]::EscapeDataString($_)}) -join '/'
        $files+=@{name=$name;bytes=$bytes;sha256=$hash;url="https://huggingface.co/$Repository/resolve/$revision/$encoded";filename=$filename;split=[bool]($name -match '-\d{5}-of-\d{5}\.gguf$');prism_only=[bool]($name -match '(?i)PQ2|PTQ1|Bonsai-2')}
        if($files.Count -ge 80){break}
    }
    return @{repository=$Repository;files=$files;url="https://huggingface.co/$Repository"}
}

Export-ModuleMember -Function Search-AiModelCatalog, Get-AiCatalogFiles
