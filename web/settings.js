'use strict';
// Settings: clients and keys, web search, OpenRouter, ChatGPT, assistants, data, sleep, updates, panel.
const ST={gateway:null,storage:null,update:null,editing:null,emoji:'',wipe:''};

// ── connection ──
function renderGateway(){const g=ST.gateway;if(!g)return;const base=g.base_url||`${location.protocol}//${location.host}/v1`;
  byId('base').value=base;byId('api-state').textContent=g.enabled?'API включён':'API выключен';byId('api-state').className='pill'+(g.enabled?' ok':'');
  const run=runningModel();byId('base-hint').innerHTML=run?`Имя модели — <span class="mono">${esc(run.alias||run.id)}</span>.${S.cloud?.expose_api?' Избранные модели OpenRouter — по их id.':''}`:'Имя модели появится, когда модель запущена; его можно изменить в карточке модели.';
  byId('require-key').checked=!!g.require_key;byId('inject-date').checked=!!g.inject_date;byId('nokey-warn').hidden=!!g.require_key;
  setHTML(byId('keys'),(g.keys||[]).map(k=>`<tr><td>${esc(k.name)}</td><td>${k.created?dateShort(k.created):'—'}</td><td>${k.last_used?new Date(k.last_used).toLocaleString('ru-RU',{day:'numeric',month:'short',hour:'2-digit',minute:'2-digit'}):'—'}</td><td class="num">${fmtN(k.requests||0)}</td><td class="num"><button class="btn ghost sm danger-text" type="button" data-revoke="${esc(k.id)}" data-name="${esc(k.name)}">Отозвать</button></td></tr>`).join('')||'<tr><td colspan="5" class="muted">Ключей нет.</td></tr>');
  byId('queue-depth').value=g.queue_depth;byId('queue-timeout').value=g.queue_timeout_seconds;byId('public-url').value=g.base_url||'';byId('public-origin').value=g.public_origin||'';byId('trusted').value=(g.trusted_proxies||[]).join(', ');}
async function saveGateway(body){const d=await api('/api/gateway',body);ST.gateway=d.gateway;renderGateway();
  if(d.key){byId('newkey').hidden=false;byId('newkey-v').value=d.key;}return d;}

// ── web search and chat data ──
// The search engines and what each needs: SearXNG an address, three of them a key, Sonar the
// OpenRouter key.
const ENGINES={'':{name:'SearXNG',hint:'Свой сервер SearXNG: бесплатно, но качество зависит от того, какие движки на нём отвечают (Google и Bing часто отвечают капчей).'},
  perplexity:{name:'Perplexity',key:'Ключ Perplexity',ph:'pplx-…',url:'https://www.perplexity.ai/account/api/keys',hint:'Лучшая выдача: ранжированные результаты с текстом страниц. Платно — около $5 за 1000 запросов с баланса API Perplexity (подписчикам Pro начисляется $5 в месяц).'},
  sonar:{name:'Perplexity Sonar',hint:'Perplexity сам ищет и кратко отвечает с источниками, а модель чата сверяет ответ с ними. Платит ваш ключ OpenRouter: около цента за поиск, расход виден в «OpenRouter».'},
  tavily:{name:'Tavily',key:'Ключ Tavily',ph:'tvly-…',url:'https://app.tavily.com/',hint:'Поиск для ИИ: сразу текст страниц. 1000 запросов в месяц бесплатно, дальше — по тарифу Tavily.'},
  brave:{name:'Brave Search',key:'Ключ Brave Search',ph:'BSA…',url:'https://api-dashboard.search.brave.com/',hint:'Собственный индекс Brave, хорошо ищет по-русски. Тариф Search: $5 бесплатного кредита в месяц (около 1000 запросов), нужна карта.'}};
function renderSearchEngine(){const s=S.chatSettings||{},e=byId('search-engine').value,x=ENGINES[e]||ENGINES[''],keys=s.search_keys||{};
  byId('search-engine-hint').textContent=x.hint;byId('searx-wrap').hidden=e!=='';byId('sonar-wrap').hidden=e!=='sonar';byId('search-key-wrap').hidden=!x.key;
  if(x.key){byId('search-key-label').textContent=x.key;byId('search-key').placeholder=x.ph;const k=keys[e];byId('search-key-saved').hidden=!k;
    byId('search-key-info').textContent=k?`Сохранён: …${k.hint.replace(/^…/,'')}${k.saved?' · '+dateShort(k.saved):''}`:'';
    setHTML(byId('search-key-hint'),`Ключ создаётся на <a class="ink" href="${x.url}" target="_blank" rel="noopener noreferrer">${esc(hostOf(x.url))}</a>. Перед сохранением OmniBox делает один пробный поиск; ключ хранится на ПК зашифрованным и в панель не возвращается.`);}
  if(e==='sonar'){byId('sonar-model').innerHTML=(s.sonar_models||['perplexity/sonar']).map(m=>`<option value="${esc(m)}">${esc(m)}</option>`).join('');byId('sonar-model').value=s.sonar_model||'perplexity/sonar';
    byId('sonar-hint').innerHTML=s.openrouter?'sonar — быстрый и дешёвый, sonar-pro — глубже и дороже.':'Нужен ключ OpenRouter: <a href="#settings" data-jump="s-cloud" class="ink">добавить</a>.';}}
