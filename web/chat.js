'use strict';
// Chat: the threads stored on the PC, the conversation, the composer.
const C={threads:[],hits:null,chat:null,chosen:'',web:false,attached:[],busy:null,starting:false,pickerQ:''};
try{C.chosen=localStorage.getItem('omnibox-model')||'';}catch(e){}
let efforts={};try{efforts=JSON.parse(localStorage.getItem('omnibox-effort')||'{}')||{};}catch(e){efforts={};}
const ACT_USER='<div class="mact"><button type="button" data-act="copy" title="Копировать" aria-label="Копировать"><svg class="i"><use href="#i-copy"/></svg></button><button type="button" data-act="resend" title="Отправить ещё раз" aria-label="Отправить ещё раз"><svg class="i"><use href="#i-retry"/></svg></button><button type="button" class="del" data-act="delete" title="Удалить сообщение" aria-label="Удалить сообщение"><svg class="i"><use href="#i-trash"/></svg></button></div>';
const ACT_BOT='<div class="mact"><button type="button" data-act="copy" title="Копировать" aria-label="Копировать"><svg class="i"><use href="#i-copy"/></svg></button><button type="button" data-act="retry" title="Ответить заново" aria-label="Ответить заново"><svg class="i"><use href="#i-retry"/></svg></button><button type="button" class="del" data-act="delete" title="Удалить сообщение" aria-label="Удалить сообщение"><svg class="i"><use href="#i-trash"/></svg></button></div>';

// ── threads ──
const hoursLeft=t=>t.expires?(Date.parse(t.expires)-Date.now())/3600000:Infinity;
const remain=t=>{const h=hoursLeft(t);return h>=24?`${Math.floor(h/24)} дн.`:h>=1?`${Math.floor(h)} ч`:'<1 ч';};
const leftText=t=>{const h=hoursLeft(t);return h>=24?`удалится через ${Math.floor(h/24)} ${plural(Math.floor(h/24),'день','дня','дней')}`:h>=1?`удалится через ${Math.floor(h)} ${plural(Math.floor(h),'час','часа','часов')}`:'удалится меньше чем через час';};
function threadRow(t){const cur=C.chat?.id===t.id;return `<li class="t"><a href="#chat" data-thread="${t.id}" class="${cur?'on':''}"><span>${esc(t.title)}</span><small>${dateShort(t.created)}${t.pinned||!t.expires?'':` · <span class="remain${hoursLeft(t)<24?' soon':''}" title="${leftText(t)}"><svg class="i"><use href="#i-clock"/></svg>${esc(remain(t))}</span>`}</small></a>`+
  `<div class="tacts"><button type="button" class="${t.pinned?'on':''}" data-pin="${t.id}" title="${t.pinned?'Открепить':'Закрепить'}" aria-label="${t.pinned?'Открепить':'Закрепить'} «${esc(t.title)}»"><svg class="i"><use href="#i-pin"/></svg></button><button type="button" class="tdel" data-del-thread="${t.id}" title="Удалить" aria-label="Удалить «${esc(t.title)}»"><svg class="i"><use href="#i-trash"/></svg></button></div></li>`;}
function renderThreads(){const list=byId('thread-list');
  if(C.hits){setHTML(list,C.hits.length?C.hits.map(h=>`<li class="t"><a href="#chat" data-thread="${h.id}"><span>${esc(h.title)}</span><small>${esc(h.snippet||dateShort(h.updated))}</small></a></li>`).join(''):'<li class="group">Ничего не найдено</li>');return;}
  const pin=C.threads.filter(t=>t.pinned),rest=C.threads.filter(t=>!t.pinned);
  const tg=(title,items)=>{if(!items.length)return '';const key='t:'+title;const shut=collapsed.has(key);return `<li class="group"><button type="button" class="grp-btn tgrp" data-grp="${key}" aria-expanded="${!shut}"><span>${title}</span><span class="grp-n">${items.length}</span>${chev(key)}</button></li>`+(shut?'':items.map(threadRow).join(''));};
  setHTML(list,tg('Закреплённые',pin)+tg('Чаты',rest)||'<li class="group">Чатов пока нет</li>');renderKeep();}
function renderKeep(){const k=byId('keep');const t=C.chat&&C.threads.find(x=>x.id===C.chat.id);k.hidden=!t||t.pinned||!t.expires;if(k.hidden)return;
  k.classList.toggle('soon',hoursLeft(t)<24);k.innerHTML=`<svg class="i"><use href="#i-clock"/></svg>${esc(remain(t))}`;k.title=`Создан ${dateShort(t.created)}, ${leftText(t)}. Срок меняется в «Настройки → Данные и хранение».`;}
async function loadThreads(){const d=await api('/api/chats');C.threads=d.chats||[];renderThreads();}

