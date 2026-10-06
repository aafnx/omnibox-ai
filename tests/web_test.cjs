// Execute the real UI code against a small DOM adapter; no browser/rendering claim.
const fs=require('fs'),vm=require('vm'),assert=require('node:assert/strict');
class Node {
  constructor(id=''){this.id=id;this.children=[];this.listeners={};this.dataset={};this.value='';this.hidden=false;this.textContent='';this.className='';this.map={};this.classList={toggle:()=>{}};}
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
const context=vm.createContext({document:{documentElement:{dataset:{}},getElementById:id=>{assert.ok(nodes[id],`Missing UI element ${id}`);return nodes[id]},createElement:()=>new Node(),querySelectorAll:()=>[],addEventListener:()=>{},hidden:false,activeElement:null},window:{matchMedia:()=>({matches:false,addEventListener:()=>{}})},localStorage:{getItem:k=>store.get(k),setItem:(k,v)=>store.set(k,v)},navigator:{},Option:function(text,value){return{text,value}},fetch:async path=>{requests.push(path);return{ok:false,status:401,json:async()=>({error:'Login required'})}},setTimeout:()=>1,clearTimeout:()=>{},setInterval:()=>1,Map,Set,Date,Number,Promise,console});
function run(code){return vm.runInContext(code,context);}
(async()=>{
  run(source);await new Promise(setImmediate);
  assert.equal(context.document.documentElement.dataset.theme,'light');
  nodes['theme-select'].listeners.change[0]({target:{value:'dark'}});
  assert.equal(context.document.documentElement.dataset.theme,'dark');assert.equal(store.get('omnibox-theme'),'dark');
  run("applyTheme('system')");assert.equal(context.document.documentElement.dataset.theme,'light');
  run("snapshot={backends:[{name:'bonsai',type:'prism',service:'Running',api:'Ready',sleeping:false,loaded_model:'model.gguf',selected_model:'model',settings:{context:32768,alias:'model'},models:[],errors:[]}],operations:[]};selectedBackend='bonsai';renderEngines();");
  assert.equal(nodes.engines.children[0].querySelector('start').disabled,true);
  assert.equal(run('Boolean(monitoringActive())'),true);
  run("snapshot.backends[0].sleeping=true");assert.equal(run('Boolean(monitoringActive())'),false);
  run("snapshot.backends[0].sleeping=false;snapshot.backends[0].service='Stopped';snapshot.operations=[{id:'op',backend:'bonsai',action:'start',state:'running',elapsed_seconds:12,message:'Starting'}];renderEngines();renderOperations(snapshot.operations);");
  assert.ok(nodes.engines.children[0].children.some(n=>n.className==='engine-progress'));
  assert.ok(nodes.operations.children[0].children.some(n=>n.className==='operation-progress'));
  run("renderTelemetry({enabled:true,sample:{timestamp:new Date().toISOString(),cpu_percent:25,ram_used_gb:16,ram_total_gb:32,ram_percent:50,cpu_temperature:null,gpu:[{name:'GPU',total:16000,used:8000,free:8000,utilization:42,temperature:65,power_watts:150}]}})");
  assert.equal(nodes['cpu-value'].textContent,'25%');assert.ok(nodes['ram-value'].textContent.includes('16 / 32'));assert.equal(nodes['temperature-value'].textContent,'65 °C');assert.equal(nodes['fan-value'].textContent,'Вентиляторы: —');
  run("renderTelemetry({enabled:false,reason:'Idle'})");assert.equal(nodes['cpu-value'].textContent,'—');assert.equal(nodes['telemetry-state'].textContent,'Idle');
  run("snapshot.backends[0].service='Running';activity={active:{backend:'bonsai',model:'model',session_id:'abc12345',context_limit:32768,input_tokens:10,output_tokens:null,total_tokens:null,context_used:null},requests:[],sessions:[]};renderActivity();");
  assert.ok(nodes['live-session'].children[0].children[4].children[1].textContent==='—');
  assert.ok(nodes['live-session'].children.find(n=>n.className==='live-context').children[0].textContent.includes('осталось —'));
  assert.ok(run("idleText({service:'Running',idle:{expires_at:new Date(Date.now()+61000).toISOString(),estimated:true}})").startsWith('≈ 1:'));
  assert.equal(run("idleText({service:'Running',idle:{busy:true}})"),'После завершения запроса');
  assert.ok(run("residencyText({mode:'hybrid',gpu_layers:12,total_layers:60})").includes('12/60'));
  console.log('UI behavior: theme selection, idle sampling, start progress, resource fields and unknown live counters passed.');
})().catch(e=>{console.error(e);process.exitCode=1});
