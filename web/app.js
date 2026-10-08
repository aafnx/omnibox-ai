'use strict';
const $ = id => document.getElementById(id);
let csrf = '', snapshot = null, activeTab = 'overview', settingsDirty = false, selectedBackend = '', pollTimer = null, loggedIn = false;
let formRevision = 0;
const submittedForms = new Map();
let gateway = null, gatewayDirty = false, panelPort = 8800;
let activity=null, refreshBusy=false, diskUsage=null;
const pendingActions=new Set();
const themeMedia=window.matchMedia('(prefers-color-scheme: dark)');
const themes=['light','dark','system'];
function applyTheme(value){document.documentElement.dataset.theme=value==='system'?(themeMedia.matches?'dark':'light'):value;themes.forEach(t=>{const b=$(`theme-${t}`);b.classList.toggle('active',t===value);b.setAttribute('aria-pressed',String(t===value));});}
let theme='system';try{const saved=localStorage.getItem('omnibox-theme');if(['light','dark','system'].includes(saved))theme=saved;}catch{}
applyTheme(theme);
themes.forEach(t=>$(`theme-${t}`).addEventListener('click',()=>{theme=t;applyTheme(theme);try{localStorage.setItem('omnibox-theme',theme);}catch{}}));
// One switch: top right of the login screen, inside the top bar of the dashboard.
function placeThemeSwitch(inDashboard){const target=inDashboard?$('topbar-actions'):document.body;if(target)target.append($('theme-switch'));}
themeMedia.addEventListener('change',()=>{if(theme==='system')applyTheme(theme);});
const fields = {placement:'placement',context:'context',idle_minutes:'idle',alias:'alias',mmproj:'mmproj',gpu_layers:'gpu-layers',n_cpu_moe:'n-cpu-moe',flash_attention:'flash',cache_key:'cache-key',cache_value:'cache-value',batch:'batch',ubatch:'ubatch',kv_offload:'kv-offload',threads:'threads',load_mode:'load-mode',reasoning:'reasoning',reasoning_effort:'reasoning-effort',reasoning_budget:'reasoning-budget',temperature:'temperature',top_k:'top-k',top_p:'top-p',min_p:'min-p',repeat_penalty:'repeat-penalty',presence_penalty:'presence-penalty',frequency_penalty:'frequency-penalty',max_tokens:'max-tokens',seed:'seed'};
const labels = {'engine-install':'Установка движка','engine-update':'Обновление движка','engine-remove':'Удаление движка','model-delete':'Удаление модели',start:'Запуск',stop:'Остановка',restart:'Перезапуск',stopall:'Остановка всех серверов',save:'Сохранение',apply:'Применение',restore:'Восстановление','auto-restart':'Автоперезапуск','self-update':'Обновление программы','preset-save':'Сохранение пресета','preset-delete':'Удаление пресета','preset-apply':'Применение пресета','model-test':'Тест модели',sleep:'Сон ПК',autostart:'Автозапуск','start-engine':'Модель при старте',shutdown:'Выключение OmniBox',load:'Загрузка',unload:'Выгрузка',pull:'Скачивание',download:'Скачивание GGUF'};
async function api(path, body) {
  const response = await fetch(path, {method:body === undefined?'GET':'POST',credentials:'same-origin',headers:body === undefined?{}:{'Content-Type':'application/json','X-Omnibox-CSRF':csrf},body:body === undefined?undefined:JSON.stringify(body)});
  const data = await response.json();
  if (!response.ok) { if(response.status===401 && path!=='/api/login') showLogin(); throw new Error(data.error||`Ошибка ${response.status}`); }
  return data;
}
// Notices pop up for a few seconds and stay in the history: this browser only, the last 50.
let noticeLog=[];try{noticeLog=JSON.parse(localStorage.getItem('omnibox-notices')||'[]');if(!Array.isArray(noticeLog))noticeLog=[];}catch{noticeLog=[];}
let unreadNotices=0;const toasts=[];
function notice(message, error=false) {
  if(!message)return;const item={at:new Date().toISOString(),text:String(message),error:!!error};
  noticeLog.unshift(item);noticeLog=noticeLog.slice(0,50);try{localStorage.setItem('omnibox-notices',JSON.stringify(noticeLog));}catch{}
  if($('notifications').hidden)unreadNotices++;renderNotifications();
  const toast=document.createElement('div');toast.className=`toast${error?' error':''}`;toast.setAttribute('role',error?'alert':'status');
  const text=document.createElement('span');text.textContent=item.text;const close=document.createElement('button');close.type='button';close.className='toast-close';close.setAttribute('aria-label','Закрыть');close.textContent='×';
  const remove=()=>{toast.remove?.();const i=toasts.indexOf(toast);if(i>=0)toasts.splice(i,1);};close.addEventListener('click',remove);
  toast.append(text,close);$('toasts').append(toast);toasts.push(toast);while(toasts.length>4)toasts[0].remove?.(),toasts.shift();
  setTimeout(remove,error?10000:5000);
}
function noticeTime(at){const d=new Date(at);return Number.isNaN(d.getTime())?'':d.toLocaleString('ru-RU',{day:'2-digit',month:'2-digit',hour:'2-digit',minute:'2-digit',second:'2-digit'});}
function renderNotifications(){
  $('notifications-count').hidden=!unreadNotices;$('notifications-count').textContent=unreadNotices>9?'9+':String(unreadNotices);
  $('notifications-list').replaceChildren(...noticeLog.map(n=>{const li=document.createElement('li');li.className=n.error?'error':'';const time=document.createElement('time');time.textContent=noticeTime(n.at);const text=document.createElement('span');text.textContent=n.text;li.append(time,text);return li;}));
  $('notifications-empty').hidden=noticeLog.length>0;
}
function toggleNotifications(open=$('notifications').hidden){$('notifications').hidden=!open;$('notifications-button').setAttribute('aria-expanded',String(open));if(open)unreadNotices=0;renderNotifications();}
function showLogin() { placeThemeSwitch(false); $('new-api-key').hidden=true;$('key-copy').replaceChildren();gatewayDirty=false;loggedIn=false; clearTimeout(pollTimer); $('login').hidden=false; $('dashboard').hidden=true; csrf=''; }
async function showDashboard() { const session=await api('/api/session'); csrf=session.csrf; panelPort=session.port; loggedIn=true; $('login').hidden=true; $('dashboard').hidden=false; placeThemeSwitch(true); await refresh(true); schedulePoll(); refreshStorage(true).catch(()=>{}); }
function monitoringActive(){return activity?.active||snapshot?.backends.some(b=>b.service==='StartPending'||(b.service==='Running'&&b.sleeping!==true&&(b.type==='prismml'||!['(none)','-',undefined,null].includes(b.loaded_model))));}
function schedulePoll() { clearTimeout(pollTimer); if(!loggedIn)return; pollTimer=setTimeout(async()=>{ if(!document.hidden) { try { await refresh(false); } catch(e) { notice(e.message,true); } } schedulePoll(); },document.hidden?10000:(monitoringActive()||snapshot?.operations?.some(o=>o.state==='running')||pendingActions.size?3000:10000)); }
document.addEventListener('visibilitychange',()=>{if(!document.hidden&&loggedIn)refresh(false,true).catch(e=>notice(e.message,true));});
function backend() { return snapshot?.backends.find(b=>b.name===selectedBackend); }
function chooseBackend(name) { selectedBackend=name; $('model-backend').value=name; settingsDirty=false; formRevision++; $('catalog-results').replaceChildren(); $('catalog-status').textContent=''; renderSettings(); checkFit(); if(activeTab==='models')refreshInstalled().catch(()=>{}); }
function switchTab(tab) { activeTab=tab; document.querySelectorAll('.tab').forEach(s=>s.hidden=s.id!==`tab-${tab}`); document.querySelectorAll('[data-tab]').forEach(b=>b.classList.toggle('active',b.dataset.tab===tab)); if(tab==='models'){refreshStorage(false).catch(()=>{});checkFit();refreshInstalled().catch(()=>{});}if(tab==='logs'){refreshLogs().catch(e=>notice(e.message,true));refreshStorage(false).catch(e=>notice(e.message,true));} if(tab==='updates'){renderUpdateInfo();refreshEngines().catch(e=>notice(e.message,true));} }
async function refresh(gpu,snapshotRequested=gpu) {
  if(refreshBusy)return;refreshBusy=true;
  try{
  const [data,apiConfig,stats]=await Promise.all([api(`/api/status${gpu?'?gpu=1':''}`),api('/api/gateway'),api('/api/activity')]);activity=stats; snapshot=data; $('version').textContent=`v${data.version}`; $('computer').textContent=data.computer;
  $('server-count').textContent=`${data.backends.filter(b=>b.service==='Running').length} / ${data.backends.length}`; renderModelSummary();
  if(gpu) { const g=data.gpu?.[0]; $('gpu-value').textContent=g?`${(g.used/1024).toFixed(1)} / ${(g.total/1024).toFixed(1)} ГБ`:'Нет данных'; $('gpu-bar').value=g?g.used/g.total*100:0; $('gpu-caption').textContent=g?`${g.name} · свободно ${(g.free/1024).toFixed(1)} ГБ`:'nvidia-smi недоступен.'; }
  renderEngines(); renderOperations(data.operations||[]); renderInlineProgress(data.operations||[]);
  for(const id of ['model-backend']) { const select=$(id), old=select.value; select.replaceChildren(); data.backends.forEach(b=>select.add(new Option(engineName(b.name),b.name))); select.value=data.backends.some(b=>b.name===old)?old:data.default_backend; }
  if(!selectedBackend)selectedBackend=data.default_backend;
  $('model-backend').value=selectedBackend;
  if(!settingsDirty)renderSettings();
  gateway=apiConfig;renderWake();renderPower();if(!gatewayDirty)renderGatewayForm();if(!$('connections').contains(document.activeElement))renderConnections();
  renderActivity();
  if(monitoringActive())renderTelemetry(await api('/api/telemetry'));
  else if(snapshotRequested)await requestTelemetrySnapshot();
  else renderTelemetry({enabled:false,reason:idleTelemetryReason()});
  }finally{refreshBusy=false;}
}
// Third summary card: what is in memory now; one engine and one model at a time.
// Engines are named by runtime; the config key (e.g. the historical "bonsai") stays internal.
function engineName(name){const b=snapshot?.backends?.find(x=>x.name===name);return b?.type==='prismml'?'PrismML':b?.type==='ollama'?'Ollama':name;}
function renderModelSummary(){
  const running=snapshot.backends.find(b=>b.service==='Running'||b.service==='StartPending');
  // A sleeping engine has unloaded its model: nothing is in memory, whatever name it still reports.
  const model=running&&!running.sleeping&&!['-','(none)',undefined,null,''].includes(running.loaded_model)?running.loaded_model:'';
  $('kpi-model').textContent=model||'Нет';$('kpi-unload').hidden=!model;$('kpi-unload').dataset.backend=running?.name||'';$('kpi-unload').dataset.model=model;
  $('kpi-model-caption').textContent=!running?'Серверы остановлены. Одновременно работает одна модель.':running.sleeping?`${engineName(running.name)} · модель выгружена после простоя, загрузится по первому запросу`:`${engineName(running.name)} · ${badgeText(running).toLowerCase()}`;
}
function badgeText(b) { if(b.service==='Running') return b.sleeping?'Модель спит':b.api==='Ready'?'API готов':'API недоступен'; return ({Stopped:'Остановлен',Missing:'Не установлен',StartPending:'Запускается',StopPending:'Останавливается'})[b.service]||b.service; }
const unknownValues=new Set(['—','Нет данных','Время неизвестно','Сервер остановлен']);
function renderEngines() {
  $('engines').replaceChildren();
  snapshot.backends.forEach(b=>{
    const card=$('engine-template').content.firstElementChild.cloneNode(true);
    card.querySelector('h2').textContent=engineName(b.name);
    card.querySelector('.engine-type').textContent=b.type==='prismml'?'PrismML llama-server · GGUF':'Ollama · библиотека моделей';
    const badge=card.querySelector('.badge'); badge.textContent=badgeText(b); badge.classList.toggle('running',b.service==='Running'); badge.classList.toggle('sleeping',!!b.sleeping);
    card.querySelector('.engine-model').textContent=b.sleeping?'Не загружена · загрузится по запросу':b.loaded_model==='-'||b.loaded_model==='(none)'||!b.loaded_model?'Не загружена':b.loaded_model;
    card.querySelector('.engine-context').textContent=b.active_context?`${Number(b.active_context).toLocaleString('ru-RU')} активный`:`${b.settings?.context||'—'} настроенный`;
    const timer=card.querySelector('.engine-idle');timer.dataset.backend=b.name;timer.textContent=idleText(b);
    card.querySelector('.engine-residency').textContent=residencyText(b.residency);
    card.querySelector('.engine-error').textContent=(b.errors||[]).join('\n');
    // Rows without a known value add noise; the badge already says whether the engine runs.
    card.querySelectorAll('dd').forEach(dd=>{if(dd.parentElement)dd.parentElement.hidden=unknownValues.has(dd.textContent);});
    card.querySelectorAll('[data-action]').forEach(button=>{
      // Show the action that fits the state instead of a disabled twin: running engines offer Stop, stopped ones Start.
      button.hidden=button.dataset.action==='start'&&(b.service==='Running'||b.service==='StartPending')||button.dataset.action==='stop'&&b.service==='Stopped';
      button.disabled=button.dataset.action==='start' && (pendingActions.size>0||snapshot.operations?.some(o=>o.state==='running'&&o.action!=='download'&&o.action!=='pull'));
      button.addEventListener('click',()=>{if(button.dataset.action==='settings'){chooseBackend(b.name);switchTab('models');}else action({action:button.dataset.action,backend:b.name}).catch(e=>notice(e.message,true));});
    });
    const op=snapshot.operations?.find(o=>o.backend===b.name&&o.state==='running'&&!['download','pull','save'].includes(o.action));
    if(op||pendingActions.has(b.name)){const box=document.createElement('div');box.className='engine-progress';const progress=document.createElement('progress');const text=document.createElement('p');text.textContent=b.service==='Stopped'?'Подготавливаем запуск…':b.api!=='Ready'?'Ожидаем загрузку модели и готовность API…':op?.action==='load'?'Загружаем модель в память…':'Завершаем операцию…';box.append(progress,text);card.append(box);}
    $('engines').append(card);
  });
}
const completed = new Set();
// Download progress: done of total, speed and time left.
function transferText(op){
  const big=op.bytes_total>=1024**3;const size=n=>big?(n/1024**3).toFixed(2):(n/1024**2).toFixed(1);const percent=Math.floor(100*(op.bytes_done||0)/op.bytes_total);
  let text=`${size(op.bytes_done||0)} из ${size(op.bytes_total)} ${big?'ГБ':'МБ'} (${percent}%)`;
  if(op.bytes_per_second>0){const left=(op.bytes_total-(op.bytes_done||0))/op.bytes_per_second;text+=` · ${(op.bytes_per_second/1024**2).toFixed(1)} МБ/с · осталось ${left>=3600?Math.floor(left/3600)+' ч '+Math.floor(left%3600/60)+' мин':Math.ceil(left/60)+' мин'}`;}
  return text;
}
// Running operations are also shown where they were started: model downloads under «Добавить
// модель», engine installs among the engines, the program update next to its form.
const inlineProgress={'models-progress':['download','pull','model-delete'],'engines-progress':['engine-install','engine-update','engine-remove'],'update-progress':['self-update']};
function progressRow(op){const row=document.createElement('div');row.className='inline-progress';
  const title=document.createElement('span');title.className='inline-progress-title';title.textContent=`${labels[op.action]||op.action}${op.backend?' · '+engineName(op.backend):''}`;
  const bar=document.createElement('progress');if(op.bytes_total){bar.max=op.bytes_total;bar.value=op.bytes_done||0;}
  const text=document.createElement('small');text.className='muted';text.textContent=op.bytes_total?transferText(op):op.bytes_done?`${(op.bytes_done/1024**2).toFixed(1)} МБ`:`${Math.floor(op.elapsed_seconds||0)} с`;
  row.append(title,bar,text);return row;}