// ── one chat ──
function chatModelName(key){return modelByKey(key)?.name||(key||'').replace(/^(prismml|ollama|cloud):/,'');}
function userHTML(m){const files=(m.attachments||[]).map(a=>`<span class="fchip"><svg class="i"><use href="#${KIND_ICON[a.kind]||'i-file'}"/></svg><span class="fn">${esc(a.name)}</span><span class="fs">${fsize(a.size||0)}</span></span>`).join('');
  return `<div class="msg user" data-id="${esc(m.id||'')}">${files?`<div class="files">${files}</div>`:''}${m.text?`<div class="bubble">${esc(m.text)}</div>`:''}${ACT_USER}</div>`;}
function metaHTML(m){const x=m.meta||{};const parts=[esc(chatModelName(m.model))];
  if(x.seconds)parts.push(`${String(x.seconds).replace('.',',')} с`);if(x.output_tokens)parts.push(`${fmtN(x.output_tokens)} ${plural(x.output_tokens,'токен','токена','токенов')}`);
  if(x.tokens_per_second)parts.push(`${String(x.tokens_per_second).replace('.',',')} ток/с`);
  return parts.map(p=>`<span>${p}</span>`).join('')+(x.cost_usd?`<span class="cost" title="Стоимость по данным OpenRouter">${money(x.cost_usd)}</span>`:'');}
function traceHTML(m){const x=m.meta||{};let h='';
  if(x.searches?.length)h+=`<details class="step"><summary><svg class="i i15"><use href="#i-globe"/></svg>Искал в интернете · ${x.searches.length} ${plural(x.searches.length,'запрос','запроса','запросов')}${x.sources?.length?` · ${x.sources.length} ${plural(x.sources.length,'источник','источника','источников')}`:''}</summary><div class="in">${x.searches.map(q=>`<span>«${esc(q)}»</span>`).join('')}</div></details>`;
  return h?`<div class="trace">${h}</div>`:'';}
function thinkHTML(m,open){if(!m.reasoning)return '';const s=m.reasoning_seconds?` · ${String(m.reasoning_seconds).replace('.',',')} с`:'';
  return `<details class="think"${open?' open':''}><summary><svg class="i i15"><use href="#i-brain"/></svg>Размышления${s}</summary><div class="in">${esc(m.reasoning)}</div></details>`;}
function sourcesHTML(list){return list?.length?`<div class="sources">${list.map((s,i)=>`<a class="src" href="${esc(s.url)}" target="_blank" rel="noopener noreferrer"><b>${i+1} · ${esc(s.title||s.url)}</b><span>${esc(hostOf(s.url))}</span></a>`).join('')}</div>`:'';}
const hostOf=u=>{try{return new URL(u).hostname.replace(/^www\./,'');}catch(e){return '';}};
function botHTML(m){return `<div class="msg bot" data-id="${esc(m.id||'')}">${traceHTML(m)}${thinkHTML(m)}${m.text?`<div class="md">${md(m.text,m.meta?.sources)}</div>`:''}${sourcesHTML(m.meta?.sources)}${m.error?`<div class="err">${esc(m.error)}</div>`:''}<div class="botfoot">${ACT_BOT}<div class="meta">${metaHTML(m)}</div></div></div>`;}
function renderChat(){const box=byId('message-list');const c=C.chat;
  if(!c||!c.messages?.length){setHTML(box,`<div class="empty"><b>${c?esc(c.title):'Новый чат'}</b><span>Спросите что-нибудь. Можно приложить файлы и включить поиск в интернете.</span></div>`);}
  else setHTML(box,c.messages.map(m=>m.role==='user'?userHTML(m):botHTML(m)).join(''));
  showChatCost();renderKeep();scrollDown(true);}
function scrollDown(force){const el=byId('messages');if(force||el.scrollHeight-el.scrollTop-el.clientHeight<160)el.scrollTop=el.scrollHeight;}
function showChatCost(){const c=byId('chat-cost');const paid=(C.chat?.messages||[]).filter(m=>m.meta?.cost_usd>0);c.hidden=!paid.length;if(!paid.length)return;
  const sum=paid.reduce((s,m)=>s+m.meta.cost_usd,0);c.innerHTML=`${money(sum)}<span class="lbl"> за чат</span>`;
  c.title=`Стоимость этого чата по данным OpenRouter: ${paid.length} ${plural(paid.length,'ответ','ответа','ответов')}. Локальные ответы бесплатны.`;}
async function openChat(id){C.chat=await api('/api/chats?id='+encodeURIComponent(id));
  if(C.chat.model&&modelByKey(C.chat.model))setChosen(C.chat.model,false);
  C.web=webDefault(C.chat.assistant_id);renderAll();}
function newChat(){C.chat=null;C.attached=[];C.web=webDefault(S.assistants.default);C.asst=S.assistants.default||'';const a=asstById(C.asst);if(a?.model&&modelByKey(a.model))setChosen(a.model,false);renderAll();byId('prompt').focus();}
function renderAll(){renderThreads();renderChat();renderPicker();renderAssist();renderThink();renderWeb();renderAttached();renderFoot();}

