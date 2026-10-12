'use strict';
// Chat: the threads stored on the PC, the conversation, the composer.
const C={threads:[],hits:null,chat:null,chosen:'',web:false,attached:[],busy:null,starting:false,pickerQ:'',renaming:null,follow:null};
try{C.chosen=localStorage.getItem('omnibox-model')||'';}catch(e){}
let efforts={};try{efforts=JSON.parse(localStorage.getItem('omnibox-effort')||'{}')||{};}catch(e){efforts={};}
const ACT_USER='<div class="mact"><button type="button" data-act="copy" title="Копировать" aria-label="Копировать"><svg class="i"><use href="#i-copy"/></svg></button><button type="button" data-act="resend" title="Отправить ещё раз" aria-label="Отправить ещё раз"><svg class="i"><use href="#i-retry"/></svg></button><button type="button" class="del" data-act="delete" title="Удалить сообщение" aria-label="Удалить сообщение"><svg class="i"><use href="#i-trash"/></svg></button></div>';
const ACT_BOT='<div class="mact"><button type="button" data-act="copy" title="Копировать" aria-label="Копировать"><svg class="i"><use href="#i-copy"/></svg></button><button type="button" data-act="retry" title="Ответить заново" aria-label="Ответить заново"><svg class="i"><use href="#i-retry"/></svg></button><button type="button" class="del" data-act="delete" title="Удалить сообщение" aria-label="Удалить сообщение"><svg class="i"><use href="#i-trash"/></svg></button></div>';

// ── threads ──
const hoursLeft=t=>t.expires?(Date.parse(t.expires)-Date.now())/3600000:Infinity;
const remain=t=>{const h=hoursLeft(t);return h>=24?`${Math.floor(h/24)} дн.`:h>=1?`${Math.floor(h)} ч`:'<1 ч';};
const leftText=t=>{const h=hoursLeft(t);return h>=24?`удалится через ${Math.floor(h/24)} ${plural(Math.floor(h/24),'день','дня','дней')}`:h>=1?`удалится через ${Math.floor(h)} ${plural(Math.floor(h),'час','часа','часов')}`:'удалится меньше чем через час';};
// A row is the title and its date; the actions (rename, favourite, delete) are in the "⋯" menu, so
// they never cover the title. A row being renamed is a field.
function threadRow(t){const cur=C.chat?.id===t.id;
  if(C.renaming===t.id)return `<li class="t"><input id="thread-rename" class="trename" value="${esc(t.title)}" maxlength="120" aria-label="Название чата" data-rename-id="${t.id}"></li>`;
  return `<li class="t"><a href="#chat" data-thread="${t.id}" class="${cur?'on':''}"><span class="tt">${esc(t.title)}</span><small>${dateShort(t.created)}${t.pinned||!t.expires?'':` · <span class="remain${hoursLeft(t)<24?' soon':''}" title="${leftText(t)}"><svg class="i"><use href="#i-clock"/></svg>${esc(remain(t))}</span>`}</small></a>`+
  `<button type="button" class="tmore" data-tmenu="${t.id}" aria-haspopup="menu" aria-expanded="false" title="Действия" aria-label="Действия с чатом «${esc(t.title)}»"><svg class="i"><use href="#i-dots"/></svg></button></li>`;}
function threadMenu(t){const f=followOn(t);return `<button type="button" role="menuitem" data-rename="${t.id}"><svg class="i"><use href="#i-edit"/></svg>Переименовать</button>`+
  `<button type="button" role="menuitemcheckbox" aria-checked="${f}" data-follow-chat="${t.id}" class="${f?'on2':''}" title="Вопросы для продолжения под последним ответом. По умолчанию — как в «Настройки → Чат»."><svg class="i"><use href="#i-spark"/></svg>Предлагать вопросы<span class="tick">${f?'вкл':'выкл'}</span></button>`+
  `<button type="button" role="menuitem" data-pin="${t.id}" class="${t.pinned?'on':''}"><svg class="i"><use href="#i-star"/></svg>${t.pinned?'Убрать из избранного':'В избранное'}</button>`+
  `<button type="button" role="menuitem" class="del" data-del-thread="${t.id}"><svg class="i"><use href="#i-trash"/></svg>Удалить</button>`;}
// The menu is fixed to the window: the list scrolls and would cut it off.
function openThreadMenu(btn){const t=C.threads.find(x=>x.id===btn.dataset.tmenu);const m=byId('thread-menu');if(!t)return;
  if(!m.hidden&&m.dataset.for===t.id){closeThreadMenu();return;}
  closeThreadMenu();setHTML(m,threadMenu(t));m.dataset.for=t.id;m.hidden=false;btn.setAttribute('aria-expanded','true');btn.classList.add('open');
  const r=btn.getBoundingClientRect(),w=m.offsetWidth,h=m.offsetHeight;
  m.style.left=Math.max(8,Math.min(innerWidth-w-8,r.right-w))+'px';m.style.top=(r.bottom+h+8>innerHeight?Math.max(8,r.top-h-4):r.bottom+4)+'px';}
