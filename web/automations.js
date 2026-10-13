'use strict';
// ── automations: the owner's own scenarios — steps one after another: a task in words, fields
// taken from data, a Word template filled — with what each may use, a history and a schedule ──
// A run's data and values live in this page only while the run is open; closing it forgets them.
const A={list:null,last:{},next:{},cur:null,file:null,run:null};
const AU_KIND={task:'Задача',fields:'Поля из данных',document:'Документ по шаблону'};
const AU_CHECKS={'':'Текст',digits:'Только цифры',number:'Число, сумма',date:'Дата',date_past:'Дата не в будущем',phone:'Телефон',email:'Email',passport_series:'Серия паспорта',passport_number:'Номер паспорта',division_code:'Код подразделения',inn:'ИНН',snils:'СНИЛС',ogrn:'ОГРН / ОГРНИП',kpp:'КПП',bik:'БИК',account:'Расчётный счёт'};
const auCheckOptions=v=>Object.entries(AU_CHECKS).map(([k,l])=>`<option value="${k}"${k===(v||'')?' selected':''}>${esc(l)}</option>`).join('');
const AU_DAYS=['Пн','Вт','Ср','Чт','Пт','Сб','Вс'];
// Presets are only a start: everything in them can be changed.
const AU_PRESETS={
  passport:{name:'Паспорт РФ',emoji:'🪪',private:true,
    instructions:'Даты — ДД.ММ.ГГГГ. ФИО — как в паспорте, с заглавной буквы. «Кем выдан» — полностью, как напечатано.',
    steps:[{kind:'fields',prompt:'Распознай данные паспорта гражданина РФ на фото: разворот с фотографией и, если есть, страницу с регистрацией. Серия и номер напечатаны на развороте и вертикально на полях — сверь их. Если внизу видна машиночитаемая строка, сверь с ней ФИО и даты.',
      fields:[{name:'Фамилия'},{name:'Имя'},{name:'Отчество'},{name:'Пол',hint:'МУЖ или ЖЕН'},{name:'Дата рождения',check:'date_past'},{name:'Место рождения'},
        {name:'Серия',hint:'4 цифры',check:'passport_series'},{name:'Номер',hint:'6 цифр',check:'passport_number'},{name:'Кем выдан'},{name:'Дата выдачи',check:'date_past'},
        {name:'Код подразделения',hint:'в виде 123-456',check:'division_code'},{name:'Адрес регистрации',hint:'со страницы регистрации, если она есть'}]}]},
  news:{name:'Сводка новостей',emoji:'📰',private:false,history:true,instructions:'',
    steps:[{kind:'task',name:'Поиск',web:true,read:true,prompt:'Найди главные новости о {{тема}} за последнюю неделю — и на русском, и на английском. Для каждой: что произошло, дата, ссылка на источник.'},
      {kind:'task',name:'Сводка',prompt:'Сделай из найденного сводку на русском из 5–10 пунктов: что произошло и почему это важно, со ссылками на источники. Убери повторы.'}]},
  // The State Duma's bills: the search stays on its site, the bill cards are opened.
  duma:{name:'Законопроекты Госдумы',emoji:'🏛️',private:false,history:true,instructions:'',
    steps:[{kind:'task',name:'Поиск в СОЗД',web:true,read:true,sites:['sozd.duma.gov.ru'],prompt:'Найди на сайте Госдумы sozd.duma.gov.ru законопроекты по теме: {{тема}}. Открой карточки найденных законопроектов (адреса вида sozd.duma.gov.ru/bill/…). Для каждого выпиши: номер, название, кто внёс, дату внесения, текущую стадию и последнее событие с датой, ссылку на карточку.'},
      {kind:'task',name:'Сводка',prompt:'Сделай из найденного сводку на русском: сначала законопроекты, по которым за последний месяц были события (внесение, чтения, принятие, подписание), затем остальные. Для каждого — 2–3 предложения: что меняет и на какой стадии, со ссылкой на карточку. Если ничего не найдено — так и скажи.'}]}};
const auSteps=a=>a.steps?.length?a.steps:[{kind:a.kind,prompt:a.prompt,web:a.web,read:a.read,fields:a.fields}];
const auWeb=a=>auSteps(a).some(s=>s.web||s.sites?.length);
const auSites=a=>[...new Set(auSteps(a).flatMap(s=>s.sites||[]))];
const auPolicy=a=>a.private?'Только ПК · без интернета · без истории · файлы не хранятся':
  [auSites(a).length?`поиск на ${auSites(a).join(', ')}`+(auSteps(a).some(s=>s.web&&!s.sites?.length)?' и в интернете':''):auWeb(a)?'с поиском в интернете':'без интернета',a.history?'история хранится':'без истории'].join(' · ');