// ── models: the picker ──
function setChosen(key,start=true){C.chosen=key;try{localStorage.setItem('omnibox-model',key);}catch(e){}
  renderPicker();renderThink();recheckAttached();renderFoot();const m=modelByKey(key);if(start&&m?.source==='local'&&!m.running)startLocal(m).catch(fail);}
const curModel=()=>modelByKey(C.chosen);
async function startLocal(m){if(C.starting)return;C.starting=true;loadBegin(m);renderBusy(`Запускаем ${m.name}`);
  try{const op=await operation({action:'model-start',model:m.key},{quiet:true});if(op.state==='failed')return;await refreshLibrary();}finally{C.starting=false;loadEnd();renderBusy();}}
function renderBusy(text){byId('model-busy').hidden=!text;if(text)byId('model-busy-text').textContent=text;byId('send').disabled=!!text&&!C.busy;}
function pickerItem(m){const pill=m.source==='cloud'?`<span class="pill sky">${price(m.prompt_usd)} / ${price(m.completion_usd)}</span>`:m.running?'<span class="pill ok">Работает</span>':'<span class="pill">Локальная</span>';
  return `<button type="button" data-pick-model="${esc(m.key)}"${m.key===C.chosen?' aria-current="true"':''}><b>${m.favorite?'★ ':''}${esc(m.name)}</b>${pill}${capIcons(m)}</button>`;}
function renderPicker(){const m=curModel();const btn=byId('picker-btn');
  btn.innerHTML=`${m?.source==='cloud'?'<svg class="i i15"><use href="#i-cloud"/></svg>':`<span class="dot${m?.running?'':' off'}"></span>`}<span class="pname">${esc(m?.name||'Выберите модель')}</span><svg class="i i15"><use href="#i-down"/></svg>`;
  if(byId('picker-menu').hidden)return;
  const q=C.pickerQ.toLowerCase(),match=x=>!q||(x.name+' '+x.id).toLowerCase().includes(q);const all=models().filter(match);
  const grp=(title,list)=>{if(!list.length)return '';const key='p:'+title;const shut=!q&&collapsed.has(key);return `<button type="button" class="grp-btn h4" data-grp="${key}" aria-expanded="${!shut}"><span>${title}</span><span class="grp-n">${list.length}</span><svg class="i chev${shut?' shut':''}"><use href="#i-down"/></svg></button>${shut?'':list.map(pickerItem).join('')}`;};
  const cloudOff=!S.cloud?.connected?'<div class="note">OpenRouter не подключён: <a href="#settings" data-go="settings" data-jump-after="s-cloud" class="ink">добавить ключ</a>.</div>':'';
  setHTML(byId('picker-list'),(grp('Избранные',all.filter(x=>x.favorite))+grp('Локальные модели',all.filter(x=>!x.favorite&&x.source==='local'))+grp('OpenRouter',all.filter(x=>!x.favorite&&x.source==='cloud'))||'<div class="note">Ничего не найдено</div>')+cloudOff);
  byId('picker-note').textContent=m?.source==='cloud'?'Облачная модель: запрос уходит в OpenRouter, локальная модель продолжает работать.':'Другая локальная модель запустится вместо текущей, это займёт 10–60 секунд.';}
function renderFoot(){const m=curModel();const f=byId('chat-foot');
  if(m?.source!=='cloud'){f.textContent='ИИ может ошибаться — проверяйте важное.';return;}
  const hist=(C.chat?.messages||[]).reduce((n,x)=>n+(x.text||'').length,0)/3+(byId('prompt').value.length/3)+300;const est=(hist*m.prompt_usd+500*m.completion_usd)/1e6;
  f.innerHTML=`<span class="cloudnote" title="Оценка: история чата ≈${fmtN(Math.round(hist))} токенов на входе и ≈500 в ответе. Точная стоимость придёт от OpenRouter после ответа."><svg class="i"><use href="#i-cloud"/></svg>OpenRouter · ≈${money(Math.max(est,0))} за ответ</span><span class="sep"> · </span><span class="disc">ИИ может ошибаться — проверяйте важное.</span>`;}

// ── assistants ──
const asstById=id=>(S.assistants.assistants||[]).find(a=>a.id===id);
const chatAsst=()=>C.chat?C.chat.assistant_id:(C.asst??S.assistants.default);
function avatar(a,cls=''){if(a)return `<span class="av ${cls}${a.emoji?' emo':''}">${a.emoji?esc(a.emoji):esc((a.name.trim()[0]||'?').toUpperCase())}</span>`;
  return cls.includes('slot')?`<span class="av none ${cls}"><svg class="i"><use href="#i-plus"/></svg></span>`:`<span class="av none ${cls}"></span>`;}
