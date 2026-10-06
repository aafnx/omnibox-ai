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
const store=new Map(),requests=[];
const context=vm.createContext({document:{documentElement:{dataset:{}},getElementById:id=>{assert.ok(nodes[id],`Missing UI element ${id}`);return nodes[id]},body:new Node('body'),createElement:()=>new Node(),querySelectorAll:()=>[],addEventListener:()=>{},hidden:false,activeElement:null},window:{matchMedia:()=>({matches:false,addEventListener:()=>{}})},localStorage:{getItem:k=>store.get(k),setItem:(k,v)=>store.set(k,v)},navigator:{},Option:function(text,value){return{text,value}},fetch:async path=>{requests.push(path);return{ok:false,status:401,json:async()=>({error:'Login required'})}},setTimeout:()=>1,clearTimeout:()=>{},setInterval:()=>1,Map,Set,Date,Number,Promise,console});
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
  run("snapshot={backends:[{name:'bonsai',type:'prism',service:'Running',api:'Ready',sleeping:false,loaded_model:'model.gguf',selected_model:'model',settings:{context:32768,alias:'model'},models:[],errors:[]}],operations:[]};selectedBackend='bonsai';renderEngines();");
  assert.equal(nodes.engines.children[0].querySelector('start').hidden,true);assert.equal(nodes.engines.children[0].querySelector('stop').hidden,false);
  // CPU placement keeps the KV cache in RAM; leaving it restores GPU offload.
  nodes.placement.value='cpu';run("syncKvOffload(true)");assert.equal(nodes['kv-offload'].value,'off');assert.equal(nodes['kv-offload'].disabled,true);
  nodes.placement.value='gpu';run("syncKvOffload(true)");assert.equal(nodes['kv-offload'].value,'on');assert.equal(nodes['kv-offload'].disabled,false);
  nodes['kv-offload'].value='off';run("syncKvOffload(false)");assert.equal(nodes['kv-offload'].value,'off');
  // Disk usage: warning only above the configured log volume; unknown retention values stay selectable.
  nodes['log-retention'].options=[{value:'30'}];nodes['log-retention'].add=o=>nodes['log-retention'].options.push(o);
  run("renderStorage({logs_bytes:5*1024**2,log_files:4,backups_bytes:1024**2,updates_bytes:0,temp_bytes:0,log_retention_days:30,log_warn_mb:200,logs_over_limit:false})");
  assert.equal(nodes['storage-warning'].hidden,true);assert.ok(nodes['storage-summary'].textContent.includes('5.0 МБ (файлов: 4)'));assert.equal(nodes['log-warn'].value,200);
  run("renderStorage({logs_bytes:300*1024**2,log_files:9,backups_bytes:0,updates_bytes:0,temp_bytes:0,log_retention_days:45,log_warn_mb:200,logs_over_limit:true})");
  assert.equal(nodes['storage-warning'].hidden,false);assert.ok(nodes['storage-warning'].textContent.includes('200 МБ'));assert.equal(nodes['log-retention'].value,'45');
  run("renderModelSummary()");assert.equal(nodes['kpi-model'].textContent,'model.gguf');assert.ok(nodes['kpi-model-caption'].textContent.startsWith('PrismML · '));
  // The prism engine is shown as PrismML whatever its historical config key.
  assert.equal(nodes.engines.children[0].querySelector('h2').textContent,'PrismML');assert.equal(run("engineName('unknown')"),'unknown');
  // Basic mode hides advanced groups; the choice is remembered per browser.
  run("applySettingsMode()");assert.equal(nodes.advanced.hidden,true);assert.equal(nodes.generation.hidden,true);assert.equal(nodes['mode-hint'].hidden,false);assert.equal(nodes['settings-mode'].hidden,false);
  nodes['mode-pro'].listeners.click[0]();assert.equal(nodes.advanced.hidden,false);assert.equal(nodes.generation.hidden,false);assert.equal(store.get('omnibox-settings-mode'),'pro');assert.equal(nodes['mode-pro'].attributes['aria-pressed'],'true');
  nodes['mode-basic'].listeners.click[0]();assert.equal(nodes.advanced.hidden,true);assert.equal(store.get('omnibox-settings-mode'),'basic');
  // Stopping everything interrupts generation, so a running server needs confirmation.
  let asked=0;context.window.confirm=()=>{asked++;return false;};const sent=requests.length;
  nodes['stop-all'].listeners.click[0]();await new Promise(setImmediate);assert.equal(asked,1);assert.equal(requests.length,sent);
  run("snapshot.backends[0].service='Stopped';renderModelSummary()");assert.equal(nodes['kpi-model'].textContent,'Нет');
  nodes['stop-all'].listeners.click[0]();await new Promise(setImmediate);assert.equal(asked,1);assert.ok(requests.length>sent);
  run("snapshot.backends[0].service='Running';renderEngines()");
  assert.equal(run('Boolean(monitoringActive())'),true);
  run("snapshot.backends[0].sleeping=true");assert.equal(run('Boolean(monitoringActive())'),false);
  run("snapshot.backends[0].sleeping=false;snapshot.backends[0].service='Stopped';snapshot.operations=[{id:'op',backend:'bonsai',action:'start',state:'running',elapsed_seconds:12,message:'Starting'}];renderEngines();renderOperations(snapshot.operations);");
  assert.ok(nodes.engines.children[0].children.some(n=>n.className==='engine-progress'));
  assert.ok(nodes.operations.children[0].children.some(n=>n.className==='operation-progress'));
  run("renderTelemetry({enabled:true,sample:{timestamp:new Date().toISOString(),cpu_percent:25,ram_used_gb:16,ram_total_gb:32,ram_percent:50,cpu_temperature:null,gpu:[{name:'GPU',total:16000,used:8000,free:8000,utilization:42,temperature:65,power_watts:150}]}})");
  assert.equal(nodes['cpu-value'].textContent,'25%');assert.ok(nodes['ram-value'].textContent.includes('16 / 32'));assert.equal(nodes['temperature-value'].textContent,'65 °C');assert.equal(nodes['fan-value'].textContent,'Вентиляторы: —');
  // Without periodic polling the last sample stays visible with its time; dashes only before any sample.
  run("renderTelemetry({enabled:false,reason:'Idle.'})");assert.equal(nodes['cpu-value'].textContent,'25%');assert.ok(nodes['telemetry-state'].textContent.startsWith('Снимок '));assert.ok(nodes['telemetry-state'].textContent.includes('Idle.'));
  run("lastSample=null;renderTelemetry({enabled:false,reason:'Idle'})");assert.equal(nodes['cpu-value'].textContent,'—');assert.equal(nodes['telemetry-state'].textContent,'Idle');
  run("renderTelemetry({enabled:true,snapshot:true,sample:{timestamp:new Date().toISOString(),cpu_percent:3,ram_used_gb:9,ram_total_gb:32,ram_percent:28,gpu:[]}})");assert.equal(nodes['cpu-value'].textContent,'3%');assert.ok(nodes['telemetry-state'].textContent.includes('Обновить данные'));
  run("snapshot.backends[0].service='Running';activity={active:{backend:'bonsai',model:'model',session_id:'abc12345',context_limit:32768,input_tokens:10,output_tokens:null,total_tokens:null,context_used:null},requests:[],sessions:[]};renderActivity();");
  assert.ok(nodes['live-session'].children[0].children[4].children[1].textContent==='—');
  assert.ok(nodes['live-session'].children.find(n=>n.className==='live-context').children[0].textContent.includes('осталось —'));
  assert.ok(run("idleText({service:'Running',idle:{expires_at:new Date(Date.now()+61000).toISOString(),estimated:true}})").startsWith('≈ 1:'));
  assert.equal(run("idleText({service:'Running',idle:{busy:true}})"),'После завершения запроса');
  assert.ok(run("residencyText({mode:'hybrid',gpu_layers:12,total_layers:60})").includes('12/60'));
  assert.ok(run("residencyText({mode:'hybrid',gpu_layers:41,total_layers:41,cpu_moe_layers:999})").includes('эксперты 41 слоёв в RAM'));
  assert.ok(!run("residencyText({mode:'vram',gpu_layers:41,total_layers:41,cpu_moe_layers:null})").includes('эксперты'));
  run("renderUpdateStatus({current:'0.9.4',latest:'0.9.4',phase:'checking',message:'Checking',gh_available:true,authenticated:true,code:''})");
  assert.equal(nodes['update-check'].disabled,true);assert.equal(nodes['update-progress'].hidden,false);assert.equal(nodes['update-apply'].hidden,true);
  run("renderUpdateStatus({current:'0.9.4',latest:'0.9.4',phase:'ready',message:'Ready',gh_available:true,authenticated:true,code:''})");
  assert.equal(nodes['update-check'].disabled,false);assert.equal(nodes['update-apply'].hidden,false);assert.equal(nodes['update-progress'].hidden,true);
  run("renderUpdateStatus({current:'0.9.4',phase:'login',message:'Login',gh_available:true,authenticated:false,code:'ABCD-1234'})");
  assert.equal(nodes['update-device'].hidden,false);assert.equal(nodes['update-code'].children.length,1);assert.equal(nodes['update-cancel'].hidden,false);
  run("renderUpdateStatus({current:'0.9.4',phase:'installing',message:'Restart',gh_available:true,authenticated:true,code:''})");
  assert.equal(nodes['update-cancel'].hidden,true);assert.equal(store.get('omnibox-update-pending'),'1');
  run("renderUpdateStatus({current:'0.9.4',phase:'done',message:'Done',gh_available:false,authenticated:true,code:''})");
  assert.equal(store.get('omnibox-update-pending'),'0');assert.equal(nodes['update-gh-install'].hidden,false);
  nodes['update-token'].value='one-use-secret';context.fetch=async(path,options)=>{requests.push({path,body:options.body});return{ok:true,status:202,json:async()=>({current:'0.9.4',phase:'checking',message:'Checking',gh_available:true,authenticated:false,code:''})}};
  nodes['update-token-form'].listeners.submit[0]({preventDefault:()=>{}});assert.equal(nodes['update-token'].value,'');await new Promise(setImmediate);
  assert.equal(JSON.parse(requests.at(-1).body).token,'one-use-secret');assert.ok([...store.values()].every(v=>!String(v).includes('secret')));
  console.log('UI behavior: theme selection, model summary, stop-all confirmation, KV offload with placement, engine names, settings modes, storage usage, idle sampling, start progress, resource fields unknown live counters, update states and one-use token clearing passed.');
})().catch(e=>{console.error(e);process.exitCode=1});