function renderInlineProgress(operations){Object.entries(inlineProgress).forEach(([id,actions])=>{const running=operations.filter(o=>o.state==='running'&&actions.includes(o.action));$(id).replaceChildren(...running.map(progressRow));$(id).hidden=!running.length;});}
function renderOperations(operations) {
  $('operations').replaceChildren();
  let notified = false;
  if(!operations.length) { const p=document.createElement('p');p.className='muted';p.textContent='Операций пока нет.';$('operations').append(p); }
  operations.forEach(op=>{
    const row=document.createElement('div');row.className='operation';
    const icon=document.createElement('span');icon.className=`op-icon ${op.state}`;icon.textContent=op.state==='running'?'↻':op.state==='done'?'✓':'!';
    const title=document.createElement('span');title.textContent=`${labels[op.action]||op.action}${op.backend?' · '+engineName(op.backend):''}`;
    const message=document.createElement('span');message.className='operation-message';message.textContent=op.message;
    row.append(icon,title,message);if(op.state==='running'){const box=document.createElement('div');box.className='operation-progress';const progress=document.createElement('progress');const text=document.createElement('span');text.textContent=`${Math.floor(op.elapsed_seconds||0)} с · ${labels[op.action]||op.action}…`;if(op.bytes_total){progress.max=op.bytes_total;progress.value=op.bytes_done||0;text.textContent=`${transferText(op)} · ${labels[op.action]||op.action}`;}else if(op.bytes_done){text.textContent=`${(op.bytes_done/1024**3).toFixed(2)} ГБ · ${labels[op.action]||op.action}`;}box.append(progress,text);if(['download','pull'].includes(op.action)){const cancel=document.createElement('button');cancel.type='button';cancel.className='text-button small';cancel.textContent='Отменить';cancel.addEventListener('click',()=>api('/api/actions',{action:'cancel',id:op.id}).then(()=>notice('Скачивание отменяется.')).catch(e=>notice(e.message,true)));box.append(cancel);}row.append(box);}$('operations').append(row);
    if(op.state!=='running'&&!completed.has(op.id)){completed.add(op.id);if(String(op.action).startsWith('engine-')&&activeTab==='updates')refreshEngines().catch(()=>{});if(['download','pull','model-delete'].includes(op.action)&&activeTab==='models')refreshInstalled().catch(()=>{});if(!notified){notice(op.message,op.state==='failed');notified=true;}if(op.state==='done'&&op.backend===selectedBackend&&submittedForms.get(op.id)===formRevision)settingsDirty=false;submittedForms.delete(op.id);}
  });
}
// Idle panel: no periodic sampling. The user gets one sample when opening or refreshing the panel.
let lastSample=null,snapshotFollowUp=0;
function idleTelemetryReason(){return snapshot?.backends.some(b=>b.service==='Running')?'Модель спит или выгружена: постоянный опрос приостановлен.':'Серверы остановлены: постоянный опрос выключен.';}
async function requestTelemetrySnapshot(){
  const data=await api('/api/telemetry?once=1');renderTelemetry(data.sample?{...data,snapshot:true}:{enabled:false,reason:'Собираем показатели ресурсов…'});
  // The sample is collected in the background: ask once more for its result, never in a loop.
  if(data.collecting){clearTimeout(snapshotFollowUp);snapshotFollowUp=setTimeout(()=>{if(loggedIn&&!monitoringActive())api('/api/telemetry?once=1').then(d=>{if(d.sample)renderTelemetry({...d,snapshot:true});}).catch(()=>{});},2500);}
}
function fmt(value) { return value===null||value===undefined?'—':Number(value).toLocaleString('ru-RU'); }
function renderTelemetry(data) {
  if(!data.enabled){
    // Keep the last sample on screen with its time instead of dashes.
    if(lastSample){renderSample(lastSample);$('telemetry-state').textContent=`Снимок ${new Date(lastSample.timestamp).toLocaleTimeString('ru-RU')}. ${data.reason||idleTelemetryReason()} Новый снимок — «Обновить данные».`;return;}
    ['cpu-value','ram-value','gpu-load','temperature-value','power-value','fan-value'].forEach(id=>$(id).textContent='—');['cpu-bar','ram-bar','gpu-load-bar'].forEach(id=>$(id).value=0);$('telemetry-state').textContent=data.reason||'Опрос ресурсов выключен.';return;}
  const s=data.sample;if(!s){$('telemetry-state').textContent=data.error||'Собираем показатели ресурсов…';return;}
  lastSample=s;renderSample(s);
  $('telemetry-state').textContent=data.snapshot?`Снимок ${new Date(s.timestamp).toLocaleTimeString('ru-RU')}. ${idleTelemetryReason()} Новый снимок — «Обновить данные».`:`Обновлено ${new Date(s.timestamp).toLocaleTimeString('ru-RU')}. Опрос при открытой панели.`;
}
function renderSample(s) {
  $('cpu-value').textContent=s.cpu_percent===null?'Недоступно':`${fmt(s.cpu_percent)}%`;$('cpu-bar').value=s.cpu_percent||0;
  $('ram-value').textContent=`${fmt(s.ram_used_gb)} / ${fmt(s.ram_total_gb)} ГБ`;$('ram-bar').value=s.ram_percent||0;
  const g=s.gpu?.[0];$('gpu-load').textContent=g?.utilization===null||g?.utilization===undefined?'Недоступно':`${fmt(g.utilization)}%`;$('gpu-load-bar').value=g?.utilization||0;
  $('temperature-value').textContent=g?.temperature===null||g?.temperature===undefined?'—':`${fmt(g.temperature)} °C`;
  $('power-value').textContent=g?.power_watts===null||g?.power_watts===undefined?'—':`${fmt(g.power_watts)} Вт`;
  $('fan-value').textContent=g?.fan_percent===null||g?.fan_percent===undefined?'Вентиляторы: —':`Вентиляторы: ${fmt(g.fan_percent)}%`;
  if(g){$('gpu-value').textContent=`${(g.used/1024).toFixed(1)} / ${(g.total/1024).toFixed(1)} ГБ`;$('gpu-bar').value=g.used/g.total*100;$('gpu-caption').textContent=`${g.name} · свободно ${(g.free/1024).toFixed(1)} ГБ`;}
}
function tableRow(values) {const row=document.createElement('tr');values.forEach(value=>{const cell=document.createElement('td');cell.textContent=value;row.append(cell);});return row;}
function renderActivity() {
  if(!activity||!snapshot)return;
  $('request-history').replaceChildren();(activity.requests||[]).forEach(r=>{const row=tableRow([new Date(r.started).toLocaleString('ru-RU'),r.session_id.slice(0,8),r.model,`${r.state==='completed'?'Готово':'Ошибка'} · ${r.status}`,fmt(r.duration_seconds),fmt(r.input_tokens),fmt(r.output_tokens),fmt(r.total_tokens)]);row.append(contentCell(r));$('request-history').append(row);});
  $('session-history').replaceChildren();(activity.sessions||[]).forEach(s=>{$('session-history').append(tableRow([s.id.slice(0,8),s.model,`${s.requests}${s.missing_usage?' · без usage: '+s.missing_usage:''}`,fmt(s.input_tokens),fmt(s.output_tokens),fmt(s.total_tokens)]));});
  $('journal-status').textContent=activity.journal_error||'Журнал содержит только метаданные запросов через /v1 OMNIBOX AI.';
  const running=snapshot.backends.find(b=>['Running','StartPending'].includes(b.service));
  const r=activity.active||(activity.requests||[]).find(x=>running&&x.backend===running.name&&(!running.selected_model||x.model===running.selected_model));
  const box=$('live-session');box.replaceChildren();
  if(!running&&!activity.active){const p=document.createElement('p');p.className='muted';p.textContent='Серверы остановлены.';box.append(p);return;}
  const grid=document.createElement('div');grid.className='live-grid';
  const values=[['Движок',engineName(activity.active?.backend||running?.name)||'—'],['Выбранная модель',activity.active?.model||running?.selected_model||running?.loaded_model||'Не выбрана'],['Состояние',activity.active?(activity.active.state==='queued'?'Запрос в очереди':'Запрос выполняется')+(activity.queued?` · в очереди: ${activity.queued}`:''):running?.sleeping?'Модель спит':running?.api==='Ready'?'Ожидает запрос':'Запускается'],['Input',fmt(r?.input_tokens)],['Output',fmt(r?.output_tokens)],['Всего в запросе',fmt(r?.total_tokens)]];
  values.forEach(([label,value])=>{const item=document.createElement('div');item.className='live-item';const caption=document.createElement('span');caption.className='card-label';caption.textContent=label;const strong=document.createElement('strong');strong.textContent=value;item.append(caption,strong);grid.append(item);});box.append(grid);
  if(r){const p=document.createElement('p');p.className='muted footnote';const session=activity.sessions?.find(s=>s.id===r.session_id);p.textContent=`${activity.active?'Текущий':'Последний'} запрос · сеанс ${r.session_id.slice(0,8)}${session?' · завершено запросов: '+session.requests+' · токенов: '+fmt(session.total_tokens):''}`;box.append(p);}
  const limit=activity.active?.context_limit||running?.active_context||Number(running?.settings?.context)||r?.context_limit||0;
  const used=r?.context_used;const context=document.createElement('div');context.className='live-context';const caption=document.createElement('p');caption.className='muted';caption.textContent=`Контекст запроса: ${fmt(used)} / ${fmt(limit)} · осталось ${used===null||used===undefined?'—':fmt(Math.max(0,limit-used))}`;const bar=document.createElement('progress');bar.max=Math.max(1,limit);bar.value=used||0;context.append(caption,bar);box.append(context);
  if(activity.active?.prompt_total){const p=document.createElement('p');p.className='muted';p.textContent=`Обработка входа: ${fmt(activity.active.prompt_processed)} / ${fmt(activity.active.prompt_total)} токенов`;box.append(p);}
}
function renderSettings() {
  const b=backend();if(!b)return; const s=b.settings||{};
  $('model-service').textContent=badgeText(b);
  const models=$('model-choice'); const old=models.value; models.replaceChildren(); (b.models||[]).forEach(m=>models.add(new Option(`${m.name} · ${m.quant?m.quant+' · ':''}${m.parts>1?`частей: ${m.parts} · `:''}${(m.bytes/1024**3).toFixed(1)} ГБ`,m.id)));
  if(b.type==='prismml'&&s.model)models.value=s.model; else if((b.models||[]).some(m=>m.id===old))models.value=old;
  models.disabled=b.type==='ollama'&&!(b.models||[]).length;
  $('model-hint').textContent=b.type==='prismml'?'GGUF из каталога модели на ПК.':'Для списка моделей Ollama должна быть запущена.';
  $('placement').querySelectorAll('option').forEach(o=>o.hidden=b.type==='ollama'&&['gpu','hybrid'].includes(o.value));
  $('placement-hint').textContent=b.type==='ollama'?'Ollama: RAM/CPU или автоматическое распределение. Полностью VRAM гарантировать нельзя.':'VRAM: попытка загрузить все слои на GPU; RAM + VRAM: число слоёв задаётся ниже. Для MoE-моделей больше VRAM выберите VRAM и задайте «Слои MoE в RAM». При нехватке памяти запуск откатится.';
  ensureProjectorOption(s.mmproj);Object.entries(fields).forEach(([key,id])=>$(id).value=s[key]??'');aliasModel=$('model-choice').value;renderProjectorHint();
  document.querySelectorAll('.prism-field').forEach(el=>el.hidden=b.type!=='prismml');applySettingsMode();syncKvOffload();
  $('settings-status').textContent=b.pending?'Есть сохранённые настройки. Они ещё не применены к работающему серверу.':b.type==='ollama'?'Серверные значения по умолчанию. Параметры внешнего клиента могут их переопределить.':'При ошибке запуска с новыми настройками выполняется откат.';
  $('restore').disabled=!b.can_restore;
  renderPresets(b);
  $('auto-restart').checked=!!b.auto_restart;$('restart-status').textContent=restartText(b);
  $('loaded-model').textContent=b.sleeping?'Модель спит; новый запрос может её загрузить.':b.loaded_model==='-'||b.loaded_model==='(none)'?'Модель не загружена.':b.loaded_model;
  $('load-model').disabled=!(b.models||[]).length;$('unload-model').disabled=b.service!=='Running';$('test-model').disabled=b.service!=='Running';
  const prism=b.type==='prismml';$('pull-label').hidden=prism;['url-label','filename-label','sha-label'].forEach(id=>$(id).hidden=!prism);
  renderModelDisk();
  $('download-hint').textContent=prism?'Скачайте GGUF по прямой ссылке Hugging Face. Формат Bonsai PQ2 поддерживает только PrismML.':'Укажите имя и тег из библиотеки Ollama. Сначала запустите сервер Ollama.';
}
// The API name follows the model file unless the owner typed their own (same rule as the server).
function autoAlias(path){const stem=String(path||'').split(/[\\/]/).pop().replace(/\.gguf$/i,'');let s=stem.toLowerCase().replace(/[^a-z0-9._-]/g,'-').replace(/-+/g,'-').replace(/^[-._]+|[-._]+$/g,'');if(s.length>80)s=s.slice(0,80).replace(/[-._]+$/,'');return s||'model';}
let aliasModel='';
function followModelAlias(){const model=$('model-choice').value;const alias=$('alias').value.trim();if(!alias||alias===autoAlias(aliasModel))$('alias').value=autoAlias(model);aliasModel=model;}
function presetText(p){if(p.auto)return 'под модель и видеокарту: Flash Attention, KV-кеш q8_0, контекст до 128k в VRAM';return Object.entries(p.settings||{}).map(([k,v])=>`${k}=${v}`).join(', ');}
function renderPresets(b){
  const list=b.presets||[];const select=$('preset-choice');const old=select.value;select.replaceChildren();
  list.forEach(p=>select.add(new Option(`${p.name}${p.builtin?'':' · свой'} — ${presetText(p)}`,p.name)));
  if(list.some(p=>p.name===old))select.value=old;
  const current=list.find(p=>p.name===select.value);$('preset-delete').disabled=!current||current.builtin;$('preset-apply').disabled=!list.length;
}
// The estimate reads only the GGUF header and is asked for by the owner, never by a timer.
let lastEstimate=null;
async function estimatePlacement(){
  const q=new URLSearchParams({model:$('model-choice').value,context:$('context').value.trim(),cache_key:$('cache-key').value,cache_value:$('cache-value').value,mmproj:$('mmproj').value||'auto'});
  $('estimate').textContent='Оценка…';$('estimate-apply').hidden=true;
  try{lastEstimate=await api(`/api/estimate?${q}`);$('estimate').textContent=lastEstimate.text;$('estimate-apply').hidden=lastEstimate.placement!=='hybrid';}
  catch(e){lastEstimate=null;$('estimate').textContent=e.message;}
  renderFit();
}
// Settings that put everything on the GPU but do not fit spill into shared memory: Windows keeps
// them working, many times slower. Checked when the owner opens or edits the form.
function renderFit(){const b=backend();const gpu=['gpu','auto'].includes($('placement').value);
  const over=b?.type==='prismml'&&gpu&&lastEstimate&&lastEstimate.placement!=='gpu';
  $('fit-warning').hidden=!over;if(over)$('fit-text').textContent=`Эти настройки не помещаются в ${(lastEstimate.vram_total/1024**3).toFixed(1)} ГБ VRAM: веса ${(lastEstimate.weights_bytes/1024**3).toFixed(1)} ГБ + KV-кеш ${(lastEstimate.kv_cache_bytes/1024**3).toFixed(1)} ГБ. Видеодрайвер вынесет часть в RAM, и генерация замедлится в разы. Пресет «Базовый» подберёт контекст и кеш под эту модель.`;}
