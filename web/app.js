'use strict';
const $ = id => document.getElementById(id);
let csrf = '', snapshot = null, activeTab = 'overview', settingsDirty = false, selectedBackend = '', pollTimer = null, loggedIn = false;
let formRevision = 0;
const submittedForms = new Map();
const clientEndpoints = new Map();
const clientModels = new Map();
const fields = {context:'context',idle_minutes:'idle',alias:'alias',gpu_layers:'gpu-layers',flash_attention:'flash',cache_key:'cache-key',cache_value:'cache-value',batch:'batch',ubatch:'ubatch'};
const labels = {start:'Запуск',stop:'Остановка',restart:'Перезапуск',stopall:'Остановка всех серверов',save:'Сохранение',apply:'Применение',restore:'Восстановление',load:'Загрузка',unload:'Выгрузка',pull:'Скачивание',download:'Скачивание GGUF'};
async function api(path, body) {
  const response = await fetch(path, {method:body === undefined?'GET':'POST',credentials:'same-origin',headers:body === undefined?{}:{'Content-Type':'application/json','X-Omnibox-CSRF':csrf},body:body === undefined?undefined:JSON.stringify(body)});
  const data = await response.json();
  if (!response.ok) { if(response.status===401 && path!=='/api/login') showLogin(); throw new Error(data.error||`Ошибка ${response.status}`); }
  return data;
}
function notice(message, error=false) { $('notice').hidden=false; $('notice').textContent=message; $('notice').classList.toggle('error',error); }
function showLogin() { loggedIn=false; clearTimeout(pollTimer); $('login').hidden=false; $('dashboard').hidden=true; csrf=''; }
async function showDashboard() { const session=await api('/api/session'); csrf=session.csrf; $('ssh-target').value=session.ssh_target; loggedIn=true; $('login').hidden=true; $('dashboard').hidden=false; await refresh(true); schedulePoll(); }
function schedulePoll() { clearTimeout(pollTimer); if(!loggedIn)return; pollTimer=setTimeout(async()=>{ if(!document.hidden) { try { await refresh(false); } catch(e) { notice(e.message,true); } } schedulePoll(); },8000); }
document.addEventListener('visibilitychange',()=>{if(!document.hidden&&loggedIn)refresh(false).catch(e=>notice(e.message,true));});
function backend() { return snapshot?.backends.find(b=>b.name===selectedBackend); }
function chooseBackend(name) { selectedBackend=name; $('model-backend').value=name; settingsDirty=false; formRevision++; renderSettings(); }
function switchTab(tab) { activeTab=tab; document.querySelectorAll('.tab').forEach(s=>s.hidden=s.id!==`tab-${tab}`); document.querySelectorAll('[data-tab]').forEach(b=>b.classList.toggle('active',b.dataset.tab===tab)); if(tab==='logs')refreshLogs().catch(e=>notice(e.message,true)); }
async function refresh(gpu) {
  const data=await api(`/api/status${gpu?'?gpu=1':''}`); snapshot=data; $('version').textContent=`v${data.version}`; $('computer').textContent=data.computer;
  $('server-count').textContent=`${data.backends.filter(b=>b.service==='Running').length} / ${data.backends.length}`; $('exclusive').textContent=data.exclusive?'По очереди':'Независимый';
  if(gpu) { const g=data.gpu?.[0]; $('gpu-value').textContent=g?`${(g.used/1024).toFixed(1)} / ${(g.total/1024).toFixed(1)} ГБ`:'Нет данных'; $('gpu-bar').value=g?g.used/g.total*100:0; $('gpu-caption').textContent=g?`${g.name} · свободно ${(g.free/1024).toFixed(1)} ГБ`:'nvidia-smi недоступен.'; }
  renderEngines(); renderOperations(data.operations||[]);
  for(const id of ['model-backend','log-backend']) { const select=$(id), old=select.value; select.replaceChildren(); data.backends.forEach(b=>select.add(new Option(b.name,b.name))); select.value=data.backends.some(b=>b.name===old)?old:data.default_backend; }
  if(!selectedBackend)selectedBackend=data.default_backend;
  $('model-backend').value=selectedBackend;
  if(!settingsDirty)renderSettings();
  if(!$('connections').contains(document.activeElement))renderConnections();
}
function badgeText(b) { if(b.service==='Running') return b.sleeping?'Модель спит':b.api==='Ready'?'API готов':'API недоступен'; return ({Stopped:'Остановлен',Missing:'Не установлен',StartPending:'Запускается',StopPending:'Останавливается'})[b.service]||b.service; }
function renderEngines() {
  $('engines').replaceChildren();
  snapshot.backends.forEach(b=>{
    const card=$('engine-template').content.firstElementChild.cloneNode(true);
    card.querySelector('h2').textContent=b.name==='bonsai'?'Bonsai / PrismML':b.name==='ollama'?'Ollama':b.name;
    card.querySelector('.engine-type').textContent=b.type==='prism'?'PrismML llama-server · GGUF':'Ollama · библиотека моделей';
    const badge=card.querySelector('.badge'); badge.textContent=badgeText(b); badge.classList.toggle('running',b.service==='Running'); badge.classList.toggle('sleeping',!!b.sleeping);
    card.querySelector('.engine-model').textContent=b.loaded_model==='-'||b.loaded_model==='(none)'?'Не загружена':b.loaded_model;
    card.querySelector('.engine-context').textContent=b.active_context?`${Number(b.active_context).toLocaleString('ru-RU')} активный`:`${b.settings?.context||'—'} настроенный`;
    card.querySelector('.engine-idle').textContent=b.settings?.idle_minutes>0?`${b.settings.idle_minutes} мин`:'Не задан';
    card.querySelector('.engine-error').textContent=(b.errors||[]).join('\n');
    card.querySelectorAll('[data-action]').forEach(button=>{
      button.disabled=button.dataset.action==='start' && (b.service==='Running'||b.service==='StartPending') || button.dataset.action==='stop' && b.service==='Stopped';
      button.addEventListener('click',()=>{if(button.dataset.action==='settings'){chooseBackend(b.name);switchTab('models');}else action({action:button.dataset.action,backend:b.name}).catch(e=>notice(e.message,true));});
    });
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
    row.append(icon,title,message);$('operations').append(row);
    if(op.state!=='running'&&!completed.has(op.id)){completed.add(op.id);if(!notified){notice(op.message,op.state==='failed');notified=true;}if(op.state==='done'&&op.backend===selectedBackend&&submittedForms.get(op.id)===formRevision)settingsDirty=false;submittedForms.delete(op.id);}
  });
}
function renderSettings() {
  const b=backend();if(!b)return; const s=b.settings||{};
  $('model-service').textContent=badgeText(b);
  const models=$('model-choice'); const old=models.value; models.replaceChildren(); (b.models||[]).forEach(m=>models.add(new Option(`${m.name} · ${(m.bytes/1024**3).toFixed(1)} ГБ`,m.id)));
  if(b.type==='prism'&&s.model)models.value=s.model; else if((b.models||[]).some(m=>m.id===old))models.value=old;
  models.disabled=b.type==='ollama'&&!(b.models||[]).length;
  $('model-hint').textContent=b.type==='prism'?'GGUF из каталога модели на ПК.':'Для списка моделей Ollama должна быть запущена.';
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
  const b=backend(), settings={context:$('context').value.trim(),idle_minutes:$('idle').value};
  if(b.type==='prism'){for(const [key,id] of Object.entries(fields))settings[key]=$(id).value.trim(); if($('model-choice').value)settings.model=$('model-choice').value;}
  return settings;
}
async function action(body) { const revision=formRevision; const op=await api('/api/actions',body);if(body.settings||body.action==='restore')submittedForms.set(op.id,revision);notice('Операция принята. Результат появится в обзоре.'); await refresh(false); return op; }
function copyRow(label,value) {
  const row=document.createElement('div');row.className='connect-row'; const caption=document.createElement('span');caption.className='card-label';caption.textContent=label;
  const field=document.createElement('div');field.className='copy-field';const code=document.createElement('code');code.textContent=value; const button=document.createElement('button');button.textContent='Копировать';button.type='button';
  button.addEventListener('click',async()=>{try{if(navigator.clipboard&&window.isSecureContext)await navigator.clipboard.writeText(value);else{const input=document.createElement('textarea');input.value=value;document.body.append(input);input.select();if(!document.execCommand('copy'))throw new Error('Выделите строку и скопируйте вручную.');input.remove();}button.textContent='Скопировано';setTimeout(()=>button.textContent='Копировать',1500);}catch(e){notice(e.message,true);}});
  field.append(code,button);row.append(caption,field);return row;
}
function renderConnections() {
  if(!snapshot)return;const target=$('ssh-target').value.trim();$('connections').replaceChildren();
  snapshot.backends.forEach((b,index)=>{
    const card=document.createElement('article');card.className='connect-card';const heading=document.createElement('h2');heading.textContent=b.name;card.append(heading);
    const localPort=11435+index, serverPort=new URL(b.endpoint).port, remoteHost=new URL(b.endpoint).hostname;
    const tunnel=/^[a-zA-Z0-9_.-]+@[a-zA-Z0-9_.-]+$/.test(target)?`ssh -N -o ExitOnForwardFailure=yes -o ServerAliveInterval=30 -L ${localPort}:${remoteHost}:${serverPort} ${target}`:'Введите SSH-адрес как user@host.';
    const base=clientEndpoints.get(b.name)||`http://127.0.0.1:${localPort}/v1`;
    const endpointRow=copyRow('OpenAI Base URL — вставьте в Jan или Pi',base);endpointRow.classList.add('endpoint-primary');card.append(endpointRow);
    const help=document.createElement('p');help.className='muted';help.textContent=clientEndpoints.has(b.name)?'Вы указали свой доступный адрес API. Он должен вести к API этого движка.':'Этот адрес работает НА НОУТБУКЕ после открытия SSH-туннеля ниже. На смартфоне также нужен туннель или отдельно доступный API.';card.append(help);
    let model=b.type==='prism'?b.settings?.alias||'Имя API-модели':clientModels.get(b.name)||b.models?.[0]?.name||'';
    if(b.type==='ollama'&&(b.models||[]).length){
      if(!b.models.some(m=>m.name===model))model=b.models[0].name;
      const label=document.createElement('label');label.className='connection-model';label.textContent='Модель для подключения';const select=document.createElement('select');b.models.forEach(m=>select.add(new Option(m.name,m.name)));select.value=model;
      select.addEventListener('change',()=>{clientModels.set(b.name,select.value);renderConnections();});label.append(select);card.append(label);
    }
    card.append(copyRow('Model / имя модели',model||'Сначала запустите Ollama и скачайте модель'),copyRow('API key — для текущего API без авторизации','local'));
    const keyHelp=document.createElement('p');keyHelp.className='muted';keyHelp.textContent='Если API движка или его proxy защищён ключом, укажите его ключ вместо local. Пароль панели сюда не подходит.';card.append(keyHelp);
    if(!clientEndpoints.has(b.name))card.append(copyRow('1. Откройте SSH-туннель на ноутбуке',tunnel));
    const details=document.createElement('details');const summary=document.createElement('summary');summary.textContent='Другой адрес API и технические адреса';details.append(summary);
    const label=document.createElement('label');label.textContent='Свой OpenAI Base URL (если API доступен через VPN или отдельный proxy)';const input=document.createElement('input');input.type='url';input.placeholder='https://models.example.com/v1';input.value=clientEndpoints.get(b.name)||'';
    input.addEventListener('change',()=>{const value=input.value.trim();if(!value){clientEndpoints.delete(b.name);renderConnections();return;}try{const u=new URL(value);if(!['http:','https:'].includes(u.protocol)||u.username||u.password||u.search||u.hash||!/\/v1\/?$/.test(u.pathname))throw new Error('Нужен HTTP/HTTPS Base URL, заканчивающийся на /v1, без пароля и параметров.');clientEndpoints.set(b.name,value.replace(/\/$/,''));renderConnections();}catch(e){notice(e.message,true);}});
    label.append(input);details.append(label,copyRow('POST endpoint для генерации',`${base}/chat/completions`),copyRow('OpenAI Base URL на самом серверном ПК',b.endpoint));
    const proxyHelp=document.createElement('p');proxyHelp.className='muted';proxyHelp.textContent='Публикация панели через NAS не публикует API движков. Другой адрес действует в этой вкладке и не меняет настройки сервера.';details.append(proxyHelp);card.append(details);
    $('connections').append(card);
  });
}
async function refreshLogs() { if(!snapshot)return; const data=await api(`/api/logs?backend=${encodeURIComponent($('log-backend').value)}`);$('logs').replaceChildren();data.logs.forEach(log=>{const h=document.createElement('h3');h.textContent=log.stream;const pre=document.createElement('pre');pre.textContent=log.text;$('logs').append(h,pre);}); }
$('login-form').addEventListener('submit',async e=>{e.preventDefault();$('login-error').textContent='';const button=e.currentTarget.querySelector('button');button.disabled=true;try{await api('/api/login',{password:$('password').value});$('password').value='';await showDashboard();}catch(error){$('login-error').textContent=error.message;}finally{button.disabled=false;}});
$('logout').addEventListener('click',()=>api('/api/logout',{}).finally(showLogin));
document.querySelectorAll('[data-tab]').forEach(b=>b.addEventListener('click',()=>switchTab(b.dataset.tab)));
$('refresh').addEventListener('click',()=>refresh(true).catch(e=>notice(e.message,true)));
$('stop-all').addEventListener('click',()=>action({action:'stopall'}).catch(e=>notice(e.message,true)));
$('model-backend').addEventListener('change',e=>chooseBackend(e.target.value));
$('settings-form').addEventListener('input',()=>{settingsDirty=true;formRevision++;});$('settings-form').addEventListener('change',()=>{settingsDirty=true;formRevision++;});
$('settings-form').addEventListener('submit',e=>{e.preventDefault();action({action:'save',backend:selectedBackend,settings:collectSettings()}).catch(error=>notice(error.message,true));});
$('apply').addEventListener('click',()=>{if($('settings-form').reportValidity())action({action:'apply',backend:selectedBackend,settings:collectSettings()}).catch(e=>notice(e.message,true));});
$('restore').addEventListener('click',()=>action({action:'restore',backend:selectedBackend}).catch(e=>notice(e.message,true)));
$('load-model').addEventListener('click',()=>{const b=backend();const body={action:'load',backend:selectedBackend};if(b.type==='prism'){if(!$('settings-form').reportValidity())return;body.settings=collectSettings();}else body.model=$('model-choice').value;action(body).catch(e=>notice(e.message,true));});
$('unload-model').addEventListener('click',()=>action({action:'unload',backend:selectedBackend,model:$('model-choice').value}).catch(e=>notice(e.message,true)));
$('download-form').addEventListener('submit',e=>{e.preventDefault();const b=backend();const body=b.type==='prism'?{action:'download',backend:selectedBackend,url:$('download-url').value.trim(),filename:$('download-filename').value.trim(),sha256:$('download-sha').value.trim()}:{action:'pull',backend:selectedBackend,model:$('pull-name').value.trim()};action(body).catch(error=>notice(error.message,true));});
$('ssh-target').addEventListener('input',renderConnections);$('refresh-logs').addEventListener('click',()=>refreshLogs().catch(e=>notice(e.message,true)));$('log-backend').addEventListener('change',()=>refreshLogs().catch(e=>notice(e.message,true)));
showDashboard().catch(()=>showLogin());
