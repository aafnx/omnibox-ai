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
// The login logo has a light and a dark file; a rule that sets display on both would show them together.
const css=fs.readFileSync('web/style.css','utf8');
assert.ok(!/\.brand-logo img\{[^}]*display/.test(css),'logo images get display only from the theme rules');
assert.ok(!html.includes('Ваш ИИ на вашем ПК'),'no slogan on the login screen');
// Every element the scripts look up by id exists.
const dynamic=new Set(sources.flatMap(s=>[...s.matchAll(/id="([\w-]+)"/g)].map(m=>m[1])));
const used=new Set(sources.flatMap(s=>[...s.matchAll(/byId\('([\w-]+)'\)/g)].map(m=>m[1])));
for(const id of used)assert.ok(ids.includes(id)||dynamic.has(id),`index.html has #${id}`);

class ClassList{constructor(){this.set=new Set();}add(...c){c.forEach(x=>this.set.add(x));}remove(...c){c.forEach(x=>this.set.delete(x));}toggle(c,on){if(on===undefined)on=!this.set.has(c);on?this.set.add(c):this.set.delete(c);return on;}contains(c){return this.set.has(c);}}
class Node{
  constructor(id='',tag='div'){this.id=id;this.tagName=tag.toUpperCase();this.children=[];this.listeners={};this.dataset={};this.value='';this.hidden=false;this.checked=false;this.disabled=false;
    this._html='';this.textContent='';this.className='';this.attributes={};this.style={setProperty(k,v){this[k]=v;}};this.classList=new ClassList();this.parent=null;}
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
  querySelector:()=>new Node(),querySelectorAll:()=>[],createElement:t=>new Node('',t),addEventListener:()=>{},removeEventListener:()=>{},execCommand:()=>true};