function renderAssist(){const a=asstById(chatAsst());const b=byId('asst-btn');b.className='asst-chip'+(a?' on':'');
  b.innerHTML=`${avatar(a,a?'':'slot')}<span class="an">${a?esc(a.name):'Без ассистента'}</span><svg class="i i14"><use href="#i-down"/></svg>`;b.title=a?`Ассистент: ${a.name}`:'Ассистент не выбран — нажмите, чтобы выбрать';
  const d=S.assistants.default;
  setHTML(byId('asst-menu'),`<button type="button" data-asst-pick=""${!a?' aria-current="true"':''}>${avatar(null)}<b>Без ассистента</b><span></span><span class="d">Обычный чат без инструкций</span></button>`+(S.assistants.assistants||[]).map(x=>`<button type="button" data-asst-pick="${x.id}"${a?.id===x.id?' aria-current="true"':''}>${avatar(x)}<b>${esc(x.name)}</b>${x.id===d?'<span class="pill iris">По умолчанию</span>':'<span></span>'}<span class="d">${esc(x.description)}</span></button>`).join('')+`<div class="mfoot"><button type="button" data-go="settings" data-jump-after="s-assist"><span></span><b>Управлять ассистентами</b><span></span></button></div>`);}
const webDefault=id=>{const a=asstById(id);return a?!!a.web_search:!!S.chatSettings?.search_default;};
async function pickAssistant(id){const a=asstById(id);if(a?.model&&modelByKey(a.model))setChosen(a.model);if(a)C.web=!!a.web_search;
  // The chat keeps its assistant with the next message.
  if(C.chat)C.chat.assistant_id=id;else C.asst=id;
  byId('asst-menu').hidden=true;renderAssist();renderWeb();}

// ── reasoning ──
function renderThink(){const m=curModel()||{reasoning:{mode:'none'}};const t=m.reasoning||{mode:'none'},S2=steps(t);
  let st=efforts[m.key];if(!S2.includes(st))st=S2.includes(t.default)?t.default:S2[Math.min(S2.length-1,S2[0]==='off'?2:1)]||S2[0];
  const on=t.mode!=='none'&&st!=='off';const lbl=t.mode==='none'?'Без размышлений':!on?'Размышления: выкл':S2.length>2||t.mode==='always'?`Размышления: ${STEP_NAME[st].toLowerCase()}`:'Размышления';
  const b=byId('think-btn');b.querySelector('.lbl').textContent=S2.length===1&&t.mode==='always'?'Размышления':lbl;b.classList.toggle('dim',!on);b.setAttribute('aria-pressed',String(on));b.title=lbl;
  let h='';
  if(t.mode==='none')h='<div class="line"><b>Размышления</b><span class="pill">Нет</span></div><span class="hint">Эта модель отвечает сразу и не умеет размышлять. Для сложных задач выберите модель со значком «Рассуждения».</span>';
  else if(S2.length===1)h='<div class="line"><b>Размышления</b><span class="pill iris">Всегда включены</span></div><span class="hint">Модель сама решает, сколько думать; настроить глубину нельзя.</span>';
  else{const i=S2.indexOf(st),n=S2.length-1;
    h=`<div class="tslider"><div class="tval" aria-hidden="true">${STEP_NAME[st]}</div><div class="ttrack"><div class="tfill" data-w="calc(36px + (100% - 36px) * ${i/n})"></div>${S2.map((s,j)=>`<span class="tdot${j<=i?' in':''}" data-l="calc(18px + (100% - 36px) * ${j/n})"></span>`).join('')}<span class="tknob" data-l="calc(18px + (100% - 36px) * ${i/n})"></span><input type="range" id="think-range" min="0" max="${n}" step="1" value="${i}" aria-label="Глубина размышлений" aria-valuetext="${STEP_NAME[st]}"></div><div class="tends"><span>${STEP_NAME[S2[0]]}</span><span>${STEP_NAME[S2[n]]}</span></div><div class="tnote">${stepNote(m,st)}</div></div>`;
    if(t.mode==='always')h+='<span class="hint">У этой модели размышления нельзя выключить — только сделать короче или глубже.</span>';}
  if(t.mode!=='none')h+=`<span class="hint">В чате будет видно: ${SHOW[t.show||'full']}.</span>`;
  if(t.no_sampling)h+='<span class="hint">Температуру и сэмплинг эта модель не принимает — OmniBox их не отправляет.</span>';
  setHTML(byId('think-pop'),h);C.effort=t.mode==='none'?'':st;}
let thinkKbd=false;
byId('think-pop').addEventListener('keydown',e=>{if(e.target.id==='think-range')thinkKbd=true;});
byId('think-pop').addEventListener('pointerdown',()=>{thinkKbd=false;});
byId('think-pop').addEventListener('input',e=>{if(e.target.id!=='think-range')return;const m=curModel();if(!m)return;efforts[m.key]=steps(m.reasoning)[+e.target.value];
  try{localStorage.setItem('omnibox-effort',JSON.stringify(efforts));}catch(x){}renderThink();if(thinkKbd){byId('think-range').focus();$('.ttrack').classList.add('kbd');}});