function renderChatSettings(){const s=S.chatSettings;if(!s)return;byId('searx').value=s.search_url||'';byId('search-engine').value=s.search_engine||'';byId('plan-queries').checked=s.plan_queries!==false;renderSearchEngine();byId('search-n').value=String(s.search_results);
  if(!['3','5','8'].includes(String(s.search_results)))byId('search-n').insertAdjacentHTML('beforeend',`<option value="${s.search_results}" selected>${s.search_results}</option>`);
  byId('search-lang').value=s.search_language||'';byId('search-default').checked=!!s.search_default;byId('site-icons').checked=!!s.site_icons;byId('read-pages').checked=s.read_pages!==false;
  // Suggestions: the chat's own model or a favourite OpenRouter one; another local model would have to be started.
  byId('follow-on').checked=!!s.follow_ups;byId('auto-title').checked=s.auto_title!==false;const fm=s.follow_up_model||'',favs=models().filter(m=>(m.source==='cloud'&&m.favorite)||m.source==='chatgpt'),where=k=>k.startsWith('chatgpt:')?'ChatGPT':'OpenRouter';
  byId('follow-model').innerHTML='<option value="">Та же модель, что в чате</option>'+favs.map(m=>`<option value="${esc(m.key)}">${esc(m.name)} · ${where(m.key)}</option>`).join('')+(fm&&!favs.some(m=>m.key===fm)?`<option value="${esc(fm)}">${esc(chatModelName(fm))} · ${where(fm)}</option>`:'');
  byId('follow-model').value=fm;byId('follow-model-hint').textContent=fm.startsWith('chatgpt:')?'Модель ChatGPT пишет вопросы для любого чата: видеокарта не занята, расходуются лимиты подписки.':fm?'Модель OpenRouter пишет вопросы для любого чата: видеокарта не занята, каждый раз — доли цента по цене модели.':'Та же модель, что отвечала: локальная — бесплатно, но несколько секунд занимает видеокарту; облачная — по цене её токенов или из лимитов подписки. В списке — модели ChatGPT и избранные модели OpenRouter.';
  // Dictation: the OpenRouter models that hear audio, cheapest first; "auto" picks a Gemini Flash.
  const ears=models().filter(m=>m.source==='cloud'&&has(m,'audio')).sort((a,b)=>(a.prompt_usd||0)-(b.prompt_usd||0)),vm=s.voice_model||'';
  byId('voice-model').innerHTML='<option value="">Авто</option>'+ears.map(m=>`<option value="${esc(m.id)}">${esc(m.name)}</option>`).join('')+(vm&&!ears.some(m=>m.id===vm)?`<option value="${esc(vm)}">${esc(vm)}</option>`:'');
  byId('voice-model').value=vm;byId('voice-server').checked=!!s.voice_server;
  byId('keep-chats').value=String(s.retention_hours);if(byId('keep-chats').value!==String(s.retention_hours))byId('keep-chats').insertAdjacentHTML('beforeend',`<option value="${s.retention_hours}" selected>${Math.round(s.retention_hours/24)} дн.</option>`);
  byId('keep-chats-hint').textContent=`Сейчас ${s.chats} ${plural(s.chats,'чат','чата','чатов')}, ${fsize(s.chats_bytes||0)}. Избранные не удаляются по сроку; новый срок действует для новых чатов.`;
  byId('search-state').textContent=s.search_ready?(ENGINES[s.search_engine||'']||ENGINES['']).name:'Не настроен';byId('search-state').className='pill'+(s.search_ready?' ok':'');}
async function saveChatSettings(body){S.chatSettings=await api('/api/chat-settings',{action:'save',...body});renderChatSettings();if(typeof renderWeb==='function')renderWeb();}

// ── OpenRouter ──
function renderCloud(){const c=S.cloud||{};const on=!!c.connected;byId('or-off').hidden=on;byId('or-on').hidden=!on;byId('or-state').textContent=on?'Подключено':'Не подключено';byId('or-state').className='pill'+(on?' ok':'');if(!on)return;
  const a=S.account;byId('or-balance').textContent=a?usd2(a.balance):'—';byId('or-credits').textContent=a?`из ${usd2(a.total_credits)} пополненных`:'баланс не получен';
  byId('or-today').textContent=money(a?a.daily:c.today);byId('or-month').textContent=money(c.month);renderKeyLimits(a);
  const low=c.low_balance_usd;byId('or-low').value=String(low);if(byId('or-low').value!==String(low))byId('or-low').insertAdjacentHTML('beforeend',`<option value="${low}" selected>$${low}</option>`);
  byId('or-low-warn').hidden=!(a&&low>0&&a.balance<low);byId('or-low-warn').textContent=`На счёте ${a?usd2(a.balance):''} — меньше ${usd2(low)}. Пополните баланс, иначе облачные модели перестанут отвечать.`;
  byId('or-expose').checked=!!c.expose_api;const favs=models().filter(m=>m.source==='cloud'&&m.favorite).length;
  byId('or-expose-hint').textContent=`Jan, Pi и другие клиенты увидят избранные модели OpenRouter (сейчас ${favs}) по адресу API OmniBox. Запросы оплачиваются с вашего ключа.`;
  byId('or-fetched').textContent=c.fetched?`Обновлён ${new Date(c.fetched).toLocaleString('ru-RU',{day:'numeric',month:'short',hour:'2-digit',minute:'2-digit'})}. Обновляется при открытии раздела «Модели», не чаще раза в час.`:'Ещё не загружен.';
  byId('or-keyinfo').textContent=`Ключ: sk-or-…${(c.hint||'').replace(/^…/,'')}${c.saved?' · сохранён '+dateShort(c.saved):''}`;
  renderSpend(c);}
// The key's own bounds, as OpenRouter reports them: its spending limit and what is left of it, and
// when it stops working. Nothing is shown for a key without a limit or an expiry but the word.
const RESET={daily:'в день',weekly:'в неделю',monthly:'в месяц'};
function keyLimits(a,now=Date.now()){if(!a)return null;const out={};
  if(a.limit!=null){const left=Math.max(0,a.limit_remaining??a.limit-a.usage);out.limit=`${money(a.limit)} ${RESET[a.limit_reset]||'всего'}`;out.left=`осталось ${money(left)}`;
    out.pct=a.limit>0?Math.min(100,Math.round(100*(a.limit-left)/a.limit)):100;out.low=a.limit>0&&left/a.limit<0.1;}
  else{out.limit='без лимита';out.left='тратит баланс аккаунта';}
  if(a.expires_at){const t=Date.parse(a.expires_at),d=Math.floor((t-now)/86400000),h=Math.floor((t-now)/3600000);
    out.until=`до ${new Date(t).toLocaleDateString('ru-RU',{day:'numeric',month:'short',year:'numeric'}).replace(' г.','')}`;
    out.ttl=t<=now?'срок истёк — создайте новый ключ':d>=1?`осталось ${d} ${plural(d,'день','дня','дней')}`:`осталось ${Math.max(h,0)} ${plural(Math.max(h,0),'час','часа','часов')}`;out.soon=t-now<7*86400000;}
  else{out.until='бессрочный';out.ttl='не истекает';}
  out.spent=`всего ${money(a.usage)}`;out.period=`за неделю ${money(a.weekly)} · за месяц ${money(a.monthly)}`;return out;}
function renderKeyLimits(a){const k=keyLimits(a),el=byId('or-keylim');el.hidden=!k;if(!k)return;
  setHTML(el,`<div><span class="label">Лимит ключа</span><b>${esc(k.limit)}</b><span class="small ${k.low?'warn-text':'faint'}">${esc(k.left)}</span>${k.pct!==undefined?`<div class="meter${k.low?' warn':''}"><i data-w="${k.pct}%"></i></div>`:''}</div>`+
    `<div><span class="label">Срок ключа</span><b>${esc(k.until)}</b><span class="small ${k.soon?'warn-text':'faint'}">${esc(k.ttl)}</span></div>`+
    `<div><span class="label">Потрачено ключом</span><b>${esc(k.spent)}</b><span class="small faint">${esc(k.period)}</span></div>`);}
