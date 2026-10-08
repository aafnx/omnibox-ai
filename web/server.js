'use strict';
// Server: power, what runs now, resources, engines, recent requests and journals.
const SV={activity:null,gateway:null,timer:0,pc:'',engines:null,releases:{}};
const PC={unload:{text:'Выгрузить локальные модели? Движки PrismML и Ollama остановятся, видеопамять и оперативная память ПК освободятся. Облачные модели OpenRouter продолжат работать, ПК останется включённым.',body:{action:'stopall'},yes:'Выгрузить'},
  sleep:{text:'Перевести ПК в сон? Локальные модели выгрузятся, панель и API станут недоступны. Разбудить можно через Home Assistant.',body:{action:'sleep'},yes:'Усыпить'},
  poweroff:{text:'Выключить ПК? Текущий ответ прервётся, панель и API станут недоступны. Включить можно через Wake-on-LAN.',body:{action:'poweroff'},yes:'Выключить'}};
const working=()=>(S.status?.backends||[]).some(b=>b.service==='Running'||b.service==='StartPending');
const uptime=s=>s>=3600?`${Math.floor(s/3600)} ч ${Math.floor(s%3600/60)} мин`:`${Math.max(1,Math.floor(s/60))} мин`;
function renderHero(){const m=runningModel();const b=(S.status?.backends||[]).find(x=>x.service==='Running'||x.service==='StartPending');const a=SV.activity;
  const last=(a?.requests||[]).find(r=>r.state==='completed'&&r.output_tokens&&r.duration_seconds);const speed=last?Math.round(last.output_tokens/Math.max(.1,last.duration_seconds-(last.queued_seconds||0))):null;
  let h=`<span class="label">Сейчас работает</span>`;
  if(!b)h+=`<div class="big"><span class="dot off"></span>Локальные модели выгружены</div><span class="muted">Видеопамять свободна. Модель запускается из чата или со страницы «Модели»; облачные модели работают всегда.</span><div class="flexrow"><button class="btn" type="button" data-go="models">Выбрать модель</button></div>`;
  else{h+=`<div class="big"><span class="dot"></span>${esc(m?.name||b.selected_model||b.loaded_model||engineLabel(b.name))}</div>${m?`<div class="caps">${allChips(m)}${ctxChip(m)}</div>`:''}
    <div class="kv"><div><span class="label">Скорость</span><b>${speed?speed+' ток/с':'—'}</b></div><div><span class="label">В очереди</span><b>${a?a.queued||0:'—'}</b></div><div><span class="label">Движок</span><b>${engineLabel(b.name)}</b></div></div>
    <div class="flexrow"><button class="btn" type="button" id="hero-unload">Выгрузить из памяти</button><button class="btn" type="button" id="hero-test"${b.api!=='Ready'?' disabled':''}>Проверить скорость</button>${m?`<button class="btn ghost" type="button" data-hero-settings="${esc(m.key)}">Настройки модели</button>`:''}</div>`;}
  setHTML(byId('hero-main'),h);renderStats();}
function res(label,value,pct){return `<div class="res"><div class="line"><span class="muted">${label}</span><b>${value}</b></div><div class="meter"><i data-w="${Math.max(0,Math.min(100,pct||0))}%"></i></div></div>`;}
function renderStats(){const s=S.sample;if(!s){setHTML(byId('hero-stats'),'<span class="muted small">Показатели появятся после первого снимка.</span>');return;}
  const g=s.gpu?.[0];let h='';
  if(g)h+=res('Видеопамять',`${gb(g.used/1024)} / ${gb(g.total/1024)} ГБ`,g.used/g.total*100);
  h+=res('Оперативная память',`${fmtN(s.ram_used_gb)} / ${fmtN(s.ram_total_gb)} ГБ`,s.ram_percent);
  if(g)h+=res('Видеокарта',`${g.utilization??'—'} %${g.temperature!=null?` · ${g.temperature} °C`:''}`,g.utilization);
  h+=res('Процессор',s.cpu_percent==null?'—':`${fmtN(s.cpu_percent)} %`,s.cpu_percent);
  h+=`<span class="hint">${working()?'Обновляется, пока открыта вкладка и работает модель.':`Снимок ${timeShort(s.timestamp)}. Модели выгружены — постоянный опрос выключен.`}</span>`;
  setHTML(byId('hero-stats'),h);}