// What the owner wrote in a step's sites: a site per word, an address cut to its site.
const auSiteList=v=>[...new Set(String(v||'').split(/[\s,;]+/).map(x=>x.trim().toLowerCase().replace(/^[a-z]+:\/\//,'').replace(/[/?#].*$/,'').replace(/^www\./,'')).filter(Boolean))];
const auEmoji=a=>`<span class="av emo">${emojiSVG(a.emoji||'⚡')}</span>`;
const auWhen=t=>new Date(t).toLocaleString('ru-RU',{day:'2-digit',month:'2-digit',hour:'2-digit',minute:'2-digit'});
const auVarPattern=/\{\{\s*([^{}]{1,80}?)\s*\}\}/g;
function auVars(prompts){const out=[];for(const p of [].concat(prompts))for(const m of String(p||'').matchAll(auVarPattern))if(!out.includes(m[1]))out.push(m[1]);return out;}
const auStepVars=a=>auVars(auSteps(a).map(s=>s.prompt));
// A schedule runs without data and without the owner: it starts with a task and builds no document.
const auSchedulable=steps=>steps[0]?.kind==='task'&&!steps.some(s=>s.kind==='document');
function auSchedText(s){if(!s?.enabled)return '';const at=s.at||'';
  if(s.every==='hours')return `каждые ${s.hours} ч от ${at}`;if(s.every==='week')return `${(s.days||[]).map(d=>AU_DAYS[d-1]).join(', ')} в ${at}`;return `каждый день в ${at}`;}

async function loadAuto(){const d=await api('/api/automations');A.list=d.automations||[];A.last=d.last||{};A.next=d.next||{};renderAuto();}
function renderAuto(){const box=byId('au-list');if(!A.list){box.innerHTML='<div class="banner">Загружаем автоматизации…</div>';return;}
  if(!A.list.length){box.innerHTML='<div class="banner"><span>Пока нет автоматизаций. «Новая автоматизация» — задача своими словами, поля из данных или документ по шаблону Word, из одного шага или нескольких; можно начать с заготовки.</span></div>';return;}
  box.innerHTML=A.list.map(a=>{const m=a.model?(modelByKey(a.model)?.name||a.model.slice(a.model.indexOf(':')+1)):'модель чата',last=A.last[a.id],sched=auSchedText(a.schedule),steps=auSteps(a);
    const n=(a.kind==='document'?a.template?.fields:steps.at(-1).fields)?.length||0,what=a.kind==='task'?'':` · ${n} ${plural(n,'поле','поля','полей')}`;
    const kind=steps.length>1?`${steps.length} ${plural(steps.length,'шаг','шага','шагов')} → ${(AU_KIND[a.kind]||'').toLowerCase()}`:AU_KIND[a.kind]||'';
    return `<div class="au-card">${auEmoji(a)}<div class="au-main"><b>${esc(a.name)}${last?.unseen?` <span class="pill iris">новых: ${last.unseen}</span>`:''}</b>`+
      `<span class="small muted">${esc(kind)}${esc(what)} · ${esc(m)}</span>`+
      `<span class="au-policy${a.private?' priv':''}">${a.private?'<svg class="i i15"><use href="#i-lock"/></svg>':''}${esc(auPolicy(a))}</span>`+
      (sched?`<span class="small muted"><svg class="i i15"><use href="#i-clock"/></svg> ${esc(sched)}${A.next[a.id]?` · следующий: ${esc(auWhen(A.next[a.id]))}`:''}</span>`:'')+
      (last?`<span class="small ${last.error?'warn-text':'faint'}">Последний запуск: ${esc(auWhen(last.created))}${last.trigger==='schedule'?' по расписанию':''}${last.error?' — ошибка':''}</span>`:'')+`</div>`+
      `<div class="au-acts"><button class="btn primary sm" type="button" data-au-run="${esc(a.id)}">Запустить</button>${a.history?`<button class="btn ghost sm" type="button" data-au-hist="${esc(a.id)}">История</button>`:''}<button class="btn ghost sm" type="button" data-au-edit="${esc(a.id)}">Настроить</button></div></div>`;}).join('');}

// The model list: models on the PC always, cloud ones (the favourites) only when not private.
function auModelOptions(cur,priv,firstLabel){const list=models().filter(m=>m.source==='local'||(!priv&&m.favorite));
  if(cur&&!list.some(m=>m.key===cur)&&modelByKey(cur))list.push(modelByKey(cur));
  return (firstLabel!==null?`<option value="">${esc(firstLabel)}</option>`:'')+list.map(m=>`<option value="${esc(m.key)}"${m.key===cur?' selected':''}>${esc(m.name)}${m.source==='local'?'':m.source==='chatgpt'?' · ChatGPT':' · облако'}${m.caps?.vision?' · видит фото':''}</option>`).join('');}
function auModels(sel,cur,priv,chatLabel){sel.innerHTML=auModelOptions(cur,priv,chatLabel);sel.value=cur||'';}

// ── new ──
byId('au-new-btn').addEventListener('click',()=>{const m=byId('au-new-menu');m.hidden=!m.hidden;byId('au-new-btn').setAttribute('aria-expanded',String(!m.hidden));});
document.addEventListener('click',e=>{if(!e.target.closest?.('.au-newwrap')){byId('au-new-menu').hidden=true;byId('au-new-btn').setAttribute('aria-expanded','false');}});
byId('au-new-menu').addEventListener('click',e=>{const k=e.target.closest('[data-au-new]'),p=e.target.closest('[data-au-preset]');if(!k&&!p)return;byId('au-new-menu').hidden=true;
  if(p){openAuto(JSON.parse(JSON.stringify(AU_PRESETS[p.dataset.auPreset])));return;}
  const kind=k.dataset.auNew;openAuto(kind==='task'?{name:'',emoji:'⚡',private:false,steps:[{kind,prompt:''}]}:{name:'',emoji:'🧾',private:true,steps:[{kind,fields:[{name:'',hint:'',check:''}]}]});});
byId('au-new').addEventListener('change',async e=>{const f=e.target.files?.[0];e.target.value='';byId('au-new-menu').hidden=true;if(!f)return;
  if(!/\.docx$/i.test(f.name)){notice('Шаблон — файл Word .docx. Файл .doc сохраните в Word как .docx.',true);return;}
  if(f.size>25*1024*1024){notice('Шаблон больше 25 МБ.',true);return;}
  try{const data=await readB64(f);openAuto({kind:'document',name:f.name.replace(/\.docx$/i,''),emoji:'📄',private:true,template:{name:f.name,fields:[]},steps:[{kind:'document'}]},{name:f.name,data});}catch(x){fail(x);}});

// ── settings drawer ──
function openAuto(a,file){A.cur=JSON.parse(JSON.stringify(a));A.file=file||null;const d=A.cur;
  d.steps=auSteps(d).map(s=>({kind:s.kind,name:s.name||'',prompt:s.prompt||'',model:s.model||'',web:!!s.web,read:!!s.read,fields:s.fields||[],sites:(s.sites||[]).join(', ')}));
  d.schedule=d.schedule||{enabled:false,every:'day',at:'08:00',days:[1,2,3,4,5],hours:4};d.vars=d.vars||{};
  byId('au-title').textContent=d.id?d.name:'Новая автоматизация';
  byId('au-name').value=d.name||'';byId('au-emoji').value=d.emoji||'';byId('au-instr').value=d.instructions||'';
  byId('au-private').checked=!!d.private;byId('au-history').checked=!!d.history;byId('au-delete').hidden=!d.id;
  const doc=d.steps.some(s=>s.kind==='document');byId('au-doc-wrap').hidden=!doc;
  const tpl=byId('au-tpl');tpl.hidden=!d.id||!doc;if(d.id&&doc){tpl.href='/api/automations/file?template='+encodeURIComponent(d.id);tpl.setAttribute('download',d.template?.name||'шаблон.docx');}
  byId('au-tpl-name').textContent=d.template?.name||'';
  const s=d.schedule;byId('au-sched-on').checked=!!s.enabled;byId('au-sched-every').value=s.every||'day';byId('au-sched-at').value=s.at||'08:00';byId('au-sched-hours').value=String(s.hours||4);
  renderAuModel();renderAuFields();syncAuPolicy();show(byId('au-drawer'));}
function auKindLabel(){const st=A.cur.steps;byId('au-kind').textContent='Автоматизация · '+(st.length>1?`${st.length} ${plural(st.length,'шаг','шага','шагов')}`:(AU_KIND[st[0]?.kind]||'').toLowerCase());}
function renderAuModel(){const priv=byId('au-private').checked;auModels(byId('au-model'),A.cur.model,priv,'Модель, выбранная в чате');
  byId('au-model-hint').textContent=priv?'Конфиденциально: только модели на этом ПК. Для фото нужна модель со зрением. У шага может быть своя модель.':'Для всех шагов, если у шага не выбрана своя. Если не выбрана — модель, выбранная в чате. Облачная модель получит данные через OpenRouter или ChatGPT.';}
// Private switches off everything that leaves the PC or stays on it; the service does the same.
function syncAuPolicy(){const priv=byId('au-private').checked;
  for(const id of ['au-history','au-sched-on']){const el=byId(id);el.disabled=priv;if(priv)el.checked=false;}
  if(priv)A.cur.steps.forEach(s=>{s.web=false;s.read=false;if(s.model&&modelByKey(s.model)?.source!=='local')s.model='';});
  renderAuSteps();renderAuSched();}
byId('au-private').addEventListener('change',()=>{A.cur.model=byId('au-model').value;renderAuModel();syncAuPolicy();});
byId('au-model').addEventListener('change',e=>{A.cur.model=e.target.value;});
// The steps: each with its words, its fields, its model and its search.
function renderAuSteps(){const st=A.cur.steps,priv=byId('au-private').checked,n=st.length;auKindLabel();
  const opts=v=>Object.entries(AU_CHECKS).map(([k,l])=>`<option value="${k}"${k===(v||'')?' selected':''}>${esc(l)}</option>`).join('');
  byId('au-steps').innerHTML=st.map((s,i)=>{const doc=s.kind==='document';
    const head=`<div class="au-shead"><span class="au-snum">${i+1}</span><input type="text" data-st="${i}" data-k="name" maxlength="80" value="${esc(s.name)}" placeholder="${esc(`Шаг ${i+1}`)}" aria-label="Название шага">`+
      (doc?`<span class="pill">Документ</span>`:`<select data-st="${i}" data-k="kind" aria-label="Вид шага"><option value="task"${s.kind==='task'?' selected':''}>Задача</option><option value="fields"${s.kind==='fields'?' selected':''}>Поля</option></select>`)+
      `<button class="btn ghost sm" type="button" data-au-smove="${i}:-1" aria-label="Выше" title="Выше"${i===0||doc?' disabled':''}><svg class="i i15 up"><use href="#i-arrow-down"/></svg></button>`+
      `<button class="btn ghost sm" type="button" data-au-smove="${i}:1" aria-label="Ниже" title="Ниже"${i>=n-1||doc||st[i+1]?.kind==='document'?' disabled':''}><svg class="i i15"><use href="#i-arrow-down"/></svg></button>`+
      `<button class="btn ghost sm" type="button" data-au-sdel="${i}" aria-label="Убрать шаг" title="Убрать шаг"${n===1||doc?' disabled':''}><svg class="i i15"><use href="#i-x"/></svg></button></div>`;
    const ph=s.kind==='task'?(i===0?'Что сделать — своими словами. Например: «Найди главные новости о {{тема}} за последнюю неделю».':'Что сделать с результатом предыдущих шагов. Например: «Переведи на английский».'):'Что сделать (необязательно). Например: «Распознай паспорт на фото».';
    const fields=s.kind==='fields'?`<div class="au-fields">${s.fields.map((x,j)=>`<div class="au-field"><div class="au-frow"><input type="text" data-au-ff="${i}:${j}" data-k="name" maxlength="80" value="${esc(x.name||'')}" placeholder="Поле, например «Серия»" aria-label="Название поля">`+
      `<button class="btn ghost sm" type="button" data-au-ffdel="${i}:${j}" aria-label="Убрать поле" title="Убрать поле"><svg class="i i15"><use href="#i-x"/></svg></button></div>`+
      `<div class="au-frow"><input type="text" data-au-ff="${i}:${j}" data-k="hint" maxlength="300" value="${esc(x.hint||'')}" placeholder="Подсказка: в каком виде" aria-label="Подсказка"><select data-au-ff="${i}:${j}" data-k="check" aria-label="Тип">${opts(x.check)}</select></div></div>`).join('')}</div>`+
      `<div><button class="btn sm" type="button" data-au-fadd="${i}"><svg class="i i15"><use href="#i-plus"/></svg>Поле</button></div>`:'';
    // Sites keep the search on them and let the step walk their pages: search and reading are on.
    const onSites=!priv&&auSiteList(s.sites).length>0;
    const tools=`<div class="au-sopts"><select data-st="${i}" data-k="model" aria-label="Модель шага">${auModelOptions(s.model,priv,'Модель автоматизации')}</select>`+
      `<label class="au-tog"><input type="checkbox" data-st="${i}" data-k="web"${s.web||onSites?' checked':''}${priv||onSites?' disabled':''}>Поиск в интернете</label>`+
      `<label class="au-tog"><input type="checkbox" data-st="${i}" data-k="read"${s.read||onSites?' checked':''}${priv||onSites||!s.web?' disabled':''}>Читать страницы</label>`+
      `<label class="au-sites"><span class="small muted" title="Шаг ищет только на этих сайтах и открывает их страницы и ссылки между ними">Сайты</span><input type="text" data-st="${i}" data-k="sites" maxlength="600" value="${esc(s.sites)}" placeholder="любые; или, например, sozd.duma.gov.ru"${priv?' disabled':''} aria-label="Сайты шага"></label></div>`;
    return `<div class="au-step">${head}<textarea data-st="${i}" data-k="prompt" rows="${s.kind==='task'?4:2}" maxlength="20000" placeholder="${esc(ph)}">${esc(s.prompt)}</textarea>${fields}${tools}${doc?'<span class="hint">Поля шаблона — ниже. Значения вы проверяете перед сборкой документа, поэтому этот шаг последний.</span>':''}</div>`;}).join('');
  $$('#au-drawer [data-au-add]').forEach(b=>b.disabled=n>=20);}
// A step's switches follow its sites in place: redrawing the steps would take the focus away.
function auSyncSites(i){const s=A.cur.steps[i],on=!byId('au-private').checked&&auSiteList(s.sites).length>0,box=byId('au-steps');
  const web=box.querySelector(`[data-st="${i}"][data-k="web"]`),read=box.querySelector(`[data-st="${i}"][data-k="read"]`);
  if(web){web.checked=s.web||on;web.disabled=on;}if(read){read.checked=s.read||on;read.disabled=on||!s.web;}}
function auStepEdit(t){
  if(t.dataset.st!==undefined){const s=A.cur.steps[+t.dataset.st],k=t.dataset.k;s[k]=t.type==='checkbox'?t.checked:t.value;
    if(k==='kind'){if(s.kind==='fields'&&!s.fields.length)s.fields=[{name:'',hint:'',check:''}];renderAuSteps();renderAuSched();}
    else if(k==='web'){if(!s.web)s.read=false;renderAuSteps();}
    else if(k==='prompt')renderAuSched();
    else if(k==='sites')auSyncSites(+t.dataset.st);
    return;}
  if(t.dataset.auFf!==undefined){const [i,j]=t.dataset.auFf.split(':').map(Number);A.cur.steps[i].fields[j][t.dataset.k]=t.value;}}
// Text is kept as it is typed; switches and lists redraw the steps when they change.
byId('au-steps').addEventListener('input',e=>{if(e.target.type!=='checkbox'&&e.target.tagName!=='SELECT')auStepEdit(e.target);});
byId('au-steps').addEventListener('change',e=>{if(e.target.type==='checkbox'||e.target.tagName==='SELECT')auStepEdit(e.target);});
byId('au-steps').addEventListener('click',e=>{const st=A.cur.steps;
  const mv=e.target.closest('[data-au-smove]'),del=e.target.closest('[data-au-sdel]'),fa=e.target.closest('[data-au-fadd]'),fd=e.target.closest('[data-au-ffdel]');
  if(mv){const [i,d]=mv.dataset.auSmove.split(':').map(Number),j=i+d;if(j<0||j>=st.length||st[j].kind==='document'||st[i].kind==='document')return;[st[i],st[j]]=[st[j],st[i]];}
  else if(del){const i=+del.dataset.auSdel;if(st.length<2||st[i].kind==='document')return;st.splice(i,1);}
  else if(fa)st[+fa.dataset.auFadd].fields.push({name:'',hint:'',check:''});
  else if(fd){const [i,j]=fd.dataset.auFfdel.split(':').map(Number);st[i].fields.splice(j,1);}
  else return;
  renderAuSteps();renderAuSched();});
// A document is always last: a new step goes before it.
byId('au-drawer').addEventListener('click',e=>{const b=e.target.closest('[data-au-add]');if(!b||!A.cur)return;const st=A.cur.steps,kind=b.dataset.auAdd;
  const s={kind,name:'',prompt:'',model:'',web:false,read:false,fields:kind==='fields'?[{name:'',hint:'',check:''}]:[]};
  st.splice(st.at(-1)?.kind==='document'?st.length-1:st.length,0,s);renderAuSteps();renderAuSched();});
// A place of a template is shown in its sentence, the text to replace marked.
function auPlace(p){const t=p.text||'',c=t.indexOf(p.context),f=c<0?-1:c+p.context.indexOf(p.find);
  const body=f<0?esc(t):esc(t.slice(0,f))+'<mark>'+esc(p.find)+'</mark>'+esc(t.slice(f+p.find.length));
  return `<div class="au-place"><span class="small faint">${esc(p.where||'абзац '+(p.para+1))}${p.all?' · во всех совпадениях':''}</span><span class="au-ptext">${body}</span></div>`;}
function renderAuFields(){if(!A.cur.steps.some(s=>s.kind==='document'))return;const f=A.cur.template?.fields||[],saved=!!A.cur.id;
  byId('au-fields-title').textContent=`Поля · ${f.length}`;byId('au-analyze').disabled=!saved;
  byId('au-fields-hint').textContent=saved?'Модель прочитала шаблон и предложила поля, их места и типы. Поправьте названия, подсказки и типы, лишнее уберите; чего не хватает — добавьте ниже. Метки вида {{ФИО}} — поля сразу.':'Сохраните — OmniBox прочитает шаблон, а модель найдёт поля и их типы.';
  byId('au-tadd').hidden=!saved;
  byId('au-fields').innerHTML=f.map((x,i)=>`<div class="au-field"><div class="au-frow"><input type="text" data-au-fname="${i}" maxlength="80" value="${esc(x.name)}" aria-label="Название поля"><button class="btn ghost sm" type="button" data-au-fdel="${i}" aria-label="Убрать поле" title="Убрать поле"><svg class="i i15"><use href="#i-x"/></svg></button></div>`+
    `<div class="au-frow"><input type="text" data-au-fhint="${i}" maxlength="300" value="${esc(x.hint||'')}" placeholder="Что вписать и в каком виде" aria-label="Подсказка модели"><select data-au-fcheck="${i}" aria-label="Тип">${auCheckOptions(x.check)}</select></div>${(x.places||[]).map(auPlace).join('')}</div>`).join('');}
const auTField=e=>{const f=A.cur.template.fields,t=e.target;
  if(t.dataset.auFname!==undefined)f[+t.dataset.auFname].name=t.value;if(t.dataset.auFhint!==undefined)f[+t.dataset.auFhint].hint=t.value;
  if(t.dataset.auFcheck!==undefined)f[+t.dataset.auFcheck].check=t.value;};
byId('au-fields').addEventListener('input',auTField);byId('au-fields').addEventListener('change',auTField);
// A field the model missed: the owner types the text as it stands in the template, OmniBox finds
// its places; a name that is there already gets the places added.
byId('au-tadd-go').addEventListener('click',async()=>{const name=byId('au-tadd-name').value.trim(),text=byId('au-tadd-text').value;
  if(!name||!text.trim()){notice('Впишите название поля и текст, который оно заменит в шаблоне.',true);return;}
  const b=byId('au-tadd-go');b.disabled=true;
  try{const d=await api('/api/automations',{action:'place',id:A.cur.id,text});const f=A.cur.template.fields,same=f.find(x=>x.name.toLowerCase()===name.toLowerCase());
    if(same)same.places=[...(same.places||[]),...d.places.filter(p=>!(same.places||[]).some(q=>q.para===p.para&&q.find===p.find))];else f.push({name,hint:'',check:byId('au-tadd-check').value,places:d.places});
    byId('au-tadd-name').value='';byId('au-tadd-text').value='';renderAuFields();notice(`Мест в шаблоне: ${d.places.length}. Сохраните автоматизацию.`);}
  catch(x){fail(x);}finally{b.disabled=false;}});
byId('au-fields').addEventListener('click',e=>{const b=e.target.closest('[data-au-fdel]');if(!b)return;A.cur.template.fields.splice(+b.dataset.auFdel,1);renderAuFields();});
// The schedule: its kind's inputs and the values of the steps' {{names}} it runs with.
function renderAuSched(){if(!A.cur)return;const ok=auSchedulable(A.cur.steps);byId('au-sched-wrap').hidden=!ok;if(!ok)return;
  const s=A.cur.schedule,on=byId('au-sched-on').checked,every=byId('au-sched-every').value;
  byId('au-sched-body').hidden=!on;byId('au-sched-days').hidden=every!=='week';byId('au-sched-hours-wrap').hidden=every!=='hours';
  byId('au-sched-at-label').textContent=every==='hours'?'Начиная с':'Время';
  byId('au-sched-days').innerHTML=AU_DAYS.map((d,i)=>`<button type="button" aria-pressed="${(s.days||[]).includes(i+1)}" data-au-day="${i+1}">${d}</button>`).join('');
  const vars=auVars(A.cur.steps.map(x=>x.prompt));
  byId('au-vars').innerHTML=vars.length?'<span class="label">Значения для запуска по расписанию</span>'+vars.map(v=>`<div class="field"><label>${esc(v)}</label><input type="text" data-au-var="${esc(v)}" maxlength="300" value="${esc(A.cur.vars[v]||'')}"></div>`).join(''):'';}
['au-sched-on','au-sched-every'].forEach(id=>byId(id).addEventListener('change',renderAuSched));
byId('au-sched-days').addEventListener('click',e=>{const b=e.target.closest('[data-au-day]');if(!b)return;const d=+b.dataset.auDay,s=A.cur.schedule;
  s.days=(s.days||[]).includes(d)?s.days.filter(x=>x!==d):[...(s.days||[]),d].sort();renderAuSched();});
byId('au-vars').addEventListener('input',e=>{const t=e.target;if(t.dataset.auVar!==undefined)A.cur.vars[t.dataset.auVar]=t.value;});
function auForm(){const d=A.cur,s=d.schedule;
  const steps=d.steps.map(x=>{const sites=auSiteList(x.sites),o={kind:x.kind,name:x.name.trim(),prompt:x.prompt,model:x.model,web:x.web||sites.length>0,read:(x.web&&x.read)||sites.length>0};
    if(sites.length)o.sites=sites;if(x.kind==='fields')o.fields=x.fields.filter(f=>f.name.trim());return o;});
  const f={kind:steps.at(-1).kind,name:byId('au-name').value.trim(),emoji:byId('au-emoji').value.trim(),private:byId('au-private').checked,model:byId('au-model').value,
    instructions:byId('au-instr').value,history:byId('au-history').checked,steps};
  if(auSchedulable(steps)){f.schedule={enabled:byId('au-sched-on').checked,every:byId('au-sched-every').value,at:byId('au-sched-at').value,days:s.days||[],hours:+byId('au-sched-hours').value||0};
    const vars={};for(const v of auVars(steps.map(x=>x.prompt)))if(d.vars[v])vars[v]=d.vars[v];f.vars=vars;}
  return f;}
byId('au-save').addEventListener('click',async()=>{const f=auForm();if(!f.name){notice('Назовите автоматизацию.',true);byId('au-name').focus();return;}
  const b=byId('au-save');b.disabled=true;
  try{if(!A.cur.id){const a=await api('/api/automations',{action:'create',...f,template:A.file||undefined});A.file=null;
      if(a.kind==='document'){openAuto(a);notice('Шаблон сохранён. Ищем поля…');await loadAuto();auAnalyze();return;}
      hideAll();notice('Автоматизация создана.');await loadAuto();return;}
    if(A.cur.template)f.fields=A.cur.template.fields;
    await api('/api/automations',{action:'save',id:A.cur.id,...f});hideAll();notice('Автоматизация сохранена.');await loadAuto();}
  catch(e){fail(e);}finally{b.disabled=false;}});
async function auAnalyze(){const b=byId('au-analyze');if(!A.cur?.id)return;b.disabled=true;const hint=byId('au-fields-hint');hint.textContent='Модель читает шаблон… Это может занять минуту-две.';
  try{const d=await api('/api/automations',{action:'analyze',id:A.cur.id,model:byId('au-model').value||C.chosen||''});A.cur.template.fields=d.fields||[];renderAuFields();
    hint.textContent=`Найдено полей: ${A.cur.template.fields.length}${d.dropped?` · отброшено мест, которых нет в шаблоне: ${d.dropped}`:''}. Проверьте и сохраните.`;}
  catch(e){fail(e);renderAuFields();}finally{b.disabled=false;}}
byId('au-analyze').addEventListener('click',auAnalyze);
byId('au-delete').addEventListener('click',async()=>{if(!A.cur?.id||!await confirmDialog({title:'Удалить автоматизацию?',text:`«${A.cur.name}», её шаблон и история будут удалены с ПК сразу, без корзины.`}))return;
  try{await api('/api/automations',{action:'delete',id:A.cur.id});hideAll();notice('Автоматизация удалена.');await loadAuto();}catch(e){fail(e);}});

// ── run ──
function openRun(a,tab){A.run={a,files:[],values:null,text:''};const steps=auSteps(a);byId('ar-title').innerHTML=`${auEmoji(a)}<span>${esc(a.name)}</span>`;byId('ar-policy').textContent=auPolicy(a);
  byId('ar-text').value='';byId('ar-status').textContent='';renderTrace([]);['ar-result','ar-answer','ar-steps'].forEach(id=>byId(id).hidden=true);byId('ar-done').hidden=true;renderRunFiles();
  byId('ar-text').placeholder=steps[0].kind==='task'?'Дополнительные данные — необязательно: текст, фото, PDF, Word, Excel, CSV.':'Данные: текстом, или приложите фото, PDF, Word, Excel, CSV.';
  byId('ar-go').textContent=steps.length===1&&a.kind!=='task'?'Распознать':'Запустить';
  byId('ar-vars').innerHTML=auStepVars(a).map(v=>`<div class="field"><label>${esc(v)}</label><input type="text" data-ar-var="${esc(v)}" maxlength="300" value="${esc(a.vars?.[v]||'')}"></div>`).join('');
  // The model is asked only when some step has none of its own and the automation has none.
  const sel=byId('ar-model');if(a.model||steps.every(s=>s.model)){auModels(sel,a.model||steps[0].model,a.private,null);sel.disabled=true;}else{sel.disabled=false;const chat=modelByKey(C.chosen);
    auModels(sel,a.private&&chat&&chat.source!=='local'?'':C.chosen,a.private,null);}
  byId('ar-tabs').hidden=!a.history;renderRunNeed();arTab(tab||'run');show(byId('au-run'));}
// A run never starts a model by itself: a model on the PC that is not running is named here with a
// button, and starting it is the owner's own action, as choosing it in the chat is.
const arModelKeys=()=>{const r=A.run,pick=byId('ar-model').value;return [...new Set(auSteps(r.a).map(s=>s.model||r.a.model||pick).filter(Boolean))];};
function renderRunNeed(){const box=byId('ar-need');if(!A.run){box.hidden=true;return;}
  const local=arModelKeys().map(modelByKey).filter(m=>m?.source==='local'),off=local.filter(m=>!m.running),m=off[0];
  box.hidden=!m;if(!m){box.innerHTML='';return;}
  const busy=A.run.starting===m.key;
  box.innerHTML=`<span>Модель ${esc(m.name)} не запущена: автоматизация сама модели не запускает.${local.length>1?' Шаги используют разные модели на ПК, а работает одна за раз — выберите для шагов одну.':''}</span>`+
    `<button class="btn sm primary" type="button" data-ar-start="${esc(m.key)}"${busy?' disabled':''}>${busy?'Запускается…':'Запустить модель'}</button>`;}
byId('ar-model').addEventListener('change',renderRunNeed);
byId('ar-need').addEventListener('click',async e=>{const b=e.target.closest('[data-ar-start]'),r=A.run;if(!b||!r||r.starting)return;const m=modelByKey(b.dataset.arStart);if(!m)return;
  r.starting=m.key;renderRunNeed();try{await startLocal(m);}catch(x){fail(x);}finally{r.starting='';renderRunNeed();}});
on('library',()=>{if(A.run)renderRunNeed();});
function arTab(t){$$('#ar-tabs [data-ar-tab]').forEach(b=>b.setAttribute('aria-pressed',String(b.dataset.arTab===t)));
  byId('ar-input').hidden=t!=='run';byId('ar-history').hidden=t!=='history';if(t==='history'){['ar-answer','ar-result','ar-steps'].forEach(id=>byId(id).hidden=true);loadHistory();}}
byId('ar-tabs').addEventListener('click',e=>{const b=e.target.closest('[data-ar-tab]');if(b)arTab(b.dataset.arTab);});
function renderRunFiles(){const r=A.run;byId('ar-files').innerHTML=(r?.files||[]).map((f,i)=>`<span class="chip">${esc(f.name)}<button type="button" data-ar-fdel="${i}" aria-label="Убрать файл"><svg class="i i15"><use href="#i-x"/></svg></button></span>`).join('');}
byId('ar-file').addEventListener('change',async e=>{const list=[...(e.target.files||[])];e.target.value='';
  for(const f of list){if(A.run.files.length>=10){notice('Не больше 10 файлов за раз.',true);break;}if(f.size>25*1024*1024){notice(`«${f.name}» больше 25 МБ.`,true);continue;}
    try{A.run.files.push({name:f.name,data:await readB64(f)});}catch(x){fail(x);}}renderRunFiles();});
byId('ar-files').addEventListener('click',e=>{const b=e.target.closest('[data-ar-fdel]');if(!b)return;A.run.files.splice(+b.dataset.arFdel,1);renderRunFiles();});
const arVarValues=()=>Object.fromEntries($$('#ar-vars [data-ar-var]').map(i=>[i.dataset.arVar,i.value]));
// A run goes on in the service; the window follows it while open and shows what it does. Closing
// the window does not stop the run: its result goes to the history when the automation keeps one.
let AR_POLL=1500;
function renderTrace(lines){const box=byId('ar-trace');box.hidden=!lines?.length;
  byId('ar-trace-list').innerHTML=(lines||[]).map(l=>`<li>${esc(l)}</li>`).join('');}
byId('ar-go').addEventListener('click',async()=>{const r=A.run;if(!r)return;const text=byId('ar-text').value.trim(),steps=auSteps(r.a);
  if(steps[0].kind!=='task'&&!text&&!r.files.length){notice('Дайте данные: текст, фото или файл.',true);return;}
  const b=byId('ar-go'),stop=byId('ar-stop'),st=byId('ar-status');b.disabled=true;renderTrace([]);
  st.textContent=(steps.length>1?`Шагов: ${steps.length}. `:'')+(auWeb(r.a)?'Модель ищет и работает… Это может занять несколько минут — окно можно закрыть, результат будет в истории.':'Модель работает… Это может занять минуту-две.');
  try{const {job}=await api('/api/automations',{action:'run',background:true,id:r.a.id,model:byId('ar-model').value,text,files:r.files,vars:arVarValues()});
    r.job=job;stop.hidden=false;let d;
    for(;;){if(A.run!==r)return;await new Promise(res=>setTimeout(res,AR_POLL));if(A.run!==r)return;
      d=await api('/api/automations',{action:'job',job});renderTrace(d.trace);if(d.done)break;}
    if(d.error){st.textContent='';fail(new Error(d.error));return;}
    st.textContent=d.run.cost_usd>0?`Стоимость: ${money(d.run.cost_usd)}`:'';showResult(d.run);if(r.a.history)loadAuto().catch(()=>{});}
  catch(e){st.textContent='';fail(e);}finally{if(A.run===r){b.disabled=false;stop.hidden=true;r.job='';}}});
byId('ar-stop').addEventListener('click',async()=>{const r=A.run;if(!r?.job)return;byId('ar-stop').hidden=true;byId('ar-status').textContent='Останавливаю…';
  try{await api('/api/automations',{action:'stop',job:r.job});}catch(e){fail(e);}});
// The trace of a run or a step, folded: where the time went, which pages opened and which not.
const auTraceHTML=lines=>lines?.length?`<details class="au-trace"><summary>Ход выполнения</summary><ol>${lines.map(l=>`<li>${esc(l)}</li>`).join('')}</ol></details>`:'';
const auValuesHTML=vals=>`<div class="au-hvals">${vals.map(v=>`<span class="faint">${esc(v.field)}</span><span>${esc(v.value)}${v.warning?` <span class="warn-text">· ${esc(v.warning)}</span>`:''}</span>`).join('')}</div>`;
// The steps before the last are shown folded: what each gave to the next. A failed one is open.
function stepsHTML(steps){return steps.map((s,i)=>`<details class="au-hrun au-sres"${s.error?' open':''}><summary>${i+1}. ${esc(s.name)}${s.error?' · <span class="warn-text">ошибка</span>':''}</summary>`+
  (s.error?`<div class="err">${esc(s.error)}</div>`:s.values?.length?auValuesHTML(s.values):`<div class="md">${md(s.text||'')}</div>`)+auTraceHTML(s.trace)+'</details>').join('');}
// With several steps the last is the result; when a step failed, every step done is shown.
const auBefore=run=>run.steps?.length>1?run.steps.slice(0,run.error?run.steps.length:-1):[];
function showResult(run){const r=A.run,box=byId('ar-steps'),before=auBefore(run);
  box.hidden=!before.length;box.innerHTML=before.length?`<span class="label">Шаги</span>${stepsHTML(before)}`:'';
  if(run.error){notice(run.error,true);if(!run.text&&!run.values?.length){byId('ar-answer').hidden=true;byId('ar-result').hidden=true;return;}}
  if(r.a.kind==='task'){r.text=run.text||'';byId('ar-answer').hidden=false;byId('ar-result').hidden=true;byId('ar-md').innerHTML=md(r.text,run.sources||[]);
    byId('ar-sources').innerHTML=sourcesHTML(run.sources||[]);return;}
  r.values=run.values||[];renderValues();}
function renderValues(){const r=A.run;byId('ar-result').hidden=false;byId('ar-answer').hidden=true;byId('ar-done').hidden=true;
  byId('ar-build').hidden=r.a.kind!=='document';
  const docs=(A.list||[]).filter(x=>x.kind==='document'&&x.id!==r.a.id);const td=byId('ar-todoc');td.hidden=r.a.kind!=='fields'||!docs.length;
  td.innerHTML='<option value="">В документ…</option>'+docs.map(x=>`<option value="${esc(x.id)}">В документ: ${esc(x.name)}</option>`).join('');
  byId('ar-values').innerHTML=r.values.map((v,i)=>`<div class="ar-row${v.missing?' miss':''}${v.warning?' bad':''}"><label for="ar-v${i}">${esc(v.field)}</label><textarea id="ar-v${i}" data-ar-v="${i}" rows="1">${esc(v.value)}</textarea>`+
    `<span class="small ${v.missing||v.warning?'warn-text':'faint'}">${esc(v.warning||(v.missing?'Нет в данных — впишите или оставьте пустым':v.source||''))}</span></div>`).join('');}
byId('ar-values').addEventListener('input',e=>{const t=e.target;if(t.dataset.arV!==undefined){const v=A.run.values[+t.dataset.arV];v.value=t.value;v.missing=!t.value.trim();}});
const auValues=()=>Object.fromEntries((A.run?.values||[]).map(v=>[v.field,v.value]));
// Fields go into a document automation by their names: the document's run opens with them.
byId('ar-todoc').addEventListener('change',e=>{const doc=A.list.find(x=>x.id===e.target.value);if(!doc)return;const got={};
  for(const v of A.run.values)got[v.field.toLowerCase()]=v.value;
  const from=A.run.a.name,values=(doc.template?.fields||[]).map(f=>{const v=got[f.name.toLowerCase()]||'';return {field:f.name,value:v,source:v?'из «'+from+'»':'',missing:!v};});
  openRun(doc);A.run.values=values;renderValues();});
byId('ar-build').addEventListener('click',async()=>{const r=A.run;if(!r?.values)return;const b=byId('ar-build');b.disabled=true;
  try{const d=await api('/api/automations',{action:'build',id:r.a.id,values:auValues()});const href='/api/automations/file?result='+encodeURIComponent(d.result);
    const done=byId('ar-done');done.hidden=false;done.innerHTML=`<a class="btn" href="${esc(href)}" download="${esc(d.name)}"><svg class="i i15"><use href="#i-download"/></svg>${esc(d.name)}</a><span class="hint">Документ хранится в памяти службы час и на диск не записывается.</span>`;
    done.querySelector('a').click?.();}catch(e){fail(e);}finally{b.disabled=false;}});
// The other formats are made here, in the browser: nothing more goes to the service.
function auSave(name,type,text){const url=URL.createObjectURL(new Blob([text],{type}));const a=document.createElement('a');a.href=url;a.download=name;document.body.append(a);a.click?.();a.remove?.();setTimeout(()=>URL.revokeObjectURL(url),1000);}
const csvCell=s=>/[";\n\r]/.test(s)?`"${s.replace(/"/g,'""')}"`:s;
function auCopy(text,name){(navigator.clipboard?.writeText?navigator.clipboard.writeText(text):Promise.reject()).then(()=>notice('Скопировано.'),()=>auSave(name,'text/plain;charset=utf-8',text));}
function auOut(kind){const r=A.run;if(!r)return;const base=r.a.name.replace(/[<>:"/\\|?*]/g,' ').trim()||'Результат';
  if(kind==='copy'){auCopy(r.text,base+'.md');return r.text;}
  if(kind==='md'){auSave(base+'.md','text/markdown;charset=utf-8',r.text);return r.text;}
  const vals=r.values||[];
  if(kind==='text'){const text=vals.map(v=>`${v.field}: ${v.value}`).join('\n');auCopy(text,base+'.txt');return text;}
  if(kind==='csv'){const text='﻿'+vals.map(v=>csvCell(v.field)).join(';')+'\r\n'+vals.map(v=>csvCell(v.value)).join(';')+'\r\n';auSave(base+'.csv','text/csv;charset=utf-8',text);return text;}
  const text=JSON.stringify(auValues(),null,2);auSave(base+'.json','application/json',text);return text;}
byId('au-run').addEventListener('click',e=>{const b=e.target.closest('[data-ar-out]');if(b)auOut(b.dataset.arOut);});
// ── history ──
async function loadHistory(){const r=A.run,box=byId('ar-history');if(!r)return;box.innerHTML='<div class="banner">Загружаем историю…</div>';
  try{const d=await api('/api/automations',{action:'runs',id:r.a.id});if(A.run!==r)return;renderHistory(d.runs||[]);loadAuto().catch(()=>{});}
  catch(e){box.innerHTML='';fail(e);}}
function renderHistory(runs){const box=byId('ar-history');
  if(!runs.length){box.innerHTML='<div class="banner">Запусков ещё не было.</div>';return;}
  box.innerHTML=runs.map(x=>{const body=x.error?`<div class="err">${esc(x.error)}</div>`:x.values?.length?auValuesHTML(x.values):`<div class="md">${md(x.text||'',x.sources||[])}</div>`;
    const last=x.steps?.length>1&&!x.error?x.steps.at(-1).trace:x.trace;
    return `<details class="au-hrun"${runs[0]===x?' open':''}><summary>${esc(auWhen(x.created))} · ${x.trigger==='schedule'?'по расписанию':'вручную'}${x.error?' · <span class="warn-text">ошибка</span>':''}${x.seen?'':' <span class="pill iris">новое</span>'}</summary>${x.input?`<div class="small faint">Данные: ${esc(x.input.slice(0,300))}</div>`:''}${stepsHTML(auBefore(x))}${body}${auTraceHTML(last)}</details>`;}).join('')+
    '<div><button class="btn ghost sm danger-text" type="button" id="ar-hdel">Удалить историю</button></div>';}
byId('ar-history').addEventListener('click',async e=>{if(!e.target.closest('#ar-hdel')||!A.run)return;
  if(!await confirmDialog({title:'Удалить историю?',text:'Все результаты этой автоматизации будут удалены с ПК сразу, без корзины.'}))return;
  try{await api('/api/automations',{action:'runs-delete',id:A.run.a.id});renderHistory([]);loadAuto().catch(()=>{});}catch(x){fail(x);}});
byId('au-list').addEventListener('click',e=>{const r=e.target.closest('[data-au-run]'),h=e.target.closest('[data-au-hist]'),ed=e.target.closest('[data-au-edit]');
  const el=r||h||ed;if(!el)return;const id=el.dataset.auRun||el.dataset.auHist||el.dataset.auEdit,a=A.list?.find(x=>x.id===id);if(!a)return;
  if(ed)openAuto(a);else openRun(a,h?'history':'run');});
// Closing the run forgets its data, values and the link to the document in this page.
function auForget(){if(!A.run)return;A.run=null;renderTrace([]);byId('ar-stop').hidden=true;byId('ar-go').disabled=false;byId('ar-text').value='';byId('ar-values').innerHTML='';byId('ar-done').innerHTML='';byId('ar-files').innerHTML='';byId('ar-md').innerHTML='';byId('ar-steps').innerHTML='';byId('ar-history').innerHTML='';}
PAGES.auto={show(){renderAuto();loadAuto().catch(fail);}};