function renderSpend(c){const days=c.days||[];const vals=days.map(d=>d.usd);const max=Math.max(...vals,0),top=max>=1?Math.ceil(max*2)/2:max>0?max*1.1:1;const peak=vals.indexOf(max);
  setHTML(byId('spend-chart'),'<div class="grid g1"></div><div class="grid g2"></div>'+days.map((d,i)=>{const lab=dateShort(d.date);return `<div class="bar" tabindex="0" data-tip="${lab}: ${money(d.usd)}"><i data-h="${d.usd/top*100}%"></i>${(i===peak&&max>0)||i===days.length-1?`<span class="vlab top" data-b="calc(${d.usd/top*100}% + 2px)">${money(d.usd)}</span>`:''}</div>`;}).join(''));
  setHTML(byId('spend-x'),days.map((d,i)=>`<span>${i%2?'':dateShort(d.date)}</span>`).join(''));
  byId('spend-table').innerHTML='<tr><th>День</th><th class="num">Расход</th></tr>'+days.map(d=>`<tr><td>${dateShort(d.date)}</td><td class="num">${money(d.usd)}</td></tr>`).join('');
  const by=c.by_model||[],bm=by[0]?.usd||1;const name=id=>modelByKey('cloud:'+id)?.name||id;
  setHTML(byId('by-model'),by.map(x=>`<div class="row"><span>${esc(name(x.model))}</span><b>${money(x.usd)}</b><div class="meter"><i data-w="${x.usd/bm*100}%"></i></div></div>`).join('')||'<span class="muted small">Расходов пока нет.</span>');
  const src=c.by_source||{};byId('by-source').textContent=`чат OmniBox ${money(src.chat)} · API ${money(src.api)}`;}
async function cloudAction(body){S.cloud=await api('/api/cloud',body);renderCloud();return S.cloud;}

// ── ChatGPT ──
// Codex is installed and signed in from here; the page follows a download or a sign-in while it is
// open, and stops asking once they end.
function gptCell(label,value,sub,pct){const lv=pct>=90?' err':pct>=70?' warn':'';
  return `<div><span class="label">${esc(label)}</span><b>${esc(value)}</b>${sub?`<span class="small faint">${esc(sub)}</span>`:''}${pct!==undefined?`<div class="meter${lv}"><i data-w="${pct}%"></i></div>`:''}</div>`;}
function renderGpt(){const g=S.gpt;if(!g)return;const st=g.status||{},signed=!!g.signed_in,inst=st.installing,busy=!!inst?.running,l=st.limits;
  byId('gpt-state').textContent=!st.installed?(busy?'Устанавливается':'Не установлено'):signed?'Подключено':'Вход не выполнен';byId('gpt-state').className='pill'+(signed?' ok':'');
  byId('gpt-setup').hidden=st.installed&&!busy;byId('gpt-install').disabled=busy;
  const pct=busy&&inst.total?Math.round(100*inst.done/inst.total):0;byId('gpt-install-meter').hidden=!busy;
  const bar=byId('gpt-install-meter');bar.className='meter'+(inst?.total?'':' run');bar.firstElementChild.style.width=inst?.total?pct+'%':'';
  byId('gpt-install-msg').textContent=busy?(inst.total?`Скачиваем Codex: ${pct}% из ${fsize(inst.total)}`:'Скачиваем Codex…'):inst?.error||'';
  byId('gpt-install-msg').classList.toggle('danger-text',!busy&&!!inst?.error);
  byId('gpt-signin').hidden=!st.installed||signed;const lg=st.login,waiting=!!lg&&!lg.done;byId('gpt-code').hidden=!waiting;byId('gpt-login').hidden=waiting;
  if(waiting){byId('gpt-url').href=lg.url;byId('gpt-code-v').textContent=lg.code;
    byId('gpt-code-hint').textContent=`Код действует до ${new Date(lg.expires).toLocaleTimeString('ru-RU',{hour:'2-digit',minute:'2-digit'})}. После входа раздел обновится сам.`;}
  else if(lg?.error)byId('gpt-login').title=lg.error;
  byId('gpt-on').hidden=!signed;byId('gpt-version').hidden=!st.installed||busy;
  byId('gpt-version-v').textContent=st.installed?`Codex ${st.version}${st.running?' · запущен':' · запускается при запросе, останавливается через 15 минут без запросов'}`:'';
  if(signed){const a=st.account;const cells=[gptCell('Аккаунт',a?.email||'—',gptPlan(a?.plan||l?.plan)?'ChatGPT '+gptPlan(a?.plan||l?.plan):'')];
    for(const w of gptWindows(l))cells.push(gptCell(gptWin(w),`${w.used_percent}%`,['израсходовано',gptReset(w)].filter(Boolean).join(' · '),w.used_percent));
    if(!l)cells.push(gptCell('Лимиты','—','нажмите «Обновить»'));
    setHTML(byId('gpt-kv'),cells.join(''));
    const more=[];for(const b of l?.others||[])for(const w of gptWindows(b))more.push(gptCell(`${b.name||b.id} · ${gptWin(w)}`,`${w.used_percent}%`,gptReset(w),w.used_percent));
    if(l?.credits&&(l.credits.has_credits||l.credits.unlimited))more.push(gptCell('Кредиты',l.credits.unlimited?'без ограничений':l.credits.balance||'—','сверх лимитов подписки'));
    byId('gpt-more').hidden=!more.length;setHTML(byId('gpt-more'),more.join(''));
    byId('gpt-warn').hidden=!(l?.reached||l?.blocked);byId('gpt-warn').textContent='Достигнут лимит подписки ChatGPT: ответы возобновятся после сброса'+(gptWindows(l).find(w=>w.used_percent>=100)?.resets_at?` (${gptReset(gptWindows(l).find(w=>w.used_percent>=100))})`:'')+'.';
    byId('gpt-limits-when').textContent=l?.updated?`Обновлены ${new Date(l.updated).toLocaleString('ru-RU',{day:'numeric',month:'short',hour:'2-digit',minute:'2-digit'})}: после каждого ответа и при открытии этого раздела.`:'Обновляются после каждого ответа и при открытии этого раздела.';
    byId('gpt-models').textContent=(g.models||[]).map(m=>m.name).join(', ')||'Список появится после первого обращения.';
    byId('gpt-expose').checked=!!g.expose_api;byId('gpt-search').checked=g.openai_search!==false;byId('gpt-account-info').textContent=a?.email?`Вход: ${a.email}`:'';}
  if(g.error&&signed)byId('gpt-limits-when').textContent=g.error;
  gptFollow(busy||waiting);}