async function telemetryLoop(){clearTimeout(SV.timer);if(S.page!=='server'||document.hidden||!loggedIn)return;
  try{const d=await api('/api/telemetry'+(working()?'':'?once=1'));if(d.sample)S.sample=d.sample;renderStats();renderNow();}catch(e){}
  if(working())SV.timer=setTimeout(async()=>{try{await Promise.all([refreshStatus(),refreshActivity()]);}catch(e){}renderHero();telemetryLoop();},3000);}
async function refreshActivity(){SV.activity=await api('/api/activity');renderRecent();}
function source(r){if(r.client==='chat')return 'Чат OmniBox';if(r.client==='test')return 'Проверка скорости';
  const k=(SV.gateway?.keys||[]).find(x=>x.id===r.key_id);return k?`Клиент · ключ «${k.name}»`:r.key_id?'Клиент · удалённый ключ':'Клиент без ключа';}
function renderRecent(){const list=(SV.activity?.requests||[]).slice(0,30);
  setHTML(byId('recent'),list.map(r=>{const sp=r.output_tokens&&r.duration_seconds?Math.round(r.output_tokens/Math.max(.1,r.duration_seconds-(r.queued_seconds||0))):null;
    return `<tr><td>${timeShort(r.started)}</td><td>${esc(source(r))}</td><td>${esc(r.model)}</td><td class="num">${r.input_tokens!=null?fmtN(r.input_tokens)+' → '+fmtN(r.output_tokens):'—'}</td><td class="num">${r.state==='failed'?`<span class="danger-text">ошибка ${r.status}</span>`:sp?sp+' ток/с':'—'}</td></tr>`;}).join('')||'<tr><td colspan="5" class="muted">Запросов пока нет.</td></tr>');
  byId('recent-note').textContent=SV.activity?.journal_error||'Локальные модели: чат OmniBox и клиенты API. Запросы к OpenRouter — в «Настройки → OpenRouter».';
  setHTML(byId('sessions'),(SV.activity?.sessions||[]).map(s=>`<tr><td>${esc(s.id.slice(0,8))}</td><td>${esc(s.model)}</td><td class="num">${fmtN(s.requests)}</td><td class="num">${fmtN(s.input_tokens)}</td><td class="num">${fmtN(s.output_tokens)}</td></tr>`).join('')||'<tr><td colspan="5" class="muted">Сеансов пока нет.</td></tr>');}
function engineState(b){if(!b)return ['Не установлен',''];return b.service==='Running'?(b.api==='Ready'?['Работает','ok']:['Запускается','warn']):b.service==='StartPending'?['Запускается','warn']:b.service==='Missing'?['Служба не найдена','warn']:['Выключен',''];}
function renderEngines(){const infos=SV.engines||[];const bs=S.status?.backends||[];
  setHTML(byId('engines'),['prismml','ollama'].map(n=>{const b=bs.find(x=>x.name===n),i=infos.find(x=>x.name===n);const [st,cls]=engineState(b&&(i?.installed??true)?b:null);
    const sub=n==='prismml'?'llama-server · GGUF':'Ollama · библиотека моделей';const ver=i?.version?` · версия ${esc(i.version)}`:'';const port=b?.endpoint?` · ${esc(b.endpoint.replace(/^https?:\/\//,''))}`:'';
    return `<div class="panel engine"><div class="line"><h2>${engineLabel(n)}</h2><span class="pill ${cls}">${st}</span></div><span class="muted small">${sub}${ver}${port}${n==='ollama'&&b?.service!=='Running'?' · включается при запуске его модели':''}</span>${(b?.errors||[]).length?`<span class="errbox">${esc(b.errors.join(' '))}</span>`:''}${b?`<div class="line small"><span class="muted">Перезапускать после сбоя</span><label class="switch"><input type="checkbox" data-auto-restart="${n}"${b.auto_restart?' checked':''} aria-label="Перезапускать ${engineLabel(n)} после сбоя"><span></span></label></div>`:''}<div class="acts"><button class="btn sm ghost" type="button" data-engine-logs="${n}">Журнал</button><button class="btn sm ghost" type="button" data-go="settings" data-jump-after="s-update">Версии</button></div></div>`;}).join(''));}