function closeThreadMenu(){const m=byId('thread-menu');m.hidden=true;m.dataset.for='';$$('.tmore.open').forEach(b=>{b.classList.remove('open');b.setAttribute('aria-expanded','false');});}
function startRename(id){if(!C.threads.some(x=>x.id===id))return;C.renaming=id;closeThreadMenu();renderThreads();const f=byId('thread-rename');f.focus();f.select();}
// Enter or leaving the field saves; Escape keeps the old title. The service trims and checks it.
async function commitRename(save){const id=C.renaming;if(!id)return;const title=(byId('thread-rename').value||'').trim();C.renaming=null;
  const t=C.threads.find(x=>x.id===id);if(!save||!title||title===t?.title){renderThreads();return;}
  try{const s=await api('/api/chats',{action:'rename',id,title});const i=C.threads.findIndex(x=>x.id===id);if(i>=0)C.threads[i]=s;if(C.chat?.id===id){C.chat.title=s.title;renderChat();}}
  catch(e){fail(e);}renderThreads();}
function renderThreads(){const list=byId('thread-list');
  if(C.hits){setHTML(list,C.hits.length?C.hits.map(h=>`<li class="t"><a href="#chat" data-thread="${h.id}"><span>${esc(h.title)}</span><small>${esc(h.snippet||dateShort(h.updated))}</small></a></li>`).join(''):'<li class="group">Ничего не найдено</li>');return;}
  const pin=C.threads.filter(t=>t.pinned),rest=C.threads.filter(t=>!t.pinned);
  const tg=(title,items)=>{if(!items.length)return '';const key='t:'+title;const shut=collapsed.has(key);return `<li class="group"><button type="button" class="grp-btn tgrp" data-grp="${key}" aria-expanded="${!shut}"><span>${title}</span><span class="grp-n">${items.length}</span>${chev(key)}</button></li>`+(shut?'':items.map(threadRow).join(''));};
  setHTML(list,tg('Избранные',pin)+tg('Чаты',rest)||'<li class="group">Чатов пока нет</li>');renderKeep();}
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
  const pages=x.pages||[];
  if(x.searches?.length||pages.length)h+=`<details class="step"><summary><svg class="i i15"><use href="#i-globe"/></svg>${x.searches?.length?`Искал в интернете · ${x.searches.length} ${plural(x.searches.length,'запрос','запроса','запросов')}`:'Читал страницы'}${x.sources?.length?` · ${x.sources.length} ${plural(x.sources.length,'источник','источника','источников')}`:''}${pages.length?` · прочитано ${pages.length} ${plural(pages.length,'страница','страницы','страниц')}`:''}</summary><div class="in">${(x.searches||[]).map(q=>`<span>«${esc(q)}»</span>`).join('')}${pages.map(u=>`<span class="rd">Прочитано: <a href="${esc(u)}" target="_blank" rel="noopener noreferrer">${esc(hostOf(u)||u)}</a></span>`).join('')}</div></details>`;
  return h?`<div class="trace">${h}</div>`:'';}
function thinkHTML(m,open){if(!m.reasoning)return '';const s=m.reasoning_seconds?` · ${String(m.reasoning_seconds).replace('.',',')} с`:'';
  return `<details class="step think"${open?' open':''}><summary><svg class="i i15"><use href="#i-brain"/></svg>Размышления${s}</summary><div class="in">${esc(m.reasoning)}</div></details>`;}
// A site's badge: its first letter on a brand tone picked by the name. With "site icons" on in the
// settings the browser loads the site's own favicon over it; a site without one keeps the letter.
function favHTML(u){let site='';try{site=new URL(u).hostname;}catch(e){}const h=site.replace(/^www\./,'');let n=0;for(const ch of h)n=(n*31+ch.charCodeAt(0))%9973;
  const img=S.chatSettings?.site_icons&&site?`<img src="https://${esc(site)}/favicon.ico" alt="" loading="lazy" referrerpolicy="no-referrer">`:'';
  return `<span class="fav t${n%3}" aria-hidden="true">${esc((h[0]||'?').toUpperCase())}${img}</span>`;}
function sourcesHTML(list){if(!list?.length)return '';const n=list.length,seen=new Set(),stack=list.filter(s=>{const h=hostOf(s.url);if(seen.has(h))return false;seen.add(h);return true;}).slice(0,3);
  return `<details class="srcs"><summary><span class="favs">${stack.map(s=>favHTML(s.url)).join('')}</span>${n} ${plural(n,'источник','источника','источников')}</summary>`+
    `<ol class="srclist">${list.map((s,i)=>`<li><a href="${esc(s.url)}" target="_blank" rel="noopener noreferrer"><span class="n">${i+1}</span>${favHTML(s.url)}<span class="st"><b>${esc(s.title||s.url)}</b><span>${esc(hostOf(s.url))}</span></span></a></li>`).join('')}</ol></details>`;}
