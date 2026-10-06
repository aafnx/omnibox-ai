# HTTP/UI integration fixture only. Never installed or included in the release package.
function Get-AiDashboard {
    param([string]$ConfigPath, [switch]$IncludeGpu)
    $state = Get-Content $ConfigPath -Raw | ConvertFrom-Json -AsHashtable
    $settings=@{context=$state.context;idle_minutes=30;model='C:\ai\models\bonsai\bonsai-2-27b-pq2_0.gguf';alias='bonsai-2-27b';gpu_layers='99';cache_key='q8_0';cache_value='q8_0';flash_attention='on';batch='512';ubatch='128'}
    $gpu=@();if($IncludeGpu){$gpu=@(@{name='NVIDIA GeForce RTX 5080';total=16303;used=15020;free=958})}
    return @{version='0.5.0';computer='OMNIBOX';default_backend='bonsai';exclusive=$true;gpu=$gpu;backends=@(
        @{name='bonsai';type='prism';service=$state.service;startup='Manual';api='Ready';active_context=[int]$state.context;sleeping=$false;loaded_model='bonsai-2-27b-pq2_0.gguf';settings=$settings;models=@(@{name='bonsai-2-27b-pq2_0.gguf';id=$settings.model;bytes=7800000000});pending=$false;can_restore=$true;errors=@();endpoint='http://127.0.0.1:18080/v1'},
        @{name='ollama';type='ollama';service='Stopped';startup='Manual';api='-';active_context=$null;sleeping=$null;loaded_model='(none)';settings=@{context='32768';idle_minutes=30;keep_alive='30m'};models=@();pending=$false;can_restore=$false;errors=@();endpoint='http://127.0.0.1:11434/v1'})}
}
function Invoke-AiPanelAction {
    param([string]$ConfigPath,$Request)
    if($Request.action -eq 'download'){Start-Sleep -Milliseconds 1600;return 'Скачивание завершено.'}
    Start-Sleep -Milliseconds 350
    $state = Get-Content $ConfigPath -Raw | ConvertFrom-Json -AsHashtable
    if($Request.action -in @('apply','save')){$state.context=[string]$Request.settings.context;if($state.context -match '^(\d+)k$'){$state.context=[string]([int]$Matches[1]*1024)}}
    if($Request.action -in @('stopall','stop','unload')){$state.service='Stopped'}
    if($Request.action -in @('start','load','apply')){$state.service='Running'}
    $state | ConvertTo-Json | Set-Content $ConfigPath
    return 'Операция завершена.'
}
Export-ModuleMember -Function Get-AiDashboard,Invoke-AiPanelAction