async function refreshEngineInfo(){try{SV.engines=(await api('/api/engines')).engines;}catch(e){}renderEngines();}
async function refreshLogs(engine){const d=await api('/api/logs'+(engine?'?backend='+engine:''));setHTML(byId('engine-logs'),(d.logs||[]).map(l=>`<h3 class="small">${esc(l.stream)}</h3><pre>${esc(l.text)}</pre>`).join('')||'<span class="muted">Журналов нет.</span>');}
function formatContent(c){const parts=[`${c.model} · ${c.route} · ${new Date(c.started).toLocaleString('ru-RU')} · ${c.status}${c.truncated?' · сокращено':''}`];
  const msgs=c.request?.messages||c.request?.input;parts.push('ЗАПРОС',Array.isArray(msgs)?msgs.map(m=>`[${m.role||m.type||'?'}] ${typeof m.content==='string'?m.content:JSON.stringify(m.content,null,2)}`).join('\n\n'):JSON.stringify(c.request??null,null,2));
  if(c.reasoning_text)parts.push('РАЗМЫШЛЕНИЯ',c.reasoning_text);parts.push('ОТВЕТ',c.response!==undefined?JSON.stringify(c.response,null,2):(c.response_text||'—'));return parts.join('\n\n');}
async function showContent(id){const c=await api('/api/request-content?id='+encodeURIComponent(id));byId('content-text').textContent=formatContent(c);byId('content-box').hidden=false;}

byId('server').addEventListener('click',e=>{
  const p=e.target.closest('[data-pc]');if(p){const x=PC[p.dataset.pc];ask(byId('pc-confirm'),x.text,x.yes).then(async yes=>{if(!yes)return;const op=await operation(x.body);if(op.state==='done'&&p.dataset.pc==='unload')await refreshLibrary();renderHero();}).catch(fail);return;}
  if(e.target.closest('#hero-unload')){const b=(S.status?.backends||[]).find(x=>x.service==='Running');if(b)operation({action:'stop',backend:b.name}).then(()=>refreshLibrary()).then(renderHero).catch(fail);return;}
  if(e.target.closest('#hero-test')){notice('Проверяем скорость: короткий запрос через очередь API…');operation({action:'model-test'}).then(()=>refreshActivity()).catch(fail);return;}
  const hs=e.target.closest('[data-hero-settings]');if(hs){go('models');openModel(hs.dataset.heroSettings);return;}
  const lg=e.target.closest('[data-engine-logs]');if(lg){byId('journals').hidden=false;byId('journals-toggle').setAttribute('aria-expanded','true');refreshLogs(lg.dataset.engineLogs).then(()=>byId('engine-logs').scrollIntoView({block:'start'})).catch(fail);return;}
  const cr=e.target.closest('[data-content]');if(cr){showContent(cr.dataset.content).catch(fail);return;}
});
byId('engines').addEventListener('change',e=>{const c=e.target.closest('[data-auto-restart]');if(!c)return;operation({action:'auto-restart',backend:c.dataset.autoRestart,enabled:c.checked}).catch(err=>{c.checked=!c.checked;fail(err);});});
byId('journals-toggle').addEventListener('click',()=>{const j=byId('journals');j.hidden=!j.hidden;byId('journals-toggle').setAttribute('aria-expanded',String(!j.hidden));if(!j.hidden)refreshLogs().catch(fail);});
byId('logs-refresh').addEventListener('click',()=>refreshLogs().catch(fail));
byId('content-close').addEventListener('click',()=>{byId('content-box').hidden=true;byId('content-text').textContent='';});
byId('content-search').addEventListener('submit',async e=>{e.preventDefault();const q=byId('content-q').value.trim();if(q.length<2){notice('Введите хотя бы 2 символа.',true);return;}
  try{const d=await api('/api/request-content?q='+encodeURIComponent(q));setHTML(byId('content-results'),(d.matches||[]).map(m=>`<div class="banner"><span><b>${new Date(m.started).toLocaleString('ru-RU')} · ${esc(m.model)}</b><br>${esc(m.snippet)}</span><span class="flexrow"><button class="btn sm" type="button" data-content="${esc(m.id)}">Открыть</button><a class="btn sm ghost" href="/api/request-content?session=${encodeURIComponent(m.session_id)}&export=1" download>Экспорт сеанса</a></span></div>`).join('')||'<span class="muted">Ничего не найдено в записанном тексте.</span>');}catch(err){fail(err);}});
on('status',()=>{if(S.page==='server'){renderHero();renderEngines();renderOps(byId('server-ops'));}});
on('library',()=>{if(S.page==='server')renderHero();});
PAGES.server={show(){renderHero();renderEngines();renderOps(byId('server-ops'));
  Promise.all([refreshStatus(true).catch(()=>{}),refreshActivity().catch(()=>{}),api('/api/gateway').then(g=>{SV.gateway=g;}).catch(()=>{})]).then(()=>{renderHero();renderRecent();renderEngines();telemetryLoop();});refreshEngineInfo();}};