// ── web search ──
function renderWeb(){const s=S.chatSettings||{};const ready=!!s.search_url;byId('web').setAttribute('aria-pressed',String(C.web&&ready));byId('web-on').checked=C.web;byId('web-on').disabled=!ready;
  byId('web-host').textContent=ready?hostOf(s.search_url)||s.search_url:'не задан';
  byId('web-hint').innerHTML=ready?(curModel()&&!has(curModel(),'tools')?'Эта модель не вызывает функции: поиск выполняется по каждому вопросу. Источники появятся под ответом.':'Модель сама решает, когда искать. Источники появятся под ответом.'):'Адрес SearXNG не задан: <a href="#settings" data-go="settings" data-jump-after="s-search" class="ink">настроить</a>.';}

// ── attachments: refused before sending when the model cannot read them ──
const KINDS={image:{name:'изображения',need:'vision',ext:['png','jpg','jpeg','webp','gif']},video:{name:'видео',need:'video',ext:['mp4','mov','webm','mkv','avi']},audio:{name:'аудио',need:'audio',ext:['mp3','wav','m4a','ogg','flac','opus']},
  word:{name:'документы Word',need:'',ext:['docx']},excel:{name:'таблицы Excel',need:'',ext:['xlsx','csv']},text:{name:'текстовые файлы',need:'',ext:['txt','md','json','log','yaml','yml','xml','html','go','py','js','ts','ps1','sh','ini','conf']}};
const KIND_ICON={image:'i-eye',video:'i-video',audio:'i-audio',word:'i-file',excel:'i-table',text:'i-file'};
const MAX_FILE=25*1024*1024;
function kindOf(name){const ext=(String(name).split('.').pop()||'').toLowerCase();for(const [k,v] of Object.entries(KINDS))if(v.ext.includes(ext))return k;return null;}
function canRead(m,k){const need=KINDS[k].need;return !need||has(m,need);}
function tokensOf(size,k){if(k==='image')return 1500;if(k==='audio'||k==='video')return null;return Math.round(size/(k==='word'||k==='excel'?6:4));}
function addFiles(list){const m=curModel();const refused=[];
  for(const f of list){const k=kindOf(f.name);
    if(!k){refused.push(`«${f.name}»: такой тип файла не поддерживается. Можно: изображения, видео, аудио, текст, Word, Excel.`);continue;}
    if(f.size>MAX_FILE){refused.push(`«${f.name}»: больше 25 МБ.`);continue;}
    if(!m){refused.push('Сначала выберите модель: от неё зависит, какие файлы она понимает.');break;}
    if(!canRead(m,k)){refused.push(`${m.name} не понимает ${KINDS[k].name} — «${f.name}» не прикреплён.`);continue;}
    if(C.attached.length>=10){refused.push('Не больше 10 файлов в одном сообщении.');break;}
    C.attached.push({name:f.name,size:f.size,kind:k,tok:tokensOf(f.size,k),file:f});}
  renderAttached();if(refused.length)attachWarn(refused);}
function attachWarn(lines){const w=byId('attach-warn');w.innerHTML=lines.map(l=>`<div>${esc(l)}</div>`).join('')+'<button type="button" class="btn ghost sm" id="attach-warn-x">Понятно</button>';w.hidden=false;}
function renderAttached(){const box=byId('attached');box.hidden=!C.attached.length;
  box.innerHTML=C.attached.map((a,i)=>`<span class="fchip"><svg class="i"><use href="#${KIND_ICON[a.kind]}"/></svg><span class="fn">${esc(a.name)}</span><span class="fs">${fsize(a.size)}${a.tok?` · ≈ ${kTok(a.tok)} ток.`:''}</span><button type="button" data-unattach="${i}" aria-label="Убрать ${esc(a.name)}"><svg class="i"><use href="#i-x"/></svg></button></span>`).join('');}
// A model switch drops attachments the new model cannot read, with the same warning.
function recheckAttached(){const m=curModel();if(!m)return;const drop=C.attached.filter(a=>!canRead(m,a.kind));if(!drop.length)return;C.attached=C.attached.filter(a=>canRead(m,a.kind));renderAttached();
  attachWarn(drop.map(a=>`${m.name} не понимает ${KINDS[a.kind].name} — «${a.name}» убран из сообщения.`));}
const readB64=f=>new Promise((resolve,reject)=>{const r=new FileReader();r.onload=()=>resolve(String(r.result).replace(/^data:[^,]*,/,''));r.onerror=()=>reject(new Error(`«${f.name}» не прочитан.`));r.readAsDataURL(f);});