let fitTimer=null;
function checkFit(){clearTimeout(fitTimer);const b=backend();if(b?.type!=='prismml'||!$('model-choice').value||activeTab!=='models'){$('fit-warning').hidden=true;return;}
  fitTimer=setTimeout(()=>estimatePlacement().catch(()=>{}),300);}
function applyEstimate(){if(!lastEstimate)return;$('placement').value='hybrid';$('gpu-layers').value=lastEstimate.gpu_layers;syncKvOffload();notice(`Подставлено: ${lastEstimate.gpu_layers} слоёв на GPU. Сохраните или примените настройки.`);}
function restartText(b){
  const rule='Не больше 2 раз за 10 минут и не при нехватке памяти. Остановка из панели или ai stop не отменяется.';
  const parts=[];if(b.restarts)parts.push(`Автоперезапусков после запуска: ${b.restarts}.`);
  if(b.last_failure)parts.push(`Последний сбой${b.failed_at?' '+new Date(b.failed_at).toLocaleString('ru-RU'):''}: ${b.last_failure}.`);
  if(b.gave_up)parts.push(`Перезапуск прекращён: ${b.gave_up}. Запустите движок вручную после исправления.`);
  return parts.length?parts.join(' '):rule;
}
// Basic mode keeps the everyday fields; advanced adds GPU, cache and sampling parameters.
let settingsMode='basic';try{if(localStorage.getItem('omnibox-settings-mode')==='pro')settingsMode='pro';}catch{}
function applySettingsMode(){const b=backend();const prism=b?.type==='prismml';const pro=prism&&settingsMode==='pro';
  $('settings-mode').hidden=!prism;['basic','pro'].forEach(m=>{const btn=$(`mode-${m}`);btn.classList.toggle('active',settingsMode===m);btn.setAttribute('aria-pressed',String(settingsMode===m));});
  $('advanced').hidden=!pro;$('generation').hidden=!pro;$('mode-hint').hidden=!prism||pro;if(pro){$('advanced').open=true;$('generation').open=true;}}