let gptTimer=0;
function gptFollow(on){clearTimeout(gptTimer);gptTimer=0;if(!on||S.page!=='settings')return;
  gptTimer=setTimeout(async()=>{if(document.hidden||S.page!=='settings'){gptFollow(true);return;}
    const was=S.gpt?.status;try{await refreshGpt(false);}catch(e){}const now=S.gpt?.status||{};
    if(was?.login&&!was.login.done&&now.login?.done){if(now.login.error)notice('Вход в ChatGPT не завершён: '+now.login.error,true);else{notice('ChatGPT подключён.');await refreshGpt(true).catch(()=>{});await refreshLibrary();}}
    if(was?.installing?.running&&!now.installing?.running&&now.installed){notice(`Codex ${now.version} установлен.`);}
    renderGpt();},2000);}
async function gptAction(body){S.gpt=await api('/api/chatgpt',body);if(S.gpt.status?.limits)S.gptLimits=S.gpt.status.limits;renderGpt();return S.gpt;}

// ── assistants ──
const EMOJIS=['🏠','💻','🌐','🧠','🔧','📝','📚','🎨','🧮','📈','🩺','⚖️','🍳','✈️','🎵','🤖','🎃'];
const STYLE={precise:'Точно',balanced:'Баланс',creative:'Творчески'};
function renderAssistants(){const A=S.assistants;const d=A.default;
  byId('asst-default').innerHTML='<option value="">Без ассистента</option>'+(A.assistants||[]).map(x=>`<option value="${x.id}"${x.id===d?' selected':''}>${esc(x.name)}</option>`).join('');
  setHTML(byId('asst-list'),(A.assistants||[]).map(x=>`<div class="arow">${avatar(x,'lg')}<div class="t"><b>${esc(x.name)}</b>${x.id===d?'<span class="pill iris">По умолчанию</span>':''}${x.model?`<span class="pill">${esc(chatModelName(x.model))}</span>`:''}${x.web_search?'<span class="pill">Веб-поиск</span>':''}${x.style?`<span class="pill">${STYLE[x.style]}</span>`:''}</div><p>${esc(x.description)}</p><div class="acts"><button class="btn sm" type="button" data-asst-edit="${x.id}">Изменить</button></div></div>`).join('')||'<div class="banner">Ассистентов пока нет. Создайте первого: название, описание и инструкции.</div>');
  if(typeof renderAssist==='function')renderAssist();}
function renderEmoji(){const name=byId('ae-name').value.trim()||'?';byId('ae-av').innerHTML=avatar({name,emoji:ST.emoji});
  byId('ae-emojis').innerHTML=EMOJIS.map(e=>`<button type="button" data-emoji="${e}" aria-pressed="${e===ST.emoji}" aria-label="${e}">${e}</button>`).join('');}
function countInstr(){const n=byId('ae-instr').value.length;byId('ae-count').textContent=`${n.toLocaleString('ru-RU')} / 20 000`;byId('ae-count').classList.toggle('danger-text',n>20000);}
function openAsst(id){ST.editing=id;const a=asstById(id)||{name:'',description:'',instructions:'',model:'',style:'',web_search:false,emoji:''};ST.emoji=a.emoji||'';
  byId('ae-emoji').value=EMOJIS.includes(ST.emoji)?'':ST.emoji;byId('ae-title').textContent=id?a.name:'Новый ассистент';byId('ae-name').value=a.name;byId('ae-desc').value=a.description;byId('ae-instr').value=a.instructions;
  byId('ae-style').value=a.style||'';byId('ae-web').checked=!!a.web_search;byId('ae-default').checked=!!id&&id===S.assistants.default;
  const opts=models().filter(m=>m.source==='local'||m.favorite);byId('ae-model').innerHTML='<option value="">Как выбрано в чате</option>'+opts.map(m=>`<option value="${esc(m.key)}">${esc(m.name)} · ${m.source==='cloud'?'OpenRouter':m.source==='chatgpt'?'ChatGPT':'локальная'}</option>`).join('')+(a.model&&!opts.some(m=>m.key===a.model)?`<option value="${esc(a.model)}">${esc(chatModelName(a.model))}</option>`:'');byId('ae-model').value=a.model||'';
  byId('ae-delete').hidden=!id;byId('ae-delete').dataset.sure='';byId('ae-delete').textContent='Удалить';byId('ae-name-msg').textContent='';countInstr();renderEmoji();show(byId('asst-drawer'));setTimeout(()=>byId('ae-name').focus(),50);}
async function saveAsst(){const name=byId('ae-name').value.trim();if(!name){byId('ae-name-msg').textContent='Нужно название.';byId('ae-name').focus();return;}
  if(byId('ae-instr').value.length>20000){byId('ae-name-msg').textContent='Инструкции длиннее 20 000 символов.';return;}
  const body={action:'save',name,emoji:ST.emoji,description:byId('ae-desc').value.trim(),instructions:byId('ae-instr').value,model:byId('ae-model').value,style:byId('ae-style').value,web_search:byId('ae-web').checked};if(ST.editing)body.id=ST.editing;
  try{const r=await api('/api/assistants',body);const def=byId('ae-default').checked;
    if(def&&r.default!==r.assistant.id)Object.assign(r,await api('/api/assistants',{action:'default',id:r.assistant.id}));else if(!def&&r.default===r.assistant.id)Object.assign(r,await api('/api/assistants',{action:'default',id:''}));
    S.assistants={assistants:r.assistants,default:r.default};hideAll();renderAssistants();}catch(e){byId('ae-name-msg').textContent=e.message;}}