// A favicon that loaded covers the letter; one that failed is removed, the letter stays.
document.addEventListener('load',e=>{const t=e.target;if(t?.tagName==='IMG'&&t.parentElement?.classList?.contains('fav'))t.parentElement.classList.add('img');},true);
document.addEventListener('error',e=>{const t=e.target;if(t?.tagName==='IMG'&&t.parentElement?.classList?.contains('fav'))t.remove();},true);
function botHTML(m,last){return `<div class="msg bot" data-id="${esc(m.id||'')}">${traceHTML(m)}${thinkHTML(m)}${m.text?`<div class="md">${md(m.text,m.meta?.sources)}</div>`:''}${sourcesHTML(m.meta?.sources)}${m.error?`<div class="err">${esc(m.error)}</div>`:''}<div class="botfoot">${ACT_BOT}<div class="meta">${metaHTML(m)}</div></div>${last?followHTML(m):''}</div>`;}
// ── suggested questions ("Продолжить") under the last answer ──
// On for a chat by its own choice, else by the setting. They are asked for after the answer as a
// separate request, which a new message or leaving the chat cancels: the model is never kept busy
// with them while the user waits.
const followOn=c=>c?.follow_ups??!!S.chatSettings?.follow_ups;
function followHTML(m){const q=m.meta?.follow_ups;if(!q?.length)return '';
  return `<div class="follow"><span class="fh">Продолжить</span>${q.map(x=>`<button type="button" data-follow="${esc(x)}"><svg class="i"><use href="#i-up"/></svg><span>${esc(x)}</span></button>`).join('')}</div>`;}
function stopFollowUps(){if(C.follow){C.follow.ctrl.abort();C.follow=null;}$$('.follow.wait').forEach(el=>el.remove());}
async function requestFollowUps(chatId,msgId){stopFollowUps();const ctrl=new AbortController();C.follow={ctrl,chat:chatId,msg:msgId};
  const el=()=>C.chat?.id===chatId?byId('message-list').querySelector(`.msg.bot[data-id="${msgId}"]`):null;
  el()?.insertAdjacentHTML('beforeend','<div class="follow wait"><span class="fh">Подбираю вопросы…</span></div>');
  let d=null,err='';
  try{const r=await fetch('/api/chat/followups',{method:'POST',credentials:'same-origin',headers:{'Content-Type':'application/json','X-Omnibox-CSRF':csrf},body:JSON.stringify({chat_id:chatId,message_id:msgId}),signal:ctrl.signal});
    d=await r.json().catch(()=>({}));if(!r.ok){err=d.error||`Ошибка ${r.status}`;d=null;}}
  catch(e){if(e.name==='AbortError')return;err=e.message;}
  if(C.follow?.ctrl!==ctrl)return;C.follow=null;
  const box=el();box?.querySelector('.follow.wait')?.remove();
  // A suggestion that failed is not worth an alert: a faint line says why, the chat goes on.
  if(!d){box?.insertAdjacentHTML('beforeend',`<div class="follow"><span class="fh err">Вопросы не предложены: ${esc(err)}</span></div>`);return;}
  const m=C.chat?.id===chatId&&C.chat.messages.find(x=>x.id===msgId);if(!m)return;
  m.meta=m.meta||{};m.meta.follow_ups=d.follow_ups||[];if(d.cost_usd)m.meta.cost_usd=(m.meta.cost_usd||0)+d.cost_usd;
  if(box){box.querySelector('.meta').innerHTML=metaHTML(m);box.insertAdjacentHTML('beforeend',followHTML(m));showChatCost();scrollDown();}}
// The last answer of the open chat, if it may get suggestions now.
function followTarget(){const msgs=C.chat?.messages||[],m=msgs[msgs.length-1];return followOn(C.chat)&&m?.role==='assistant'&&m.id&&m.text&&!m.error&&!m.meta?.follow_ups?.length?m:null;}
function renderChat(){const box=byId('message-list');const c=C.chat;
  if(!c||!c.messages?.length){setHTML(box,`<div class="empty"><b>${c?esc(c.title):'Новый чат'}</b><span>Спросите что-нибудь. Можно приложить файлы и включить поиск в интернете.</span></div>`);}
  else setHTML(box,c.messages.map((m,i)=>m.role==='user'?userHTML(m):botHTML(m,i===c.messages.length-1&&followOn(c))).join(''));
  showChatCost();renderKeep();scrollDown(true);}