// ── sending: the answer streams as server-sent events ──
async function send(opts={}){if(C.busy)return;const m=curModel();
  if(!m){notice('Выберите модель в списке над чатом.',true);byId('picker-btn').click();return;}
  if(C.starting){notice('Модель ещё запускается — подождите несколько секунд.');return;}
  const text=opts.retry?'':(opts.text??byId('prompt').value).trim();const files=opts.retry||opts.text!==undefined?[]:C.attached;
  if(!text&&!files.length&&!opts.retry)return;
  if(m.source==='local'&&!m.running){await startLocal(m);if(!modelByKey(m.key)?.running)return;}
  if(!C.chat){C.chat=await api('/api/chats',{action:'create',model:m.key,assistant_id:chatAsst()||''});C.chat.messages=[];C.threads.unshift(C.chat);renderThreads();}
  let data;try{data=await Promise.all(files.map(async a=>({name:a.name,data:await readB64(a.file)})));}catch(e){fail(e);return;}
  const box=byId('message-list');if(!C.chat.messages.length)box.innerHTML='';
  if(!opts.retry){box.insertAdjacentHTML('beforeend',userHTML({text,attachments:files.map(a=>({name:a.name,kind:a.kind,size:a.size}))}));if(opts.text===undefined){byId('prompt').value='';C.attached=[];renderAttached();byId('attach-warn').hidden=true;}}
  box.insertAdjacentHTML('beforeend','<div class="msg bot live"><div class="trace"></div><details class="think" hidden><summary><svg class="i i15"><use href="#i-brain"/></svg><span>Размышляет…</span></summary><div class="in"></div></details><div class="md caret"></div><div class="sources-slot"></div><div class="status-line" hidden></div></div>');
  const live=box.lastElementChild;scrollDown(true);
  const ctrl=new AbortController();C.busy=ctrl;renderSend();
  const body={chat_id:C.chat.id,model:m.key,text,files:data,web:!!(C.web&&S.chatSettings?.search_url),effort:C.effort||'',assistant_id:chatAsst()||''};if(opts.retry)body.retry=opts.retry;
  let answer='',thinking='',sources=[],done=null;
  try{const r=await fetch('/api/chat/send',{method:'POST',credentials:'same-origin',headers:{'Content-Type':'application/json','X-Omnibox-CSRF':csrf},body:JSON.stringify(body),signal:ctrl.signal});
    if(!r.ok){let msg=`Ошибка ${r.status}`;try{msg=(await r.json()).error||msg;}catch(e){}if(r.status===401)showLogin();throw new Error(msg);}
    const reader=r.body.getReader(),dec=new TextDecoder();let buf='',pending=0;
    const paintAnswer=()=>{pending=0;live.querySelector('.md').innerHTML=md(answer,sources);scrollDown();};
    for(;;){const {value,done:end}=await reader.read();if(end)break;buf+=dec.decode(value,{stream:true});let i;
      while((i=buf.indexOf('\n\n'))>=0){const block=buf.slice(0,i);buf=buf.slice(i+2);let ev='',dt='';block.split('\n').forEach(l=>{if(l.startsWith('event: '))ev=l.slice(7);else if(l.startsWith('data: '))dt+=l.slice(6);});
        let v;try{v=JSON.parse(dt);}catch(e){continue;}
        if(ev==='user'){const u=box.querySelector('.msg.user:not([data-id]),.msg.user[data-id=""]');if(u&&!opts.retry)u.dataset.id=v.id;}
        else if(ev==='delta'){answer+=v.t;if(!pending)pending=requestAnimationFrame(paintAnswer);}
        else if(ev==='reasoning'){thinking+=v.t;const d=live.querySelector('details.think');d.hidden=false;d.querySelector('.in').textContent=thinking;}
        else if(ev==='search'){live.querySelector('.trace').insertAdjacentHTML('beforeend',`<span class="status-line"><svg class="i i15"><use href="#i-globe"/></svg>Ищу: «${esc(v.query)}»</span>`);scrollDown();}
        else if(ev==='sources'){sources=v||[];}
        else if(ev==='status'){const s=live.querySelector('.status-line:last-child');s.hidden=false;s.textContent=v.text;}
        else if(ev==='done'){done=v;}}}
  }catch(e){if(e.name!=='AbortError'){live.querySelector('.md').classList.remove('caret');live.insertAdjacentHTML('beforeend',`<div class="err">${esc(e.message)}</div>`);}}
  finally{C.busy=null;renderSend();}
  if(done?.message){const msg=done.message;C.chat.messages=C.chat.messages||[];
    if(opts.retry){const i=C.chat.messages.findIndex(x=>x.id===opts.retry);C.chat.messages=C.chat.messages.slice(0,i+1);}
    else if(text||files.length){C.chat.messages.push({id:[...box.querySelectorAll('.msg.user')].pop()?.dataset.id,role:'user',text,attachments:files.map(a=>({name:a.name,kind:a.kind,size:a.size}))});}
    C.chat.messages.push(msg);live.outerHTML=botHTML(msg);paint(box);
    if(done.chat){const i=C.threads.findIndex(t=>t.id===done.chat.id);if(i>=0)C.threads[i]=done.chat;else C.threads.unshift(done.chat);C.chat.title=done.chat.title;}
    renderThreads();showChatCost();renderFoot();if(msg.meta?.cost_usd)refreshAccount();
  }else if(C.chat){setTimeout(()=>openChat(C.chat.id).catch(()=>{}),400);}
  scrollDown();}
