// Execute the real UI code against a small DOM adapter; no browser/rendering claim.
const fs=require('fs'),vm=require('vm'),assert=require('node:assert/strict');
const html=fs.readFileSync('web/index.html','utf8');
const files=['app.js','chat.js','models.js','server.js','settings.js'];
const sources=files.map(f=>fs.readFileSync('web/'+f,'utf8'));
// The page and every script are loaded in this order.
files.forEach(f=>assert.ok(html.includes(`<script src="/${f}"></script>`),`index.html loads ${f}`));
const ids=[...html.matchAll(/\bid="([^"]+)"/g)].map(m=>m[1]);
assert.equal(new Set(ids).size,ids.length,'ids are unique');
// The CSP allows no inline styles or scripts: sizes go through the CSSOM.
assert.ok(!/\sstyle="/.test(html),'no style attributes in index.html');
sources.forEach((s,i)=>{assert.ok(!/\sstyle="/.test(s),`no style attributes in ${files[i]}`);assert.ok(!/<[a-z][^>]*\son[a-z]+=/.test(s),`no inline handlers in ${files[i]}`);});
assert.ok(!/<script>/.test(html),'no inline script');
// Every element the scripts look up by id exists.
const dynamic=new Set(sources.flatMap(s=>[...s.matchAll(/id="([\w-]+)"/g)].map(m=>m[1])));
const used=new Set(sources.flatMap(s=>[...s.matchAll(/byId\('([\w-]+)'\)/g)].map(m=>m[1])));
for(const id of used)assert.ok(ids.includes(id)||dynamic.has(id),`index.html has #${id}`);

class ClassList{constructor(){this.set=new Set();}add(...c){c.forEach(x=>this.set.add(x));}remove(...c){c.forEach(x=>this.set.delete(x));}toggle(c,on){if(on===undefined)on=!this.set.has(c);on?this.set.add(c):this.set.delete(c);return on;}contains(c){return this.set.has(c);}}
class Node{
  constructor(id='',tag='div'){this.id=id;this.tagName=tag.toUpperCase();this.children=[];this.listeners={};this.dataset={};this.value='';this.hidden=false;this.checked=false;this.disabled=false;
    this._html='';this.textContent='';this.className='';this.attributes={};this.style={};this.classList=new ClassList();this.parent=null;}
  get innerHTML(){return this._html;}set innerHTML(v){this._html=String(v);this.children=[];}
  insertAdjacentHTML(_,v){this._html+=String(v);}
  get outerHTML(){return this._html;}set outerHTML(v){this._html=String(v);}
  setAttribute(n,v){this.attributes[n]=String(v);}getAttribute(n){return this.attributes[n]??null;}removeAttribute(n){delete this.attributes[n];}
  addEventListener(t,fn){(this.listeners[t]??=[]).push(fn);}removeEventListener(){}
  append(...n){n.forEach(x=>{x.parent=this;this.children.push(x);});}
  get firstElementChild(){return this.children[0]||new Node();}get lastElementChild(){return this.children.at(-1)||new Node();}
  querySelector(){return new Node();}querySelectorAll(){return [];}closest(){return null;}matches(){return false;}
  remove(){if(this.parent)this.parent.children=this.parent.children.filter(x=>x!==this);}
  focus(){}select(){}click(){}scrollIntoView(){}setSelectionRange(){}requestSubmit(){}getBoundingClientRect(){return {left:0,right:0,top:0,bottom:0,width:0,height:0};}
  get offsetWidth(){return 100;}
}
const nodes=Object.fromEntries(ids.map(id=>[id,new Node(id)]));
const store=new Map(),requests=[];
const document={documentElement:new Node('html'),body:new Node('body'),hidden:false,activeElement:null,
  getElementById:id=>{assert.ok(nodes[id]||dynamic.has(id),`Missing UI element ${id}`);return nodes[id]??=new Node(id);},
  querySelector:()=>new Node(),querySelectorAll:()=>[],createElement:t=>new Node('',t),addEventListener:()=>{},execCommand:()=>true};
const winListeners={};
const context=vm.createContext({document,window:{addEventListener:(t,fn)=>(winListeners[t]??=[]).push(fn),isSecureContext:false},location:{hash:'',protocol:'http:',host:'omnibox:8800'},history:{replaceState:()=>{}},
  localStorage:{getItem:k=>store.get(k)??null,setItem:(k,v)=>store.set(k,String(v))},navigator:{hardwareConcurrency:16},innerWidth:1280,
  matchMedia:()=>({matches:false,addEventListener:()=>{}}),requestAnimationFrame:fn=>{fn();return 1;},
  fetch:async(path)=>{requests.push(path);return {ok:false,status:401,json:async()=>({error:'Войди в панель.'})};},
  setTimeout:()=>1,clearTimeout:()=>{},setInterval:()=>1,console,URL,TextDecoder,AbortController,FileReader:function(){},Promise,Map,Set,Date,Number,JSON,Math,Object,Array,String,Error});
const run=code=>vm.runInContext(code,context);
// Values made in the page's realm are compared as JSON: their prototypes differ from the test's.
const J=code=>JSON.parse(run(`JSON.stringify(${code})`));
sources.forEach((s,i)=>vm.runInContext(s,context,{filename:files[i]}));

(async()=>{
  // Theme: the system's unless chosen; the choice is remembered in this browser.
  run("applyTheme('dark')");assert.equal(document.documentElement.attributes['data-theme'],'dark');assert.equal(store.get('omnibox-theme'),'dark');
  run("applyTheme('system')");assert.equal(document.documentElement.attributes['data-theme'],undefined);

  // Markdown: text is escaped first; links only http(s); citations link to the sources.
  const out=run("md('<script>x</script> **жирный** [ссылка](javascript:alert(1)) [сайт](https://example.com) [1]\\n\\n```go\\nfmt.Println(\"<b>\")\\n```\\n\\n- раз\\n- два\\n\\n| a | b |\\n|---|---|\\n| 1 | 2 |',[{title:'S',url:'https://src.example/'}])");
  assert.ok(out.includes('&lt;script&gt;')&&!out.includes('<script>'),'html is escaped');
  assert.ok(out.includes('<b>жирный</b>'));assert.ok(!out.includes('href="javascript'),'no javascript links');assert.ok(out.includes('<a href="https://example.com"'));
  assert.ok(out.includes('<sup class="c">1</sup>')&&out.includes('href="https://src.example/"'));
  assert.ok(out.includes('<pre><button type="button" class="copycode">')&&out.includes('fmt.Println(&quot;&lt;b&gt;&quot;)'));
  assert.ok(out.includes('<ul><li>раз</li><li>два</li></ul>'));assert.ok(out.includes('<table><thead><tr><th>a</th><th>b</th></tr></thead><tbody><tr><td>1</td><td>2</td></tr></tbody></table>'));

  // Reasoning scale: "off" only where thinking can be switched off; tokens and cost as a small note.
  assert.deepEqual(J("steps({mode:'none'})"),[]);
  assert.deepEqual(J("steps({mode:'toggle'})"),['off','on']);
  assert.deepEqual(J("steps({mode:'always',efforts:['low','medium','high','xhigh','max']})"),['low','medium','high','xhigh','max']);
  assert.deepEqual(J("steps({mode:'toggle',budget:true})"),['off','low','medium','high','max']);
  assert.deepEqual(J("steps({mode:'always'})"),['auto']);
  assert.equal(run("stepNote({reasoning:{mode:'always'},completion_usd:50},'medium')"),'≈ 3–8 тыс. токенов на размышления · ≈ $0,15–0,40');
  assert.equal(run("stepNote({reasoning:{mode:'toggle',budget:true}},'high')"),'до 20 тыс. токенов на размышления');
  assert.equal(run("stepNote({},'off')"),'Отвечает сразу, без размышлений.');

  // Capability icons: every capability on cards, dimmed when missing; tips name what is missing.
  const chips=run("allChips({caps:{vision:true,tools:true},reasoning:{mode:'none'}})");
  assert.equal((chips.match(/class="ico"/g)||[]).length,2);assert.equal((chips.match(/class="ico off"/g)||[]).length,4);assert.ok(chips.includes('Не понимает аудио.'));
  assert.ok(run("capIcons({caps:{vision:true},context:131072})").includes('128K'));

  // Memory estimate: weights + KV cache + buffers against the GPU's 16 GB.
  run("S.library={system:{vram_bytes:16*GIB,ram_bytes:32*GIB,desktop_vram_bytes:0.75*GIB},models:[]}");
  const m="({context:131072,memory:{weights_bytes:7*GIB,kv_key_per_token:8*128*48,kv_value_per_token:8*128*48,layers:48}})";
  assert.equal(run(`fit(${m},32768,{}).level`),'gpu');
  assert.equal(run(`fit(${m},131072,{}).level`),'ram');
  assert.equal(run(`fit(${m},131072,{cache_key:'q4_0',cache_value:'q4_0'}).level`),'gpu','a compressed cache fits more');
  assert.equal(run(`fit(${m},262144,{}).level`),'over');
  assert.equal(run(`best(${m},{})`),65536);
  assert.equal(run("fit({memory:{weights_bytes:40*GIB,kv_key_per_token:1,kv_value_per_token:1,layers:40,experts:128}},8192,{}).level"),'moe');
  assert.equal(run("fit({memory:{weights_bytes:80*GIB,kv_key_per_token:1,kv_value_per_token:1,layers:40}},8192,{}).level"),'none');
  assert.equal(run("fit({},8192,{}).level"),'unknown');

  // Advanced settings: the form's words become stored values and back.
  assert.equal(run("JSON.stringify(toConfig({flash_attention:'выкл',kv_offload:'нет',load_mode:'целиком в память',reasoning_effort:'высокая',temperature:'0,7',gpu_layers:'все',n_cpu_moe:'все',seed:'случайное'}))"),
    JSON.stringify({n_cpu_moe:'999',flash_attention:'off',kv_offload:'off',load_mode:'none',temperature:'0.7',reasoning_effort:'high'}));
  assert.equal(run("JSON.stringify(fromConfig({flash_attention:'on',cache_value:'q4_0',gpu_layers:'999',n_cpu_moe:'999',reasoning_effort:'low'}))"),JSON.stringify({n_cpu_moe:'все',cache_value:'q4_0',reasoning_effort:'низкая'}));
  // Impossible values are errors, doubtful ones warnings.
  const E=run(`check(${m},{batch:'512',ubatch:'1024',flash_attention:'выкл',temperature:'1.5',top_k:'3'},32768)`);
  assert.equal(E.ubatch.lv,'err');assert.equal(E.flash_attention.lv,'err');assert.equal(E.temperature.lv,'warn');assert.equal(E.top_k.lv,'warn');
  assert.equal(run(`check(${m},{temperature:'abc'},32768)`).temperature.lv,'err');
  assert.equal(run(`check(${m},{gpu_layers:'60'},32768)`).gpu_layers.t,'Не больше 48 — у модели 48 слоёв');
  assert.deepEqual(J(`Object.keys(check(${m},{},32768))`),[]);

  // Attachments: a file the model cannot read is refused before anything is sent.
  assert.equal(run("kindOf('Фото.JPG')"),'image');assert.equal(run("kindOf('a.docx')"),'word');assert.equal(run("kindOf('a.exe')"),null);
  run("S.library.models=[{key:'cloud:x/y',source:'cloud',name:'Y',caps:{vision:false,video:true},reasoning:{mode:'none'}},{key:'cloud:x/z',source:'cloud',name:'Z',caps:{},reasoning:{mode:'none'}}];C.chosen='cloud:x/y'");
  run("addFiles([{name:'a.png',size:10},{name:'b.mp4',size:10},{name:'c.md',size:400},{name:'d.bin',size:1},{name:'e.txt',size:30*1024*1024}])");
  assert.deepEqual(J("C.attached.map(a=>a.name)"),['b.mp4','c.md']);
  const warn=nodes['attach-warn'].innerHTML;assert.ok(warn.includes('Y не понимает изображения — «a.png» не прикреплён.')&&warn.includes('«d.bin»: такой тип файла не поддерживается')&&warn.includes('«e.txt»: больше 25 МБ.'));
  // A switch to a model that cannot read an attached file drops it with the same warning.
  run("C.chosen='cloud:x/z';recheckAttached()");assert.deepEqual(J("C.attached.map(a=>a.name)"),['c.md']);assert.ok(nodes['attach-warn'].innerHTML.includes('Z не понимает видео — «b.mp4» убран из сообщения.'));

  // Chats: pinned first, a countdown for the others, none for pinned chats.
  const now=Date.now(),iso=h=>new Date(now+h*3600000).toISOString();
  run(`C.threads=[{id:'a',title:'Старый',created:'${iso(-100)}',updated:'${iso(-1)}',pinned:false,expires:'${iso(30)}'},{id:'b',title:'Закреплённый',created:'${iso(-900)}',updated:'${iso(-2)}',pinned:true,expires:null},{id:'c',title:'Скоро',created:'${iso(-10)}',updated:'${iso(-3)}',pinned:false,expires:'${iso(0.5)}'}];C.hits=null;renderThreads()`);
  const list=nodes['thread-list'].innerHTML;
  assert.ok(list.indexOf('Закреплённые')<list.indexOf('Закреплённый')&&list.indexOf('Закреплённый')<list.indexOf('Старый'));
  assert.ok(list.includes('1 дн.')&&list.includes('&lt;1 ч')&&list.includes('remain soon'));
  assert.equal(run("remain({expires:new Date(Date.now()+5.5*3600000).toISOString()})"),'5 ч');

  // Models list: favourites first; OpenRouter asks for a key until one is saved.
  run("S.cloud={connected:false};S.library.models=[{key:'prismml:a.gguf',source:'local',engine:'prismml',name:'Локальная',caps:{},reasoning:{mode:'none'},favorite:true,running:true},{key:'ollama:q:8b',source:'local',engine:'ollama',name:'Ollama',caps:{},reasoning:{mode:'none'}}];renderModels()");
  let ml=nodes['model-list'].innerHTML;assert.ok(ml.indexOf('Избранные')<ml.indexOf('Локальные модели'));assert.ok(ml.includes('Подключите OpenRouter'));assert.ok(ml.includes('data-stop-model')&&ml.includes('data-start-model'));
  run("S.cloud={connected:true};S.library.models.push({key:'cloud:o/m',source:'cloud',engine:'openrouter',id:'o/m',name:'Облачная',provider:'O',prompt_usd:1.25,completion_usd:10,caps:{tools:true},reasoning:{mode:'none'},context:400000});renderModels()");
  ml=nodes['model-list'].innerHTML;assert.ok(ml.includes('$1,25 / $10 за 1M токенов')&&ml.includes('data-chat-model="cloud:o/m"'));

  // The reasoning control of a model that always thinks has no "off"; one that cannot think says so.
  run("S.library.models.push({key:'cloud:anthropic/claude-fable-5.1',source:'cloud',name:'Fable',caps:{reasoning:true},completion_usd:50,reasoning:{mode:'always',efforts:['low','medium','high','xhigh','max'],default:'medium',show:'summary',no_sampling:true}});C.chosen='cloud:anthropic/claude-fable-5.1';renderThink()");
  const pop=nodes['think-pop'].innerHTML;assert.ok(pop.includes('Обычно')&&!pop.includes('>Выкл<')&&pop.includes('нельзя выключить')&&pop.includes('Температуру и сэмплинг'));
  assert.equal(run('C.effort'),'medium');
  run("C.chosen='cloud:x/z';renderThink()");assert.ok(nodes['think-pop'].innerHTML.includes('не умеет размышлять'));assert.equal(run('C.effort'),'');

  // Assistants: none by default shows an empty slot; one with an emoji shows it.
  run("S.assistants={assistants:[{id:'a1',emoji:'🏠',name:'Домашний админ',description:'Сеть',instructions:'x',model:'',style:'',web_search:true}],default:'a1'};C.chat=null;C.asst='';renderAssist()");
  assert.ok(nodes['asst-btn'].innerHTML.includes('Без ассистента')&&nodes['asst-btn'].innerHTML.includes('av none slot'));
  run("C.asst='a1';renderAssist()");assert.ok(nodes['asst-btn'].innerHTML.includes('🏠')&&nodes['asst-btn'].innerHTML.includes('Домашний админ'));
  assert.equal(run("webDefault('a1')"),true);

  // A message's footer: time, tokens and speed; the cost only for a paid answer.
  const foot=run("metaHTML({model:'cloud:o/m',meta:{seconds:5.8,output_tokens:480,tokens_per_second:83.2,cost_usd:0.0042}})");
  assert.ok(foot.includes('Облачная')&&foot.includes('5,8 с')&&foot.includes('480 токенов')&&foot.includes('83,2 ток/с')&&foot.includes('$0,0042'));
  assert.ok(!run("metaHTML({model:'prismml:a.gguf',meta:{seconds:1}})").includes('cost'));

  // Logged out: the API answers 401 and the login form is shown.
  winListeners.DOMContentLoaded.forEach(fn=>fn());await new Promise(r=>setTimeout(r,10));
  assert.ok(requests.includes('/api/session'));assert.equal(nodes.login.hidden,false);assert.equal(nodes.app.hidden,true);
  console.log('web_test: ok');
})().catch(e=>{console.error(e);process.exit(1);});