function scrollDown(force){const el=byId('messages');if(force||el.scrollHeight-el.scrollTop-el.clientHeight<160)el.scrollTop=el.scrollHeight;}
function showChatCost(){const c=byId('chat-cost');const paid=(C.chat?.messages||[]).filter(m=>m.meta?.cost_usd>0);c.hidden=!paid.length;if(!paid.length)return;
  const sum=paid.reduce((s,m)=>s+m.meta.cost_usd,0);c.innerHTML=`${money(sum)}<span class="lbl"> за чат</span>`;
  c.title=`Стоимость этого чата по данным OpenRouter: ${paid.length} ${plural(paid.length,'ответ','ответа','ответов')}. Локальные ответы бесплатны.`;}
async function openChat(id){stopFollowUps();C.chat=await api('/api/chats?id='+encodeURIComponent(id));
  if(C.chat.model&&modelByKey(C.chat.model))setChosen(C.chat.model,false);
  C.web=webDefault(C.chat.assistant_id);renderAll();}
function newChat(){stopFollowUps();C.chat=null;C.attached=[];C.web=webDefault(S.assistants.default);C.asst=S.assistants.default||'';const a=asstById(C.asst);if(a?.model&&modelByKey(a.model))setChosen(a.model,false);renderAll();byId('prompt').focus();}
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
// ── context: how much of the model's window the chat fills ──
// The window is the context the local model runs with (or will start with), or the cloud model's limit.
// The fill is what the last answer's request held, as the engine counted it; chats from before 0.22
// have no such count and get an estimate from the text.
const tk=(n,k=1024)=>{const f=(v,u)=>v.toFixed(1).replace(/\.0$/,'').replace('.',',')+u;return n>=k*k?f(n/(k*k),'M'):n>=k?f(n/k,'K'):String(n);};
function ctxWindow(m){if(!m)return 0;if(m.source==='cloud')return m.context||0;
  const b=(S.status?.backends||[]).find(x=>x.name===m.engine);if(m.running&&b?.active_context)return b.active_context;return curCtx(m)||m.context||0;}
function ctxUsed(){const msgs=C.chat?.messages||[];
  for(let i=msgs.length-1;i>=0;i--){const n=msgs[i].meta?.context_tokens;if(n)return {n,est:false};}
  const chars=msgs.reduce((n,x)=>n+(x.text||'').length+(x.attachments||[]).reduce((k,a)=>k+(a.text||'').length,0),0);
  return {n:Math.round(chars/3.5),est:msgs.length>0};}
function ctxInfo(){const m=curModel(),total=ctxWindow(m);if(!total)return null;const u=ctxUsed(),used=Math.min(u.n,total);
  return {m,total,used,left:total-used,pct:Math.min(100,Math.round(100*used/total)),est:u.est};}
function renderCtxMeter(){const i=ctxInfo(),w=byId('ctx-wrap');w.hidden=!i;if(!i)return;const k=kbase(i.total),t=n=>tk(n,k);
  const lv=i.pct>=90?'err':i.pct>=70?'warn':'';const b=byId('ctx-btn');b.className='tog ctxring'+(lv?' '+lv:'');
  byId('ctx-ring').setAttribute('stroke-dasharray',`${Math.max(i.pct,i.used?2:0)} 100`);
  b.title=`Контекст: занято ${i.pct}%`;b.setAttribute('aria-label',`Контекст: занято ${i.pct}%, осталось ${t(i.left)} токенов`);
  setHTML(byId('ctx-pop'),`<div class="line"><b>Контекст</b><span class="muted">${i.est?'≈ ':''}${t(i.used)} / ${t(i.total)} (${i.pct}%)</span></div>
    <div class="meter${lv?' '+lv:''}"><i data-w="${i.pct}%"></i></div>
    <dl class="ctxkv"><dt>Длина контекста</dt><dd>${t(i.total)}</dd><dt>Занято</dt><dd>${i.est?'≈ ':''}${t(i.used)}</dd><dt>Осталось</dt><dd>${i.est?'≈ ':''}${t(i.left)}</dd></dl>
    <span class="hint">${i.m.source==='cloud'?'Предел модели в OpenRouter.':i.m.running?'С этой длиной модель запущена; изменить — «Модели → Настроить».':'С этой длиной модель запустится; изменить — «Модели → Настроить».'} Занято — история чата, инструкции ассистента и файлы${i.est?'; это оценка по тексту — точное число появится после следующего ответа':' по последнему ответу модели'}.${i.pct>=70?' Ближе к 100% начните новый чат: длинную историю модель может не принять или забыть её начало.':''}</span>`);}
function renderFoot(){const m=curModel();const f=byId('chat-foot');renderCtxMeter();
  if(m?.source!=='cloud'){f.textContent='ИИ может ошибаться — проверяйте важное.';return;}
  const hist=(C.chat?.messages||[]).reduce((n,x)=>n+(x.text||'').length,0)/3+(byId('prompt').value.length/3)+300;const est=(hist*m.prompt_usd+500*m.completion_usd)/1e6;
  f.innerHTML=`<span class="cloudnote" title="Оценка: история чата ≈${fmtN(Math.round(hist))} токенов на входе и ≈500 в ответе. Точная стоимость придёт от OpenRouter после ответа."><svg class="i"><use href="#i-cloud"/></svg>OpenRouter · ≈${money(Math.max(est,0))} за ответ</span><span class="sep"> · </span><span class="disc">ИИ может ошибаться — проверяйте важное.</span>`;}