function renderSend(){const b=byId('send');b.classList.toggle('stop',!!C.busy);b.setAttribute('aria-label',C.busy?'Остановить ответ':'Отправить');b.title=C.busy?'Остановить ответ':'';
  b.innerHTML=`<svg class="i"><use href="#${C.busy?'i-stop':'i-up'}"/></svg>`;b.disabled=false;}

// ── message actions ──
async function messageAction(act,el){const msg=el.closest('.msg');const id=msg.dataset.id;const stored=(C.chat?.messages||[]).find(m=>m.id===id);
  if(act==='copy'){await copyText(stored?.text??msg.innerText);el.title='Скопировано';return;}
  if(act==='delete'){if(!id||!C.chat)return msg.remove();const s=await api('/api/chats',{action:'delete-message',id:C.chat.id,message_id:id});C.chat.messages=C.chat.messages.filter(m=>m.id!==id);msg.remove();
    const i=C.threads.findIndex(t=>t.id===s.id);if(i>=0)C.threads[i]=s;showChatCost();return;}
  if(act==='resend'){send({text:stored?.text||msg.querySelector('.bubble')?.textContent||''});return;}
  if(act==='retry'){const all=C.chat?.messages||[];const i=all.findIndex(m=>m.id===id);const user=[...all.slice(0,i)].reverse().find(m=>m.role==='user');if(!user)return;
    C.chat.messages=all.slice(0,all.indexOf(user)+1);let n=msg;while(n){const next=n.nextElementSibling;n.remove();n=next;}send({retry:user.id});}}

// ── deleting chats ──
function askDelAll(step){const pins=C.threads.filter(t=>t.pinned).length,rest=C.threads.length-pins;byId('del-all').hidden=false;
  if(step===1){byId('del-all-text').textContent=`Удалить чаты (${C.threads.length}) с диска? Восстановить будет нельзя.`;byId('del-all-btns').innerHTML='<button class="btn danger sm" type="button" data-del-all-step>Удалить</button><button class="btn ghost sm" type="button" data-del-all-no>Отмена</button>';}
  else{byId('del-all-text').textContent=`Среди них закреплённые: ${pins}. Удалить и их?`;byId('del-all-btns').innerHTML=`<button class="btn danger sm" type="button" data-del-all-go="all">Удалить все</button><button class="btn sm" type="button" data-del-all-go="keep">Только незакреплённые (${rest})</button><button class="btn ghost sm" type="button" data-del-all-no>Отмена</button>`;}}
async function delAll(all){const r=await api('/api/chats',{action:'delete-all',keep_pinned:!all});byId('del-all').hidden=true;notice(`Удалено чатов: ${r.deleted}.`);
  await loadThreads();if(C.chat&&!C.threads.some(t=>t.id===C.chat.id))newChat();}

