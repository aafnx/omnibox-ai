'use strict';
const $ = id => document.getElementById(id);
let csrf = '', snapshot = null, activeTab = 'overview', settingsDirty = false, selectedBackend = '', pollTimer = null, loggedIn = false;
let formRevision = 0;
const submittedForms = new Map();
let gateway = null, gatewayDirty = false, panelPort = 18090;
let activity=null, refreshBusy=false;
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
const fields = {placement:'placement',context:'context',idle_minutes:'idle',alias:'alias',gpu_layers:'gpu-layers',n_cpu_moe:'n-cpu-moe',flash_attention:'flash',cache_key:'cache-key',cache_value:'cache-value',batch:'batch',ubatch:'ubatch'};
const labels = {start:'Запуск',stop:'Остановка',restart:'Перезапуск',stopall:'Остановка всех серверов',save:'Сохранение',apply:'Применение',restore:'Восстановление',load:'Загрузка',unload:'Выгрузка',pull:'Скачивание',download:'Скачивание GGUF'};
async function api(path, body) {
  const response = await fetch(path, {method:body === undefined?'GET':'POST',credentials:'same-origin',headers:body === undefined?{}:{'Content-Type':'application/json','X-Omnibox-CSRF':csrf},body:body === undefined?undefined:JSON.stringify(body)});
  const data = await response.json();
  if (!response.ok) { if(response.status===401 && path!=='/api/login') showLogin(); throw new Error(data.error||`Ошибка ${response.status}`); }
  return data;
}
function notice(message, error=false) { $('notice').hidden=false; $('notice').textContent=message; $('notice').classList.toggle('error',error); }
function showLogin() { placeThemeSwitch(false); $('new-api-key').hidden=true;$('key-copy').replaceChildren();gatewayDirty=false;loggedIn=false; clearTimeout(pollTimer); $('login').hidden=false; $('dashboard').hidden=true; csrf=''; }
async function showDashboard() { const session=await api('/api/session'); csrf=session.csrf; panelPort=session.port; loggedIn=true; $('login').hidden=true; $('dashboard').hidden=false; placeThemeSwitch(true); await refresh(true); schedulePoll(); if(localStorage.getItem('omnibox-update-pending')==='1')switchTab('updates'); }
function monitoringActive(){return activity?.active||snapshot?.backends.some(b=>b.service==='StartPending'||(b.service==='Running'&&b.sleeping!==true&&(b.type==='prism'||!['(none)','-',undefined,null].includes(b.loaded_model))));}
function schedulePoll() { clearTimeout(pollTimer); if(!loggedIn)return; pollTimer=setTimeout(async()=>{ if(!document.hidden) { try { await refresh(false); } catch(e) { notice(e.message,true); } } schedulePoll(); },document.hidden?10000:(monitoringActive()||snapshot?.operations?.some(o=>o.state==='running')||pendingActions.size?3000:10000)); }
document.addEventListener('visibilitychange',()=>{if(!document.hidden&&loggedIn)refresh(false,true).catch(e=>notice(e.message,true));});
function backend() { return snapshot?.backends.find(b=>b.name===selectedBackend); }
function chooseBackend(name) { selectedBackend=name; $('model-backend').value=name; settingsDirty=false; formRevision++; $('catalog-results').replaceChildren(); $('catalog-status').textContent=''; renderSettings(); }
function switchTab(tab) { activeTab=tab; document.querySelectorAll('.tab').forEach(s=>s.hidden=s.id!==`tab-${tab}`); document.querySelectorAll('[data-tab]').forEach(b=>b.classList.toggle('active',b.dataset.tab===tab)); if(tab==='logs')refreshLogs().catch(e=>notice(e.message,true)); if(tab==='updates')refreshUpdateStatus().catch(e=>notice(e.message,true)); }
async function refresh(gpu,snapshotRequested=gpu) {
  if(refreshBusy)return;refreshBusy=true;
  try{
  const [data,apiConfig,stats]=await Promise.all([api(`/api/status${gpu?'?gpu=1':''}`),api('/api/gateway'),api('/api/activity')]);activity=stats; snapshot=data; $('version').textContent=`v${data.version}`; $('computer').textContent=data.computer;
  $('server-count').textContent=`${data.backends.filter(b=>b.service==='Running').length} / ${data.backends.length}`; renderModelSummary();
  if(gpu) { const g=data.gpu?.[0]; $('gpu-value').textContent=g?`${(g.used/1024).toFixed(1)} / ${(g.total/1024).toFixed(1)} ГБ`:'Нет данных'; $('gpu-bar').value=g?g.used/g.total*100:0; $('gpu-caption').textContent=g?`${g.name} · свободно ${(g.free/1024).toFixed(1)} ГБ`:'nvidia-smi недоступен.'; }
  renderEngines(); renderOperations(data.operations||[]);
  for(const id of ['model-backend']) { const select=$(id), old=select.value; select.replaceChildren(); data.backends.forEach(b=>select.add(new Option(b.name,b.name))); select.value=data.backends.some(b=>b.name===old)?old:data.default_backend; }
  if(!selectedBackend)selectedBackend=data.default_backend;
  $('model-backend').value=selectedBackend;
  if(!settingsDirty)renderSettings();
  gateway=apiConfig;if(!gatewayDirty)renderGatewayForm();if(!$('connections').contains(document.activeElement))renderConnections();
  renderActivity();
  if(monitoringActive())renderTelemetry(await api('/api/telemetry'));
  else if(snapshotRequested)await requestTelemetrySnapshot();
  else renderTelemetry({enabled:false,reason:idleTelemetryReason()});
  }finally{refreshBusy=false;}
}
// Third summary card: what is in memory now; one engine and one model at a time.
function renderModelSummary(){
  const running=snapshot.backends.find(b=>b.service==='Running'||b.service==='StartPending');
  const model=running&&!['-','(none)',undefined,null,''].includes(running.loaded_model)?running.loaded_model:'';
  $('kpi-model').textContent=model||(running?'Не загружена':'Нет');
  $('kpi-model-caption').textContent=running?`${running.name} · ${badgeText(running).toLowerCase()}`:'Серверы остановлены. Одновременно работает одна модель.';
}
function badgeText(b) { if(b.service==='Running') return b.sleeping?'Модель спит':b.api==='Ready'?'API готов':'API недоступен'; return ({Stopped:'Остановлен',Missing:'Не установлен',StartPending:'Запускается',StopPending:'Останавливается'})[b.service]||b.service; }
const unknownValues=new Set(['—','Нет данных','Время неизвестно','Сервер остановлен']);
function renderEngines() {
  $('engines').replaceChildren();
  snapshot.backends.forEach(b=>{
    const card=$('engine-template').content.firstElementChild.cloneNode(true);
    card.querySelector('h2').textContent=b.name==='bonsai'?'Bonsai / PrismML':b.name==='ollama'?'Ollama':b.name;
    card.querySelector('.engine-type').textContent=b.type==='prism'?'PrismML llama-server · GGUF':'Ollama · библиотека моделей';
    const badge=card.querySelector('.badge'); badge.textContent=badgeText(b); badge.classList.toggle('running',b.service==='Running'); badge.classList.toggle('sleeping',!!b.sleeping);
    card.querySelector('.engine-model').textContent=b.loaded_model==='-'||b.loaded_model==='(none)'?'Не загружена':b.loaded_model;
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
function renderOperations(operations) {
  $('operations').replaceChildren();
  let notified = false;
  if(!operations.length) { const p=document.createElement('p');p.className='muted';p.textContent='Операций пока нет.';$('operations').append(p); }
  operations.forEach(op=>{
    const row=document.createElement('div');row.className='operation';
    const icon=document.createElement('span');icon.className=`op-icon ${op.state}`;icon.textContent=op.state==='running'?'↻':op.state==='done'?'✓':'!';
    const title=document.createElement('span');title.textContent=`${labels[op.action]||op.action}${op.backend?' · '+op.backend:''}`;
    const message=document.createElement('span');message.className='operation-message';message.textContent=op.message;
    row.append(icon,title,message);if(op.state==='running'){const box=document.createElement('div');box.className='operation-progress';const progress=document.createElement('progress');const text=document.createElement('span');text.textContent=`${Math.floor(op.elapsed_seconds||0)} с · ${labels[op.action]||op.action}…`;box.append(progress,text);row.append(box);}$('operations').append(row);
    if(op.state!=='running'&&!completed.has(op.id)){completed.add(op.id);if(!notified){notice(op.message,op.state==='failed');notified=true;}if(op.state==='done'&&op.backend===selectedBackend&&submittedForms.get(op.id)===formRevision)settingsDirty=false;submittedForms.delete(op.id);}
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
  $('request-history').replaceChildren();(activity.requests||[]).forEach(r=>{$('request-history').append(tableRow([new Date(r.started).toLocaleString('ru-RU'),r.session_id.slice(0,8),r.model,`${r.state==='completed'?'Готово':'Ошибка'} · ${r.status}`,fmt(r.duration_seconds),fmt(r.input_tokens),fmt(r.output_tokens),fmt(r.total_tokens)]));});
  $('session-history').replaceChildren();(activity.sessions||[]).forEach(s=>{$('session-history').append(tableRow([s.id.slice(0,8),s.model,`${s.requests}${s.missing_usage?' · без usage: '+s.missing_usage:''}`,fmt(s.input_tokens),fmt(s.output_tokens),fmt(s.total_tokens)]));});
  $('journal-status').textContent=activity.journal_error||'Журнал содержит только метаданные запросов через /v1 OMNIBOX AI.';
  const running=snapshot.backends.find(b=>['Running','StartPending'].includes(b.service));
  const r=activity.active||(activity.requests||[]).find(x=>running&&x.backend===running.name&&(!running.selected_model||x.model===running.selected_model));
  const box=$('live-session');box.replaceChildren();
  if(!running&&!activity.active){const p=document.createElement('p');p.className='muted';p.textContent='Серверы остановлены.';box.append(p);return;}
  const grid=document.createElement('div');grid.className='live-grid';
  const values=[['Движок',activity.active?.backend||running?.name||'—'],['Выбранная модель',activity.active?.model||running?.selected_model||running?.loaded_model||'Не выбрана'],['Состояние',activity.active?'Запрос выполняется':running?.sleeping?'Модель спит':running?.api==='Ready'?'Ожидает запрос':'Запускается'],['Input',fmt(r?.input_tokens)],['Output',fmt(r?.output_tokens)],['Всего в запросе',fmt(r?.total_tokens)]];
  values.forEach(([label,value])=>{const item=document.createElement('div');item.className='live-item';const caption=document.createElement('span');caption.className='card-label';caption.textContent=label;const strong=document.createElement('strong');strong.textContent=value;item.append(caption,strong);grid.append(item);});box.append(grid);
  if(r){const p=document.createElement('p');p.className='muted footnote';const session=activity.sessions?.find(s=>s.id===r.session_id);p.textContent=`${activity.active?'Текущий':'Последний'} запрос · сеанс ${r.session_id.slice(0,8)}${session?' · завершено запросов: '+session.requests+' · токенов: '+fmt(session.total_tokens):''}`;box.append(p);}
  const limit=activity.active?.context_limit||running?.active_context||Number(running?.settings?.context)||r?.context_limit||0;
  const used=r?.context_used;const context=document.createElement('div');context.className='live-context';const caption=document.createElement('p');caption.className='muted';caption.textContent=`Контекст запроса: ${fmt(used)} / ${fmt(limit)} · осталось ${used===null||used===undefined?'—':fmt(Math.max(0,limit-used))}`;const bar=document.createElement('progress');bar.max=Math.max(1,limit);bar.value=used||0;context.append(caption,bar);box.append(context);
  if(activity.active?.prompt_total){const p=document.createElement('p');p.className='muted';p.textContent=`Обработка входа: ${fmt(activity.active.prompt_processed)} / ${fmt(activity.active.prompt_total)} токенов`;box.append(p);}
}
function renderSettings() {
  const b=backend();if(!b)return; const s=b.settings||{};
  $('model-service').textContent=badgeText(b);
  const models=$('model-choice'); const old=models.value; models.replaceChildren(); (b.models||[]).forEach(m=>models.add(new Option(`${m.name} · ${(m.bytes/1024**3).toFixed(1)} ГБ`,m.id)));
  if(b.type==='prism'&&s.model)models.value=s.model; else if((b.models||[]).some(m=>m.id===old))models.value=old;
  models.disabled=b.type==='ollama'&&!(b.models||[]).length;
  $('model-hint').textContent=b.type==='prism'?'GGUF из каталога модели на ПК.':'Для списка моделей Ollama должна быть запущена.';
  $('placement').querySelectorAll('option').forEach(o=>o.hidden=b.type==='ollama'&&['gpu','hybrid'].includes(o.value));
  $('placement-hint').textContent=b.type==='ollama'?'Ollama: RAM/CPU или автоматическое распределение. Полностью VRAM гарантировать нельзя.':'VRAM: попытка загрузить все слои на GPU; RAM + VRAM: число слоёв задаётся ниже. Для MoE-моделей больше VRAM выберите VRAM и задайте «Слои MoE в RAM». При нехватке памяти запуск откатится.';
  Object.entries(fields).forEach(([key,id])=>$(id).value=s[key]??'');
  document.querySelectorAll('.prism-field').forEach(el=>el.hidden=b.type!=='prism');$('advanced').hidden=b.type!=='prism';
  $('settings-status').textContent=b.pending?'Есть сохранённые настройки. Они ещё не применены к работающему серверу.':b.type==='ollama'?'Серверные значения по умолчанию. Параметры внешнего клиента могут их переопределить.':'При ошибке запуска с новыми настройками выполняется откат.';
  $('restore').disabled=!b.can_restore;
  $('loaded-model').textContent=b.sleeping?'Модель спит; новый запрос может её загрузить.':b.loaded_model==='-'||b.loaded_model==='(none)'?'Модель не загружена.':b.loaded_model;
  $('load-model').disabled=!(b.models||[]).length; $('unload-model').disabled=b.service!=='Running';
  const prism=b.type==='prism';$('pull-label').hidden=prism;['url-label','filename-label','sha-label'].forEach(id=>$(id).hidden=!prism);
  $('download-hint').textContent=prism?'Скачайте GGUF по прямой ссылке Hugging Face. PrismML нужен для Bonsai PQ2.':'Укажите имя и тег из библиотеки Ollama. Сначала запустите сервер Ollama.';
}
function collectSettings() {
  const b=backend(), settings={context:$('context').value.trim(),idle_minutes:$('idle').value,placement:$('placement').value};
  if(b.type==='prism'){for(const [key,id] of Object.entries(fields))settings[key]=$(id).value.trim(); if($('model-choice').value)settings.model=$('model-choice').value;}
  return settings;
}
async function action(body) { if(updateStatus?.phase==='installing')throw new Error('Дождитесь завершения обновления приложения.'); const revision=formRevision;const key=body.backend||'*';pendingActions.add(key);renderEngines();try{const op=await api('/api/actions',body);if(body.settings||body.action==='restore')submittedForms.set(op.id,revision);notice('Операция принята. Результат появится в обзоре.');await refresh(false);schedulePoll();return op;}finally{pendingActions.delete(key);renderEngines();} }
function copyRow(label,value) {
  const row=document.createElement('div');row.className='connect-row'; const caption=document.createElement('span');caption.className='card-label';caption.textContent=label;
  const field=document.createElement('div');field.className='copy-field';const code=document.createElement('code');code.textContent=value; const button=document.createElement('button');button.textContent='Копировать';button.type='button';
  button.addEventListener('click',async()=>{try{if(navigator.clipboard&&window.isSecureContext)await navigator.clipboard.writeText(value);else{const input=document.createElement('textarea');input.value=value;document.body.append(input);input.select();if(!document.execCommand('copy'))throw new Error('Выделите строку и скопируйте вручную.');input.remove();}button.textContent='Скопировано';setTimeout(()=>button.textContent='Копировать',1500);}catch(e){notice(e.message,true);}});
  field.append(code,button);row.append(caption,field);return row;
}
function renderGatewayForm() {
  if(!snapshot||!gateway)return;
  $('api-backend').replaceChildren();snapshot.backends.forEach(b=>$('api-backend').add(new Option(b.name,b.name)));$('api-backend').value=gateway.backend;
  $('api-base-url').value=gateway.base_url;$('api-require-key').checked=gateway.require_key;
  $('api-state').textContent=!gateway.enabled?'Сетевой API не включён. Повторите установщик и выберите OpenAI API.':`API доступен на порту панели ${panelPort} по пути /v1. Настроенный порт API: ${gateway.port}. ${gateway.require_key?'Используйте сохранённый ключ или сгенерируйте новый.':'Авторизация отключена: ключ не нужен.'}`;
  $('save-api').disabled=!gateway.enabled;$('generate-api-key').disabled=!gateway.enabled;
}
function renderConnections() {
  if(!snapshot||!gateway)return;$('connections').replaceChildren();if(!gateway.enabled)return;
  const b=snapshot.backends.find(b=>b.name===gateway.backend);if(!b)return;
  const card=document.createElement('article');card.className='connect-card';const heading=document.createElement('h2');heading.textContent=`OpenAI API · ${b.name}`;card.append(heading);
  const endpointRow=copyRow('OpenAI Base URL — вставьте в Jan или Pi',gateway.base_url);endpointRow.classList.add('endpoint-primary');card.append(endpointRow);
  const help=document.createElement('p');help.className='muted';help.textContent=`${badgeText(b)}. Сервер должен быть запущен; запрос к остановленному серверу его не включает.`;card.append(help);
  const model=b.selected_model||(b.type==='prism'?b.settings?.alias:'')||'';
  card.append(copyRow('Model / имя модели',model||'Выберите модель и нажмите Загрузить'));
  if(!gateway.require_key)card.append(copyRow('API key — отключён; local только если клиент требует значение','local'));
  else{const p=document.createElement('p');p.className='muted';p.textContent='API key: используйте ключ из Bitwarden. При утрате сгенерируйте новый выше. Пароль панели сюда не подходит.';card.append(p);}
  const details=document.createElement('details');const summary=document.createElement('summary');summary.textContent='Технические адреса';details.append(summary,copyRow('POST endpoint',`${gateway.base_url}/chat/completions`),copyRow('Локальный API выбранного движка на ПК',b.endpoint));card.append(details);$('connections').append(card);
}
async function saveGateway(body) {
  const data=await api('/api/gateway',body);gateway=data.gateway;gatewayDirty=false;renderGatewayForm();renderConnections();
  if(data.key){$('new-api-key').hidden=false;$('key-copy').replaceChildren(copyRow('API key — скопируйте и сохраните сейчас',data.key));}
  else if(!gateway.require_key){$('new-api-key').hidden=true;$('key-copy').replaceChildren();}
  notice(data.key?'Новый ключ сгенерирован. Предыдущий ключ отозван.':'Настройки API сохранены. Движок автоматически не запускается.');
}
let catalogBusy=false;
async function catalogRequest(mode,value) {
  if(catalogBusy)throw new Error('Дождитесь текущего поиска.');catalogBusy=true;$('catalog-search-button').disabled=true;$('catalog-status').textContent='Получаем данные Hugging Face…';
  try{const job=await api('/api/catalog',{mode,value});for(let i=0;i<45;i++){await new Promise(resolve=>setTimeout(resolve,750));const result=await api(`/api/catalog?id=${encodeURIComponent(job.id)}`);if(result.state==='failed')throw new Error(result.error);if(result.state==='done'){$('catalog-status').textContent='';return result.data;}}throw new Error('Поиск не ответил вовремя. Попробуйте позже.');}
  finally{catalogBusy=false;$('catalog-search-button').disabled=false;}
}
function sourceLink(label,url) {const a=document.createElement('a');a.textContent=label;a.href=url;a.target='_blank';a.rel='noopener noreferrer';return a;}
async function searchCatalog(query) {
  try{const data=await catalogRequest('search',query);$('catalog-results').replaceChildren();if(!data.models.length)$('catalog-status').textContent='Модели не найдены. Измените запрос или откройте каталог источника.';
  data.models.forEach(m=>{const card=document.createElement('article');card.className='catalog-card';const h=document.createElement('h3');h.textContent=m.name;const p=document.createElement('p');p.className='muted';p.textContent=`Скачиваний: ${Number(m.downloads).toLocaleString('ru-RU')}${m.gated?' · требуется доступ автора':''}`;const button=document.createElement('button');button.className='secondary small';button.textContent='Выбрать GGUF';button.addEventListener('click',()=>showCatalogFiles(m.id));card.append(h,p,sourceLink('Страница и описание ↗',m.url),button);$('catalog-results').append(card);});
  }catch(e){$('catalog-status').textContent=e.message;notice(e.message,true);}
}
async function showCatalogFiles(repository) {
  try{const data=await catalogRequest('files',repository);$('catalog-results').replaceChildren();$('catalog-results').append(sourceLink(`${repository} · описание и лицензия ↗`,data.url));if(!data.files.length)$('catalog-status').textContent='Подходящие текстовые GGUF не найдены.';
  data.files.forEach(f=>{const row=document.createElement('article');row.className='catalog-file';const title=document.createElement('strong');title.textContent=f.name;const meta=document.createElement('span');meta.className='muted';meta.textContent=`${f.bytes?(f.bytes/1024**3).toFixed(2)+' ГБ':'Размер не указан'}${f.prism_only?' · нужен PrismML':''}${f.split?' · многотомный файл':''}`;const button=document.createElement('button');button.className='secondary small';button.textContent='Скачать на ПК';const b=backend();const tag=`hf.co/${repository}:${f.name}`;button.disabled=f.split||(b.type==='ollama'&&(f.prism_only||tag.length>200));button.addEventListener('click',()=>{const b=backend();if(!b||f.split||(b.type==='ollama'&&(f.prism_only||tag.length>200))){notice('Этот файл не поддерживается выбранным движком.',true);return;}const body=b.type==='prism'?{action:'download',backend:selectedBackend,url:f.url,filename:f.filename,sha256:f.sha256}:{action:'pull',backend:selectedBackend,model:tag};action(body).catch(e=>notice(e.message,true));});row.append(title,meta,button);$('catalog-results').append(row);});
  }catch(e){$('catalog-status').textContent=e.message;notice(e.message,true);}
}
async function refreshLogs() { if(!snapshot)return; const data=await api('/api/logs');$('logs').replaceChildren();data.logs.forEach(log=>{const h=document.createElement('h3');h.textContent=log.stream;const pre=document.createElement('pre');pre.textContent=log.text;$('logs').append(h,pre);}); }
$('login-form').addEventListener('submit',async e=>{e.preventDefault();$('login-error').textContent='';const button=e.currentTarget.querySelector('button');button.disabled=true;try{await api('/api/login',{password:$('password').value});$('password').value='';await showDashboard();}catch(error){$('login-error').textContent=error.message;}finally{button.disabled=false;}});
$('logout').addEventListener('click',()=>api('/api/logout',{}).finally(showLogin));
document.querySelectorAll('[data-tab]').forEach(b=>b.addEventListener('click',()=>switchTab(b.dataset.tab)));
$('refresh').addEventListener('click',()=>refresh(true).catch(e=>notice(e.message,true)));
$('stop-all').addEventListener('click',()=>{
  // Stopping interrupts the current generation: ask first when something runs.
  const running=snapshot?.backends.some(b=>b.service==='Running'||b.service==='StartPending');
  if(running&&!window.confirm('Остановить все серверы? Текущая генерация будет прервана, модель выгрузится из памяти.'))return;
  action({action:'stopall'}).catch(e=>notice(e.message,true));
});
$('model-backend').addEventListener('change',e=>chooseBackend(e.target.value));
$('settings-form').addEventListener('input',()=>{settingsDirty=true;formRevision++;});$('settings-form').addEventListener('change',()=>{settingsDirty=true;formRevision++;});
$('settings-form').addEventListener('submit',e=>{e.preventDefault();action({action:'save',backend:selectedBackend,settings:collectSettings()}).catch(error=>notice(error.message,true));});
$('apply').addEventListener('click',()=>{if($('settings-form').reportValidity())action({action:'apply',backend:selectedBackend,settings:collectSettings()}).catch(e=>notice(e.message,true));});
$('restore').addEventListener('click',()=>action({action:'restore',backend:selectedBackend}).catch(e=>notice(e.message,true)));
$('load-model').addEventListener('click',()=>{const b=backend();const body={action:'load',backend:selectedBackend};if(b.type==='prism'){if(!$('settings-form').reportValidity())return;body.settings=collectSettings();}else {body.model=$('model-choice').value;if(settingsDirty){if(!$('settings-form').reportValidity())return;body.settings=collectSettings();}}action(body).catch(e=>notice(e.message,true));});
$('unload-model').addEventListener('click',()=>action({action:'unload',backend:selectedBackend,model:$('model-choice').value}).catch(e=>notice(e.message,true)));
$('download-form').addEventListener('submit',e=>{e.preventDefault();const b=backend();const body=b.type==='prism'?{action:'download',backend:selectedBackend,url:$('download-url').value.trim(),filename:$('download-filename').value.trim(),sha256:$('download-sha').value.trim()}:{action:'pull',backend:selectedBackend,model:$('pull-name').value.trim()};action(body).catch(error=>notice(error.message,true));});
$('refresh-logs').addEventListener('click',()=>refreshLogs().catch(e=>notice(e.message,true)));
$('api-settings-form').addEventListener('input',()=>gatewayDirty=true);
$('api-settings-form').addEventListener('submit',e=>{e.preventDefault();saveGateway({action:'save',backend:$('api-backend').value,base_url:$('api-base-url').value.trim(),require_key:$('api-require-key').checked}).catch(e=>notice(e.message,true));});
$('generate-api-key').addEventListener('click',()=>saveGateway({action:'generate'}).catch(e=>notice(e.message,true)));
$('catalog-form').addEventListener('submit',e=>{e.preventDefault();searchCatalog($('catalog-query').value.trim());});
document.querySelectorAll('[data-search]').forEach(button=>button.addEventListener('click',()=>{$('catalog-query').value=button.dataset.search;searchCatalog(button.dataset.search);}));
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

let updateStatus=null,updateTimer=null,updateBusy=false;
function renderUpdateStatus(data){
 updateStatus=data;
 const busy=['checking','login','installing'].includes(data.phase);
 $('update-version').textContent=`Установлена ${data.current}${data.latest?' · доступна '+data.latest:''}`;
 $('update-message').textContent=data.message;
 $('update-progress').hidden=!busy;
 $('update-check').disabled=busy||updateBusy;
 $('update-login').disabled=busy||updateBusy||!data.gh_available;
 $('update-gh-install').hidden=data.gh_available;
 $('update-forget').disabled=busy||!data.authenticated;
 $('update-token-submit').disabled=busy||updateBusy;
 $('update-apply').hidden=data.phase!=='ready';$('update-apply').disabled=updateBusy;
 $('update-cancel').hidden=!['checking','login'].includes(data.phase);
 $('update-auth').textContent=data.authenticated?'Вход gh подключён к панели. Доступ хранится в зашифрованном хранилище Windows.':'Сохранённого входа панели нет. Войдите через gh по коду или используйте разовый токен.';
 $('update-device').hidden=!data.code;
 if(data.code&&$('update-code').dataset.code!==data.code)$('update-code').replaceChildren(copyRow('Одноразовый код GitHub',data.code));else if(!data.code)$('update-code').replaceChildren();$('update-code').dataset.code=data.code||'';
 if(data.phase==='installing')localStorage.setItem('omnibox-update-pending','1');
 if(['done','failed'].includes(data.phase))localStorage.setItem('omnibox-update-pending','0');
}
function scheduleUpdatePoll(){
 clearTimeout(updateTimer);
 if(!loggedIn)return;
 if(activeTab!=='updates'&&!['checking','login','installing'].includes(updateStatus?.phase))return;
 updateTimer=setTimeout(async()=>{if(!document.hidden){try{await refreshUpdateStatus();}catch(e){if(localStorage.getItem('omnibox-update-pending')==='1')$('update-message').textContent='Панель перезапускается. Ожидаем подключения…';else notice(e.message,true);}}scheduleUpdatePoll();},2000);
}
async function refreshUpdateStatus(){renderUpdateStatus(await api('/api/updates'));scheduleUpdatePoll();}
async function updateAction(body){
 if(updateBusy)return;updateBusy=true;
 try{renderUpdateStatus(await api('/api/updates',body));scheduleUpdatePoll();}
 finally{if(body.token)body.token='';updateBusy=false;if(updateStatus)renderUpdateStatus(updateStatus);}
}
for(const [id,action] of [['update-check','check'],['update-login','login'],['update-forget','forget'],['update-cancel','cancel'],['update-apply','apply']]){
 $(id).addEventListener('click',()=>updateAction({action}).catch(e=>notice(e.message,true)));
}
$('update-token-form').addEventListener('submit',e=>{
 e.preventDefault();const token=$('update-token').value.trim();$('update-token').value='';
 updateAction({action:'token',token}).catch(e=>notice(e.message,true));
});