// ── assistants ──
const asstById=id=>(S.assistants.assistants||[]).find(a=>a.id===id);
const chatAsst=()=>C.chat?C.chat.assistant_id:(C.asst??S.assistants.default);
// An emoji sits in its own box: emoji fonts carry their own ascent, which pushed it off the circle's centre.
function avatar(a,cls=''){if(a)return `<span class="av ${cls}${a.emoji?' emo':''}">${a.emoji?`<span class="em">${esc(a.emoji)}</span>`:esc((a.name.trim()[0]||'?').toUpperCase())}</span>`;
  return cls.includes('slot')?`<span class="av none ${cls}"><svg class="i"><use href="#i-user"/></svg></span>`:`<span class="av none ${cls}"></span>`;}
function renderAssist(){const a=asstById(chatAsst());const b=byId('asst-btn');b.className='asst-chip'+(a?' on':'');
  b.innerHTML=avatar(a,a?'':'slot');b.title=a?`Ассистент: ${a.name}`:'Без ассистента — нажмите, чтобы выбрать';b.setAttribute('aria-label',b.title);
  const d=S.assistants.default;
  setHTML(byId('asst-menu'),`<div class="mhead">Ассистент</div><button type="button" data-asst-pick=""${!a?' aria-current="true"':''}>${avatar(null)}<b>Без ассистента</b><span></span><span class="d">Обычный чат без инструкций</span></button>`+(S.assistants.assistants||[]).map(x=>`<button type="button" data-asst-pick="${x.id}"${a?.id===x.id?' aria-current="true"':''}>${avatar(x)}<b>${esc(x.name)}</b>${x.id===d?'<span class="pill iris">По умолчанию</span>':'<span></span>'}<span class="d">${esc(x.description)}</span></button>`).join('')+`<div class="mfoot"><button type="button" data-go="settings" data-jump-after="s-assist"><span></span><b>Управлять ассистентами</b><span></span></button></div>`);}
const webDefault=id=>{const a=asstById(id);return a?!!a.web_search:!!S.chatSettings?.search_default;};
async function pickAssistant(id){const a=asstById(id);if(a?.model&&modelByKey(a.model))setChosen(a.model);if(a)C.web=!!a.web_search;
  // The chat keeps its assistant with the next message.
  if(C.chat)C.chat.assistant_id=id;else C.asst=id;
  byId('asst-menu').hidden=true;renderAssist();renderWeb();}