// ── events ──
byId('composer').addEventListener('submit',e=>{e.preventDefault();if(C.busy){C.busy.abort();return;}send().catch(fail);});
byId('prompt').addEventListener('keydown',e=>{if(e.key==='Enter'&&!e.shiftKey&&!e.isComposing&&!matchMedia('(hover:none)').matches){e.preventDefault();byId('composer').requestSubmit();}});
byId('prompt').addEventListener('input',()=>{if(curModel()?.source==='cloud')renderFoot();});
byId('new-chat').addEventListener('click',()=>{newChat();$('.threads').classList.remove('open');});
let searchTimer=0;
byId('thread-search').addEventListener('input',e=>{clearTimeout(searchTimer);const q=e.target.value.trim();searchTimer=setTimeout(async()=>{if(!q){C.hits=null;renderThreads();return;}try{C.hits=(await api('/api/chats?q='+encodeURIComponent(q))).hits||[];renderThreads();}catch(x){fail(x);}},250);});
byId('picker-q').addEventListener('input',e=>{C.pickerQ=e.target.value;renderPicker();});
byId('attach-btn').addEventListener('click',()=>byId('attach-input').click());
byId('attach-input').addEventListener('change',e=>{addFiles([...e.target.files]);e.target.value='';});
byId('attached').addEventListener('click',e=>{const b=e.target.closest('[data-unattach]');if(b){C.attached.splice(+b.dataset.unattach,1);renderAttached();}});
byId('attach-warn').addEventListener('click',e=>{if(e.target.closest('#attach-warn-x'))byId('attach-warn').hidden=true;});
byId('web-on').addEventListener('change',e=>{C.web=e.target.checked;renderWeb();});
const conv=byId('conv');let dragDepth=0;
conv.addEventListener('dragenter',e=>{if(![...e.dataTransfer.types].includes('Files'))return;e.preventDefault();dragDepth++;byId('drop').hidden=false;});
conv.addEventListener('dragover',e=>{if([...e.dataTransfer.types].includes('Files'))e.preventDefault();});
conv.addEventListener('dragleave',()=>{if(--dragDepth<=0){dragDepth=0;byId('drop').hidden=true;}});
conv.addEventListener('drop',e=>{e.preventDefault();dragDepth=0;byId('drop').hidden=true;addFiles([...e.dataTransfer.files]);});
document.addEventListener('click',e=>{
  if(!e.target.closest('.thinkwrap'))byId('think-pop').hidden=true;
  if(!e.target.closest('.webwrap')){byId('web-pop').hidden=true;byId('web').setAttribute('aria-expanded','false');}
  if(!e.target.closest('.asst-picker'))byId('asst-menu').hidden=true;
  if(!e.target.closest('.conv-head .picker'))byId('picker-menu').hidden=true;
  if(e.target.closest('#think-btn')){byId('think-pop').hidden=!byId('think-pop').hidden;return;}
  if(e.target.closest('#web')){const p=byId('web-pop');p.hidden=!p.hidden;byId('web').setAttribute('aria-expanded',String(!p.hidden));return;}
  if(e.target.closest('#asst-btn')){const m=byId('asst-menu');m.hidden=!m.hidden;byId('asst-btn').setAttribute('aria-expanded',String(!m.hidden));return;}
  const ap=e.target.closest('[data-asst-pick]');if(ap){pickAssistant(ap.dataset.asstPick).catch(fail);return;}
  if(e.target.closest('#picker-btn')){const m=byId('picker-menu');m.hidden=!m.hidden;byId('picker-btn').setAttribute('aria-expanded',String(!m.hidden));if(!m.hidden){renderPicker();byId('picker-q').focus();}return;}
  const pm=e.target.closest('[data-pick-model]');if(pm){byId('picker-menu').hidden=true;setChosen(pm.dataset.pickModel);return;}
  const gb2=e.target.closest('[data-grp]');if(gb2&&(gb2.dataset.grp[0]==='t'||gb2.dataset.grp[0]==='p')){toggleGroup(gb2.dataset.grp);gb2.dataset.grp[0]==='t'?renderThreads():renderPicker();return;}
  const act=e.target.closest('[data-act]');if(act){messageAction(act.dataset.act,act).catch(fail);return;}
  if(e.target.closest('#threads-btn')){$('.threads').classList.add('open');return;}
  if(e.target.closest('[data-close-threads]')){$('.threads').classList.remove('open');return;}
  const ta=e.target.closest('[data-thread]');if(ta){e.preventDefault();$('.threads').classList.remove('open');openChat(ta.dataset.thread).catch(fail);return;}
  const pn=e.target.closest('[data-pin]');if(pn){const t=C.threads.find(x=>x.id===pn.dataset.pin);api('/api/chats',{action:t?.pinned?'unpin':'pin',id:pn.dataset.pin}).then(s=>{const i=C.threads.findIndex(x=>x.id===s.id);C.threads[i]=s;C.threads.sort((a,b)=>(b.pinned-a.pinned)||Date.parse(b.updated)-Date.parse(a.updated));renderThreads();}).catch(fail);return;}
  const td=e.target.closest('[data-del-thread]');if(td){const id=td.dataset.delThread;api('/api/chats',{action:'delete',id}).then(()=>{C.threads=C.threads.filter(t=>t.id!==id);if(C.chat?.id===id)newChat();else renderThreads();}).catch(fail);return;}
  if(e.target.closest('[data-del-all]')){if(C.threads.length)askDelAll(1);return;}
  if(e.target.closest('[data-del-all-no]')){byId('del-all').hidden=true;return;}
  if(e.target.closest('[data-del-all-step]')){C.threads.some(t=>t.pinned)?askDelAll(2):delAll(true).catch(fail);return;}
  const da=e.target.closest('[data-del-all-go]');if(da){delAll(da.dataset.delAllGo==='all').catch(fail);return;}
});
on('library',()=>{if(S.page==='chat'){renderPicker();renderThink();renderWeb();renderFoot();}});
PAGES.chat={show(again){if(!C.chosen||!modelByKey(C.chosen)){const r=runningModel()||models().find(m=>m.favorite)||models()[0];if(r)C.chosen=r.key;}
  if(again){refreshLibrary().catch(()=>{});}
  loadThreads().then(()=>{if(!C.chat)C.web=webDefault(chatAsst());renderAll();}).catch(fail);renderAll();}};
document.addEventListener('keydown',e=>{if(e.key==='Escape')['think-pop','web-pop','asst-menu','picker-menu'].forEach(id=>byId(id).hidden=true);});