function setSettingsMode(mode){settingsMode=mode;try{localStorage.setItem('omnibox-settings-mode',mode);}catch{}applySettingsMode();}
// CPU placement keeps the KV cache in RAM; leaving it restores GPU offload.
function syncKvOffload(fromPlacement){const cpu=$('placement').value==='cpu';if(cpu)$('kv-offload').value='off';else if(fromPlacement)$('kv-offload').value='on';$('kv-offload').disabled=cpu;}
function collectSettings() {
  const b=backend(), settings={context:$('context').value.trim(),idle_minutes:$('idle').value,placement:$('placement').value};
  if(b.type==='prismml'){for(const [key,id] of Object.entries(fields))settings[key]=$(id).value.trim(); if($('model-choice').value)settings.model=$('model-choice').value;}
  return settings;
}
async function action(body) { const revision=formRevision;const key=body.backend||'*';pendingActions.add(key);renderEngines();try{const op=await api('/api/actions',body);if(body.settings||body.action==='restore')submittedForms.set(op.id,revision);notice('Операция принята. Результат появится в обзоре.');await refresh(false);schedulePoll();return op;}finally{pendingActions.delete(key);renderEngines();} }
function copyRow(label,value) {
  const row=document.createElement('div');row.className='connect-row'; const caption=document.createElement('span');caption.className='card-label';caption.textContent=label;
  const field=document.createElement('div');field.className='copy-field';const code=document.createElement('code');code.textContent=value; const button=document.createElement('button');button.textContent='Копировать';button.type='button';
  button.addEventListener('click',async()=>{try{if(navigator.clipboard&&window.isSecureContext)await navigator.clipboard.writeText(value);else{const input=document.createElement('textarea');input.value=value;document.body.append(input);input.select();if(!document.execCommand('copy'))throw new Error('Выделите строку и скопируйте вручную.');input.remove();}button.textContent='Скопировано';setTimeout(()=>button.textContent='Копировать',1500);}catch(e){notice(e.message,true);}});
  field.append(code,button);row.append(caption,field);return row;
}
function renderGatewayForm() {
  if(!snapshot||!gateway)return;
  $('api-queue-depth').value=gateway.queue_depth;$('api-queue-timeout').value=gateway.queue_timeout_seconds;
  $('api-base-url').value=gateway.base_url;$('api-require-key').checked=gateway.require_key;
  $('api-public-origin').value=gateway.public_origin||'';$('api-trusted-proxies').value=(gateway.trusted_proxies||[]).join(', ');
  $('api-state').textContent=!gateway.enabled?'Сетевой API не включён. Повторите установщик и выберите OpenAI API.':`API доступен на порту панели ${panelPort} по пути /v1 и обслуживает запущенный движок. ${gateway.require_key?'Используйте сохранённый ключ или сгенерируйте новый.':'Авторизация отключена: ключ не нужен.'}`;
  $('save-api').disabled=!gateway.enabled;$('generate-api-key').disabled=!gateway.enabled;$('add-api-key').disabled=!gateway.enabled;
  renderKeys();
  $('api-no-key-warning').hidden=!gateway.enabled||gateway.require_key;
}
function renderKeys(){
  const when=t=>t?new Date(t).toLocaleString('ru-RU'):'—';
  $('api-key-list').replaceChildren(...(gateway.keys||[]).map(k=>{const tr=document.createElement('tr');
    [k.name,when(k.created),when(k.last_used),fmt(k.requests),fmt(k.tokens)].forEach(v=>{const td=document.createElement('td');td.textContent=v;tr.append(td);});
    const td=document.createElement('td');const b=document.createElement('button');b.type='button';b.className='text-button';b.textContent='Отозвать';
    b.addEventListener('click',()=>{if(!window.confirm(`Отозвать ключ «${k.name}»? Клиент с ним сразу потеряет доступ.`))return;saveGateway({action:'revoke',id:k.id}).catch(e=>notice(e.message,true));});
    td.append(b);tr.append(td);return tr;}));
}
function renderPower(){
  const s=snapshot?.system;if(!s)return;
  $('autostart').checked=!!s.autostart;$('start-engine').value=s.start_engine||'';
  [...$('start-engine').querySelectorAll('option')].forEach(o=>o.disabled=!!o.value&&!snapshot.backends.some(b=>b.name===o.value));
}
function renderWake(){
  $('wake-adapters').replaceChildren(...(snapshot?.network||[]).map(a=>copyRow(`MAC для Wake-on-LAN · ${a.name} · ${a.ipv4}`,a.mac)));
}
function renderConnections() {
  if(!snapshot||!gateway)return;$('connections').replaceChildren();if(!gateway.enabled)return;
  // /v1 serves whichever engine runs; only one runs at a time.
  const b=snapshot.backends.find(b=>b.service==='Running')||snapshot.backends.find(b=>b.name===snapshot.default_backend)||snapshot.backends[0];if(!b)return;
  const card=document.createElement('article');card.className='connect-card';const heading=document.createElement('h2');heading.textContent=`OpenAI API · ${engineName(b.name)}`;card.append(heading);
  const endpointRow=copyRow('OpenAI Base URL — вставьте в Jan или Pi',gateway.base_url);endpointRow.classList.add('endpoint-primary');card.append(endpointRow);
  const help=document.createElement('p');help.className='muted';help.textContent=`${badgeText(b)}. API обслуживает запущенный движок; запрос к остановленному серверу его не включает.`;card.append(help);
  const model=b.selected_model||(b.type==='prismml'?b.settings?.alias:'')||'';
  card.append(copyRow('Model / имя модели',model||'Выберите модель и нажмите Загрузить'));
  if(!gateway.require_key)card.append(copyRow('API key — отключён; local только если клиент требует значение','local'));
  else{const p=document.createElement('p');p.className='muted';p.textContent='API key: используйте ключ из Bitwarden. При утрате сгенерируйте новый выше. Пароль панели сюда не подходит.';card.append(p);}
  const details=document.createElement('details');const summary=document.createElement('summary');summary.textContent='Технические адреса';details.append(summary,copyRow('POST endpoint',`${gateway.base_url}/chat/completions`),copyRow('Локальный API выбранного движка на ПК',b.endpoint));card.append(details);$('connections').append(card);
}
async function saveGateway(body) {
  const data=await api('/api/gateway',body);gateway=data.gateway;gatewayDirty=false;renderGatewayForm();renderConnections();
  if(data.key){$('new-api-key').hidden=false;$('key-copy').replaceChildren(copyRow('API key — скопируйте и сохраните сейчас',data.key));}
  else if(!gateway.require_key){$('new-api-key').hidden=true;$('key-copy').replaceChildren();}
  notice(body.action==='add'?'Ключ создан. Скопируйте его сейчас: он больше не будет показан.':body.action==='generate'?'Новый ключ сгенерирован. Все прежние ключи отозваны.':body.action==='revoke'?'Ключ отозван.':'Настройки API сохранены. Движок автоматически не запускается.');
}
let catalogBusy=false;
async function catalogRequest(mode,value) {
  if(catalogBusy)throw new Error('Дождитесь текущего поиска.');catalogBusy=true;$('catalog-search-button').disabled=true;$('catalog-status').textContent='Получаем данные Hugging Face…';
  try{const data=await api(`/api/catalog?${mode==='search'?'q':'repo'}=${encodeURIComponent(value)}`);$('catalog-status').textContent='';return data;}
  finally{catalogBusy=false;$('catalog-search-button').disabled=false;}
}
// Capabilities as small badges, like on openrouter.ai.
// Capabilities are icons with a tooltip on hover or focus, the same everywhere in the panel.
const capabilityLabels={reasoning:'Рассуждения',tools:'Инструменты',vision:'Зрение',moe:'MoE',context:'Контекст'};
const capabilityHints={reasoning:'Рассуждения: модель умеет думать перед ответом (thinking) — точнее, но дольше.',tools:'Инструменты: вызов функций (function calling) — для агентов и Open WebUI tools.',vision:'Зрение: принимает изображения; нужен проектор mmproj этой модели.',moe:'MoE (Mixture of Experts): на каждый токен работает часть весов — быстро даже частично в RAM.',context:'Обученный контекст: сколько токенов модель держит в одном разговоре.'};
const capabilityIcons={reasoning:'<path d="M9 18h6M10 21h4M12 3a6 6 0 0 0-3.5 10.9c.6.5 1 1.2 1 2.1h5c0-.9.4-1.6 1-2.1A6 6 0 0 0 12 3z"/>',tools:'<path d="M14.7 6.3a4 4 0 0 0-5.4 5.4L3 18l3 3 6.3-6.3a4 4 0 0 0 5.4-5.4l-2.5 2.5-2.8-.7-.7-2.8z"/>',vision:'<path d="M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7S2 12 2 12z"/><circle cx="12" cy="12" r="3"/>',moe:'<rect x="3" y="3" width="7" height="7" rx="1.5"/><rect x="14" y="3" width="7" height="7" rx="1.5"/><rect x="3" y="14" width="7" height="7" rx="1.5"/><rect x="14" y="14" width="7" height="7" rx="1.5"/>'};
function capabilityBadge(c,text){const b=document.createElement('span');b.className=`capability ${c}`;b.tabIndex=0;b.setAttribute('role','img');b.setAttribute('aria-label',capabilityHints[c]||c);b.dataset.tip=capabilityHints[c]||c;
  if(capabilityIcons[c])b.innerHTML=`<svg class="icon" viewBox="0 0 24 24" aria-hidden="true">${capabilityIcons[c]}</svg>`;else b.textContent=text||capabilityLabels[c]||c;return b;}
