// Execute the real UI code against a small DOM adapter; no browser/rendering claim.
const fs=require('fs'),vm=require('vm'),assert=require('node:assert/strict');
class Node {
  constructor(id=''){this.id=id;this.children=[];this.listeners={};this.dataset={};this.value='';this.hidden=false;this.textContent='';this.className='';this.map={};this.classList={toggle:()=>{}};this.attributes={};}
  setAttribute(name,value){this.attributes[name]=String(value);}
  addEventListener(type,fn){(this.listeners[type]??=[]).push(fn);}
  append(...nodes){this.children.push(...nodes);}
  replaceChildren(...nodes){this.children=nodes;}
  add(option){this.children.push(option);}
  contains(){return false;}
  querySelector(key){return this.map[key]??=new Node(key);}
  querySelectorAll(key){if(key==='[data-action]')return ['start','stop','settings'].map(action=>{const n=this.querySelector(action);n.dataset.action=action;return n;});return [];}
  cloneNode(){return new Node(this.id);}
  reportValidity(){return true;}
}
const html=fs.readFileSync('web/index.html','utf8'),source=fs.readFileSync('web/app.js','utf8');
const ids=[...html.matchAll(/\bid="([^"]+)"/g)].map(m=>m[1]);assert.equal(new Set(ids).size,ids.length);
const nodes=Object.fromEntries(ids.map(id=>[id,new Node(id)]));
nodes['engine-template'].content={firstElementChild:new Node('engine-card')};
const store=new Map(),requests=[],bodies=[];
const context=vm.createContext({document:{documentElement:{dataset:{}},getElementById:id=>{assert.ok(nodes[id],`Missing UI element ${id}`);return nodes[id]},body:new Node('body'),createElement:()=>new Node(),querySelectorAll:()=>[],addEventListener:()=>{},hidden:false,activeElement:null},window:{matchMedia:()=>({matches:false,addEventListener:()=>{}})},localStorage:{getItem:k=>store.get(k),setItem:(k,v)=>store.set(k,v)},navigator:{},Option:function(text,value){return{text,value}},fetch:async (path,options)=>{requests.push(path);bodies.push(options&&options.body);return{ok:false,status:401,json:async()=>({error:'Login required'})}},setTimeout:()=>1,clearTimeout:()=>{},setInterval:()=>1,Map,Set,Date,Number,Promise,URLSearchParams,console});
function run(code){return vm.runInContext(code,context);}
(async()=>{
  run(source);await new Promise(setImmediate);
  assert.equal(context.document.documentElement.dataset.theme,'light');
  assert.equal(nodes['theme-system'].attributes['aria-pressed'],'true');assert.equal(nodes['theme-light'].attributes['aria-pressed'],'false');
  nodes['theme-dark'].listeners.click[0]();
  assert.equal(context.document.documentElement.dataset.theme,'dark');assert.equal(store.get('omnibox-theme'),'dark');
  assert.equal(nodes['theme-dark'].attributes['aria-pressed'],'true');assert.equal(nodes['theme-light'].attributes['aria-pressed'],'false');
  run("placeThemeSwitch(true)");assert.ok(nodes['topbar-actions'].children.includes(nodes['theme-switch']));
  run("placeThemeSwitch(false)");assert.ok(context.document.body.children.includes(nodes['theme-switch']));
  run("applyTheme('system')");assert.equal(context.document.documentElement.dataset.theme,'light');
  run("snapshot={version:'0.18.0',backends:[{name:'prismml',type:'prismml',service:'Running',api:'Ready',sleeping:false,loaded_model:'model.gguf',selected_model:'model',settings:{context:32768,alias:'model'},models:[],errors:[]}],operations:[]};selectedBackend='prismml';renderEngines();");
  assert.equal(nodes.engines.children[0].querySelector('start').hidden,true);assert.equal(nodes.engines.children[0].querySelector('stop').hidden,false);
  // CPU placement keeps the KV cache in RAM; leaving it restores GPU offload.
  nodes.placement.value='cpu';run("syncKvOffload(true)");assert.equal(nodes['kv-offload'].value,'off');assert.equal(nodes['kv-offload'].disabled,true);
  nodes.placement.value='gpu';run("syncKvOffload(true)");assert.equal(nodes['kv-offload'].value,'on');assert.equal(nodes['kv-offload'].disabled,false);
  nodes['kv-offload'].value='off';run("syncKvOffload(false)");assert.equal(nodes['kv-offload'].value,'off');
  // Disk usage: warning only above the configured log volume; unknown retention values stay selectable.
  nodes['log-retention'].options=['h6','d1','d7','d30','0','custom'].map(value=>({value}));
  run("renderStorage({logs_bytes:5*1024**2,log_files:4,temp_bytes:0,log_retention_days:30,log_warn_mb:200,logs_over_limit:false})");
  assert.equal(nodes['storage-warning'].hidden,true);assert.ok(nodes['storage-summary'].textContent.includes('5.0 МБ (файлов: 4)'));assert.equal(nodes['log-warn'].value,200);
  assert.equal(nodes['log-retention'].value,'d30');assert.equal(nodes['log-retention-custom'].hidden,true);
  run("renderStorage({logs_bytes:300*1024**2,log_files:9,temp_bytes:0,log_retention_days:45,log_warn_mb:200,logs_over_limit:true})");
  assert.equal(nodes['storage-warning'].hidden,false);assert.ok(nodes['storage-warning'].textContent.includes('200 МБ'));
  assert.equal(nodes['log-retention'].value,'custom');assert.equal(nodes['log-retention-value'].value,45);assert.equal(nodes['log-retention-unit'].value,'days');
  // Retention in hours: the 6-hour preset, or the owner's own hours.
  run("renderStorage({logs_bytes:0,log_files:0,log_retention_hours:6,log_retention_days:0,log_warn_mb:200})");assert.equal(nodes['log-retention'].value,'h6');
  assert.equal(run("JSON.stringify(retentionBody())"),'{"log_retention_hours":6}');
  nodes['log-retention'].value='custom';nodes['log-retention-value'].value='12';nodes['log-retention-unit'].value='hours';assert.equal(run("JSON.stringify(retentionBody())"),'{"log_retention_hours":12}');
  nodes['log-retention'].value='d1';assert.equal(run("JSON.stringify(retentionBody())"),'{"log_retention_days":1}');
  // Request content: openable only in content mode; messages, reasoning and the answer are shown as text.
  assert.equal(run("contentCell({id:'a'})").textContent,'—');run("requestLog='content'");assert.equal(run("contentCell({id:'a'})").children[0].textContent,'Открыть');run("requestLog='metadata'");
  const shown=run("formatRequestContent({model:'m',route:'/v1/chat/completions',started:new Date().toISOString(),status:200,truncated:false,request:{messages:[{role:'user',content:'hi'}]},reasoning_text:'think',response_text:'answer'})");
  assert.ok(shown.includes('[user] hi')&&shown.includes('РАЗМЫШЛЕНИЯ\n\nthink')&&shown.includes('ОТВЕТ\n\nanswer'));
  // Engine manager: status text, install vs update actions and version list after a lookup.
  assert.equal(run("engineStatusText({installed:false})"),'Не установлен');assert.equal(run("engineStatusText({installed:true,state:'Stopped',version:''})"),'Установлен вне OmniBox');
  run("engineReleases={prismml:[{tag:'b2',published:'2026-10-02T00:00:00Z',prerelease:false},{tag:'b1',published:'2026-09-01T00:00:00Z',prerelease:true}]};renderEngineManager([{name:'prismml',label:'PrismML',installed:true,state:'Stopped',version:'b2',managed:true,versions:['b2'],pending:false},{name:'ollama',label:'Ollama',installed:false,state:'NotInstalled',version:'',managed:false,versions:[]}])");
  const [prismBox,ollamaBox]=nodes['engine-manager'].children;const prismRow=prismBox.children[2],ollamaRow=ollamaBox.children[2];
  assert.equal(prismBox.children[0].children[1].textContent,'Версия b2');assert.equal(prismRow.children[0].children.length,2);assert.ok(prismRow.children[0].children[0].text.includes('установлена'));
  assert.ok(prismBox.children[1].textContent.includes('omnibox-prismml'));
  assert.equal(prismRow.children[3].textContent,'Обновить до последней');assert.equal(prismRow.children[4].textContent,'Удалить');
  assert.equal(ollamaRow.children[0].hidden,true);assert.equal(ollamaRow.children[3].textContent,'Установить последнюю');assert.equal(ollamaRow.children.length,4);
  const engineCalls=[];context.window.confirm=()=>true;const realAction=run('action');run('action=async body=>{globalThis.__sent.push(body);}');context.__sent=engineCalls;
  await run("engineAction({installed:true,label:'PrismML',name:'prismml'},'b1')");await run("engineAction({installed:false,label:'Ollama',name:'ollama'},'')");await run("removeEngine({label:'PrismML',name:'prismml'})");
  assert.equal(JSON.stringify(engineCalls),JSON.stringify([{action:'engine-update',engine:'prismml',version:'b1'},{action:'engine-install',engine:'ollama',version:''},{action:'engine-remove',engine:'prismml'}]));
  context.__realAction=realAction;run('action=globalThis.__realAction');
  // Drives and model sizes: one row per drive, warning for a nearly full model drive.
  run("renderDrives({models:[{name:'prismml',engine:'PrismML',bytes:29*1024**3,models:3,drive:'D:'},{name:'ollama',engine:'Ollama',bytes:45*1024**3,models:4,drive:'C:'}],drives:[{name:'C:',label:'System',total:1000*1024**3,free:400*1024**3,used:600*1024**3,roles:['OmniBox AI'],low:false},{name:'D:',label:'',total:2000*1024**3,free:15*1024**3,used:1985*1024**3,roles:['модели PrismML'],low:true}]})");
  assert.equal(nodes['storage-models'].textContent,'Модели: PrismML 29.0 ГБ (3) · Ollama 45.0 ГБ (4)');
  const [driveC,driveD]=nodes['storage-drives'].children;assert.equal(driveC.className,'drive');assert.equal(driveD.className,'drive low');
  assert.equal(driveC.children[1].value,60);assert.ok(driveD.children[0].children[1].textContent.includes('из 1.95 ТБ'));assert.ok(driveD.children[2].textContent.includes('мало свободного места'));
  assert.equal(nodes['model-disk'].hidden,false);assert.ok(nodes['model-disk'].textContent.startsWith('Свободно на диске моделей D:: 15.0 ГБ'));
  // The download form uses the drive of the selected engine's models, even when another engine's drive has more room.
  run("renderDrives({models:[{name:'ollama',engine:'Ollama',bytes:45*1024**3,models:4,drive:'C:'},{name:'prismml',engine:'PrismML',bytes:29*1024**3,models:3,drive:'D:'}],drives:[{name:'C:',label:'',total:1000*1024**3,free:400*1024**3,used:600*1024**3,roles:['модели Ollama'],low:false},{name:'D:',label:'',total:100*1024**3,free:5*1024**3,used:95*1024**3,roles:['модели PrismML'],low:true}]})");
  assert.ok(nodes['model-disk'].textContent.startsWith('Свободно на диске моделей D:: 5.0 ГБ · модели PrismML'),nodes['model-disk'].textContent);
  run("selectedBackend='ollama';renderModelDisk()");assert.ok(nodes['model-disk'].textContent.startsWith('Свободно на диске моделей C:: 400.0 ГБ · модели Ollama'));
  run("selectedBackend='missing';renderModelDisk()");assert.equal(nodes['model-disk'].hidden,true);run("selectedBackend='prismml';renderModelDisk()");
  run("renderModelSummary()");assert.equal(nodes['kpi-model'].textContent,'model.gguf');assert.ok(nodes['kpi-model-caption'].textContent.startsWith('PrismML · '));
  assert.equal(nodes.engines.children[0].querySelector('h2').textContent,'PrismML');assert.equal(run("engineName('unknown')"),'unknown');
  // Basic mode hides advanced groups; the choice is remembered per browser.
  run("applySettingsMode()");assert.equal(nodes.advanced.hidden,true);assert.equal(nodes.generation.hidden,true);assert.equal(nodes['mode-hint'].hidden,false);assert.equal(nodes['settings-mode'].hidden,false);
  nodes['mode-pro'].listeners.click[0]();assert.equal(nodes.advanced.hidden,false);assert.equal(nodes.generation.hidden,false);assert.equal(store.get('omnibox-settings-mode'),'pro');assert.equal(nodes['mode-pro'].attributes['aria-pressed'],'true');
  nodes['mode-basic'].listeners.click[0]();assert.equal(nodes.advanced.hidden,true);assert.equal(store.get('omnibox-settings-mode'),'basic');
  // Stopping everything interrupts generation, so a running server needs confirmation.
  let asked=0;context.window.confirm=()=>{asked++;return false;};const sent=requests.length;
  nodes['stop-all'].listeners.click[0]();await new Promise(setImmediate);assert.equal(asked,1);assert.equal(requests.length,sent);
  // The model in memory can be unloaded from its card.
  assert.equal(nodes['kpi-unload'].hidden,false);context.window.confirm=()=>true;const unloadBefore=requests.length;nodes['kpi-unload'].listeners.click[0]();await new Promise(r=>setTimeout(r,0));
  assert.deepEqual(JSON.parse(bodies[unloadBefore]),{action:'unload',backend:'prismml'});
  // A sleeping engine shows no model in memory.
  run("snapshot.backends[0].sleeping=true;renderModelSummary()");assert.equal(nodes['kpi-model'].textContent,'Нет');assert.ok(nodes['kpi-model-caption'].textContent.includes('выгружена'));assert.equal(nodes['kpi-unload'].hidden,true);
  run("snapshot.backends[0].sleeping=false");
  run("snapshot.backends[0].service='Stopped';renderModelSummary()");assert.equal(nodes['kpi-model'].textContent,'Нет');
  nodes['stop-all'].listeners.click[0]();await new Promise(setImmediate);assert.equal(asked,1);assert.ok(requests.length>sent);
  run("snapshot.backends[0].service='Running';renderEngines()");
  assert.equal(run('Boolean(monitoringActive())'),true);
  run("snapshot.backends[0].sleeping=true");assert.equal(run('Boolean(monitoringActive())'),false);
  run("snapshot.backends[0].sleeping=false;snapshot.backends[0].service='Stopped';snapshot.operations=[{id:'op',backend:'prismml',action:'start',state:'running',elapsed_seconds:12,message:'Starting'}];renderEngines();renderOperations(snapshot.operations);");
  assert.ok(nodes.engines.children[0].children.some(n=>n.className==='engine-progress'));
  assert.ok(nodes.operations.children[0].children.some(n=>n.className==='operation-progress'));
  run("renderTelemetry({enabled:true,sample:{timestamp:new Date().toISOString(),cpu_percent:25,ram_used_gb:16,ram_total_gb:32,ram_percent:50,cpu_temperature:null,gpu:[{name:'GPU',total:16000,used:8000,free:8000,utilization:42,temperature:65,power_watts:150}]}})");
  assert.equal(nodes['cpu-value'].textContent,'25%');assert.ok(nodes['ram-value'].textContent.includes('16 / 32'));assert.equal(nodes['temperature-value'].textContent,'65 °C');assert.equal(nodes['fan-value'].textContent,'Вентиляторы: —');
  // Without periodic polling the last sample stays visible with its time; dashes only before any sample.
  run("renderTelemetry({enabled:false,reason:'Idle.'})");assert.equal(nodes['cpu-value'].textContent,'25%');assert.ok(nodes['telemetry-state'].textContent.startsWith('Снимок '));assert.ok(nodes['telemetry-state'].textContent.includes('Idle.'));
  run("lastSample=null;renderTelemetry({enabled:false,reason:'Idle'})");assert.equal(nodes['cpu-value'].textContent,'—');assert.equal(nodes['telemetry-state'].textContent,'Idle');
  run("renderTelemetry({enabled:true,snapshot:true,sample:{timestamp:new Date().toISOString(),cpu_percent:3,ram_used_gb:9,ram_total_gb:32,ram_percent:28,gpu:[]}})");assert.equal(nodes['cpu-value'].textContent,'3%');assert.ok(nodes['telemetry-state'].textContent.includes('Обновить данные'));
  run("snapshot.backends[0].service='Running';activity={active:{backend:'prismml',model:'model',session_id:'abc12345',context_limit:32768,input_tokens:10,output_tokens:null,total_tokens:null,context_used:null},requests:[],sessions:[]};renderActivity();");
  assert.ok(nodes['live-session'].children[0].children[4].children[1].textContent==='—');
  assert.ok(nodes['live-session'].children.find(n=>n.className==='live-context').children[0].textContent.includes('осталось —'));
  assert.ok(run("idleText({service:'Running',idle:{expires_at:new Date(Date.now()+61000).toISOString(),estimated:true}})").startsWith('≈ 1:'));
  assert.equal(run("idleText({service:'Running',idle:{busy:true}})"),'После завершения запроса');
  assert.ok(run("residencyText({mode:'hybrid',gpu_layers:12,total_layers:60})").includes('12/60'));
  assert.ok(run("residencyText({mode:'hybrid',gpu_layers:41,total_layers:41,cpu_moe_layers:999})").includes('эксперты 41 слоёв в RAM'));
  assert.ok(!run("residencyText({mode:'vram',gpu_layers:41,total_layers:41,cpu_moe_layers:null})").includes('эксперты'));
  // Updates: the panel shows the version and the one command that installs and updates.
  run("renderUpdateInfo()");assert.equal(nodes['update-version'].textContent,'Установлена версия 0.18.0.');assert.equal(nodes['update-commands'].children.length,3);
  assert.equal(nodes['update-commands'].children[0].children[1].children[0].textContent,'ai update');assert.ok(nodes['update-commands'].children[1].children[1].children[0].textContent.includes('install.ps1'));
  // Updates from the panel: without a saved token the check is off and the token form is open,
  // with a link to GitHub's prefilled form; the token is sent once and cleared from the page.
  run("renderUpdateToken({current:'0.20.2',token:false,token_link:'https://github.com/settings/personal-access-tokens/new?name=OmniBox&contents=read&expires_in=365'})");
  assert.equal(nodes['update-check'].disabled,true);assert.equal(nodes['update-token-box'].open,true);assert.ok(nodes['update-token-link'].href.includes('contents=read'));
  nodes['update-token'].value=' github_pat_secret ';const tokenBefore=requests.length;
  nodes['update-token-form'].listeners.submit[0]({preventDefault:()=>{}});await new Promise(r=>setTimeout(r,0));
  assert.equal(nodes['update-token'].value,'');assert.equal(requests[tokenBefore],'/api/update');
  assert.deepEqual(JSON.parse(bodies[tokenBefore]),{action:'save-token',token:'github_pat_secret'});
  run("renderUpdateToken({current:'0.20.2',token:true,token_expires:'2027-10-08 10:00:00 UTC',token_link:'x'})");
  assert.equal(nodes['update-check'].disabled,false);assert.equal(nodes['update-token-forget'].hidden,false);assert.ok(nodes['update-token-summary'].textContent.startsWith('GitHub token для обновлений: сохранён · действует до'));
  // Check first, then update to the version found; another version is installed only by name.
  const checkBefore=requests.length;nodes['update-check'].listeners.click[0]();await new Promise(r=>setTimeout(r,0));
  assert.deepEqual(JSON.parse(bodies[checkBefore]),{action:'check'});
  context.window.confirm=()=>true;nodes['update-install'].dataset.version='0.20.3';const installBefore=requests.length;
  nodes['update-install'].listeners.click[0]();await new Promise(r=>setTimeout(r,0));assert.deepEqual(JSON.parse(bodies[installBefore]),{action:'self-update',version:'0.20.3'});
  nodes['update-target'].value='0.20.1';const otherBefore=requests.length;nodes['self-update-form'].listeners.submit[0]({preventDefault:()=>{}});await new Promise(r=>setTimeout(r,0));
  assert.deepEqual(JSON.parse(bodies[otherBefore]),{action:'self-update',version:'0.20.1'});
  run("snapshot={...snapshot,last_update:{ok:false,from:'0.19.0',version:'0.20.0',message:'Панель не ответила. Изменения отменены.',time:'2026-10-07T12:00:00Z'}};renderUpdateInfo()");
  assert.equal(nodes['last-update'].hidden,false);assert.ok(nodes['last-update'].textContent.includes('не удалось (0.19.0 → 0.20.0)'),nodes['last-update'].textContent);
  // Downloads show progress and can be canceled; other operations cannot.
  run("renderOperations([{id:'d1',action:'download',backend:'prismml',state:'running',elapsed_seconds:5,message:'',bytes_done:1024**3,bytes_total:4*1024**3,bytes_per_second:50*1024**2},{id:'s1',action:'start',backend:'prismml',state:'running',elapsed_seconds:2,message:''}])");
  const [downloadRow,startRow]=nodes.operations.children;const downloadBox=downloadRow.children.find(n=>n.className==='operation-progress');
  assert.ok(downloadBox.children[1].textContent.startsWith('1.00 из 4.00 ГБ (25%) · 50.0 МБ/с · осталось 2 мин'),downloadBox.children[1].textContent);
  assert.equal(downloadBox.children[2].textContent,'Отменить');assert.equal(startRow.children.find(n=>n.className==='operation-progress').children.length,2);
  // API settings: queue fields, no engine choice; /v1 serves the running engine.
  run("gateway={enabled:true,base_url:'https://llm.fnx.red/v1',require_key:true,has_key:true,queue_depth:4,queue_timeout_seconds:60,public_origin:'https://llm.fnx.red',trusted_proxies:['192.168.1.10','192.168.1.11']};renderGatewayForm()");
  assert.equal(nodes['api-queue-depth'].value,4);assert.equal(nodes['api-queue-timeout'].value,60);
  // Content search lists matches with a link to export their session.
  run("renderContentResults([{id:'a1',session_id:'0123456789abcdef01234567',model:'ornith',started:'2026-10-07T10:00:00Z',snippet:'…про Байкал…'}])");
  const match=nodes['content-results'].children[0];assert.equal(match.children[1].textContent,'…про Байкал…');
  assert.ok(match.children[2].children[1].href.includes('session=0123456789abcdef01234567&export=1'));
  run("renderContentResults([])");assert.ok(nodes['content-results'].children[0].textContent.includes('Ничего не найдено'));
  // The placement estimate is asked for explicitly and can fill in the recommended GPU layers.
  const estimateBefore=requests.length;await run("estimatePlacement()");assert.ok(requests[estimateBefore].startsWith('/api/estimate?'));
  run("lastEstimate={placement:'hybrid',gpu_layers:31,text:'x'};applyEstimate()");assert.equal(nodes.placement.value,'hybrid');assert.equal(nodes['gpu-layers'].value,31);
  // Toasts pop up and stay in the history; opening the history clears the unread count.
  nodes.notifications.hidden=true;run("notice('Скачано: model.gguf');notice('Сбой',true)");
  assert.equal(nodes.toasts.children.length>=2,true);assert.equal(nodes['notifications-count'].hidden,false);
  assert.equal(nodes['notifications-list'].children[0].children[1].textContent,'Сбой');assert.equal(nodes['notifications-list'].children[0].className,'error');
  assert.ok(JSON.parse(store.get('omnibox-notices')).length>=2);
  nodes['notifications-button'].listeners.click[0]();assert.equal(nodes.notifications.hidden,false);assert.equal(nodes['notifications-count'].hidden,true);
  nodes['notifications-clear'].listeners.click[0]();assert.equal(nodes['notifications-list'].children.length,0);assert.equal(nodes['notifications-empty'].hidden,false);
  // Running operations show progress where they were started.
  run("renderInlineProgress([{action:'download',state:'running',backend:'prismml',bytes_done:512*1024**2,bytes_total:2*1024**3},{action:'engine-install',state:'running',backend:'ollama',bytes_done:50*1024**2,bytes_total:200*1024**2},{action:'pull',state:'done'}])");
  assert.equal(nodes['models-progress'].hidden,false);assert.equal(nodes['models-progress'].children.length,1);assert.equal(nodes['models-progress'].children[0].children[1].value,512*1024**2);
  assert.ok(nodes['models-progress'].children[0].children[2].textContent.startsWith('0.50 из 2.00 ГБ (25%)'));
  assert.ok(nodes['engines-progress'].children[0].children[2].textContent.startsWith('50.0 из 200.0 МБ (25%)'));assert.equal(nodes['update-progress'].hidden,true);
  // Installed models: every model with its capabilities and source; the one in use cannot be deleted.
  run("selectedBackend='prismml';renderInstalled([{id:'/m/a-Q4_K_M.gguf',name:'a-Q4_K_M.gguf',bytes:5*1024**3,quant:'Q4_K_M',params:'27B',context:131072,capabilities:['reasoning','tools'],source:'https://huggingface.co/u/a-GGUF',selected:true,in_use:true},{id:'/m/b-Q8_0.gguf',name:'b-Q8_0.gguf',bytes:1024**3,quant:'Q8_0',capabilities:['vision'],source:'https://huggingface.co/models?search=b',source_search:true,selected:false,in_use:false}])");
  const rows=nodes['installed-models'].children;assert.equal(rows.length,2);assert.ok(nodes['installed-status'].textContent.startsWith('Моделей: 2'));
  assert.equal(rows[0].children[1].textContent,'27B · Q4_K_M · 5.0 ГБ');
  // Capabilities are icons with a tooltip; the trained context is a badge of its own.
  const badges=rows[0].children[2].children;assert.deepEqual(badges.map(c=>c.className),['capability reasoning','capability tools','capability context']);
  assert.ok(badges[0].dataset.tip.startsWith('Рассуждения:'));assert.ok(badges[0].innerHTML.includes('<svg'));assert.equal(badges[0].attributes['aria-label'],badges[0].dataset.tip);assert.equal(badges[2].textContent,'128K');
  const firstActions=rows[0].children[3].children;assert.equal(firstActions.some(b=>b.textContent==='Выбрать'),false);assert.equal(firstActions.find(b=>b.textContent==='Удалить').disabled,true);
  const second=rows[1].children[3].children;assert.equal(second[1].textContent,'Найти на Hugging Face ↗');
  context.window.confirm=()=>true;const deleteBefore=requests.length;second.find(b=>b.textContent==='Удалить').listeners.click[0]();await new Promise(r=>setTimeout(r,0));
  assert.deepEqual(JSON.parse(bodies[deleteBefore]),{action:'model-delete',backend:'prismml',model:'/m/b-Q8_0.gguf'});
  nodes['model-choice'].options=[{value:'/m/a-Q4_K_M.gguf'},{value:'/m/b-Q8_0.gguf'}];nodes.alias.value='a-q4_k_m';run("aliasModel='/m/a-Q4_K_M.gguf'");
  second[0].listeners.click[0]();assert.equal(nodes['model-choice'].value,'/m/b-Q8_0.gguf');assert.equal(nodes.alias.value,'b-q8_0');
  // The API name follows the model unless the owner typed their own.
  nodes['model-choice'].value='/m/Old-Q4.gguf';run("aliasModel='/m/Old-Q4.gguf'");nodes.alias.value='old-q4';nodes['model-choice'].value='/m/Ternary-Bonsai-2-27B-PQ2_0.gguf';
  run("followModelAlias()");assert.equal(nodes.alias.value,'ternary-bonsai-2-27b-pq2_0');
  nodes.alias.value='coder';nodes['model-choice'].value='/m/Other.gguf';run("followModelAlias()");assert.equal(nodes.alias.value,'coder');
  // Settings that put everything on the GPU but overflow VRAM are flagged, with the base preset as the fix.
  nodes.placement.value='gpu';run("selectedBackend='prismml';lastEstimate={placement:'hybrid',vram_total:17095983104,weights_bytes:7194070000,kv_cache_bytes:12884901888};renderFit()");
  assert.equal(nodes['fit-warning'].hidden,false);assert.ok(nodes['fit-text'].textContent.includes('замедлится'));
  const fitBefore=requests.length;nodes['fit-base'].listeners.click[0]();await new Promise(r=>setTimeout(r,0));
  assert.deepEqual(JSON.parse(bodies[fitBefore]),{action:'preset-apply',backend:'prismml',model:'Базовый'});
  run("lastEstimate={placement:'gpu'};renderFit()");assert.equal(nodes['fit-warning'].hidden,true);
  // Start and shutdown: autostart and the engine at boot come from the status; shutdown asks first.
  run("snapshot={...snapshot,system:{autostart:true,start_engine:'prismml'}};renderPower()");
  assert.equal(nodes.autostart.checked,true);assert.equal(nodes['start-engine'].value,'prismml');
  const powerBefore=requests.length;nodes['start-engine'].value='';nodes['start-engine'].listeners.change[0]({target:nodes['start-engine']});await new Promise(r=>setTimeout(r,0));
  assert.deepEqual(JSON.parse(bodies[powerBefore]),{action:'start-engine',engine:''});
  context.window.confirm=()=>true;const shutdownBefore=requests.length;nodes['shutdown-omnibox'].listeners.click[0]();await new Promise(r=>setTimeout(r,0));
  assert.deepEqual(JSON.parse(bodies[shutdownBefore]),{action:'shutdown'});
  // Sleep asks first; the MAC addresses for Wake-on-LAN are listed for Home Assistant.
  context.window.confirm=()=>true;const sleepBefore=requests.length;nodes['sleep-pc'].listeners.click[0]();await new Promise(r=>setTimeout(r,0));
  assert.deepEqual(JSON.parse(bodies[sleepBefore]),{action:'sleep'});
  run("snapshot={...snapshot,network:[{name:'Ethernet',mac:'aa:bb:cc:dd:ee:ff',ipv4:'192.168.1.77'}]};renderWake()");
  assert.equal(nodes['wake-adapters'].children.length,1);
  // The model test runs only against a running model.
  const testBefore=requests.length;nodes['test-model'].listeners.click[0]();await new Promise(r=>setTimeout(r,0));
  assert.deepEqual(JSON.parse(bodies[testBefore]),{action:'model-test'});
  // Presets: built-in ones cannot be deleted; own ones can.
  run("renderPresets({presets:[{name:'Код 32k',builtin:true,settings:{context:'32768'}},{name:'Мой',builtin:false,settings:{context:'65536'}}]})");
  assert.equal(nodes['preset-choice'].children.length,2);assert.equal(nodes['preset-delete'].disabled,true);
  assert.ok(nodes['preset-choice'].children[0].text.includes('context=32768'));
  run("renderPresets({presets:[{name:'Базовый',builtin:true,auto:true,settings:{}}]})");assert.ok(nodes['preset-choice'].children[0].text.includes('под модель и видеокарту'));
  // F3: the engine card explains automatic restarts and why they stopped.
  assert.ok(run("restartText({})").includes('Не больше 2 раз'));
  const restartLine=run("restartText({restarts:2,last_failure:'процесс движка завершился с кодом 3',gave_up:'уже 2 перезапуска за 10 минут'})");
  assert.ok(restartLine.includes('Автоперезапусков после запуска: 2')&&restartLine.includes('Перезапуск прекращён: уже 2')&&restartLine.includes('кодом 3'),restartLine);
  // Several named keys: each row shows its use and can be revoked alone.
  run("gateway={...gateway,keys:[{id:'aaaaaaaaaaaa',name:'Jan',created:'2026-10-07T10:00:00Z',last_used:'2026-10-07T11:00:00Z',requests:3,tokens:1200},{id:'bbbbbbbbbbbb',name:'Pi',created:null,last_used:null,requests:0,tokens:0}]};renderGatewayForm()");
  assert.equal(nodes['api-key-list'].children.length,2);assert.equal(nodes['api-key-list'].children[0].children[0].textContent,'Jan');
  context.window.confirm=()=>true;const revokeBefore=requests.length;nodes['api-key-list'].children[1].children[5].children[0].listeners.click[0]();await new Promise(r=>setTimeout(r,0));
  assert.deepEqual(JSON.parse(bodies[revokeBefore]),{action:'revoke',id:'bbbbbbbbbbbb'});
  // S1: working without a key is allowed but shown as a warning.
  assert.equal(nodes['api-no-key-warning'].hidden,true);
  run("gateway={...gateway,require_key:false};renderGatewayForm()");assert.equal(nodes['api-no-key-warning'].hidden,false);
  run("gateway={...gateway,require_key:true};renderGatewayForm()");
  // The NAS proxy is edited with the API settings and sent as a list of addresses.
  assert.equal(nodes['api-public-origin'].value,'https://llm.fnx.red');assert.equal(nodes['api-trusted-proxies'].value,'192.168.1.10, 192.168.1.11');
  nodes['api-trusted-proxies'].value=' 192.168.1.10 ,, ';const savedBefore=requests.length;
  nodes['api-settings-form'].listeners.submit[0]({preventDefault:()=>{}});await new Promise(r=>setTimeout(r,0));
  const saved=JSON.parse(bodies[savedBefore]);assert.equal(requests[savedBefore],'/api/gateway');assert.deepEqual(saved.trusted_proxies,['192.168.1.10']);assert.equal(saved.public_origin,'https://llm.fnx.red');
  // Password change: mismatched repeat is refused before the request and the fields are cleared.
  const before=requests.length;nodes['password-current'].value='old-password-123';nodes['password-new'].value='new-password-1234';nodes['password-repeat'].value='other-password-123';
  await nodes['password-form'].listeners.submit[0]({preventDefault:()=>{}});
  assert.equal(requests.length,before);assert.equal(nodes['password-current'].value,'');assert.equal(nodes['password-new'].value,'');
  console.log('UI behavior: theme selection, model summary, stop-all confirmation, KV offload with placement, engine names, settings modes, storage usage, request content, engine manager, drives, idle sampling, start progress, resource fields unknown live counters, update command, download progress and cancel, API queue and NAS settings, named keys, no-key warning, content search, placement estimate, VRAM overflow warning, base preset, toasts and history, API name from the model, installed models, log retention in hours, inline progress, capability icons with tooltips, unload from the model card, start and shutdown, sleep and wake, model test, presets, auto-restart status, update check and saved token, self-update and password change passed.');
})().catch(e=>{console.error(e);process.exitCode=1});