// ── data and storage ──
const KEEP_LOGS={h6:{log_retention_hours:6},d1:{log_retention_days:1},d7:{log_retention_days:7},d30:{log_retention_days:30},d90:{log_retention_days:90},'0':{log_retention_days:0}};
function renderStorage(){const u=ST.storage;if(!u)return;const v=u.log_retention_hours>0?`h${u.log_retention_hours}`:u.log_retention_days>0?`d${u.log_retention_days}`:'0';
  byId('keep-logs').value=v;if(byId('keep-logs').value!==v)byId('keep-logs').insertAdjacentHTML('beforeend',`<option value="${v}" selected>${u.log_retention_hours||u.log_retention_days} ${u.log_retention_hours?'ч':'дн.'}</option>`);
  byId('keep-logs-hint').textContent=`Сейчас ${fsize(u.logs_bytes||0)}${u.content_bytes?`, из них текст запросов ${fsize(u.content_bytes)}`:''}.`;byId('request-text').checked=u.request_log==='content';
  setHTML(byId('drives'),(u.drives||[]).map(d=>`<div class="drive${d.low?' low':''}"><div class="line"><b>${esc(d.name)}${d.label?' · '+esc(d.label):''}</b><span class="small muted">свободно ${bytesGb(d.free)} из ${bytesGb(d.total)}</span></div><div class="meter"><i data-w="${d.total?Math.round(d.used/d.total*100):0}%"></i></div><span class="small faint">${esc((d.roles||[]).join(', ')||'Данных OmniBox нет')}${d.low?' · мало свободного места':''}</span></div>`).join(''));}
async function refreshStorage(){ST.storage=await api('/api/storage');renderStorage();}
const WIPE={chats:{text:()=>`Удалить все чаты (${S.chatSettings?.chats||0}), включая избранные? Они сразу стираются с диска, восстановить нельзя.`,run:()=>api('/api/chats',{action:'delete-all',keep_pinned:false}).then(r=>`Удалено чатов: ${r.deleted}.`)},
  logs:{text:()=>`Удалить все журналы (${fsize(ST.storage?.logs_bytes||0)})? Файлы, которые сейчас пишет работающий движок, останутся до следующей ротации.`,run:()=>api('/api/storage',{action:'purge'}).then(u=>{ST.storage=u;return `Удалено файлов журналов: ${u.cleanup?.removed||0}.`;})},
  all:{text:()=>'Удалить все чаты, журналы и текст запросов? Модели и настройки останутся.',run:()=>api('/api/chat-settings',{action:'delete-all-data'}).then(d=>{S.chatSettings=d;return `Удалено чатов: ${d.deleted_chats}, файлов журналов: ${d.logs?.removed||0}.`;})}};

// ── sleep, wake, autostart ──
function renderSleep(){const sys2=S.status?.system||{};byId('autostart').checked=!!sys2.autostart;
  const locals=models().filter(m=>m.source==='local');const cur=S.library?.start_model||'';
  byId('start-model').innerHTML='<option value="">Не загружать</option>'+locals.map(m=>`<option value="${esc(m.key)}"${m.key===cur?' selected':''}>${esc(m.name)}</option>`).join('');
  setHTML(byId('macs'),(S.status?.network||[]).map(a=>`<div class="line"><span class="muted">MAC для Wake-on-LAN · ${esc(a.name)}${a.ipv4?' · '+esc(a.ipv4):''}</span><span class="mono">${esc(a.mac)}</span></div>`).join(''));}

// ── updates ──
function renderUpdate(){const u=ST.update||{};const v=S.status?.version||'';byId('version-pill').textContent=`Версия ${v}`;byId('panel-version').textContent=`OmniBox AI ${v}`;
  byId('token-forget').hidden=!u.token;byId('update-check').disabled=!u.token;byId('token-box').open=!u.token;
  const until=u.token_expires?` · действует до ${new Date(String(u.token_expires).replace(' UTC','Z').replace(' ','T')).toLocaleDateString('ru-RU')}`:'';
  byId('token-summary').textContent=u.token?`GitHub token для обновлений: сохранён${until}`:'GitHub token для обновлений: не задан';
  if(u.token_link)byId('token-link').href=u.token_link;
  const last=S.status?.last_update;if(!byId('update-sub').dataset.checked)byId('update-sub').textContent=last?`Последнее обновление из панели: ${last.ok?'успешно':'не удалось'}${last.from?' ('+last.from+' → '+last.version+')':''}.`:u.token?'Нажмите «Проверить».':'Сначала сохраните GitHub token ниже.';
  renderOps(byId('update-ops'),['self-update','engine-install','engine-update','engine-remove']);}
async function checkUpdate(){byId('update-title').textContent='Спрашиваем GitHub…';byId('update-install').hidden=true;
  try{const r=await api('/api/update',{action:'check'});byId('update-sub').dataset.checked='1';
    byId('update-title').textContent=r.newer?`Доступна версия ${r.latest}`:`Установлена последняя версия ${r.current}`;byId('update-sub').textContent=r.newer?(r.notes||'').split('\n').find(l=>l.trim())||`Опубликована ${r.published?new Date(r.published).toLocaleDateString('ru-RU'):''}`:'';
    byId('update-install').hidden=!r.newer;byId('update-install').dataset.version=r.latest||'';byId('update-install').textContent=`Обновить до ${r.latest}`;}
  catch(e){byId('update-title').textContent='Проверка не удалась';byId('update-sub').textContent=e.message;}}
// ── other versions: the list is asked from GitHub only when the owner opens it ──
async function loadVersions(){setHTML(byId('versions'),'<span class="small muted">Спрашиваем GitHub…</span>');
  try{ST.versions=await api('/api/update',{action:'list'});renderVersions();}catch(e){setHTML(byId('versions'),`<span class="small danger-text">${esc(e.message)}</span> <button class="btn sm" type="button" id="versions-retry" data-versions-retry>Повторить</button>`);throw e;}}
function versionRow(r){const kind=r.current?'<span class="pill ok">Установлена</span>':r.newer?'<span class="pill iris">Новее</span>':'<span class="pill">Старше</span>';
  return `<div class="vrow"><div class="vt"><b>${esc(r.version)}</b>${kind}${r.prerelease?'<span class="pill warn">Предварительная</span>':''}<span class="small faint">${r.published?dateShort(r.published)+' '+new Date(r.published).getFullYear():''}</span></div>`+
    `<span class="small muted">${esc(r.summary||'')}</span>${r.current?'':`<button class="btn sm${r.newer?' primary':''}" type="button" data-install-version="${esc(r.version)}">${r.newer?'Обновить':'Откатить'}</button>`}</div>`;}
function renderVersions(){const v=ST.versions;if(!v)return;setHTML(byId('versions'),(v.releases||[]).map(versionRow).join('')||'<span class="small muted">Выпусков нет.</span>');}
// Installing another version asks first: it restarts the panel, and a rollback drops what is newer.
async function installVersion(version){const cur=S.status?.version||ST.update?.current||'',older=cur&&newerThan(cur,version);
  const ok=await confirmDialog({title:older?`Откатить на ${version}?`:`Установить ${version}?`,yes:older?'Откатить':'Установить',
    text:older?`Сейчас установлена ${cur}. Настройки, пароль, ключи, чаты и модели останутся; возможности, которых нет в ${version}, пропадут. Панель перезапустится примерно через минуту. Если установка не пройдёт, вернётся ${cur}.`:`Панель перезапустится примерно через минуту. Если установка не пройдёт, вернётся ${cur||'текущая версия'}.`});
  if(ok)await operation({action:'self-update',version});}