function capabilityBadges(list,context){const row=document.createElement('div');row.className='capabilities';(list||[]).forEach(c=>row.append(capabilityBadge(c)));
  if(context)row.append(capabilityBadge('context',context>=1024?`${Math.round(context/1024)}K`:String(context)));return row;}
function contextText(n){return n>=1024?`${Math.round(n/1024)}K контекст`:n?`${n} контекст`:'';}
// Vision projectors (mmproj) of the models directory, from the installed models listing.
let projectors=[],installedModels=[];
function ensureProjectorOption(value){const select=$('mmproj');if(!value||value==='auto'||value==='off')return;if(![...(select.options||[])].some(o=>o.value===value))select.add(new Option(value.split(/[\\/]/).pop(),value));}
function renderProjectorOptions(){const select=$('mmproj');const keep=select.value;const fixed=[...(select.options||[])].filter(o=>o.value==='auto'||o.value==='off');select.replaceChildren(...fixed);
  projectors.forEach(p=>select.add(new Option(`${p.name} · ${formatGb(p.bytes)}`,p.id)));ensureProjectorOption(keep);select.value=keep||'auto';renderProjectorHint();}
function renderProjectorHint(){const m=installedModels.find(x=>x.id===$('model-choice').value);const v=$('mmproj').value;
  $('mmproj-hint').textContent=v==='off'?'Изображения не принимаются.':v!=='auto'?'Изображения принимаются с выбранным проектором.':m?.projector?`Найден проектор ${m.projector.split(/[\\/]/).pop()}: модель принимает изображения.`:'Проектор этой модели не найден: изображения не принимаются. Скачайте mmproj из её репозитория в «Добавить модель».';}
let installedBusy=false;
async function refreshInstalled(){const b=backend();if(!b||installedBusy)return;installedBusy=true;$('installed-status').textContent='Читаем модели…';
  try{const data=await api(`/api/models?backend=${encodeURIComponent(b.name)}`);installedModels=data.models||[];projectors=data.projectors||[];renderInstalled(installedModels);if(b.type==='prismml')renderProjectorOptions();}
  catch(e){$('installed-models').replaceChildren();$('installed-status').textContent=e.message;}
  finally{installedBusy=false;}}
function renderInstalled(models){const b=backend();$('installed-models').replaceChildren();
  const total=models.reduce((n,m)=>n+(m.bytes||0),0);$('installed-status').textContent=models.length?`Моделей: ${models.length} · ${formatGb(total)}`:'Моделей пока нет. Добавьте модель ниже.';
  models.forEach(m=>{const row=document.createElement('article');row.className=`installed-model${m.selected?' selected':''}`;
    const head=document.createElement('div');head.className='installed-head';const name=document.createElement('strong');name.textContent=m.name;head.append(name);
    if(m.selected||m.in_use){const tag=document.createElement('span');tag.className='badge';tag.textContent=m.selected?'Выбрана':'Нужна для отката';head.append(tag);}
    const meta=document.createElement('span');meta.className='muted installed-meta';meta.textContent=[m.params,m.quant,formatGb(m.bytes),m.parts>1?`частей: ${m.parts}`:''].filter(Boolean).join(' · ');
    const actions=document.createElement('div');actions.className='button-row installed-actions';
    if(!m.selected){const pick=document.createElement('button');pick.type='button';pick.className='secondary small';pick.textContent='Выбрать';pick.addEventListener('click',()=>pickModel(m));actions.append(pick);}
    if(m.source){const link=sourceLink(m.source_search?'Найти на Hugging Face ↗':m.source.includes('ollama.com')?'Ollama ↗':'Hugging Face ↗',m.source);link.className='text-button small';actions.append(link);}
    const del=document.createElement('button');del.type='button';del.className='text-button small danger-text';del.textContent='Удалить';del.disabled=!!m.in_use;
    if(m.in_use)del.title='Модель выбрана в настройках или нужна для отката: сначала выберите и сохраните другую.';
    del.addEventListener('click',()=>{if(!window.confirm(b.type==='prismml'?`Удалить модель ${m.name}? Файл будет удалён с диска, освободится ${formatGb(m.bytes)}.`:`Удалить модель ${m.name} из Ollama?`))return;
      action({action:'model-delete',backend:selectedBackend,model:m.id}).catch(e=>notice(e.message,true));});actions.append(del);
    row.append(head,meta,capabilityBadges(m.capabilities,m.context),actions);$('installed-models').append(row);});}
// Choosing a model fills the settings form; saving or applying stays the owner's action.
function pickModel(m){const select=$('model-choice');if(![...(select.options||select.children||[])].some(o=>o.value===m.id))select.add(new Option(m.name,m.id));select.value=m.id;
  if(backend()?.type==='prismml')followModelAlias();settingsDirty=true;formRevision++;checkFit();
  notice(backend()?.type==='prismml'?`Выбрана ${m.name}. Нажмите «Сохранить» или «Применить и запустить».`:`Выбрана ${m.name}. Нажмите «Загрузить».`);}