// ── reasoning ──
function renderThink(){const m=curModel()||{reasoning:{mode:'none'}};const t=m.reasoning||{mode:'none'},S2=steps(t);
  let st=efforts[m.key];if(!S2.includes(st))st=S2.includes(t.default)?t.default:S2[Math.min(S2.length-1,S2[0]==='off'?2:1)]||S2[0];
  const on=t.mode!=='none'&&st!=='off';const lbl=t.mode==='none'?'Без размышлений':!on?'Размышления: выкл':S2.length>2||t.mode==='always'?`Размышления: ${STEP_NAME[st].toLowerCase()}`:'Размышления';
  const b=byId('think-btn');b.classList.toggle('dim',!on);b.setAttribute('aria-pressed',String(on));b.title=lbl;b.setAttribute('aria-label',lbl);
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
function renderWeb(){const s=S.chatSettings||{};const ready=!!s.search_url;byId('web').setAttribute('aria-pressed',String(C.web&&ready));
  const tip=!ready?'Веб-поиск: SearXNG не настроен':C.web?'Веб-поиск: включён':'Веб-поиск: выключен';byId('web').title=tip;byId('web').setAttribute('aria-label',tip);byId('web-on').checked=C.web;byId('web-on').disabled=!ready;
  byId('web-host').textContent=ready?hostOf(s.search_url)||s.search_url:'не задан';
  byId('web-hint').innerHTML=ready?(curModel()&&!has(curModel(),'tools')?'Эта модель не вызывает функции: поиск выполняется по каждому вопросу. Источники появятся под ответом.':'Модель сама решает, когда искать. Источники появятся под ответом.'):'Адрес SearXNG не задан: <a href="#settings" data-go="settings" data-jump-after="s-search" class="ink">настроить</a>.';}

// ── attachments: refused before sending when the model cannot read them ──
const KINDS={image:{name:'изображения',need:'vision',ext:['png','jpg','jpeg','webp','gif']},video:{name:'видео',need:'video',ext:['mp4','mov','webm','mkv','avi']},audio:{name:'аудио',need:'audio',ext:['mp3','wav','m4a','ogg','flac','opus']},
  word:{name:'документы Word',need:'',ext:['docx']},excel:{name:'таблицы Excel',need:'',ext:['xlsx','csv']},text:{name:'текстовые файлы',need:'',ext:['txt','md','json','log','yaml','yml','xml','html','go','py','js','ts','ps1','sh','ini','conf']}};
const KIND_ICON={image:'i-eye',video:'i-video',audio:'i-audio',word:'i-file',excel:'i-table',text:'i-file'};
const MAX_FILE=25*1024*1024;
const MAX_FILES_BYTES=75*1024*1024;
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
    if(C.attached.reduce((n,a)=>n+a.size,0)+f.size>MAX_FILES_BYTES){refused.push('Вложения в одном сообщении: не больше 75 МБ суммарно.');continue;}
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
async function send(opts={}){if(C.busy)return;stopFollowUps();const m=curModel();
  if(!m){notice('Выберите модель в списке над чатом.',true);byId('picker-btn').click();return;}
  if(C.starting){notice('Модель ещё запускается — подождите несколько секунд.');return;}
  const text=opts.retry?'':(opts.text??byId('prompt').value).trim();const files=opts.retry||opts.text!==undefined?[]:C.attached;
  if(!text&&!files.length&&!opts.retry)return;
  if(m.source==='local'&&!m.running){await startLocal(m);if(!modelByKey(m.key)?.running)return;}
  if(!C.chat){C.chat=await api('/api/chats',{action:'create',model:m.key,assistant_id:chatAsst()||''});C.chat.messages=[];C.threads.unshift(C.chat);renderThreads();}
  const chat=C.chat;
  let data;try{data=await Promise.all(files.map(async a=>({name:a.name,data:await readB64(a.file)})));}catch(e){fail(e);return;}
  if(C.chat?.id!==chat.id)return;
  const box=byId('message-list');if(!C.chat.messages.length)box.innerHTML='';box.querySelectorAll('.follow').forEach(el=>el.remove());
  if(!opts.retry){box.insertAdjacentHTML('beforeend',userHTML({text,attachments:files.map(a=>({name:a.name,kind:a.kind,size:a.size}))}));if(opts.text===undefined){byId('prompt').value='';C.attached=[];renderAttached();byId('attach-warn').hidden=true;}}
  box.insertAdjacentHTML('beforeend','<div class="msg bot live"><div class="trace"></div><details class="step think" hidden><summary><svg class="i i15"><use href="#i-brain"/></svg><span>Размышляет…</span></summary><div class="in"></div></details><div class="md caret"></div><div class="sources-slot"></div><div class="status-line" hidden></div></div>');
  const live=box.lastElementChild;scrollDown(true);
  const ctrl=new AbortController();C.busy=ctrl;renderSend();
  const body={chat_id:chat.id,model:m.key,text,files:data,web:!!(C.web&&S.chatSettings?.search_url),effort:C.effort||'',assistant_id:chatAsst()||''};if(opts.retry)body.retry=opts.retry;
  let answer='',thinking='',sources=[],done=null,userMessage=null;
  try{const r=await fetch('/api/chat/send',{method:'POST',credentials:'same-origin',headers:{'Content-Type':'application/json','X-Omnibox-CSRF':csrf},body:JSON.stringify(body),signal:ctrl.signal});
    if(!r.ok){let msg=`Ошибка ${r.status}`;try{msg=(await r.json()).error||msg;}catch(e){}if(r.status===401)showLogin();throw new Error(msg);}
    const reader=r.body.getReader(),dec=new TextDecoder();let buf='',pending=0;
    const paintAnswer=()=>{pending=0;live.querySelector('.md').innerHTML=md(answer,sources);if(C.chat?.id===chat.id)scrollDown();};
    for(;;){const {value,done:end}=await reader.read();if(end)break;buf+=dec.decode(value,{stream:true});let i;
      while((i=buf.indexOf('\n\n'))>=0){const block=buf.slice(0,i);buf=buf.slice(i+2);let ev='',dt='';block.split('\n').forEach(l=>{if(l.startsWith('event: '))ev=l.slice(7);else if(l.startsWith('data: '))dt+=l.slice(6);});
        let v;try{v=JSON.parse(dt);}catch(e){continue;}
        if(ev==='user'){userMessage=v;const u=box.querySelector('.msg.user:not([data-id]),.msg.user[data-id=""]');if(u&&!opts.retry)u.dataset.id=v.id;}
        else if(ev==='delta'){answer+=v.t;if(!pending)pending=requestAnimationFrame(paintAnswer);}
        else if(ev==='reasoning'){thinking+=v.t;const d=live.querySelector('details.think');d.hidden=false;d.querySelector('.in').textContent=thinking;}
        else if(ev==='search'){live.querySelector('.trace').insertAdjacentHTML('beforeend',`<span class="status-line"><svg class="i i15"><use href="#i-globe"/></svg>Ищу: «${esc(v.query)}»</span>`);scrollDown();}
        else if(ev==='read'){live.querySelector('.trace').insertAdjacentHTML('beforeend',`<span class="status-line"><svg class="i i15"><use href="#i-file"/></svg>Читаю: ${esc(hostOf(v.url)||v.url)}</span>`);scrollDown();}
        else if(ev==='sources'){sources=v||[];}
        else if(ev==='status'){const s=live.querySelector('.status-line:last-child');s.hidden=false;s.textContent=v.text;}
        else if(ev==='done'){done=v;}}}
  }catch(e){if(e.name!=='AbortError'){live.querySelector('.md').classList.remove('caret');live.insertAdjacentHTML('beforeend',`<div class="err">${esc(e.message)}</div>`);}}
  finally{C.busy=null;renderSend();}
  if(done?.message){const msg=done.message,target=C.chat?.id===chat.id?C.chat:chat;target.messages=target.messages||[];
    if(opts.retry){const i=target.messages.findIndex(x=>x.id===opts.retry);if(i>=0)target.messages=target.messages.slice(0,i+1);}
    else if((text||files.length)&&!target.messages.some(x=>userMessage?.id&&x.id===userMessage.id)){target.messages.push(userMessage||{role:'user',text,attachments:files.map(a=>({name:a.name,kind:a.kind,size:a.size}))});}
    if(!target.messages.some(x=>x.id===msg.id))target.messages.push(msg);
    if(done.chat){const i=C.threads.findIndex(t=>t.id===done.chat.id);if(i>=0)C.threads[i]=done.chat;else C.threads.unshift(done.chat);target.title=done.chat.title;}
    renderThreads();if(C.chat?.id===chat.id){renderChat();showChatCost();renderFoot();const f=followTarget();if(f)requestFollowUps(chat.id,f.id);}if(msg.meta?.cost_usd)refreshAccount();
  }else if(C.chat?.id===chat.id){setTimeout(()=>{if(C.chat?.id===chat.id)openChat(chat.id).catch(()=>{});},400);}
  if(C.chat?.id===chat.id)scrollDown();}
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

// The chat's own switch; a choice equal to the setting goes back to following the setting.
async function toggleChatFollow(id){const t=C.threads.find(x=>x.id===id);if(!t)return;const next=!followOn(t),glob=!!S.chatSettings?.follow_ups;
  const s=await api('/api/chats',{action:'follow-ups',id,mode:next===glob?'auto':next?'on':'off'});const i=C.threads.findIndex(x=>x.id===id);if(i>=0)C.threads[i]=s;
  notice(next?'В этом чате под ответами будут вопросы для продолжения.':'В этом чате вопросы после ответов выключены.');
  if(C.chat?.id===id){C.chat.follow_ups=s.follow_ups;if(!next){stopFollowUps();renderChat();}else{const f=followTarget();if(f)requestFollowUps(id,f.id);}}}

// ── deleting chats ──
function askDelAll(step){const pins=C.threads.filter(t=>t.pinned).length,rest=C.threads.length-pins;byId('del-all').hidden=false;
  if(step===1){byId('del-all-text').textContent=`Удалить чаты (${C.threads.length}) с диска? Восстановить будет нельзя.`;byId('del-all-btns').innerHTML='<button class="btn danger sm" type="button" data-del-all-step>Удалить</button><button class="btn ghost sm" type="button" data-del-all-no>Отмена</button>';}
  else{byId('del-all-text').textContent=`Среди них избранные: ${pins}. Удалить и их?`;byId('del-all-btns').innerHTML=`<button class="btn danger sm" type="button" data-del-all-go="all">Удалить все</button><button class="btn sm" type="button" data-del-all-go="keep">Только незакреплённые (${rest})</button><button class="btn ghost sm" type="button" data-del-all-no>Отмена</button>`;}}
async function delAll(all){const r=await api('/api/chats',{action:'delete-all',keep_pinned:!all});byId('del-all').hidden=true;notice(`Удалено чатов: ${r.deleted}.`);
  await loadThreads();if(C.chat&&!C.threads.some(t=>t.id===C.chat.id))newChat();}

// ── events ──
byId('composer').addEventListener('submit',e=>{e.preventDefault();if(C.busy){C.busy.abort();return;}send().catch(fail);});
// Enter sends; Ctrl+Enter (or Shift+Enter) starts a new line. A phone's keyboard has no Ctrl, so on a
// touch screen Enter starts a new line and the arrow button sends.
const touchOnly=()=>matchMedia('(hover:none) and (pointer:coarse)').matches;
function newLine(t){const a=t.selectionStart??t.value.length,b=t.selectionEnd??a;t.value=t.value.slice(0,a)+'\n'+t.value.slice(b);t.selectionStart=t.selectionEnd=a+1;t.dispatchEvent?.(new Event('input'));}
function promptKey(e){if(e.key!=='Enter'||e.isComposing)return;
  if(e.ctrlKey||e.metaKey){e.preventDefault();newLine(e.target);return;}
  if(!e.shiftKey&&!e.altKey&&!touchOnly()){e.preventDefault();byId('composer').requestSubmit();}}
byId('prompt').addEventListener('keydown',promptKey);
byId('prompt').addEventListener('input',()=>{if(curModel()?.source==='cloud')renderFoot();});
byId('thread-list').addEventListener('keydown',e=>{if(e.target.id!=='thread-rename')return;if(e.key==='Enter'){e.preventDefault();commitRename(true);}else if(e.key==='Escape'){e.preventDefault();e.stopPropagation();commitRename(false);}});
byId('thread-list').addEventListener('focusout',e=>{if(e.target.id==='thread-rename')commitRename(true);});
byId('thread-list').addEventListener('dblclick',e=>{const a=e.target.closest('[data-thread]');if(a&&!C.hits){e.preventDefault();startRename(a.dataset.thread);}});
byId('thread-list').addEventListener('scroll',closeThreadMenu);
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
  if(!e.target.closest('.ctxwrap')){byId('ctx-pop').hidden=true;byId('ctx-btn').setAttribute('aria-expanded','false');}
  if(!e.target.closest('.webwrap')){byId('web-pop').hidden=true;byId('web').setAttribute('aria-expanded','false');}
  if(!e.target.closest('.asst-picker'))byId('asst-menu').hidden=true;
  if(!e.target.closest('.conv-head .picker'))byId('picker-menu').hidden=true;
  if(!e.target.closest('#thread-menu')&&!e.target.closest('[data-tmenu]'))closeThreadMenu();
  const tm=e.target.closest('[data-tmenu]');if(tm){openThreadMenu(tm);return;}
  const rn=e.target.closest('[data-rename]');if(rn){startRename(rn.dataset.rename);return;}
  const fc=e.target.closest('[data-follow-chat]');if(fc){closeThreadMenu();toggleChatFollow(fc.dataset.followChat).catch(fail);return;}
  const fq=e.target.closest('[data-follow]');if(fq){if(!C.busy)send({text:fq.dataset.follow}).catch(fail);return;}
  if(e.target.closest('#think-btn')){byId('think-pop').hidden=!byId('think-pop').hidden;return;}
  if(e.target.closest('#ctx-btn')){const p=byId('ctx-pop');p.hidden=!p.hidden;byId('ctx-btn').setAttribute('aria-expanded',String(!p.hidden));if(!p.hidden)renderCtxMeter();return;}
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
  const pn=e.target.closest('[data-pin]');if(pn){closeThreadMenu();const t=C.threads.find(x=>x.id===pn.dataset.pin);api('/api/chats',{action:t?.pinned?'unpin':'pin',id:pn.dataset.pin}).then(s=>{const i=C.threads.findIndex(x=>x.id===s.id);C.threads[i]=s;C.threads.sort((a,b)=>(b.pinned-a.pinned)||Date.parse(b.updated)-Date.parse(a.updated));renderThreads();}).catch(fail);return;}
  const td=e.target.closest('[data-del-thread]');if(td){closeThreadMenu();const id=td.dataset.delThread,t=C.threads.find(x=>x.id===id);confirmDialog({title:'Удалить чат?',text:`«${t?.title||'Без названия'}» сразу сотрётся с диска${t?.pinned?' — он в избранном':''}. Восстановить его будет нельзя.`,yes:'Удалить'}).then(ok=>ok&&api('/api/chats',{action:'delete',id}).then(r=>{if(!r)return;C.threads=C.threads.filter(t=>t.id!==id);if(C.chat?.id===id)newChat();else renderThreads();})).catch(fail);return;}
  if(e.target.closest('[data-del-all]')){if(C.threads.length)askDelAll(1);return;}
  if(e.target.closest('[data-del-all-no]')){byId('del-all').hidden=true;return;}
  if(e.target.closest('[data-del-all-step]')){C.threads.some(t=>t.pinned)?askDelAll(2):delAll(true).catch(fail);return;}
  const da=e.target.closest('[data-del-all-go]');if(da){delAll(da.dataset.delAllGo==='all').catch(fail);return;}
});
on('library',()=>{if(S.page==='chat'){renderPicker();renderThink();renderWeb();renderFoot();}});
on('status',()=>{if(S.page==='chat')renderCtxMeter();});
PAGES.chat={show(again){if(!C.chosen||!modelByKey(C.chosen)){const r=runningModel()||models().find(m=>m.favorite)||models()[0];if(r)C.chosen=r.key;}
  if(again){refreshLibrary().catch(()=>{});}
  loadThreads().then(()=>{if(!C.chat)C.web=webDefault(chatAsst());renderAll();}).catch(fail);renderAll();}};
document.addEventListener('keydown',e=>{if(e.key==='Escape'){['think-pop','web-pop','asst-menu','picker-menu','ctx-pop'].forEach(id=>byId(id).hidden=true);closeThreadMenu();}});