const newerThan=(a,b)=>{const x=a.split('.').map(Number),y=b.split('.').map(Number);for(let i=0;i<3;i++)if((x[i]||0)!==(y[i]||0))return (x[i]||0)>(y[i]||0);return false;};
function renderEngineManager(list){setHTML(byId('engine-manager'),(list||[]).map(e=>{const rel=SV.releases[e.name]||[];
  return `<div class="stack8"><div class="line"><b>${esc(e.label)}</b><span class="pill">${!e.installed?'Не установлен':e.version?'Версия '+esc(e.version):'Установлен вне OmniBox'}</span></div>
    <span class="small muted">${e.installed?(e.managed?`Скачанные версии: ${esc((e.versions||[]).join(', ')||'—')}.${e.pending?' Новая версия применится при следующем запуске.':''}`:'Движок установлен вне OmniBox; обновление перенесёт его в каталог OmniBox.'):'Будут скачаны файлы с GitHub и создана служба с ручным запуском.'}</span>
    <div class="flexrow">${rel.length?`<select class="sel-fit" data-rel="${e.name}" aria-label="Версия ${esc(e.label)}">${rel.map(r=>`<option value="${esc(r.tag)}">${esc(r.tag)} · ${String(r.published).slice(0,10)}${r.prerelease?' · предварительная':''}${r.tag===e.version?' · установлена':''}</option>`).join('')}</select><button class="btn sm" type="button" data-eng-pick="${e.name}">Установить выбранную</button>`:''}
      <button class="btn sm" type="button" data-eng-rel="${e.name}">${rel.length?'Обновить список':'Показать версии'}</button><button class="btn sm primary" type="button" data-eng-latest="${e.name}" data-installed="${e.installed}">${e.installed?'Обновить до последней':'Установить'}</button>${e.installed?`<button class="btn ghost sm danger-text" type="button" data-eng-remove="${e.name}">Удалить</button>`:''}</div></div>`;}).join(''));}
async function engineAction(body){const op=await operation(body);SV.engines=(await api('/api/engines')).engines;renderEngineManager(SV.engines);return op;}

// ── events ──
byId('inject-date').addEventListener('change',e=>saveGateway({action:'save',inject_date:e.target.checked}).then(()=>notice(e.target.checked?'Модели в API получают текущую дату.':'Дата в запросы API больше не добавляется.')).catch(err=>{e.target.checked=!e.target.checked;fail(err);}));
byId('require-key').addEventListener('change',e=>{const on=e.target.checked;if(on&&!(ST.gateway?.keys||[]).length){e.target.checked=false;notice('Сначала создайте ключ ниже.',true);return;}
  saveGateway({action:'save',require_key:on}).then(()=>notice(on?'Ключ API теперь обязателен.':'API доступен без ключа.')).catch(err=>{e.target.checked=!on;fail(err);});});
byId('key-add').addEventListener('click',()=>{const name=byId('key-name').value.trim();if(!name){notice('Введите имя клиента.',true);return;}saveGateway({action:'add',name}).then(()=>{byId('key-name').value='';}).catch(fail);});
byId('api-save').addEventListener('click',()=>saveGateway({action:'save',base_url:byId('public-url').value.trim(),queue_depth:Number(byId('queue-depth').value),queue_timeout_seconds:Number(byId('queue-timeout').value),
  public_origin:byId('public-origin').value.trim(),trusted_proxies:byId('trusted').value.split(',').map(s=>s.trim()).filter(Boolean)}).then(()=>notice('Настройки API сохранены.')).catch(fail));
byId('searx-check').addEventListener('click',async()=>{const st=byId('search-state');st.textContent='Проверяем…';st.className='pill';
  try{const d=await api('/api/chat-settings',{action:'check-search',search_url:byId('searx').value.trim()});st.textContent=`Работает · ${d.search_ms} мс`;st.className='pill ok';}catch(e){st.textContent='Ошибка';st.className='pill warn';fail(e);}});
byId('search-engine').addEventListener('change',renderSearchEngine);
byId('search-key-save').addEventListener('click',async()=>{const engine=byId('search-engine').value,key=byId('search-key').value.trim();byId('search-key').value='';if(!key){notice('Вставьте ключ.',true);return;}
  try{S.chatSettings=await api('/api/chat-settings',{action:'search-key',engine,key});renderChatSettings();byId('search-engine').value=engine;renderSearchEngine();notice(`Ключ ${ENGINES[engine].name} проверен и сохранён. Нажмите «Сохранить», чтобы искать через ${ENGINES[engine].name}.`);}catch(e){fail(e);}});
byId('search-key-forget').addEventListener('click',()=>{const engine=byId('search-engine').value;api('/api/chat-settings',{action:'search-key-forget',engine}).then(d=>{S.chatSettings=d;renderChatSettings();byId('search-engine').value=engine;renderSearchEngine();if(typeof renderWeb==='function')renderWeb();notice('Ключ удалён с ПК.');}).catch(fail);});
byId('search-check').addEventListener('click',async()=>{const st=byId('search-state');st.textContent='Проверяем…';
  try{const d=await api('/api/chat-settings',{action:'check-search'});S.chatSettings={...S.chatSettings,...d};st.textContent=`Работает · ${d.search_ms} мс`;st.className='pill ok';}catch(e){st.textContent='Ошибка';st.className='pill warn';fail(e);}});
byId('search-save').addEventListener('click',()=>saveChatSettings({search_engine:byId('search-engine').value,sonar_model:byId('search-engine').value==='sonar'?byId('sonar-model').value:undefined,plan_queries:byId('plan-queries').checked,search_url:byId('searx').value.trim(),search_results:Number(byId('search-n').value),search_language:byId('search-lang').value,search_default:byId('search-default').checked,site_icons:byId('site-icons').checked,read_pages:byId('read-pages').checked}).then(()=>notice('Настройки веб-поиска сохранены.')).catch(fail));
byId('or-connect').addEventListener('click',async()=>{const key=byId('or-key').value.trim();byId('or-key').value='';if(!key){notice('Вставьте ключ OpenRouter.',true);return;}
  try{await cloudAction({action:'key-save',key});notice('OpenRouter подключён: список моделей загружен.');await Promise.all([refreshAccount(),refreshLibrary()]);renderCloud();}catch(e){fail(e);}});
