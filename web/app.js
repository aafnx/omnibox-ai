'use strict';
// OMNIBOX AI panel: the core shared by the pages (chat.js, models.js, server.js, settings.js).
// Nothing here polls on its own: data is asked for on login, on a page change, on the owner's
// action, and while an operation runs or a model works with the Server page open.
const $=s=>document.querySelector(s), $$=s=>[...document.querySelectorAll(s)];
const byId=id=>document.getElementById(id);
const esc=t=>String(t??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
let csrf='', loggedIn=false;

// ── API ──
async function api(path,body){
  const post=body!==undefined;
  const r=await fetch(path,{method:post?'POST':'GET',credentials:'same-origin',headers:post?{'Content-Type':'application/json','X-Omnibox-CSRF':csrf}:{},body:post?JSON.stringify(body):undefined});
  let data={};try{data=await r.json();}catch(e){data={};}
  if(!r.ok){if(r.status===401&&path!=='/api/login')showLogin();const err=new Error(typeof data.error==='string'?data.error:data.error?.message||`Ошибка ${r.status}`);err.status=r.status;throw err;}
  return data;
}

// ── notices ──
function notice(message,error=false){
  if(!message)return;
  const t=document.createElement('div');t.className='toast'+(error?' error':'');t.setAttribute('role',error?'alert':'status');
  t.innerHTML=`<span>${esc(message)}</span><button type="button" aria-label="Закрыть">×</button>`;
  const remove=()=>t.remove();t.querySelector('button').addEventListener('click',remove);
  const box=byId('toasts');box.append(t);while(box.children.length>4)box.firstElementChild.remove();
  setTimeout(remove,error?10000:5000);
}
const fail=e=>notice(e.message||String(e),true);

// ── theme: system unless chosen; stored in this browser only ──
const THEMES=['system','light','dark'];
let theme='system';try{const v=localStorage.getItem('omnibox-theme');if(THEMES.includes(v))theme=v;}catch(e){}
function applyTheme(v){theme=v;const root=document.documentElement;if(v==='system')root.removeAttribute('data-theme');else root.setAttribute('data-theme',v);
  $$('#theme [data-theme-set]').forEach(b=>b.setAttribute('aria-pressed',String(b.dataset.themeSet===v)));try{localStorage.setItem('omnibox-theme',v);}catch(e){}}
applyTheme(theme);

// ── formatting ──
const plural=(n,a,b,c)=>{const m=n%10,h=n%100;return m===1&&h!==11?a:m>=2&&m<=4&&(h<12||h>14)?b:c;};
const fmtN=n=>n===null||n===undefined||Number.isNaN(n)?'—':Number(n).toLocaleString('ru-RU');
const gb=x=>x.toFixed(1).replace('.',',');
const bytesGb=b=>gb((b||0)/1073741824)+' ГБ';
const fsize=n=>n<1048576?Math.max(1,Math.round(n/1024))+' КБ':(n/1048576).toFixed(1).replace('.',',')+' МБ';
const money=x=>x<0.01?'$'+x.toFixed(4).replace('.',','):'$'+x.toFixed(2).replace('.',',');
const usd2=x=>'$'+(x||0).toFixed(2).replace('.',',');
const price=x=>x<0?'—':'$'+(Math.round(x*100)/100).toString().replace('.',',');
const kfmt=t=>t>=1048576?Math.round(t/1048576)+'M':t>=1024?Math.round(t/1024)+'K':String(t);
const kTok=n=>n>=1000?Math.round(n/1000)+' тыс.':String(n);
const dateShort=d=>new Date(d).toLocaleDateString('ru-RU',{day:'numeric',month:'short'}).replace('.','');
const timeShort=d=>new Date(d).toLocaleTimeString('ru-RU',{hour:'2-digit',minute:'2-digit'});
// Inline style attributes are not allowed by the panel's CSP: sizes are set through the CSSOM.
function paint(root){root.querySelectorAll('[data-w],[data-h],[data-l],[data-b]').forEach(el=>{const d=el.dataset;
  if(d.w!==undefined)el.style.width=d.w;if(d.h!==undefined)el.style.height=d.h;if(d.l!==undefined)el.style.left=d.l;if(d.b!==undefined)el.style.bottom=d.b;});}
function setHTML(el,html){el.innerHTML=html;paint(el);}

// ── capabilities: the same icons everywhere ──
const CAP={
  vision:{icon:'i-eye',name:'Изображения',tip:'Понимает изображения: скриншоты, фото, сканы документов.'},
  novision:{icon:'i-eye',name:'Изображения: нужен проектор',tip:'Модель умеет видеть, но файл проектора mmproj не скачан. Откройте модель → «Скачать проектор».',missing:true},
  video:{icon:'i-video',name:'Видео',tip:'Понимает видео: смотрит ролик и отвечает по нему.'},
  audio:{icon:'i-audio',name:'Аудио',tip:'Понимает аудио: речь, записи разговоров, звуки.'},
  reasoning:{icon:'i-brain',name:'Рассуждения',tip:'Думает перед ответом: точнее на задачах с логикой, кодом и математикой, но отвечает дольше.'},
  tools:{icon:'i-tool',name:'Функции',tip:'Вызывает инструменты: веб-поиск в чате OmniBox, функции и MCP-серверы в Open WebUI или Jan.'},
  draws:{icon:'i-spark',name:'Рисует',tip:'Рисует изображения по текстовому описанию.'},
};
const OFF={vision:'Не понимает изображения.',video:'Не понимает видео.',audio:'Не понимает аудио.',reasoning:'Отвечает сразу, без отдельного этапа размышлений.',tools:'Не вызывает инструменты: веб-поиск в чате будет выполняться по каждому вопросу.',draws:'Не рисует: отвечает только текстом.'};
const has=(m,k)=>!!m?.caps?.[k];
const EFFORT={minimal:'минимум',low:'быстро',medium:'обычно',high:'глубоко',xhigh:'очень глубоко',max:'максимум'};
const SHOW={full:'полный текст размышлений',summary:'краткое изложение размышлений',hidden:'только время размышлений'};
function thinkTip(m){const t=m.reasoning||{mode:'none'};if(t.mode==='none')return OFF.reasoning;
  return `Рассуждения: ${t.mode==='always'?'всегда включены, выключить нельзя':'можно включать и выключать'}${t.efforts?.length?`; глубина: ${t.efforts.map(e=>EFFORT[e]).join(', ')}`:''}${t.budget?'; можно ограничить бюджет токенов':''}. В чате видно ${SHOW[t.show||'full']}.${t.no_sampling?' Температуру и сэмплинг модель не принимает.':''}`;}
function ico(m,k,on){const c=CAP[k];const tip=k==='reasoning'?thinkTip(m):on?`${c.name}. ${c.tip}`:OFF[k.replace('novision','vision')];
  return `<span class="ico${on?'':' off'}${c.missing?' missing':''}" tabindex="0" data-tip="${esc(tip)}" aria-label="${esc(c.name)}${on?'':': нет'}"><svg><use href="#${c.icon}"/></svg></span>`;}
// Every capability, the missing ones dimmed: the cards of the model list.
function allChips(m){const miss=has(m,'vision_missing');
  return `<span class="icaps">${[miss?ico(m,'novision',true):ico(m,'vision',has(m,'vision')),ico(m,'video',has(m,'video')),ico(m,'audio',has(m,'audio')),ico(m,'reasoning',has(m,'reasoning')),ico(m,'tools',has(m,'tools')),ico(m,'draws',has(m,'draws'))].join('')}</span>`;}
// Only what the model can do, for compact lists.
function capIcons(m){const keys=['vision','video','audio','reasoning','tools','draws'].filter(k=>has(m,k));if(has(m,'vision_missing'))keys.unshift('novision');
  return `<span class="icaps">${keys.map(k=>`<span class="ico${CAP[k].missing?' missing':''}" data-tip="${esc(k==='reasoning'?thinkTip(m):CAP[k].name+'. '+CAP[k].tip)}" aria-label="${CAP[k].name}"><svg><use href="#${CAP[k].icon}"/></svg></span>`).join('')}${m.context?`<span class="ictx" data-tip="Контекст: сколько текста модель держит в разговоре.">${kfmt(m.context)}</span>`:''}</span>`;}
const ctxChip=m=>m.context?`<span class="ictx chip" tabindex="0" data-tip="Контекст: сколько текста модель держит в разговоре."><svg class="i"><use href="#i-ctx"/></svg>${kfmt(m.context)}</span>`:'';

// ── reasoning scale: one effort scale for every model ──
const STEP_TOK={off:[0,0],minimal:[200,1000],low:[1000,3000],medium:[3000,8000],high:[8000,20000],xhigh:[20000,48000],max:[48000,64000]};
const STEP_NAME={off:'Выкл',on:'Вкл',auto:'Авто',minimal:'Минимум',low:'Быстро',medium:'Обычно',high:'Глубоко',xhigh:'Очень глубоко',max:'Максимум'};
function steps(t){if(!t||t.mode==='none')return [];let s=t.efforts?.length?[...t.efforts]:t.budget?['low','medium','high','max']:[t.mode==='toggle'?'on':'auto'];if(t.mode==='toggle')s=['off',...s];return s;}
const range=(a,b)=>a>=1000&&b>=1000?`${Math.round(a/1000)}–${Math.round(b/1000)} тыс.`:`${kTok(a)}–${kTok(b)}`;
const usd3=x=>x.toFixed(x<0.01?3:2).replace('.',',');
function stepNote(m,step){const t=m.reasoning||{};if(step==='off')return 'Отвечает сразу, без размышлений.';if(step==='on'||step==='auto')return 'Модель сама решает, сколько думать.';
  const [a,b]=STEP_TOK[step]||[0,0];const tok=t.budget?`до ${kTok(b)} токенов на размышления`:`≈ ${range(a,b)} токенов на размышления`;
  const out=m.completion_usd>0?m.completion_usd:0;const cost=!out?'':t.budget?` · до $${usd3(b*out/1e6)}`:` · ≈ $${usd3(a*out/1e6)}–${usd3(b*out/1e6)}`;
  return tok+cost;}

// ── tooltips for icons (fixed, so they are never cut by a scrolling list) ──
const ftip=document.createElement('div');ftip.id='ftip';ftip.hidden=true;ftip.setAttribute('role','tooltip');document.body.append(ftip);
function showTip(e){const t=e.target.closest?.('.ico[data-tip],.ictx[data-tip]');if(!t){ftip.hidden=true;return;}ftip.textContent=t.dataset.tip;ftip.hidden=false;
  const r=t.getBoundingClientRect();const w=ftip.offsetWidth;ftip.style.left=Math.max(8,Math.min(innerWidth-w-8,r.left+r.width/2-w/2))+'px';ftip.style.top=(r.bottom+6)+'px';}
document.addEventListener('mouseover',showTip);document.addEventListener('focusin',showTip);document.addEventListener('scroll',()=>{ftip.hidden=true;},true);

// ── collapsible groups, remembered in this browser ──
let collapsed=new Set();try{collapsed=new Set(JSON.parse(localStorage.getItem('omnibox-collapsed')||'[]'));}catch(e){}
function toggleGroup(key){collapsed.has(key)?collapsed.delete(key):collapsed.add(key);try{localStorage.setItem('omnibox-collapsed',JSON.stringify([...collapsed]));}catch(e){}}
const chev=key=>`<svg class="i chev${collapsed.has(key)?' shut':''}"><use href="#i-down"/></svg>`;

// ── shared state ──
const S={status:null,library:null,cloud:null,account:null,chatSettings:null,assistants:{assistants:[],default:''},page:'chat'};
const listeners={status:[],library:[]};
const on=(name,fn)=>listeners[name].push(fn);
const emit=name=>listeners[name].forEach(fn=>{try{fn();}catch(e){console.error(e);}});
async function refreshStatus(gpu){S.status=await api('/api/status'+(gpu?'?gpu=1':''));emit('status');return S.status;}
let libraryBusy=null;
async function refreshLibrary(){if(libraryBusy)return libraryBusy;libraryBusy=api('/api/library').then(d=>{S.library=d;emit('library');return d;}).finally(()=>{libraryBusy=null;});return libraryBusy;}
const models=()=>S.library?.models||[];
const modelByKey=key=>models().find(m=>m.key===key);
const runningModel=()=>modelByKey(S.library?.running);
const engineLabel=n=>({prismml:'PrismML',ollama:'Ollama',openrouter:'OpenRouter'})[n]||n;

// ── operations: started by the owner, followed only while they run ──
const LABELS={'model-start':'Запуск модели',stopall:'Выгрузка моделей',unload:'Выгрузка',sleep:'Сон ПК',poweroff:'Выключение ПК',download:'Скачивание модели',pull:'Скачивание модели','model-delete':'Удаление модели','engine-install':'Установка движка','engine-update':'Обновление движка','engine-remove':'Удаление движка','self-update':'Обновление программы','model-test':'Проверка скорости',autostart:'Автозапуск',start:'Запуск',stop:'Остановка'};
const waiting=new Map();let opTimer=0;
async function runAction(body){const r=await api('/api/actions',body);const p=new Promise((resolve)=>waiting.set(r.id,resolve));watchOps();return {id:r.id,done:p};}
function watchOps(){clearTimeout(opTimer);opTimer=setTimeout(async()=>{try{await refreshStatus();}catch(e){}
  const ops=S.status?.operations||[];
  for(const [id,resolve] of waiting){const op=ops.find(o=>o.id===id);if(op&&op.state!=='running'){waiting.delete(id);resolve(op);}}
  if(waiting.size||ops.some(o=>o.state==='running'))watchOps();},waiting.size?1200:3000);}
// Starts an operation and tells the result; returns the finished operation.
async function operation(body,{quiet=false}={}){const {done}=await runAction(body);const op=await done;if(!quiet||op.state==='failed')notice(op.message,op.state==='failed');return op;}
function transferText(op){if(!op.bytes_total)return op.bytes_done?`${(op.bytes_done/1048576).toFixed(1)} МБ`:`${Math.floor(op.elapsed_seconds||0)} с`;
  const big=op.bytes_total>=1073741824,size=n=>big?(n/1073741824).toFixed(2).replace('.',','):(n/1048576).toFixed(1).replace('.',',');const pct=Math.floor(100*(op.bytes_done||0)/op.bytes_total);
  let t=`${size(op.bytes_done||0)} из ${size(op.bytes_total)} ${big?'ГБ':'МБ'} (${pct}%)`;if(op.bytes_per_second>0){const left=(op.bytes_total-(op.bytes_done||0))/op.bytes_per_second;t+=` · ${(op.bytes_per_second/1048576).toFixed(1).replace('.',',')} МБ/с · осталось ${left>=3600?Math.floor(left/3600)+' ч '+Math.floor(left%3600/60)+' мин':Math.max(1,Math.ceil(left/60))+' мин'}`;}return t;}
// Running operations of the given actions, rendered where they were started.
function renderOps(el,actions){const ops=(S.status?.operations||[]).filter(o=>o.state==='running'&&(!actions||actions.includes(o.action)));el.hidden=!ops.length;
  setHTML(el,ops.map(o=>`<div class="op"><span class="st">↻</span><b>${esc(LABELS[o.action]||o.action)}${o.backend?' · '+engineLabel(o.backend):''}</b>${['download','pull'].includes(o.action)?`<button class="btn ghost sm" type="button" data-cancel-op="${esc(o.id)}">Отменить</button>`:'<span></span>'}<div class="meter${o.bytes_total?'':' run'}"><i${o.bytes_total?` data-w="${Math.floor(100*(o.bytes_done||0)/o.bytes_total)}%"`:''}></i></div><span class="msg">${esc(transferText(o))}</span></div>`).join(''));}
document.addEventListener('click',e=>{const c=e.target.closest('[data-cancel-op]');if(c)api('/api/actions',{action:'cancel',id:c.dataset.cancelOp}).then(()=>notice('Скачивание отменяется.')).catch(fail);});

// ── confirmation inline, never a browser dialog ──
function ask(box,text,yes){const t=box.querySelector('span');t.textContent=text;box.hidden=false;
  return new Promise(resolve=>{const y=box.querySelector('.btn.danger'),n=box.querySelector('.btn.ghost');
    const done=v=>{box.hidden=true;y.removeEventListener('click',ok);n.removeEventListener('click',no);resolve(v);};const ok=()=>done(true),no=()=>done(false);
    if(yes)y.textContent=yes;y.addEventListener('click',ok);n.addEventListener('click',no);});}

// ── markdown: escaped first, then a small safe subset ──
function inlineMd(s,sources){
  const codes=[];s=s.replace(/`([^`\n]+)`/g,(_,c)=>{codes.push(c);return `\u0000${codes.length-1}\u0000`;});
  s=s.replace(/\[([^\]\n]{1,200})\]\((https?:\/\/[^\s)]{1,800})\)/g,(_,t,u)=>`<a href="${u}" target="_blank" rel="noopener noreferrer">${t}</a>`);
  s=s.replace(/\*\*([^*\n]+)\*\*/g,'<b>$1</b>').replace(/(^|[^*\w])\*([^*\n]+)\*(?=[^*\w]|$)/g,'$1<i>$2</i>');
  if(sources?.length)s=s.replace(/\[(\d{1,2})\](?!\()/g,(m,n)=>{const src=sources[+n-1];return src?`<a class="cite" href="${esc(src.url)}" target="_blank" rel="noopener noreferrer"><sup class="c">${n}</sup></a>`:m;});
  return s.replace(/\u0000(\d+)\u0000/g,(_,i)=>`<code>${codes[+i]}</code>`);}
function md(text,sources){
  const lines=esc(text).split('\n');let out=[],i=0;
  const para=[];const flush=()=>{if(para.length){out.push(`<p>${inlineMd(para.join('<br>'),sources)}</p>`);para.length=0;}};
  while(i<lines.length){const l=lines[i];
    const fence=l.match(/^\s*```\s*([\w+-]*)\s*$/);
    if(fence){flush();const code=[];i++;while(i<lines.length&&!/^\s*```\s*$/.test(lines[i]))code.push(lines[i++]);i++;out.push(`<pre><button type="button" class="copycode">Копировать</button><code>${code.join('\n')}</code></pre>`);continue;}
    const h=l.match(/^(#{1,6})\s+(.+)$/);if(h){flush();out.push(`<${h[1].length<=2?'h3':'h4'}>${inlineMd(h[2],sources)}</${h[1].length<=2?'h3':'h4'}>`);i++;continue;}
    if(/^\s*([-*_])\s*\1\s*\1[\s\1]*$/.test(l)){flush();out.push('<hr>');i++;continue;}
    if(/^\s*&gt;\s?/.test(l)){flush();const q=[];while(i<lines.length&&/^\s*&gt;\s?/.test(lines[i]))q.push(lines[i++].replace(/^\s*&gt;\s?/,''));out.push(`<blockquote>${inlineMd(q.join('<br>'),sources)}</blockquote>`);continue;}
    if(/^\s*([-*+]|\d{1,3}[.)])\s+/.test(l)){flush();const ordered=/^\s*\d/.test(l);const items=[];
      while(i<lines.length&&/^\s*([-*+]|\d{1,3}[.)])\s+/.test(lines[i])){let item=lines[i++].replace(/^\s*([-*+]|\d{1,3}[.)])\s+/,'');while(i<lines.length&&/^\s{2,}\S/.test(lines[i])&&!/^\s*([-*+]|\d{1,3}[.)])\s+/.test(lines[i]))item+='<br>'+lines[i++].trim();items.push(`<li>${inlineMd(item,sources)}</li>`);}
      out.push(ordered?`<ol>${items.join('')}</ol>`:`<ul>${items.join('')}</ul>`);continue;}
    if(/^\s*\|.*\|\s*$/.test(l)&&i+1<lines.length&&/^\s*\|[\s:|-]+\|\s*$/.test(lines[i+1])){flush();const cells=r=>r.trim().replace(/^\||\|$/g,'').split('|').map(c=>inlineMd(c.trim(),sources));
      const head=cells(l);i+=2;const rows=[];while(i<lines.length&&/^\s*\|.*\|\s*$/.test(lines[i]))rows.push(cells(lines[i++]));
      out.push(`<table><thead><tr>${head.map(c=>`<th>${c}</th>`).join('')}</tr></thead><tbody>${rows.map(r=>`<tr>${r.map(c=>`<td>${c}</td>`).join('')}</tr>`).join('')}</tbody></table>`);continue;}
    if(!l.trim()){flush();i++;continue;}
    para.push(l);i++;}
  flush();return out.join('');}
document.addEventListener('click',e=>{const b=e.target.closest('.copycode');if(!b)return;copyText(b.nextElementSibling.textContent).then(()=>{b.textContent='Скопировано';setTimeout(()=>b.textContent='Копировать',1500);},fail);});
async function copyText(text){if(navigator.clipboard&&window.isSecureContext){await navigator.clipboard.writeText(text);return;}
  const t=document.createElement('textarea');t.value=text;document.body.append(t);t.select();const ok=document.execCommand('copy');t.remove();if(!ok)throw new Error('Выделите текст и скопируйте вручную.');}

// ── drawers and dialogs ──
function show(el){byId('scrim').hidden=false;el.hidden=false;}
function hideAll(){byId('scrim').hidden=true;['drawer','add','asst-drawer'].forEach(id=>byId(id).hidden=true);}
document.addEventListener('keydown',e=>{if(e.key==='Escape')hideAll();});

// ── navigation ──
const PAGES={};
function go(page){if(!PAGES[page])page='chat';S.page=page;$$('[data-page]').forEach(p=>p.hidden=p.id!==page);
  $$('.nav ul a').forEach(a=>a.dataset.go===page?a.setAttribute('aria-current','page'):a.removeAttribute('aria-current'));
  if(location.hash!=='#'+page)history.replaceState(null,'','#'+page);if(page!=='chat')window.scrollTo(0,0);PAGES[page].show?.();}
document.addEventListener('click',e=>{
  const g=e.target.closest('[data-go]');if(g){e.preventDefault();go(g.dataset.go);const j=g.dataset.jumpAfter;if(j)setTimeout(()=>byId(j)?.scrollIntoView({block:'start'}),60);return;}
  const j=e.target.closest('[data-jump]');if(j){e.preventDefault();byId(j.dataset.jump)?.scrollIntoView({behavior:'smooth',block:'start'});return;}
  if(e.target.closest('[data-close]')||e.target===byId('scrim')){hideAll();return;}
  const cp=e.target.closest('[data-copy]');if(cp){const el=byId(cp.dataset.copy);copyText(el.value).then(()=>{cp.textContent='Скопировано';setTimeout(()=>cp.textContent='Копировать',1500);},()=>el.select());return;}
  const t=e.target.closest('[data-theme-set]');if(t){applyTheme(t.dataset.themeSet);return;}
});

// ── the box under the navigation: what runs now ──
function renderNow(){const m=runningModel();const b=(S.status?.backends||[]).find(x=>x.service==='Running'||x.service==='StartPending');
  byId('now-dot').className='dot'+(m||b?'':' off');byId('now-model').textContent=m?.name||b?.selected_model||'Модель не запущена';
  byId('now-sub').textContent=b?`${engineLabel(b.name)} · ${b.service==='StartPending'?'запускается':b.api==='Ready'?'готов':'запускается'}`:'Облачные модели работают без запуска';
  const g=S.sample?.gpu?.[0];byId('now-meter').hidden=byId('now-vram').hidden=!g;
  if(g){const i=byId('now-meter').firstElementChild;i.style.width=Math.round(g.used/g.total*100)+'%';byId('now-vram').textContent=`Видеопамять ${gb(g.used/1024)} / ${gb(g.total/1024)} ГБ`;}
  const a=S.account;byId('now-balance').hidden=!a;if(a)byId('now-balance-v').textContent=usd2(a.balance);}
on('status',renderNow);on('library',renderNow);

// OpenRouter's balance is asked on login and when the owner opens its settings.
async function refreshAccount(){if(!S.cloud?.connected){S.account=null;renderNow();return null;}
  try{S.account=await api('/api/cloud?account=1');}catch(e){S.account=null;}renderNow();return S.account;}
async function refreshCloud(){S.cloud=await api('/api/cloud');return S.cloud;}

// ── login ──
function showLogin(){loggedIn=false;csrf='';byId('app').hidden=true;byId('login').hidden=false;hideAll();setTimeout(()=>byId('password').focus(),50);}
async function showApp(){const s=await api('/api/session');csrf=s.csrf;loggedIn=true;byId('login').hidden=true;byId('app').hidden=false;
  // The page opens at once; what it shows fills in as the data arrives.
  const start=(location.hash||'').slice(1);go(PAGES[start]?start:'chat');
  await Promise.all([refreshStatus(true).catch(fail),refreshLibrary().catch(fail),refreshCloud().catch(()=>{}),
    api('/api/chat-settings').then(d=>{S.chatSettings=d;}).catch(()=>{}),api('/api/assistants').then(d=>{S.assistants=d;}).catch(()=>{})]);
  PAGES[S.page].show?.();
  // One sample of the resources and the balance at login; nothing repeats by itself.
  api('/api/telemetry?once=1').then(d=>{if(d.sample){S.sample=d.sample;renderNow();}}).catch(()=>{});
  refreshAccount().then(a=>{const low=S.cloud?.low_balance_usd;if(a&&low>0&&a.balance<low)notice(`На счёте OpenRouter ${usd2(a.balance)} — меньше ${usd2(low)}. Пополните баланс в настройках OpenRouter.`,true);});
  if((S.status?.operations||[]).some(o=>o.state==='running'))watchOps();}
byId('login-form').addEventListener('submit',async e=>{e.preventDefault();byId('login-error').textContent='';const b=e.currentTarget.querySelector('button');b.disabled=true;
  try{await api('/api/login',{password:byId('password').value});byId('password').value='';await showApp();}catch(err){byId('login-error').textContent=err.message;}finally{b.disabled=false;}});
window.addEventListener('DOMContentLoaded',()=>{showApp().catch(()=>showLogin());});
// Returning to the tab refreshes what is shown once.
document.addEventListener('visibilitychange',()=>{if(!document.hidden&&loggedIn)PAGES[S.page]?.show?.(true);});
if('serviceWorker' in navigator&&window.isSecureContext)navigator.serviceWorker.register('/sw.js').catch(()=>{});