function sourceLink(label,url) {const a=document.createElement('a');a.textContent=label;a.href=url;a.target='_blank';a.rel='noopener noreferrer';return a;}
async function searchCatalog(query) {
  try{const data=await catalogRequest('search',query);$('catalog-results').replaceChildren();if(!data.models.length)$('catalog-status').textContent='Модели не найдены. Измените запрос или откройте каталог источника.';
  data.models.forEach(m=>{const card=document.createElement('article');card.className='catalog-card';const h=document.createElement('h3');h.textContent=m.name;const p=document.createElement('p');p.className='muted';p.textContent=`Скачиваний: ${Number(m.downloads).toLocaleString('ru-RU')}${m.gated?' · требуется доступ автора':''}`;const button=document.createElement('button');button.className='secondary small';button.textContent='Выбрать GGUF';button.addEventListener('click',()=>showCatalogFiles(m.id));card.append(h,p,capabilityBadges(m.capabilities),sourceLink('Страница и описание ↗',m.url),button);$('catalog-results').append(card);});
  }catch(e){$('catalog-status').textContent=e.message;notice(e.message,true);}
}
async function showCatalogFiles(repository) {
  try{const data=await catalogRequest('files',repository);$('catalog-results').replaceChildren();const repoHead=document.createElement('div');repoHead.className='repo-head';repoHead.append(sourceLink(`${repository} · описание и лицензия ↗`,data.url),capabilityBadges(data.capabilities,data.context));$('catalog-results').append(repoHead);if(!data.files.length)$('catalog-status').textContent='Подходящие GGUF не найдены.';
  data.files.forEach(f=>{const row=document.createElement('article');row.className='catalog-file';const title=document.createElement('strong');title.textContent=f.name;const meta=document.createElement('span');meta.className='muted';meta.textContent=`${f.projector?'Проектор изображений (mmproj) — даёт модели этого репозитория зрение · ':''}${f.bytes?(f.bytes/1024**3).toFixed(2)+' ГБ':'Размер не указан'}${f.prism_only?' · нужен PrismML':''}${f.split?' · многотомный файл':''}`;const button=document.createElement('button');button.className='secondary small';button.textContent=f.projector?'Скачать проектор':'Скачать на ПК';const b=backend();const tag=`hf.co/${repository}:${f.name}`;button.disabled=f.split||(b.type==='ollama'&&(f.prism_only||f.projector||tag.length>200));button.addEventListener('click',()=>{const b=backend();if(!b||f.split||(b.type==='ollama'&&(f.prism_only||f.projector||tag.length>200))){notice('Этот файл не поддерживается выбранным движком.',true);return;}const body=b.type==='prismml'?{action:'download',backend:selectedBackend,url:f.url,filename:f.filename,sha256:f.sha256,size:f.bytes?String(f.bytes):''}:{action:'pull',backend:selectedBackend,model:tag};action(body).catch(e=>notice(e.message,true));});const fit=document.createElement('span');fit.className=`fit fit-${f.fit?.level||'none'}`;fit.textContent=f.fit?.text||'';fit.hidden=!f.fit;row.append(title,meta,fit,button);$('catalog-results').append(row);});
  }catch(e){$('catalog-status').textContent=e.message;notice(e.message,true);}
}
// Engine versions come from GitHub only when the owner asks; installs run as panel operations.
let engineReleases={};
function engineStatusText(e){
  if(!e.installed)return 'Не установлен';
  if(e.state==='Missing')return 'Служба не найдена';
  return e.version?`Версия ${e.version}`:'Установлен вне OmniBox';
}
function renderEngineManager(engines){
  const root=$('engine-manager');root.replaceChildren();
  engines.forEach(e=>{
    const box=document.createElement('div');box.className='engine-manage';box.dataset.engine=e.name;
    const head=document.createElement('div');head.className='engine-manage-head';const title=document.createElement('h3');title.textContent=e.label;const badge=document.createElement('span');badge.className='badge';badge.textContent=engineStatusText(e);head.append(title,badge);
    const info=document.createElement('p');info.className='muted';info.textContent=e.installed?(e.managed?`Служба omnibox-${e.name}. Скачанные версии: ${(e.versions||[]).join(', ')||'—'}.${e.pending?' Новая версия применится при следующем запуске.':''}`:`Служба omnibox-${e.name}. Движок установлен вне OmniBox; обновление перенесёт его в каталог OmniBox, прежние файлы останутся на месте.`):'Будут скачаны файлы с GitHub и создана служба с ручным запуском. Модели скачиваются отдельно.';
    const row=document.createElement('div');row.className='button-row';
    const select=document.createElement('select');select.className='engine-version';select.setAttribute('aria-label',`Версия ${e.label}`);
    const releases=engineReleases[e.name]||[];select.hidden=!releases.length;
    releases.forEach(r=>select.add(new Option(`${r.tag} · ${String(r.published).slice(0,10)}${r.size?` · ${(r.size/1024**3).toFixed(1)} ГБ`:''}${r.prerelease?' · предварительная':''}${r.tag===e.version?' · установлена':''}`,r.tag)));
    const button=(text,cls,handler)=>{const b=document.createElement('button');b.type='button';b.className=cls;b.textContent=text;b.addEventListener('click',()=>handler().catch(err=>notice(err.message,true)));return b;};
    const versions=button(releases.length?'Обновить список':'Показать версии','secondary small',()=>loadEngineReleases(e.name));
    const latest=button(e.installed?'Обновить до последней':'Установить последнюю','primary small',()=>engineAction(e,''));
    const chosen=button('Установить выбранную','secondary small',()=>engineAction(e,select.value));chosen.hidden=!releases.length;
    row.append(select,chosen,versions,latest);
    if(e.installed)row.append(button('Удалить','text-button danger-text',()=>removeEngine(e)));
    box.append(head,info,row);root.append(box);
  });
}
async function refreshEngines(){const data=await api('/api/engines');renderEngineManager(data.engines);return data.engines;}
async function loadEngineReleases(cli){
  notice('Загружаем список версий с GitHub…');
  const r=await api(`/api/engine-releases?engine=${encodeURIComponent(cli)}`);
  engineReleases[cli]=r.releases||[];await refreshEngines();notice(engineReleases[cli].length?'Версии загружены.':'Подходящих сборок для этой системы не найдено.');
}
async function engineAction(e,version){
  const target=version||'последней версии';
  if(e.installed&&!window.confirm(`Перевести ${e.label} на ${target}? Работающий движок перезапустится; при ошибке вернётся прежняя версия.`))return;
  if(!e.installed&&!window.confirm(`Установить ${e.label} (${target})? Будут скачаны файлы с GitHub и создана служба.`))return;
  await action(e.installed?{action:'engine-update',engine:e.name,version}:{action:'engine-install',engine:e.name,version});
}
async function removeEngine(e){
  if(!window.confirm(`Удалить ${e.label}? Служба и скачанные OmniBox файлы движка будут удалены, модели останутся на диске.`))return;
  await action({action:'engine-remove',engine:e.name});
}
// Disk usage is computed only on login and on the Logs tab, never on the status poll.
let storageDirty=false,requestLog='metadata';
// Request text is stored only in content mode; metadata rows offer nothing to open.
function contentCell(r){const cell=document.createElement('td');if(requestLog!=='content'){cell.textContent='—';return cell;}const button=document.createElement('button');button.type='button';button.className='text-button small';button.textContent='Открыть';button.addEventListener('click',()=>showRequestContent(r.id).catch(e=>notice(e.message,true)));cell.append(button);return cell;}
function formatRequestContent(c){
  const parts=[`${c.model} · ${c.route} · ${new Date(c.started).toLocaleString('ru-RU')} · ${c.status}${c.truncated?' · сокращено':''}`];
  const messages=c.request?.messages||c.request?.input;
  parts.push('ЗАПРОС',Array.isArray(messages)?messages.map(m=>`[${m.role||m.type||'?'}] ${typeof m.content==='string'?m.content:JSON.stringify(m.content,null,2)}`).join('\n\n'):JSON.stringify(c.request??null,null,2));
  if(c.reasoning_text)parts.push('РАЗМЫШЛЕНИЯ',c.reasoning_text);
  parts.push('ОТВЕТ',c.response!==undefined?JSON.stringify(c.response,null,2):(c.response_text||'—'));
  return parts.join('\n\n');
}
function renderContentResults(matches){
  const when=t=>new Date(t).toLocaleString('ru-RU');
  if(!matches.length){const p=document.createElement('p');p.className='muted';p.textContent='Ничего не найдено в сохранённом содержимом.';$('content-results').replaceChildren(p);return;}
  $('content-results').replaceChildren(...matches.map(m=>{const row=document.createElement('div');row.className='content-match';
    const head=document.createElement('p');head.className='muted';head.textContent=`${when(m.started)} · ${m.model} · сеанс ${m.session_id.slice(0,8)}`;
    const text=document.createElement('p');text.textContent=m.snippet;
    const open=document.createElement('button');open.type='button';open.className='text-button';open.textContent='Открыть';open.addEventListener('click',()=>showRequestContent(m.id).catch(e=>notice(e.message,true)));
    const exp=document.createElement('a');exp.className='text-button';exp.textContent='Экспорт сеанса';exp.href=`/api/request-content?session=${encodeURIComponent(m.session_id)}&export=1`;exp.setAttribute('download','');
    const buttons=document.createElement('div');buttons.className='button-row';buttons.append(open,exp);row.append(head,text,buttons);return row;}));
}
async function searchContent(){const q=$('content-query').value.trim();if(q.length<2){notice('Введите хотя бы 2 символа.',true);return;}
  const data=await api(`/api/request-content?q=${encodeURIComponent(q)}`);renderContentResults(data.matches||[]);}