const winListeners={};
const context=vm.createContext({document,window:{addEventListener:(t,fn)=>(winListeners[t]??=[]).push(fn),isSecureContext:false},location:{hash:'',protocol:'http:',host:'omnibox:8800'},history:{replaceState:()=>{}},
  localStorage:{getItem:k=>store.get(k)??null,setItem:(k,v)=>store.set(k,String(v))},navigator:{hardwareConcurrency:16},innerWidth:1280,innerHeight:800,
  matchMedia:()=>({matches:false,addEventListener:()=>{}}),requestAnimationFrame:fn=>{fn();return 1;},
  fetch:async(path)=>{requests.push(path);return {ok:false,status:401,json:async()=>({error:'Войди в панель.'})};},
  setTimeout:()=>1,clearTimeout:()=>{},setInterval:()=>1,clearInterval:()=>{},console,URL,TextDecoder,AbortController,FileReader:function(){},Promise,Map,Set,Date,Number,JSON,Math,Object,Array,String,Error});
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
  assert.ok(out.includes('<span class="c">src.example</span>')&&out.includes('href="https://src.example/"')&&out.includes('title="S"'),'a citation names its site');
  // A run of citations is one chip: the first site, "+N" and every title in the tip; unknown numbers stay text.
  const run2=run("md('Факт [1][2] и [9].',[{title:'Один',url:'https://www.one.example/a'},{title:'Два',url:'https://two.example/b'}])");
  assert.ok(run2.includes('<span class="c">one.example +1</span>')&&run2.includes('title="Один · Два"')&&run2.includes('[9]'),run2);
  assert.ok(out.includes('<pre data-lang="go"><button type="button" class="copycode">')&&out.includes('<code class="language-go">fmt.Println(&quot;&lt;b&gt;&quot;)'),out);
  // Formulas are kept from escaping and Markdown, then marked for KaTeX; prices and code are not math.
  const mth=run("md('Площадь $S = \\\\pi r^2$ и $$\\\\frac{a<b}{c}$$, цена $5 и $10, `echo $HOME$`\\n\\n```sh\\necho $a$\\n```\\n\\n\\\\[x_1\\\\]')");
  assert.ok(mth.includes('<span class="math" data-tex="S = \\pi r^2">')&&mth.includes('<span class="math display" data-tex="\\frac{a&lt;b}{c}">'),mth);
  assert.ok(mth.includes('цена $5 и $10')&&mth.includes('<code>echo $HOME$</code>')&&mth.includes('echo $a$</code></pre>')&&mth.includes('data-tex="x_1"'),mth);
  assert.ok(out.includes('<ul><li>раз</li><li>два</li></ul>'));assert.ok(out.includes('<table><thead><tr><th>a</th><th>b</th></tr></thead><tbody><tr><td>1</td><td>2</td></tr></tbody></table>'));

  // Lists: bullets under a numbered item nest in it; a numbered list after a paragraph keeps counting.
  const lst=run("md('1. **Реестр.** Но:\\n   - может сломать\\n   - перезапишется\\n2. **Варианты:**\\n   - поддержка\\n\\nТекст\\n\\n3. Третий\\n- маркер')");
  assert.ok(lst.includes('<ol><li><b>Реестр.</b> Но:<ul><li>может сломать</li><li>перезапишется</li></ul></li><li><b>Варианты:</b><ul><li>поддержка</li></ul></li></ol>'),lst);
  assert.ok(lst.includes('<ol start="3"><li>Третий</li></ol><ul><li>маркер</li></ul>'),lst);

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
  assert.equal(run("fit({memory:{weights_bytes:24*GIB,kv_key_per_token:1,kv_value_per_token:1,layers:40,experts:128}},8192,{}).level"),'moe');
  assert.equal(run("fit({memory:{weights_bytes:40*GIB,kv_key_per_token:1,kv_value_per_token:1,layers:40,experts:128}},8192,{}).level"),'ram','all experts do not fit in free RAM');
  assert.equal(run("fit({memory:{weights_bytes:80*GIB,kv_key_per_token:1,kv_value_per_token:1,layers:40,experts:128}},8192,{}).level"),'none','MoE weights still need physical memory');
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
  run("C.attached=[];addFiles([1,2,3,4].map(i=>({name:i+'.md',size:25*1024*1024})))");
  assert.equal(run('C.attached.length'),3);assert.ok(nodes['attach-warn'].innerHTML.includes('75 МБ суммарно'));run('C.attached=[]');

  // Completing A's stream after navigating to B or a new chat must not touch that view.
  const oldFetch=context.fetch;
  for(const destination of ["{id:'chatB',title:'B',messages:[]}","null","{id:'chatA',title:'A',messages:[{id:'userA',role:'user',text:'Вопрос A'}]}"]){
    let release,entered;
    const waiting=new Promise(resolve=>{entered=resolve;});
    const gate=new Promise(resolve=>{release=resolve;});
    let reads=0;
    context.fetch=async()=>({ok:true,status:200,body:{getReader:()=>({read:async()=>{if(reads++)return {done:true};entered();await gate;
      return {done:false,value:new TextEncoder().encode('event: user\ndata: {"id":"userA","role":"user","text":"Вопрос A"}\n\nevent: done\ndata: {"message":{"id":"answerA","role":"assistant","text":"Ответ A"},"chat":{"id":"chatA","title":"A"}}\n\n')};}})}});
    run("C.chat={id:'chatA',title:'A',messages:[]};C.busy=null;C.starting=false;C.chosen='cloud:x/z';C.threads=[{id:'chatA',title:'A'}]");
    const sending=run("send({text:'Вопрос A'})");await waiting;run('C.chat='+destination);release();await sending;
    if(destination==='null')assert.equal(run('C.chat'),null,'new chat stays empty');
    else if(destination.includes('chatB'))assert.deepEqual(J('C.chat'),{id:'chatB',title:'B',messages:[]},'another chat stays intact');
    else assert.equal(run('C.chat.messages.length'),2,'reopening the sending chat does not duplicate its user message');
    assert.equal(run('C.busy'),null);
  }
  context.fetch=oldFetch;run('C.chat=null;C.threads=[]');
  // Power commands acknowledge admission without waiting for the PC to go offline.
  context.fetch=async(path,opts)=>{assert.equal(path,'/api/actions');assert.equal(JSON.parse(opts.body).action,'poweroff');return {ok:true,json:async()=>({id:'power'})};};
  assert.equal((await run("pcAction('poweroff')")).id,'power');assert.equal(run('waiting.size'),0);
  context.fetch=async()=>({ok:false,status:400,json:async()=>({error:'Дождитесь завершения операции.'})});
  await assert.rejects(run("pcAction('poweroff')"),/Дождитесь завершения/);context.fetch=oldFetch;

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
  assert.equal(nodes['think-btn'].style['--lvl'],'0.25','the depth shows in the icon: medium is a quarter up the scale');
  assert.equal(run("thinkLevel(['off','low','high'],'off')"),0);assert.equal(run("thinkLevel(['low','medium','high','xhigh','max'],'max')"),1);assert.equal(run("thinkLevel(['auto'],'auto')"),0.5);
  const pop=nodes['think-pop'].innerHTML;assert.ok(pop.includes('Обычно')&&!pop.includes('>Выкл<')&&pop.includes('нельзя выключить')&&pop.includes('Температуру и сэмплинг'));
  assert.equal(run('C.effort'),'medium');
  run("C.chosen='cloud:x/z';renderThink()");assert.ok(nodes['think-pop'].innerHTML.includes('не умеет размышлять'));assert.equal(run('C.effort'),'');

  // Assistants: none by default shows an empty slot; one with an emoji shows it.
  run("S.assistants={assistants:[{id:'a1',emoji:'🏠',name:'Домашний админ',description:'Сеть',instructions:'x',model:'',style:'',web_search:true}],default:'a1'};C.chat=null;C.asst='';renderAssist()");
  // The composer shows only the icon; the name is in its tip and in the menu.
  assert.ok(nodes['asst-btn'].innerHTML.includes('av none slot')&&!nodes['asst-btn'].innerHTML.includes('Без ассистента')&&nodes['asst-btn'].title.includes('Без ассистента'));
  run("C.asst='a1';renderAssist()");const ab=nodes['asst-btn'];
  assert.ok(ab.innerHTML.includes('dominant-baseline="central">🏠</text>')&&!ab.innerHTML.includes('Домашний админ')&&ab.title==='Ассистент: Домашний админ'&&ab.attributes['aria-label']===ab.title);
  assert.ok(nodes['asst-menu'].innerHTML.includes('<b>Домашний админ</b>')&&nodes['asst-menu'].innerHTML.includes('По умолчанию'));
  assert.equal(run("webDefault('a1')"),true);

  // A message's footer: time, tokens and speed; the cost only for a paid answer.
  const foot=run("metaHTML({model:'cloud:o/m',meta:{seconds:5.8,output_tokens:480,tokens_per_second:83.2,cost_usd:0.0042}})");
  assert.ok(foot.includes('Облачная')&&foot.includes('5,8 с')&&foot.includes('480 токенов')&&foot.includes('83,2 ток/с')&&foot.includes('$0,0042'));
  assert.ok(!run("metaHTML({model:'prismml:a.gguf',meta:{seconds:1}})").includes('cost'));

  // Model start: the stage comes from the engine status, the share is estimated from the file size.
  run("S.library.models.push({key:'prismml:big.gguf',source:'local',engine:'prismml',name:'Big',bytes:12e9,caps:{},reasoning:{mode:'none'}});S.status={backends:[{name:'prismml',service:'Running',api:'Ready',loaded_model:'a.gguf'},{name:'ollama',service:'Stopped'}]};loadBegin(modelByKey('prismml:big.gguf'))");
  assert.equal(run('loadInfo().stage'),'Выгружаем прежнюю модель');
  run("S.status.backends[0]={name:'prismml',service:'Stopped'}");assert.equal(run('loadInfo().stage'),'Запускаем движок');
  run("S.status.backends[0]={name:'prismml',service:'StartPending',api:'Starting'};LOAD.t0=Date.now()-7000");
  assert.equal(run('loadInfo().stage'),'Загружаем модель в память');assert.equal(run('loadInfo().pct'),42,'7 s of the expected 15 s');
  run("S.status.backends[0]={name:'prismml',service:'Running',api:'Ready',loaded_model:'big.gguf'};LOAD.t0=Date.now()-3600000");
  assert.equal(run('loadInfo().stage'),'Проверяем модель');assert.equal(run('loadInfo().pct'),98,'never 100 before the engine answers');
  run('loadEnd()');assert.equal(run('loadInfo()'),null);
  // A model row being started carries the bar.
  run("LOAD.key='prismml:big.gguf';renderModels()");assert.ok(nodes['model-list'].innerHTML.includes('data-load'));run("LOAD.key=''");

  // Engines on the Server page say what they run now and what the restart switch does.
  run("SV.engines=[{name:'prismml',installed:true,version:'b9000'},{name:'ollama',installed:true}];S.status={backends:[{name:'prismml',service:'Running',api:'Ready',loaded_model:'big.gguf',endpoint:'http://127.0.0.1:8801/v1'},{name:'ollama',service:'Stopped'}]};renderEngines()");
  const eng=nodes.engines.innerHTML;
  assert.ok(eng.includes('Сейчас: <b>big.gguf</b>')&&eng.includes('Сейчас без модели')&&eng.includes('версия b9000')&&eng.includes('Перезапуск после сбоя'));

  // Storage lists every drive, also those without OmniBox data.
  run("ST.storage={log_retention_days:30,drives:[{name:'C:',total:1e12,used:5e11,free:5e11,roles:['OmniBox AI']},{name:'D:',total:2e12,used:1e11,free:1.9e12,roles:[]}]};renderStorage()");
  const dr=nodes.drives.innerHTML;assert.ok(dr.includes('C:')&&dr.includes('D:')&&dr.includes('Данных OmniBox нет'));

  // Advanced mode opens from the switch and from a tap anywhere on its row (Safari on iPhone).
  run("M.advOn=false");nodes.adv.hidden=true;nodes['adv-on'].checked=true;run("syncAdv()");assert.equal(nodes.adv.hidden,false);
  nodes['adv-on'].checked=false;run("syncAdv()");assert.equal(nodes.adv.hidden,true);

  // Phones: with the keyboard open the panel takes the visible height; the tab bar stays above the keyboard.
  assert.ok(!/\.kb \.nav\{display:none/.test(css),'the tab bar is not hidden with the keyboard');
  const rootEl=document.documentElement;rootEl.style={setProperty(k,v){this[k]=v;},removeProperty(k){delete this[k];}};
  Object.assign(context.window,{innerHeight:800,scrollY:0,scrollTo(){},visualViewport:{height:800,offsetTop:0}});
  run('fitViewport()');assert.equal(rootEl.classList.contains('kb'),false);
  document.activeElement={matches:()=>true};Object.assign(context.window.visualViewport,{height:420,offsetTop:36});run('fitViewport()');
  assert.equal(rootEl.classList.contains('kb'),true);assert.equal(rootEl.style['--vvh'],'420px');assert.equal(rootEl.style['--vvt'],'36px');
  document.activeElement=null;run('fitViewport()');assert.equal(rootEl.classList.contains('kb'),false,'a shrunk viewport without a focused field is not the keyboard');
  assert.equal(rootEl.style['--vvh'],undefined);
  Object.assign(context.window.visualViewport,{height:800,offsetTop:0});

  // Context sizes: local contexts are powers of two, cloud limits round decimals.
  assert.equal(run("kfmt(65536)"),'64K');assert.equal(run("kfmt(1000000)"),'1M');assert.equal(run("kfmt(400000)"),'400K');
  // Context meter: the window of the chosen model, filled as far as the last answer counted.
  run("C.chosen='cloud:o/m';C.chat={id:'x',messages:[{role:'user',text:'привет'},{role:'assistant',text:'ок',meta:{context_tokens:100000}}]}");
  assert.deepEqual(J("ctxInfo()").pct,25);assert.equal(J("ctxInfo()").est,false);
  run('renderCtxMeter()');const cp=nodes['ctx-pop'].innerHTML;
  assert.equal(nodes['ctx-wrap'].hidden,false);assert.ok(cp.includes('100K / 400K (25%)')&&cp.includes('<dt>Осталось</dt><dd>300K</dd>')&&cp.includes('Предел модели в OpenRouter'),cp);
  // A chat from before the count is estimated from its text and says so.
  run("C.chat={id:'y',messages:[{role:'user',text:'x'.repeat(7000)}]}");assert.equal(J("ctxInfo()").est,true);assert.equal(J("ctxInfo()").used,2000);
  run('renderCtxMeter()');assert.ok(nodes['ctx-pop'].innerHTML.includes('≈ 2K')&&nodes['ctx-pop'].innerHTML.includes('это оценка'));
  // No model, no meter.
  run("C.chosen='none';renderCtxMeter()");assert.equal(nodes['ctx-wrap'].hidden,true);run("C.chosen='cloud:o/m';C.chat=null");

  // Favourite chats have a star; deleting a chat asks in a window first.
  run(`C.threads=[{id:'f',title:'Избранный',created:'${iso(-5)}',updated:'${iso(-1)}',pinned:true,expires:null}];renderThreads()`);
  const fl=nodes['thread-list'].innerHTML;assert.ok(fl.includes('Избранные')&&fl.includes('<span class="tt">Избранный</span>')&&fl.includes('data-tmenu="f"')&&!fl.includes('#i-pin'));
  // The row has only the "⋯" button: rename, favourite and delete are in its menu, so nothing covers the title.
  assert.ok(!fl.includes('data-pin=')&&!fl.includes('data-del-thread='),'no action buttons over the title');
  run("openThreadMenu({dataset:{tmenu:'f'},getBoundingClientRect:()=>({right:300,bottom:100,top:80}),setAttribute(){},classList:{add(){}}})");
  const tmenu=nodes['thread-menu'];assert.equal(tmenu.hidden,false);
  assert.ok(tmenu.innerHTML.includes('data-rename="f"')&&tmenu.innerHTML.includes('Убрать из избранного')&&tmenu.innerHTML.includes('#i-star')&&tmenu.innerHTML.includes('data-del-thread="f"'));
  // Only the chat as a whole is in the list's menu: what happens inside it is in the composer's "+".
  assert.ok(!tmenu.innerHTML.includes('data-export')&&!tmenu.innerHTML.includes('Предлагать'));
  assert.equal(tmenu.style.left,'200px');assert.equal(tmenu.style.top,'104px');
  run('closeThreadMenu()');assert.equal(tmenu.hidden,true);
  // Renaming: the row becomes a field; Enter saves through the API, Escape keeps the old title.
  run("startRename('f')");assert.ok(nodes['thread-list'].innerHTML.includes('id="thread-rename"')&&nodes['thread-list'].innerHTML.includes('value="Избранный"'));
  const sent=[];context.fetch=async(path,opts)=>{sent.push(JSON.parse(opts.body));return {ok:true,json:async()=>({id:'f',title:'Отпуск',pinned:true,created:iso(-5),updated:iso(0)})};};
  nodes['thread-rename'].value='  Отпуск ';await run('commitRename(true)');
  assert.deepEqual(sent,[{action:'rename',id:'f',title:'Отпуск'}]);assert.equal(run("C.threads[0].title"),'Отпуск');assert.equal(run('C.renaming'),null);
  run("startRename('f')");nodes['thread-rename'].value='Другое';await run('commitRename(false)');assert.equal(sent.length,1,'Escape does not save');assert.equal(run("C.threads[0].title"),'Отпуск');
  context.fetch=oldFetch;
  const asked=run("confirmDialog({title:'Удалить чат?',text:'«Избранный» сразу сотрётся с диска.'})");
  assert.equal(nodes.confirm.hidden,false);assert.equal(nodes['confirm-title'].textContent,'Удалить чат?');
  nodes['confirm-no'].listeners.click.at(-1)();assert.equal(await asked,false);assert.equal(nodes.confirm.hidden,true);
  const asked2=run("confirmDialog({title:'Удалить чат?',text:'x'})");nodes['confirm-yes'].listeners.click.at(-1)();assert.equal(await asked2,true);

  // Enter sends; Ctrl+Enter and Shift+Enter start a new line (Shift by the browser itself).
  let submitted=0;nodes.composer.requestSubmit=()=>{submitted++;};const key=(o)=>{const e={key:'Enter',target:nodes.prompt,prevented:false,preventDefault(){this.prevented=true;},...o};run('promptKey')(e);return e;};
  nodes.prompt.value='первая';nodes.prompt.selectionStart=nodes.prompt.selectionEnd=6;
  assert.equal(key({ctrlKey:true}).prevented,true);assert.equal(nodes.prompt.value,'первая\n');assert.equal(submitted,0);
  assert.equal(key({shiftKey:true}).prevented,false);assert.equal(submitted,0);
  assert.equal(key({}).prevented,true);assert.equal(submitted,1);
  key({isComposing:true});assert.equal(submitted,1,'an IME composition is not sent');
  // On a touch screen Enter starts a new line: a phone keyboard has no Ctrl.
  context.matchMedia=q=>({matches:q.includes('pointer:coarse'),addEventListener:()=>{}});assert.equal(key({}).prevented,false);assert.equal(submitted,1);
  context.matchMedia=()=>({matches:false,addEventListener:()=>{}});
  // The composer's reasoning and web search are icons; their state is in the tip.
  assert.ok(!html.includes('<span class="lbl">Размышления</span>')&&!html.includes('<span class="lbl">Веб-поиск</span>'));
  run("S.chatSettings={search_url:'https://search.example',search_ready:true};C.web=false;renderWeb()");assert.equal(nodes.web.title,'Веб-поиск: выключен');
  run("C.web=true;renderWeb()");assert.equal(nodes.web.title,'Веб-поиск: включён');

  // Sources: one chip with up to three sites' badges; the list under it. Letters unless site icons are on.
  const srcs=run("sourcesHTML([{title:'A',url:'https://www.yandex.ru/pogoda'},{title:'B',url:'https://yandex.ru/x'},{title:'C',url:'https://gismeteo.ru/'},{title:'D',url:'https://rp5.ru/'},{title:'E',url:'https://inkazan.ru/'}])");
  assert.ok(srcs.includes('5 источников')&&srcs.includes('<span class="n">5</span>')&&!srcs.includes('<img'),srcs);
  assert.equal((srcs.split('<ol')[0].match(/class="fav /g)||[]).length,3,'three distinct sites in the chip');
  assert.ok(/class="fav t[012]" aria-hidden="true">Y</.test(srcs));
  run("S.chatSettings.site_icons=true");const withIcons=run("favHTML('https://www.yandex.ru/pogoda')");
  assert.ok(withIcons.includes('<img src="https://www.yandex.ru/favicon.ico"')&&withIcons.includes('referrerpolicy="no-referrer"'));
  assert.ok(!run("favHTML('javascript:alert(1)')").includes('<img'),'no icon for a link without a site');run("S.chatSettings.site_icons=false");
  // The search trace and the reasoning are the same kind of chip.
  const bot=run("botHTML({id:'m',text:'ok',reasoning:'думаю',reasoning_seconds:12,meta:{searches:['погода'],sources:[{title:'A',url:'https://a.example/'}]}})");
  assert.ok(bot.includes('<details class="step"><summary>')&&bot.includes('<details class="step think"><summary>'));

  // Suggested questions: under the last answer only; the chat's own choice wins over the setting.
  const fm={id:'m9',role:'assistant',text:'ok',meta:{follow_ups:['Что завтра?','<b>x</b>']}};
  const fh=run(`botHTML(${JSON.stringify(fm)},true)`);assert.ok(fh.includes('Продолжить')&&fh.includes('data-follow="Что завтра?"')&&fh.includes('&lt;b&gt;x&lt;/b&gt;'),fh);
  assert.ok(!run(`botHTML(${JSON.stringify(fm)},false)`).includes('Продолжить'),'only the last answer');
  run("S.chatSettings.follow_ups=false");assert.equal(run("followOn({})"),false);assert.equal(run("followOn({follow_ups:true})"),true);
  run("S.chatSettings.follow_ups=true");assert.equal(run("followOn({})"),true);assert.equal(run("followOn({follow_ups:false})"),false);
  run("C.chat={id:'t1',messages:[{role:'user',text:'?'}]};renderPlus()");const pl=nodes['plus-pop'].innerHTML;
  assert.ok(pl.includes('id="plus-follow" checked')&&pl.includes('data-export="md">')&&pl.includes('data-export="copy">'),pl);
  run("C.chat=null;renderPlus()");assert.ok(nodes['plus-pop'].innerHTML.includes('data-export="md" disabled'),'nothing to export in a new chat');
  // Asked for after an answer; the reply fills the message, its cost is added to the answer's.
  run("C.chat={id:'fc',follow_ups:null,messages:[{id:'u1',role:'user',text:'?'},{id:'a1',role:'assistant',text:'Ответ',meta:{cost_usd:0.001}}]}");
  assert.equal(run('followTarget().id'),'a1');
  const fsent=[];context.fetch=async(path,opts)=>{fsent.push([path,JSON.parse(opts.body)]);return {ok:true,status:200,json:async()=>({follow_ups:['Ещё?'],cost_usd:0.0002})};};
  await run("requestFollowUps('fc','a1')");
  assert.deepEqual(fsent,[['/api/chat/followups',{chat_id:'fc',message_id:'a1'}]]);
  assert.deepEqual(J("C.chat.messages[1].meta.follow_ups"),['Ещё?']);assert.equal(run("Math.round(C.chat.messages[1].meta.cost_usd*1e4)"),12);
  assert.equal(run('followTarget()'),null,'an answer with suggestions is not asked again');
  run("C.chat.follow_ups=false;renderChat()");assert.ok(!nodes['message-list'].innerHTML.includes('Продолжить'),'off in the chat hides stored suggestions');
  run("C.chat.follow_ups=null;renderChat()");assert.ok(nodes['message-list'].innerHTML.includes('data-follow="Ещё?"'));
  // A new message cancels a pending request: the model is not kept busy with suggestions.
  let aborted=false;context.fetch=(path,opts)=>new Promise((_,reject)=>opts.signal.addEventListener('abort',()=>{aborted=true;reject(Object.assign(new Error('x'),{name:'AbortError'}));}));
  run("C.chat.messages.push({id:'a2',role:'assistant',text:'Ещё ответ'})");const pend=run("requestFollowUps('fc','a2')");run('stopFollowUps()');await pend;assert.equal(aborted,true);assert.equal(run('C.follow'),null);
  // The chat's switch: a choice equal to the setting follows the setting again.
  const tsent=[];context.fetch=async(path,opts)=>{const b=JSON.parse(opts.body);tsent.push(b);return {ok:true,json:async()=>({id:'t1',title:'T',follow_ups:b.mode==='auto'?undefined:b.mode==='on'})};};
  run("C.threads=[{id:'t1',title:'T'}];C.chat={id:'t1',messages:[]};S.chatSettings.follow_ups=true");await run("setChatFollow(false)");assert.equal(tsent[0].mode,'off');
  await run("setChatFollow(true)");assert.equal(tsent[1].mode,'auto');
  // A new chat keeps the choice until it is created.
  run("C.chat=null");await run("setChatFollow(false)");assert.equal(run('C.followNew'),false);assert.equal(tsent.length,2);run("C.followNew=undefined");context.fetch=oldFetch;run("S.chatSettings.follow_ups=false;C.chat=null");

  // Versions: "‹ 2/3 ›" under an answer whose question has earlier versions; the ends are disabled.
  const vq={id:'q1',role:'user',text:'?',branches:[[{id:'q1',role:'user',text:'old'}],[{id:'q1',role:'user',text:'old2'}]],branch:2};
  const vh=run(`versHTML(${JSON.stringify(vq)})`);assert.ok(vh.includes('<span>3/3</span>')&&vh.includes('data-to="1"')&&/data-to="3" disabled/.test(vh),vh);
  assert.equal(run("versHTML({id:'q',role:'user'})"),'');
  run(`C.chat={id:'vc',messages:[${JSON.stringify(vq)},{id:'a1',role:'assistant',text:'ok'}]};renderChat()`);assert.ok(nodes['message-list'].innerHTML.includes('class="vers"'));
  const bsent=[];context.fetch=async(p,o)=>{bsent.push(JSON.parse(o.body));return {ok:true,json:async()=>({id:'vc',messages:[{id:'q1',role:'user',text:'old2',branches:[[],[]],branch:1}]})};};
  await run("switchBranch({dataset:{branch:'q1',to:'1'}})");assert.deepEqual(bsent[0],{action:'branch',id:'vc',message_id:'q1',index:1});assert.equal(run('C.chat.messages[0].text'),'old2');
  // The title is asked once, after the first answer of a chat the owner did not name.
  run("S.chatSettings.auto_title=true;C.chat={id:'tc',messages:[{role:'user',text:'q'},{role:'assistant',text:'a'}]}");assert.equal(run('titleWanted()'),true);
  run("C.chat.title_set=true");assert.equal(run('titleWanted()'),false);run("C.chat.title_set=false;C.chat.messages.push({role:'assistant',text:'b'})");assert.equal(run('titleWanted()'),false);
  context.fetch=async(p,o)=>{bsent.push([p,JSON.parse(o.body)]);return {ok:true,json:async()=>({id:'tc',title:'Погода в Казани'})};};
  run("C.threads=[{id:'tc',title:'найди погоду'}]");await run("requestTitle('tc')");assert.equal(run('C.chat.title'),'Погода в Казани');assert.equal(run('C.threads[0].title'),'Погода в Казани');
  // Editing a question sends it as an edit; the composer is not touched.
  const esent=[];context.fetch=async(p,o)=>{esent.push(JSON.parse(o.body));return {ok:false,status:500,json:async()=>({error:'x'})};};
  run("C.busy=null;C.starting=false;C.chosen='cloud:o/m';C.chat={id:'ec',messages:[{id:'u1',role:'user',text:'старый'},{id:'a1',role:'assistant',text:'ответ'}]}");nodes.prompt.value='черновик';
  const em={dataset:{id:'u1'},nextElementSibling:null,classList:{add(){},remove(){}},querySelector:s=>s==='.edit-ta'?{value:' новый '}:s==='.bubble'?{}:null,closest:()=>null,insertAdjacentHTML(){}};
  await run('sendEdit')(em);assert.equal(esent[0].edit,'u1');assert.equal(esent[0].text,'новый');assert.equal(nodes.prompt.value,'черновик');
  context.fetch=oldFetch;run('C.chat=null;C.busy=null');

  // Reading aloud skips Markdown, links and citation numbers; code is named, not spelled.
  assert.equal(run("speechText('## Итог\\n**Ясно** [1], см. [сайт](https://x.example).\\n```js\\nx()\\n```')"),'Итог\nЯсно , см. сайт.\n (фрагмент кода)');
  // Export: Markdown with the speakers, files and sources; a file name without forbidden characters.
  const ec={title:'Погода: Казань/сегодня',created:'2026-10-12T09:00:00Z',messages:[{role:'user',text:'Погода?',attachments:[{name:'a.docx'}]},{role:'assistant',model:'cloud:o/m',text:'Ясно [1].',meta:{sources:[{title:'Я[н]декс',url:'https://ya.ru/p'}]}}]};
  const emd=run(`chatMarkdown(${JSON.stringify(ec)})`);
  assert.ok(emd.startsWith('# Погода: Казань/сегодня\n')&&emd.includes('## Вы\n\nПогода?\n')&&emd.includes('_Файлы: a.docx_')&&emd.includes('## Модель · Облачная')&&emd.includes('1. [Яндекс](https://ya.ru/p)'),emd);
  assert.equal(run(`fileName(${JSON.stringify(ec)},'md')`),'Погода Казань сегодня 2026-10-12.md');
  // Dictation: a browser without speech recognition shows no microphone.
  run('renderMic()');assert.equal(nodes['mic-btn'].hidden,true);

  // Background answers: "busy" is the open chat's answer; the stop button asks the service to stop.
  run("C.streams={};C.chat={id:'bg',messages:[]}");assert.equal(run('C.busy'),null);
  run("newStream('other',{})");assert.equal(run('C.busy'),null,'an answer in another chat does not block this one');
  run("newStream('bg',{})");assert.equal(run('C.busy.chat'),'bg');
  const stops=[];context.fetch=async(p,o)=>{stops.push([p,JSON.parse(o.body)]);return {ok:true,json:async()=>({stopped:true})};};
  await run('stopAnswer()');assert.deepEqual(stops,[['/api/chat/stop',{chat_id:'bg'}]]);assert.equal(run('C.busy.ctrl.signal.aborted'),false,'the page does not cut the stream itself');
  // The chat list marks chats whose answer is being written.
  run("C.threads=[{id:'bg',title:'Фон',created:'2026-10-12T09:00:00Z'},{id:'x',title:'X',created:'2026-10-12T09:00:00Z'}];C.hits=null;renderThreads()");
  assert.equal((nodes['thread-list'].innerHTML.match(/отвечает…/g)||[]).length,1);run("C.streams={}");
  // Coming back to a chat whose answer is being written follows it from the start, then reads the chat as stored.
  const seen=[];context.fetch=async(p)=>{seen.push(p);
    if(p==='/api/chat/active')return {ok:true,json:async()=>({chats:['bg']})};
    if(p.startsWith('/api/chat/stream'))return {ok:true,status:200,body:{getReader:()=>{let n=0;return {read:async()=>n++?{done:true}:{done:false,value:new TextEncoder().encode('event: delta\ndata: {"t":"Пишу"}\n\nevent: done\ndata: {"message":{"id":"a9","role":"assistant","text":"Пишу"}}\n\n')}};}}};
    return {ok:true,json:async()=>({id:'bg',title:'Фон',messages:[{id:'u9',role:'user',text:'?'},{id:'a9',role:'assistant',text:'Пишу'}]})};};
  await run('checkActive()');await new Promise(r=>setTimeout(r,20));
  assert.ok(seen.includes('/api/chat/stream?chat_id=bg')&&seen.includes('/api/chats?id=bg'),seen.join(' '));assert.equal(run("Object.keys(C.streams).length"),0);
  context.fetch=oldFetch;run("C.chat=null;C.active=new Set()");

  // Spending: fractions of a cent stay visible, nothing spent reads $0,00.
  assert.equal(run('money(0.00042)'),'$0,0004');assert.equal(run('money(0.00001)'),'<$0,0001');assert.equal(run('money(0)'),'$0,00');assert.equal(run('money(1.5)'),'$1,50');assert.equal(run('money(undefined)'),'$0,00');
  // The key's limit, its period, what is left, and when it expires.
  const day=86400000,t0=Date.parse('2026-10-12T00:00:00Z');
  const k=J(`keyLimits({limit:5,limit_remaining:0.4,limit_reset:'monthly',usage:4.6,weekly:0.002,monthly:1.2,expires_at:'${new Date(t0+92*day+3600000).toISOString()}'},${t0})`);
  assert.equal(k.limit,'$5,00 в месяц');assert.equal(k.left,'осталось $0,40');assert.equal(k.pct,92);assert.equal(k.low,true);
  assert.ok(k.until.startsWith('до 12 янв')&&k.until.includes('2027'),k.until);assert.equal(k.ttl,'осталось 92 дня');assert.equal(k.soon,false);
  assert.equal(k.spent,'всего $4,60');assert.equal(k.period,'за неделю $0,0020 · за месяц $1,20');
  const k2=J(`keyLimits({limit:null,usage:0,expires_at:null},${t0})`);assert.equal(k2.limit,'без лимита');assert.equal(k2.until,'бессрочный');
  assert.equal(J(`keyLimits({limit:1,limit_remaining:1,usage:0,expires_at:'${new Date(t0-day).toISOString()}'},${t0})`).ttl,'срок истёк — создайте новый ключ');
  assert.equal(J(`keyLimits({limit:1,limit_remaining:1,usage:0,expires_at:'${new Date(t0+5*3600000).toISOString()}'},${t0})`).ttl,'осталось 5 часов');
  run("renderKeyLimits({limit:5,limit_remaining:4,limit_reset:'weekly',usage:1})");assert.equal(nodes['or-keylim'].hidden,false);assert.ok(nodes['or-keylim'].innerHTML.includes('$5,00 в неделю')&&nodes['or-keylim'].innerHTML.includes('data-w="20%"'));

  // ChatGPT: windows are named by their length, resets counted down; the section shows the account and limits.
  assert.equal(run("gptWin({window_minutes:300})"),'5 часов');assert.equal(run("gptWin({window_minutes:10080})"),'Неделя');assert.equal(run("gptWin({window_minutes:1440})"),'1 день');
  assert.equal(run(`gptReset({resets_at:'${new Date(t0+130*60000).toISOString()}'},${t0})`),'сброс через 2 ч 10 мин');assert.equal(run(`gptReset({resets_at:'${new Date(t0-1000).toISOString()}'},${t0})`),'сбрасывается');
  const gl="{plan:'plus',primary:{used_percent:42,window_minutes:300},secondary:{used_percent:95,window_minutes:10080},others:[{id:'gpt-6-astra',name:'GPT-6-Astra',primary:{used_percent:10,window_minutes:300}}],credits:{has_credits:true,unlimited:false,balance:'12.50'},updated:'2026-10-12T10:00:00Z'}";
  assert.equal(run(`gptShort(${gl})`),'5 часов: 42% · Неделя: 95%');
  run(`S.gpt={signed_in:true,expose_api:true,models:[{id:'gpt-5.5',name:'GPT-5.5'}],status:{installed:true,version:'rust-v0.162.1',account:{email:'o@example.com',plan:'plus'},limits:${gl}}};renderGpt()`);
  assert.equal(nodes['gpt-on'].hidden,false);assert.equal(nodes['gpt-setup'].hidden,true);assert.equal(nodes['gpt-signin'].hidden,true);assert.equal(nodes['gpt-state'].textContent,'Подключено');
  const kv=nodes['gpt-kv'].innerHTML;assert.ok(kv.includes('o@example.com')&&kv.includes('ChatGPT Plus')&&kv.includes('<b>42%</b>')&&kv.includes('израсходовано')&&kv.includes('data-w="95%"')&&kv.includes('meter err'),kv);
  assert.ok(nodes['gpt-more'].innerHTML.includes('GPT-6-Astra · 5 часов')&&nodes['gpt-more'].innerHTML.includes('12.50'));assert.equal(nodes['gpt-expose'].checked,true);assert.equal(nodes['gpt-models'].textContent,'GPT-5.5');
  // Not signed in: a code to enter on OpenAI's page; nothing is installed: the download.
  run(`S.gpt={signed_in:false,models:[],status:{installed:true,version:'v',login:{url:'https://auth.openai.com/codex/device',code:'ABCD-1234',expires:'2026-10-12T10:15:00Z'}}};renderGpt()`);
  assert.equal(nodes['gpt-code'].hidden,false);assert.equal(nodes['gpt-code-v'].textContent,'ABCD-1234');assert.equal(nodes['gpt-url'].href,'https://auth.openai.com/codex/device');assert.equal(nodes['gpt-on'].hidden,true);
  run(`S.gpt={signed_in:false,models:[],status:{installed:false,installing:{running:true,done:50,total:200}}};renderGpt()`);
  assert.equal(nodes['gpt-setup'].hidden,false);assert.equal(nodes['gpt-install'].disabled,true);assert.ok(nodes['gpt-install-msg'].textContent.includes('25%'),nodes['gpt-install-msg'].textContent);
  // In the chat: a group of its own, a "subscription" pill and the limits under the message box.
  run(`S.library.chatgpt={connected:true};S.library.models.push({key:'chatgpt:gpt-5.5',source:'chatgpt',engine:'chatgpt',id:'gpt-5.5',name:'GPT-5.5',caps:{vision:true,reasoning:true},reasoning:{mode:'always',efforts:['low','high'],default:'low'}});S.gptLimits=${gl}`);
  // A favourite stays in its own group too, with a filled star; ChatGPT's own search shows as a capability.
  run("S.library.models.find(m=>m.key==='chatgpt:gpt-5.5').favorite=true;S.library.models.find(m=>m.key==='chatgpt:gpt-5.5').caps.search=true;C.pickerQ='';document.getElementById('picker-menu').hidden=false;renderPicker()");
  const favl=nodes['picker-list'].innerHTML;assert.equal((favl.match(/data-pick-model="chatgpt:gpt-5.5"/g)||[]).length,2,'in «Избранные» and in «ChatGPT»');assert.ok(favl.includes('class="pstar on"')&&favl.includes('Веб-поиск'),favl);
  run("M.engineF='all';renderModels()");assert.equal((nodes['model-list'].innerHTML.match(/data-cloud="chatgpt:gpt-5.5"/g)||[]).length,2,'the models page keeps it in its group too');
  run("S.library.models.find(m=>m.key==='chatgpt:gpt-5.5').favorite=false;document.getElementById('picker-menu').hidden=true");
  const pi=run("pickerItem(modelByKey('chatgpt:gpt-5.5'))");assert.ok(pi.includes('Подписка')&&pi.includes('data-pick-star="chatgpt:gpt-5.5"')&&pi.includes('aria-pressed="false"'),pi);
  // A ChatGPT model searches with OpenAI's search when it is on, even with no engine set up; another model needs the engine.
  run("S.chatSettings={search_ready:false,chatgpt_search:true};C.chosen='chatgpt:gpt-5.5';renderWeb()");assert.equal(run('webMode()'),'openai');assert.equal(nodes['web-host'].textContent,'поиск OpenAI');
  run("C.chosen='prismml:a.gguf';renderWeb()");assert.equal(run('webMode()'),'');assert.equal(nodes['web-on'].disabled,true);
  run("S.chatSettings={search_ready:true,search_engine:'tavily',chatgpt_search:false};renderWeb()");assert.equal(nodes['web-host'].textContent,'Tavily');
  // The search settings show what the chosen engine needs: a key (never the key itself), a model or an address.
  run("S.chatSettings={search_engine:'tavily',search_ready:true,search_results:5,retention_hours:720,search_keys:{tavily:{hint:'…WXYZ',saved:'2026-10-12T10:00:00Z'}},plan_queries:true};renderChatSettings()");
  assert.equal(nodes['search-key-wrap'].hidden,false);assert.equal(nodes['searx-wrap'].hidden,true);assert.ok(nodes['search-key-info'].textContent.includes('…WXYZ'));assert.equal(nodes['search-state'].textContent,'Tavily');assert.equal(nodes['plan-queries'].checked,true);
  run("document.getElementById('search-engine').value='sonar';S.chatSettings.openrouter=true;S.chatSettings.sonar_models=['perplexity/sonar','perplexity/sonar-pro'];renderSearchEngine()");
  assert.equal(nodes['sonar-wrap'].hidden,false);assert.equal(nodes['search-key-wrap'].hidden,true);assert.ok(nodes['sonar-model'].innerHTML.includes('perplexity/sonar-pro'));
  run("C.chosen='chatgpt:gpt-5.5';renderFoot()");assert.ok(nodes['chat-foot'].innerHTML.includes('ChatGPT · 5 часов: 42% · Неделя: 95%')&&nodes['chat-foot'].innerHTML.includes('warn-text'),nodes['chat-foot'].innerHTML);
  run("M.engineF='chatgpt';renderModels()");assert.ok(nodes['model-list'].innerHTML.includes('по подписке')&&!nodes['model-list'].innerHTML.includes('Локальные модели'));run("M.engineF='all';C.chosen=''");

  // The navigation folds to icons and remembers it in this browser.
  run('setRail(true)');assert.equal(nodes.app.classList.contains('rail'),true);assert.equal(store.get('omnibox-rail'),'1');assert.equal(nodes['nav-fold'].title,'Развернуть меню');
  run('setRail(false)');assert.equal(nodes.app.classList.contains('rail'),false);assert.equal(store.get('omnibox-rail'),'');
  assert.ok(['Чат','Модели','Сервер','Настройки'].every(n=>html.includes(`title="${n}"><svg`)||html.includes(`title="${n}">`)),'rail icons have tips');

  // Other versions: newer ones update, older ones roll back; the installed one has no button.
  run("ST.versions={current:'0.23.0',releases:[{version:'0.24.0',newer:true,summary:'Новое',published:'2026-10-13T10:00:00Z'},{version:'0.23.0',current:true},{version:'0.22.0',summary:'<b>x</b>'}]};renderVersions()");
  const vl=nodes.versions.innerHTML;assert.ok(vl.includes('data-install-version="0.24.0">Обновить')&&vl.includes('data-install-version="0.22.0">Откатить')&&!vl.includes('data-install-version="0.23.0"')&&vl.includes('&lt;b&gt;x'),vl);
  assert.equal(run("newerThan('0.23.0','0.9.9')"),true);assert.equal(run("newerThan('0.9.9','0.23.0')"),false);
  // A rollback asks first and names what stays; cancelling installs nothing.
  run("S.status={version:'0.23.0'}");const ops=[];context.fetch=async(p,o)=>{ops.push(JSON.parse(o.body));return {ok:true,json:async()=>({id:'u'})};};
  const roll=run("installVersion('0.22.0')");assert.equal(nodes['confirm-title'].textContent,'Откатить на 0.22.0?');assert.ok(nodes['confirm-text'].textContent.includes('чаты и модели останутся'));
  nodes['confirm-no'].listeners.click.at(-1)();await roll;assert.equal(ops.length,0);
  const roll2=run("installVersion('0.22.0')");nodes['confirm-yes'].listeners.click.at(-1)();await new Promise(r=>setImmediate(r));
  assert.deepEqual(ops[0],{action:'self-update',version:'0.22.0'});run('waiting.clear()');context.fetch=oldFetch;

  // Logged out: the API answers 401 and the login form is shown.
  winListeners.DOMContentLoaded.forEach(fn=>fn());await new Promise(r=>setTimeout(r,10));
  assert.ok(requests.includes('/api/session'));assert.equal(nodes.login.hidden,false);assert.equal(nodes.app.hidden,true);
  console.log('web_test: ok');
})().catch(e=>{console.error(e);process.exit(1);});