byId('or-disconnect').addEventListener('click',()=>cloudAction({action:'key-forget'}).then(()=>{S.account=null;renderNow();notice('Ключ OpenRouter удалён с ПК.');return refreshLibrary();}).catch(fail));
byId('or-refresh').addEventListener('click',()=>cloudAction({action:'refresh'}).then(()=>refreshLibrary()).then(()=>notice('Список моделей OpenRouter обновлён.')).catch(fail));
byId('or-low').addEventListener('change',e=>cloudAction({action:'settings',low_balance_usd:Number(e.target.value)}).catch(fail));
byId('gpt-install').addEventListener('click',()=>gptAction({action:'install'}).catch(fail));
byId('gpt-update').addEventListener('click',()=>gptAction({action:'install'}).then(()=>notice('Проверяем новую версию Codex…')).catch(fail));
byId('gpt-login').addEventListener('click',()=>gptAction({action:'login'}).catch(fail));
byId('gpt-login-cancel').addEventListener('click',()=>gptAction({action:'login-cancel'}).catch(fail));
byId('gpt-code-copy').addEventListener('click',()=>copyText(byId('gpt-code-v').textContent).then(()=>notice('Код скопирован.')).catch(fail));
byId('gpt-refresh').addEventListener('click',()=>gptAction({action:'limits'}).then(()=>notice('Лимиты ChatGPT обновлены.')).catch(fail));
byId('gpt-search').addEventListener('change',e=>gptAction({action:'settings',openai_search:e.target.checked}).then(()=>{if(S.chatSettings)S.chatSettings.chatgpt_search=e.target.checked;if(typeof renderWeb==='function')renderWeb();notice(e.target.checked?'Модели ChatGPT ищут встроенным поиском OpenAI.':'Модели ChatGPT ищут поисковой системой OmniBox.');}).catch(fail));
byId('gpt-expose').addEventListener('change',e=>gptAction({action:'settings',expose_api:e.target.checked}).then(()=>notice(e.target.checked?'Модели ChatGPT видны клиентам API.':'Модели ChatGPT скрыты из API.')).catch(fail));
byId('gpt-logout').addEventListener('click',e=>{const b=e.currentTarget;if(b.dataset.sure!=='1'){b.dataset.sure='1';b.textContent='Точно выйти?';return;}b.dataset.sure='';b.textContent='Выйти из аккаунта';
  gptAction({action:'logout'}).then(()=>{S.gptLimits=null;notice('Вы вышли из ChatGPT: вход удалён с ПК.');return refreshLibrary();}).catch(fail);});
byId('gpt-remove').addEventListener('click',e=>{const b=e.currentTarget;if(b.dataset.sure!=='1'){b.dataset.sure='1';b.textContent='Точно удалить Codex и вход?';return;}b.dataset.sure='';b.textContent='Удалить Codex и вход';
  gptAction({action:'remove'}).then(()=>{S.gptLimits=null;notice('Codex и вход в ChatGPT удалены с ПК.');return refreshLibrary();}).catch(fail);});
byId('or-expose').addEventListener('change',e=>cloudAction({action:'settings',expose_api:e.target.checked}).then(()=>notice(e.target.checked?'Избранные модели OpenRouter видны клиентам API.':'Модели OpenRouter скрыты из API.')).catch(fail));
byId('asst-default').addEventListener('change',e=>api('/api/assistants',{action:'default',id:e.target.value}).then(r=>{S.assistants=r;renderAssistants();}).catch(fail));
byId('ae-instr').addEventListener('input',countInstr);byId('ae-name').addEventListener('input',renderEmoji);
byId('ae-emojis').addEventListener('click',e=>{const b=e.target.closest('[data-emoji]');if(!b)return;ST.emoji=b.dataset.emoji;byId('ae-emoji').value='';renderEmoji();});
byId('ae-emoji').addEventListener('input',e=>{ST.emoji=[...e.target.value.trim()].slice(0,2).join('');renderEmoji();});
byId('ae-letter').addEventListener('click',()=>{ST.emoji='';byId('ae-emoji').value='';renderEmoji();});
byId('ae-file').addEventListener('change',e=>{const f=e.target.files[0];if(!f)return;const rd=new FileReader();rd.onload=()=>{byId('ae-instr').value=String(rd.result);countInstr();if(!byId('ae-name').value)byId('ae-name').value=f.name.replace(/\.(md|txt)$/i,'').slice(0,40);renderEmoji();};rd.readAsText(f);e.target.value='';});
byId('ae-save').addEventListener('click',()=>saveAsst());
byId('ae-delete').addEventListener('click',async()=>{const b=byId('ae-delete');if(b.dataset.sure!=='1'){b.dataset.sure='1';b.textContent='Точно удалить? Нажмите ещё раз';return;}
  try{S.assistants=await api('/api/assistants',{action:'delete',id:ST.editing});hideAll();renderAssistants();}catch(e){fail(e);}});