async function showRequestContent(id){const c=await api(`/api/request-content?id=${encodeURIComponent(id)}`);$('request-content').textContent=formatRequestContent(c);$('request-content-box').hidden=false;}
function formatMb(bytes){return `${((bytes||0)/1024**2).toFixed(1)} МБ`;}
function formatGb(bytes){return (bytes||0)>=1024**4?`${(bytes/1024**4).toFixed(2)} ТБ`:`${((bytes||0)/1024**3).toFixed(1)} ГБ`;}
// Drives: used/total with the brand bar; a drive that stores models or OmniBox AI warns when nearly full.
function renderDrives(u){
  const models=(u.models||[]);
  $('storage-models').textContent=models.length?`Модели: ${models.map(m=>`${m.engine} ${formatGb(m.bytes)} (${m.models})`).join(' · ')}`:'Модели: —';
  const list=$('storage-drives');list.replaceChildren();
  (u.drives||[]).forEach(d=>{
    const item=document.createElement('div');item.className='drive'+(d.low?' low':'');
    const head=document.createElement('div');head.className='drive-head';
    const name=document.createElement('strong');name.textContent=`${d.name}${d.label?' · '+d.label:''}${d.kind==='removable'?' · съёмный':d.kind==='network'?' · сетевой':''}`;
    const numbers=document.createElement('span');numbers.textContent=`занято ${formatGb(d.used)} из ${formatGb(d.total)} · свободно ${formatGb(d.free)}`;
    head.append(name,numbers);
    const bar=document.createElement('progress');bar.max=100;bar.value=d.total?Math.round(d.used/d.total*100):0;bar.setAttribute('aria-label',`Занято на ${d.name}`);
    const roles=document.createElement('small');roles.className='muted';roles.textContent=[(d.roles||[]).join(', '),d.low?'мало свободного места':''].filter(Boolean).join(' · ');
    item.append(head,bar,roles);list.append(item);
  });
  diskUsage=u;renderModelDisk();
}
// Free space for the download form: the drive of the selected engine's model directory, not of any engine.
function renderModelDisk(){
  const place=(diskUsage?.models||[]).find(m=>m.name===selectedBackend);const drive=place&&(diskUsage.drives||[]).find(d=>d.name===place.drive);
  $('model-disk').hidden=!drive;if(drive)$('model-disk').textContent=`Свободно на диске моделей ${drive.name}: ${formatGb(drive.free)} · модели ${place.engine} занимают ${formatGb(place.bytes)}.`;
}
// Retention: preset values, or the owner's own number of hours or days.
function showRetention(hours,days){const select=$('log-retention');const preset=hours>0?`h${hours}`:days>0?`d${days}`:'0';
  const known=[...(select.options||[])].some(o=>o.value===preset);select.value=known?preset:'custom';
  if(!known){$('log-retention-value').value=hours||days;$('log-retention-unit').value=hours>0?'hours':'days';}
  $('log-retention-custom').hidden=select.value!=='custom';}
function retentionBody(){const v=$('log-retention').value;
  if(v==='custom'){const n=Number($('log-retention-value').value);return $('log-retention-unit').value==='hours'?{log_retention_hours:n}:{log_retention_days:n};}
  if(v.startsWith('h'))return {log_retention_hours:Number(v.slice(1))};return {log_retention_days:Number(v.replace('d',''))};}
function renderStorage(u){
  requestLog=u.request_log||'metadata';renderDrives(u);
  $('storage-summary').textContent=`Журналы: ${formatMb(u.logs_bytes)} (файлов: ${u.log_files}${u.content_bytes?`, содержимое запросов: ${formatMb(u.content_bytes)}`:''})${u.engines_bytes?` · движки: ${formatMb(u.engines_bytes)}`:''}${u.temp_bytes?` · временные файлы: ${formatMb(u.temp_bytes)}`:''}`;
  $('storage-warning').hidden=!u.logs_over_limit;$('storage-warning').textContent=`Журналы занимают больше ${u.log_warn_mb} МБ. Сократите срок хранения или удалите устаревшие.`;
  if(!storageDirty){showRetention(u.log_retention_hours||0,u.log_retention_days||0);$('log-warn').value=u.log_warn_mb;$('request-log').value=requestLog;}
  if(activity&&snapshot)renderActivity();
}
async function refreshStorage(notify){const u=await api('/api/storage');renderStorage(u);const low=(u.drives||[]).filter(d=>d.low);if(notify&&low.length)notice(`Мало места на диске ${low.map(d=>`${d.name} (свободно ${formatGb(d.free)})`).join(', ')}.`,true);if(notify&&u.logs_over_limit)notice(`Журналы занимают ${formatMb(u.logs_bytes)} — больше порога ${u.log_warn_mb} МБ. Настройка хранения — в разделе «Журналы».`,true);return u;}
async function refreshLogs() { if(!snapshot)return; const data=await api('/api/logs');$('logs').replaceChildren();data.logs.forEach(log=>{const h=document.createElement('h3');h.textContent=log.stream;const pre=document.createElement('pre');pre.textContent=log.text;$('logs').append(h,pre);}); }
$('login-form').addEventListener('submit',async e=>{e.preventDefault();$('login-error').textContent='';const button=e.currentTarget.querySelector('button');button.disabled=true;try{await api('/api/login',{password:$('password').value});$('password').value='';await showDashboard();}catch(error){$('login-error').textContent=error.message;}finally{button.disabled=false;}});
$('logout').addEventListener('click',()=>api('/api/logout',{}).finally(showLogin));
document.querySelectorAll('[data-tab]').forEach(b=>b.addEventListener('click',()=>switchTab(b.dataset.tab)));
$('kpi-unload').addEventListener('click',()=>{const b=snapshot?.backends.find(x=>x.name===$('kpi-unload').dataset.backend);if(!b)return;
  if(!window.confirm(b.type==='prismml'?`Выгрузить ${$('kpi-unload').dataset.model}? Сервер PrismML остановится и освободит память; новый запрос его не запустит.`:`Выгрузить ${$('kpi-unload').dataset.model} из Ollama? Сервер останется работать.`))return;
  action({action:'unload',backend:b.name,...(b.type==='ollama'?{model:$('kpi-unload').dataset.model}:{})}).catch(e=>notice(e.message,true));});
$('notifications-button').addEventListener('click',()=>toggleNotifications());
$('notifications-close').addEventListener('click',()=>toggleNotifications(false));
$('notifications-clear').addEventListener('click',()=>{noticeLog=[];unreadNotices=0;try{localStorage.setItem('omnibox-notices','[]');}catch{}renderNotifications();});
renderNotifications();
$('refresh').addEventListener('click',()=>refresh(true).catch(e=>notice(e.message,true)));
$('stop-all').addEventListener('click',()=>{
  // Stopping interrupts the current generation: ask first when something runs.
  const running=snapshot?.backends.some(b=>b.service==='Running'||b.service==='StartPending');
  if(running&&!window.confirm('Остановить все серверы? Текущая генерация будет прервана, модель выгрузится из памяти.'))return;
  action({action:'stopall'}).catch(e=>notice(e.message,true));
});
$('autostart').addEventListener('change',e=>action({action:'autostart',enabled:e.target.checked}).catch(err=>{e.target.checked=!e.target.checked;notice(err.message,true);}));
$('start-engine').addEventListener('change',e=>action({action:'start-engine',engine:e.target.value}).catch(err=>notice(err.message,true)));
$('shutdown-omnibox').addEventListener('click',()=>{
  if(!window.confirm('Выключить OmniBox полностью? Движки, панель и API остановятся; текущая генерация будет прервана. Включить снова — ai on на ПК или перезагрузка, если включён автозапуск.'))return;
  action({action:'shutdown'}).catch(e=>notice(e.message,true));
});
$('sleep-pc').addEventListener('click',()=>{
  if(!window.confirm('Перевести ПК в сон? Панель и API станут недоступны до пробуждения по Wake-on-LAN или кнопкой питания.'))return;
  action({action:'sleep'}).catch(e=>notice(e.message,true));
});
$('model-backend').addEventListener('change',e=>chooseBackend(e.target.value));
$('settings-form').addEventListener('input',()=>{settingsDirty=true;formRevision++;});$('settings-form').addEventListener('change',()=>{settingsDirty=true;formRevision++;});
$('settings-form').addEventListener('submit',e=>{e.preventDefault();action({action:'save',backend:selectedBackend,settings:collectSettings()}).catch(error=>notice(error.message,true));});
$('apply').addEventListener('click',()=>{if($('settings-form').reportValidity())action({action:'apply',backend:selectedBackend,settings:collectSettings()}).catch(e=>notice(e.message,true));});
$('estimate-button').addEventListener('click',()=>estimatePlacement());
$('estimate-apply').addEventListener('click',applyEstimate);
$('model-choice').addEventListener('change',()=>{followModelAlias();renderProjectorHint();});
$('mmproj').addEventListener('change',renderProjectorHint);
['model-choice','context','cache-key','cache-value','placement','mmproj'].forEach(id=>$(id).addEventListener('change',checkFit));
$('fit-base').addEventListener('click',()=>action({action:'preset-apply',backend:selectedBackend,model:'Базовый'}).then(()=>{$('fit-warning').hidden=true;}).catch(e=>notice(e.message,true)));
$('test-model').addEventListener('click',()=>action({action:'model-test'}).catch(e=>notice(e.message,true)));
$('preset-choice').addEventListener('change',()=>{const b=backend();if(b)renderPresets(b);});
$('preset-apply').addEventListener('click',()=>action({action:'preset-apply',backend:selectedBackend,model:$('preset-choice').value}).catch(e=>notice(e.message,true)));
$('preset-delete').addEventListener('click',()=>action({action:'preset-delete',backend:selectedBackend,model:$('preset-choice').value}).catch(e=>notice(e.message,true)));
$('preset-save').addEventListener('click',()=>{const name=$('preset-name').value.trim();if(!name){notice('Введите имя пресета.',true);return;}
  action({action:'preset-save',backend:selectedBackend,model:name}).then(()=>{$('preset-name').value='';}).catch(e=>notice(e.message,true));});
$('auto-restart').addEventListener('change',e=>action({action:'auto-restart',backend:selectedBackend,enabled:e.target.checked}).catch(err=>{e.target.checked=!e.target.checked;notice(err.message,true);}));
$('restore').addEventListener('click',()=>action({action:'restore',backend:selectedBackend}).catch(e=>notice(e.message,true)));
$('load-model').addEventListener('click',()=>{const b=backend();const body={action:'load',backend:selectedBackend};if(b.type==='prismml'){if(!$('settings-form').reportValidity())return;body.settings=collectSettings();}else {body.model=$('model-choice').value;if(settingsDirty){if(!$('settings-form').reportValidity())return;body.settings=collectSettings();}}action(body).catch(e=>notice(e.message,true));});
$('unload-model').addEventListener('click',()=>action({action:'unload',backend:selectedBackend,model:$('model-choice').value}).catch(e=>notice(e.message,true)));
$('download-form').addEventListener('submit',e=>{e.preventDefault();const b=backend();const body=b.type==='prismml'?{action:'download',backend:selectedBackend,url:$('download-url').value.trim(),filename:$('download-filename').value.trim(),sha256:$('download-sha').value.trim()}:{action:'pull',backend:selectedBackend,model:$('pull-name').value.trim()};action(body).catch(error=>notice(error.message,true));});
$('refresh-logs').addEventListener('click',()=>refreshLogs().catch(e=>notice(e.message,true)));
$('placement').addEventListener('change',()=>syncKvOffload(true));
$('storage-form').addEventListener('input',()=>storageDirty=true);
$('storage-form').addEventListener('submit',async e=>{e.preventDefault();if(requestLog==='content'&&$('request-log').value==='metadata'&&!window.confirm('Выключить запись содержимого? Уже сохранённые запросы и ответы будут удалены.'))return;try{const u=await api('/api/storage',{action:'save',...retentionBody(),log_warn_mb:Number($('log-warn').value),request_log:$('request-log').value});$('request-content-box').hidden=true;storageDirty=false;renderStorage(u);notice('Настройки хранения журналов сохранены.');}catch(error){notice(error.message,true);}});
$('content-search-form').addEventListener('submit',e=>{e.preventDefault();searchContent().catch(err=>notice(err.message,true));});
$('request-content-close').addEventListener('click',()=>{$('request-content-box').hidden=true;$('request-content').textContent='';});
$('log-retention').addEventListener('change',()=>{$('log-retention-custom').hidden=$('log-retention').value!=='custom';});
$('storage-purge').addEventListener('click',async()=>{if(!window.confirm('Удалить все журналы: движков, панели и запросов (с содержимым)? Файлы, которые сейчас пишет работающий движок, останутся до следующей ротации.'))return;
  try{const u=await api('/api/storage',{action:'purge'});renderStorage(u);notice(`Удалено файлов журналов: ${u.cleanup.removed}, освобождено ${formatMb(u.cleanup.freed_bytes)}.${u.cleanup.busy?` Заняты и остались: ${u.cleanup.busy}.`:''}`);}catch(error){notice(error.message,true);}});
$('storage-clean').addEventListener('click',async()=>{try{const u=await api('/api/storage',{action:'cleanup'});renderStorage(u);notice(`Удалено файлов: ${u.cleanup.removed}, освобождено ${formatMb(u.cleanup.freed_bytes)}.`);}catch(error){notice(error.message,true);}});
$('mode-basic').addEventListener('click',()=>setSettingsMode('basic'));$('mode-pro').addEventListener('click',()=>setSettingsMode('pro'));
$('api-settings-form').addEventListener('input',()=>gatewayDirty=true);
$('api-settings-form').addEventListener('submit',e=>{e.preventDefault();saveGateway({action:'save',base_url:$('api-base-url').value.trim(),require_key:$('api-require-key').checked,queue_depth:Number($('api-queue-depth').value),queue_timeout_seconds:Number($('api-queue-timeout').value),public_origin:$('api-public-origin').value.trim(),trusted_proxies:$('api-trusted-proxies').value.split(',').map(s=>s.trim()).filter(Boolean)}).catch(e=>notice(e.message,true));});
$('generate-api-key').addEventListener('click',()=>{if(!window.confirm('Заменить все ключи одним новым? Все клиенты потеряют доступ, пока не получат новый ключ.'))return;saveGateway({action:'generate'}).catch(e=>notice(e.message,true));});
$('add-api-key').addEventListener('click',()=>{const name=$('api-key-name').value.trim();if(!name){notice('Введите имя клиента для ключа.',true);return;}
  saveGateway({action:'add',name}).then(()=>{$('api-key-name').value='';}).catch(e=>notice(e.message,true));});
$('catalog-form').addEventListener('submit',e=>{e.preventDefault();searchCatalog($('catalog-query').value.trim());});
document.querySelectorAll('[data-repo]').forEach(button=>button.addEventListener('click',()=>showCatalogFiles(button.dataset.repo)));
$('installed-refresh').addEventListener('click',()=>refreshInstalled().catch(e=>notice(e.message,true)));
showDashboard().catch(()=>showLogin());

function idleText(b){
 if(b.service!=='Running')return 'Сервер остановлен';
 if(b.idle?.busy||activity?.active?.backend===b.name)return 'После завершения запроса';
 if(b.sleeping||b.idle?.state==='unloaded')return 'Модель выгружена';
 if(!b.idle?.expires_at)return 'Время неизвестно';
 const seconds=Math.max(0,Math.ceil((Date.parse(b.idle.expires_at)-Date.now())/1000));
 if(!seconds)return 'Ожидаем выгрузку…';
 return `${b.idle.estimated?'≈ ':''}${Math.floor(seconds/60)}:${String(seconds%60).padStart(2,'0')}`;
}
function residencyText(r){
 const label=({ram:'RAM · CPU',vram:'VRAM · GPU',hybrid:'RAM + VRAM'})[r?.mode]||'Нет данных';
 if(r?.gpu_layers!==null&&r?.gpu_layers!==undefined)return `${label} · GPU ${r.gpu_layers}/${r.total_layers} слоёв${r.cpu_moe_layers?` · эксперты ${Math.min(r.cpu_moe_layers,r.total_layers)} слоёв в RAM`:''}`;
 if(r?.vram_bytes!==null&&r?.vram_bytes!==undefined)return `${label} · VRAM ${(r.vram_bytes/1024**3).toFixed(1)} ГБ · RAM ${((r.ram_bytes||0)/1024**3).toFixed(1)} ГБ`;
 return label;
}
setInterval(()=>{if(loggedIn&&!document.hidden&&snapshot)document.querySelectorAll('.engine-idle').forEach(el=>{const b=snapshot.backends.find(b=>b.name===el.dataset.backend);if(b)el.textContent=idleText(b);});},1000);

// The program is installed and updated by the same one command; settings, password and key stay.
const updateCommands=[['Обычно — в терминале от имени администратора (Windows) или через sudo (Linux)','ai update'],['Если команда ai не работает: Windows (PowerShell от имени администратора)','gh release download --repo aafnx/omnibox-ai --pattern install.ps1 --dir $env:TEMP --clobber; powershell -NoProfile -ExecutionPolicy Bypass -File $env:TEMP\\install.ps1'],['Linux','gh release download --repo aafnx/omnibox-ai --pattern install.sh --dir /tmp --clobber && sudo sh /tmp/install.sh']];
function renderUpdateInfo(){
 $('update-version').textContent=snapshot?`Установлена версия ${snapshot.version}.`:'—';
 const last=snapshot?.last_update;$('last-update').hidden=!last;
 if(last)$('last-update').textContent=`Последнее обновление из панели${last.time?' '+new Date(last.time).toLocaleString('ru-RU'):''}: ${last.ok?'успешно':'не удалось'}${last.from?' ('+last.from+' → '+last.version+')':''}. ${last.message} Подробности — logs/update.log.`;
 $('update-commands').replaceChildren(...updateCommands.map(([label,command])=>copyRow(label,command)));
}
$('self-update-form').addEventListener('submit',e=>{e.preventDefault();
 // The token leaves the page at once: it is sent for this download and never kept.
 const token=$('update-token').value.trim(),version=$('update-target').value.trim();$('update-token').value='';
 action({action:'self-update',token,...(version?{version}:{})}).catch(err=>notice(err.message,true));});
$('password-form').addEventListener('submit',async e=>{
 e.preventDefault();const current=$('password-current').value,next=$('password-new').value,repeat=$('password-repeat').value;
 // The fields are cleared whatever the result: passwords never stay in the page.
 $('password-current').value='';$('password-new').value='';$('password-repeat').value='';
 if(next!==repeat){notice('Новый пароль и повтор не совпадают.',true);return;}
 try{await api('/api/password',{current,password:next});notice('Пароль панели изменён. Другие сеансы входа завершены.');}catch(error){notice(error.message,true);}
});

// The panel installs on a phone through HTTPS (the NAS address); plain HTTP in the LAN skips it.
if('serviceWorker' in navigator&&window.isSecureContext)navigator.serviceWorker.register('/sw.js').catch(()=>{});