byId('follow-on').addEventListener('change',e=>saveChatSettings({follow_ups:e.target.checked}).then(()=>notice(e.target.checked?'Под ответами появятся вопросы для продолжения.':'Вопросы после ответов выключены.')).catch(err=>{e.target.checked=!e.target.checked;fail(err);}));
byId('auto-title').addEventListener('change',e=>saveChatSettings({auto_title:e.target.checked}).then(()=>notice(e.target.checked?'Новые чаты будет называть модель.':'Название чата — начало первого вопроса.')).catch(err=>{e.target.checked=!e.target.checked;fail(err);}));
byId('voice-server').addEventListener('change',e=>saveChatSettings({voice_server:e.target.checked}).then(()=>{if(typeof renderMic==='function')renderMic();notice(e.target.checked?'Голос распознаёт модель OpenRouter во всех браузерах.':'Голос распознаёт браузер, если умеет.');}).catch(fail));
byId('voice-model').addEventListener('change',e=>saveChatSettings({voice_model:e.target.value}).then(()=>notice('Модель для голоса сохранена.')).catch(fail));
byId('follow-model').addEventListener('change',e=>saveChatSettings({follow_up_model:e.target.value}).then(()=>notice('Модель для вопросов сохранена.')).catch(fail));
byId('keep-chats').addEventListener('change',e=>saveChatSettings({retention_hours:Number(e.target.value)}).then(()=>notice('Срок хранения новых чатов сохранён.')).catch(fail));
byId('keep-logs').addEventListener('change',e=>api('/api/storage',{action:'save',...KEEP_LOGS[e.target.value]}).then(u=>{ST.storage=u;renderStorage();notice('Срок хранения журналов сохранён.');}).catch(fail));
byId('request-text').addEventListener('change',e=>api('/api/storage',{action:'save',request_log:e.target.checked?'content':'metadata'}).then(u=>{ST.storage=u;renderStorage();notice(e.target.checked?'Текст запросов клиентов теперь записывается.':'Запись текста выключена, записанный текст удалён.');}).catch(err=>{e.target.checked=!e.target.checked;fail(err);}));
byId('autostart').addEventListener('change',e=>operation({action:'autostart',enabled:e.target.checked}).catch(err=>{e.target.checked=!e.target.checked;fail(err);}));
byId('start-model').addEventListener('change',e=>api('/api/library',{action:'start-model',model:e.target.value}).then(()=>{S.library.start_model=e.target.value;notice(e.target.value?'Эта модель загрузится после включения ПК.':'После включения ПК модели не загружаются.');}).catch(fail));
byId('update-check').addEventListener('click',()=>checkUpdate());
byId('shutdown-omnibox').addEventListener('click',()=>ask(byId('shutdown-confirm'),'Выключить OmniBox? Панель закроется через несколько секунд и откроется снова только после ai on или перезагрузки ПК.').then(yes=>yes&&operation({action:'shutdown'})).catch(fail));
byId('update-install').addEventListener('click',()=>operation({action:'self-update',version:byId('update-install').dataset.version}).catch(fail));
byId('update-other').addEventListener('click',()=>{const v=byId('update-target').value.trim();if(!/^\d+\.\d+\.\d+$/.test(v)){notice('Версия — три числа через точку, например 0.20.5.',true);return;}installVersion(v).catch(fail);});
byId('versions-load').addEventListener('click',()=>loadVersions().catch(fail));
byId('versions').addEventListener('click',e=>{const b=e.target.closest('[data-install-version]');if(b){installVersion(b.dataset.installVersion).catch(fail);return;}if(e.target.closest('[data-versions-retry]'))loadVersions().catch(()=>{});});
byId('token-save').addEventListener('click',async()=>{const token=byId('token').value.trim();byId('token').value='';if(!token){notice('Вставьте GitHub token.',true);return;}
  try{ST.update=await api('/api/update',{action:'save-token',token});renderUpdate();notice('Токен проверен на GitHub и сохранён.');}catch(e){fail(e);}});
byId('token-forget').addEventListener('click',async()=>{try{ST.update=await api('/api/update',{action:'forget-token'});renderUpdate();notice('Токен удалён с ПК.');}catch(e){fail(e);}});
byId('password-form').addEventListener('submit',async e=>{e.preventDefault();const current=byId('pw-current').value,next=byId('pw-new').value,repeat=byId('pw-repeat').value;
  ['pw-current','pw-new','pw-repeat'].forEach(id=>byId(id).value='');if(next!==repeat){notice('Новый пароль и повтор не совпадают.',true);return;}
  try{await api('/api/password',{current,password:next});notice('Пароль панели изменён. Другие сеансы входа завершены.');}catch(err){fail(err);}});
byId('logout').addEventListener('click',()=>api('/api/logout',{}).finally(showLogin));
byId('settings').addEventListener('click',e=>{
  const rv=e.target.closest('[data-revoke]');if(rv){const b=rv;if(b.dataset.sure!=='1'){b.dataset.sure='1';b.textContent='Точно отозвать?';return;}saveGateway({action:'revoke',id:b.dataset.revoke}).then(()=>notice(`Ключ «${b.dataset.name}» отозван.`)).catch(fail);return;}
  if(e.target.closest('[data-asst-new]')){openAsst(null);return;}
  const ed=e.target.closest('[data-asst-edit]');if(ed){openAsst(ed.dataset.asstEdit);return;}
  const w=e.target.closest('[data-wipe]');if(w){const x=WIPE[w.dataset.wipe];ask(byId('wipe-confirm'),x.text(),'Удалить').then(async yes=>{if(!yes)return;const msg=await x.run();notice(msg);
    S.chatSettings=await api('/api/chat-settings');renderChatSettings();await refreshStorage();if(typeof loadThreads==='function'){await loadThreads();if(C.chat&&!C.threads.some(t=>t.id===C.chat.id))newChat();}}).catch(fail);return;}
  const er=e.target.closest('[data-eng-rel]');if(er){api('/api/engine-releases?engine='+er.dataset.engRel).then(r=>{SV.releases[er.dataset.engRel]=r.releases||[];renderEngineManager(SV.engines);if(!r.releases?.length)notice('Подходящих сборок для этой системы не найдено.');}).catch(fail);return;}
  const el=e.target.closest('[data-eng-latest]');if(el){const n=el.dataset.engLatest;engineAction(el.dataset.installed==='true'?{action:'engine-update',engine:n,version:''}:{action:'engine-install',engine:n,version:''}).catch(fail);return;}
  const ep=e.target.closest('[data-eng-pick]');if(ep){const n=ep.dataset.engPick;engineAction({action:'engine-update',engine:n,version:$(`[data-rel="${n}"]`).value}).catch(fail);return;}
  const ex=e.target.closest('[data-eng-remove]');if(ex){if(ex.dataset.sure!=='1'){ex.dataset.sure='1';ex.textContent='Точно удалить? Модели останутся';return;}engineAction({action:'engine-remove',engine:ex.dataset.engRemove}).catch(fail);return;}
});
on('status',()=>{if(S.page==='settings'){renderUpdate();renderSleep();}});
on('library',()=>{if(S.page==='settings'){renderSleep();renderCloud();renderGateway();}});
PAGES.settings={show(){renderCloud();renderAssistants();renderChatSettings();renderSleep();renderUpdate();applyTheme(theme);
  api('/api/gateway').then(g=>{ST.gateway=g;renderGateway();}).catch(fail);refreshStorage().catch(()=>{});
  api('/api/chat-settings').then(d=>{S.chatSettings=d;renderChatSettings();}).catch(()=>{});
  refreshCloud().then(()=>{renderCloud();return refreshAccount();}).then(renderCloud).catch(()=>{});
  refreshGpt(false).then(g=>{renderGpt();if(g.signed_in)return refreshGpt(true).then(renderGpt);}).catch(()=>{});
  api('/api/update').then(u=>{ST.update=u;renderUpdate();}).catch(()=>{});
  api('/api/engines').then(d=>{SV.engines=d.engines;renderEngineManager(SV.engines);}).catch(()=>{});
  api('/api/assistants').then(d=>{S.assistants=d;renderAssistants();}).catch(()=>{});}};
