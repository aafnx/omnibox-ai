'use strict';
// ── automations: the owner's own tasks — a task in words, fields taken from data, or a Word
// template filled — with what each may use, its history and its schedule ──
// A run's data and values live in this page only while the run is open; closing it forgets them.
const A={list:null,last:{},next:{},cur:null,file:null,run:null};
const AU_KIND={task:'Задача',fields:'Поля из данных',document:'Документ по шаблону'};
const AU_CHECKS={'':'Без проверки',digits:'Только цифры',date:'Дата',date_past:'Дата не в будущем',passport_series:'Серия паспорта',passport_number:'Номер паспорта',division_code:'Код подразделения',inn:'ИНН',snils:'СНИЛС'};
const AU_DAYS=['Пн','Вт','Ср','Чт','Пт','Сб','Вс'];
// Presets are only a start: everything in them can be changed.
const AU_PRESETS={
  passport:{kind:'fields',name:'Паспорт РФ',emoji:'🪪',private:true,
    prompt:'Распознай данные паспорта гражданина РФ на фото: разворот с фотографией и, если есть, страницу с регистрацией. Серия и номер напечатаны на развороте и вертикально на полях — сверь их. Если внизу видна машиночитаемая строка, сверь с ней ФИО и даты.',
    instructions:'Даты — ДД.ММ.ГГГГ. ФИО — как в паспорте, с заглавной буквы. «Кем выдан» — полностью, как напечатано.',
    fields:[{name:'Фамилия'},{name:'Имя'},{name:'Отчество'},{name:'Пол',hint:'МУЖ или ЖЕН'},{name:'Дата рождения',check:'date_past'},{name:'Место рождения'},
      {name:'Серия',hint:'4 цифры',check:'passport_series'},{name:'Номер',hint:'6 цифр',check:'passport_number'},{name:'Кем выдан'},{name:'Дата выдачи',check:'date_past'},
      {name:'Код подразделения',hint:'в виде 123-456',check:'division_code'},{name:'Адрес регистрации',hint:'со страницы регистрации, если она есть'}]},
  news:{kind:'task',name:'Сводка новостей',emoji:'📰',private:false,web:true,read:true,history:true,
    prompt:'Найди главные новости о {{тема}} за последнюю неделю. Сделай сводку из 5–10 пунктов: что произошло, почему это важно, ссылка на источник. Ищи и на русском, и на английском.',instructions:''}};
const auPolicy=a=>a.private?'Только ПК · без интернета · без истории · файлы не хранятся':
  [a.web?'с поиском в интернете':'без интернета',a.history?'история хранится':'без истории'].join(' · ');
const auEmoji=a=>`<span class="av emo">${emojiSVG(a.emoji||'⚡')}</span>`;
const auWhen=t=>new Date(t).toLocaleString('ru-RU',{day:'2-digit',month:'2-digit',hour:'2-digit',minute:'2-digit'});
const auVarPattern=/\{\{\s*([^{}]{1,80}?)\s*\}\}/g;
function auVars(prompt){const out=[];for(const m of String(prompt||'').matchAll(auVarPattern))if(!out.includes(m[1]))out.push(m[1]);return out;}
function auSchedText(s){if(!s?.enabled)return '';const at=s.at||'';
  if(s.every==='hours')return `каждые ${s.hours} ч от ${at}`;if(s.every==='week')return `${(s.days||[]).map(d=>AU_DAYS[d-1]).join(', ')} в ${at}`;return `каждый день в ${at}`;}

async function loadAuto(){const d=await api('/api/automations');A.list=d.automations||[];A.last=d.last||{};A.next=d.next||{};renderAuto();}
function renderAuto(){const box=byId('au-list');if(!A.list){box.innerHTML='<div class="banner">Загружаем автоматизации…</div>';return;}
  if(!A.list.length){box.innerHTML='<div class="banner"><span>Пока нет автоматизаций. «Новая автоматизация» — задача своими словами, поля из данных или документ по шаблону Word; можно начать с заготовки.</span></div>';return;}
  box.innerHTML=A.list.map(a=>{const m=a.model?(modelByKey(a.model)?.name||a.model.slice(a.model.indexOf(':')+1)):'модель чата',last=A.last[a.id],sched=auSchedText(a.schedule);
    const n=(a.kind==='document'?a.template?.fields:a.fields)?.length||0,what=a.kind==='task'?'':` · ${n} ${plural(n,'поле','поля','полей')}`;
    return `<div class="au-card">${auEmoji(a)}<div class="au-main"><b>${esc(a.name)}${last?.unseen?` <span class="pill iris">новых: ${last.unseen}</span>`:''}</b>`+
      `<span class="small muted">${esc(AU_KIND[a.kind]||'')}${esc(what)} · ${esc(m)}</span>`+
      `<span class="au-policy${a.private?' priv':''}">${a.private?'<svg class="i i15"><use href="#i-lock"/></svg>':''}${esc(auPolicy(a))}</span>`+
      (sched?`<span class="small muted"><svg class="i i15"><use href="#i-clock"/></svg> ${esc(sched)}${A.next[a.id]?` · следующий: ${esc(auWhen(A.next[a.id]))}`:''}</span>`:'')+
      (last?`<span class="small ${last.error?'warn-text':'faint'}">Последний запуск: ${esc(auWhen(last.created))}${last.trigger==='schedule'?' по расписанию':''}${last.error?' — ошибка':''}</span>`:'')+`</div>`+
      `<div class="au-acts"><button class="btn primary sm" type="button" data-au-run="${esc(a.id)}">Запустить</button>${a.history?`<button class="btn ghost sm" type="button" data-au-hist="${esc(a.id)}">История</button>`:''}<button class="btn ghost sm" type="button" data-au-edit="${esc(a.id)}">Настроить</button></div></div>`;}).join('');}

// The model list: models on the PC always, cloud ones (the favourites) only when not private.
function auModels(sel,cur,priv,chatLabel){const list=models().filter(m=>m.source==='local'||(!priv&&m.favorite));
  if(cur&&!list.some(m=>m.key===cur)&&modelByKey(cur))list.push(modelByKey(cur));
  sel.innerHTML=(chatLabel!==null?`<option value="">${esc(chatLabel)}</option>`:'')+list.map(m=>`<option value="${esc(m.key)}">${esc(m.name)}${m.source==='local'?'':m.source==='chatgpt'?' · ChatGPT':' · облако'}${m.caps?.vision?' · видит фото':''}</option>`).join('');
  sel.value=cur||'';}

// ── new ──
byId('au-new-btn').addEventListener('click',()=>{const m=byId('au-new-menu');m.hidden=!m.hidden;byId('au-new-btn').setAttribute('aria-expanded',String(!m.hidden));});
document.addEventListener('click',e=>{if(!e.target.closest?.('.au-newwrap')){byId('au-new-menu').hidden=true;byId('au-new-btn').setAttribute('aria-expanded','false');}});
byId('au-new-menu').addEventListener('click',e=>{const k=e.target.closest('[data-au-new]'),p=e.target.closest('[data-au-preset]');if(!k&&!p)return;byId('au-new-menu').hidden=true;
  if(p){openAuto(JSON.parse(JSON.stringify(AU_PRESETS[p.dataset.auPreset])));return;}
  const kind=k.dataset.auNew;openAuto(kind==='task'?{kind,name:'',emoji:'⚡',prompt:'',private:false}:{kind,name:'',emoji:'🧾',private:true,fields:[{name:'',hint:'',check:''}]});});
byId('au-new').addEventListener('change',async e=>{const f=e.target.files?.[0];e.target.value='';byId('au-new-menu').hidden=true;if(!f)return;
  if(!/\.docx$/i.test(f.name)){notice('Шаблон — файл Word .docx. Файл .doc сохраните в Word как .docx.',true);return;}
  if(f.size>25*1024*1024){notice('Шаблон больше 25 МБ.',true);return;}
  try{const data=await readB64(f);openAuto({kind:'document',name:f.name.replace(/\.docx$/i,''),emoji:'📄',private:true,template:{name:f.name,fields:[]}},{name:f.name,data});}catch(x){fail(x);}});

// ── settings drawer ──
function openAuto(a,file){A.cur=JSON.parse(JSON.stringify(a));A.file=file||null;const d=A.cur,k=d.kind;
  d.fields=d.fields||[];d.schedule=d.schedule||{enabled:false,every:'day',at:'08:00',days:[1,2,3,4,5],hours:4};d.vars=d.vars||{};
  byId('au-kind').textContent='Автоматизация · '+(AU_KIND[k]||'').toLowerCase();byId('au-title').textContent=d.id?d.name:'Новая автоматизация';
  byId('au-name').value=d.name||'';byId('au-emoji').value=d.emoji||'';byId('au-prompt').value=d.prompt||'';byId('au-instr').value=d.instructions||'';
  byId('au-private').checked=!!d.private;byId('au-web').checked=!!d.web;byId('au-read').checked=!!d.read;byId('au-history').checked=!!d.history;
  byId('au-prompt-wrap').hidden=k==='document';byId('au-prompt-label').textContent=k==='task'?'Задача':'Что сделать (необязательно)';
  byId('au-ffields-wrap').hidden=k!=='fields';byId('au-doc-wrap').hidden=k!=='document';byId('au-sched-wrap').hidden=k!=='task';byId('au-delete').hidden=!d.id;
  const tpl=byId('au-tpl');tpl.hidden=!d.id||k!=='document';if(d.id&&k==='document'){tpl.href='/api/automations/file?template='+encodeURIComponent(d.id);tpl.setAttribute('download',d.template?.name||'шаблон.docx');}
  byId('au-tpl-name').textContent=d.template?.name||'';
  const s=d.schedule;byId('au-sched-on').checked=!!s.enabled;byId('au-sched-every').value=s.every||'day';byId('au-sched-at').value=s.at||'08:00';byId('au-sched-hours').value=String(s.hours||4);
  renderAuModel();renderAuFields();renderAuFFields();renderAuSched();syncAuPolicy();show(byId('au-drawer'));}
function renderAuModel(){const priv=byId('au-private').checked;auModels(byId('au-model'),A.cur.model,priv,'Модель, выбранная в чате');
  byId('au-model-hint').textContent=priv?'Конфиденциально: только модели на этом ПК. Для фото нужна модель со зрением.':'Если не выбрана — модель, выбранная в чате. Облачная модель получит данные через OpenRouter или ChatGPT.';}
// Private switches off everything that leaves the PC or stays on it; the service does the same.
function syncAuPolicy(){const priv=byId('au-private').checked;
  for(const id of ['au-web','au-read','au-history','au-sched-on']){const el=byId(id);el.disabled=priv;if(priv)el.checked=false;}
  if(!byId('au-web').checked){byId('au-read').checked=false;byId('au-read').disabled=true;}
  byId('au-web-hint').textContent=priv?'Выключено: конфиденциальная автоматизация не выходит в интернет':'Модель может искать в интернете, как в чате';renderAuSched();}
['au-private','au-web'].forEach(id=>byId(id).addEventListener('change',()=>{if(id==='au-private'){A.cur.model=byId('au-model').value;renderAuModel();}syncAuPolicy();}));
byId('au-model').addEventListener('change',e=>{A.cur.model=e.target.value;});
// A place of a template is shown in its sentence, the text to replace marked.
function auPlace(p){const t=p.text||'',c=t.indexOf(p.context),f=c<0?-1:c+p.context.indexOf(p.find);
  const body=f<0?esc(t):esc(t.slice(0,f))+'<mark>'+esc(p.find)+'</mark>'+esc(t.slice(f+p.find.length));
  return `<div class="au-place"><span class="small faint">${esc(p.where||'абзац '+(p.para+1))}${p.all?' · во всех совпадениях':''}</span><span class="au-ptext">${body}</span></div>`;}
function renderAuFields(){if(A.cur.kind!=='document')return;const f=A.cur.template?.fields||[],saved=!!A.cur.id;
  byId('au-fields-title').textContent=`Поля · ${f.length}`;byId('au-analyze').disabled=!saved;
  byId('au-fields-hint').textContent=saved?'Модель прочитает шаблон и предложит места для заполнения. Метки вида {{ФИО}} — поля сразу. Лишнее уберите, названия поправьте.':'Сохраните — OmniBox прочитает шаблон, а модель найдёт поля.';
  byId('au-fields').innerHTML=f.map((x,i)=>`<div class="au-field"><div class="au-frow"><input type="text" data-au-fname="${i}" maxlength="80" value="${esc(x.name)}" aria-label="Название поля"><button class="btn ghost sm" type="button" data-au-fdel="${i}" aria-label="Убрать поле" title="Убрать поле"><svg class="i i15"><use href="#i-x"/></svg></button></div>`+
    `<input type="text" data-au-fhint="${i}" maxlength="300" value="${esc(x.hint||'')}" placeholder="Что вписать и в каком виде" aria-label="Подсказка модели">${(x.places||[]).map(auPlace).join('')}</div>`).join('');}
function renderAuFFields(){if(A.cur.kind!=='fields')return;const opts=v=>Object.entries(AU_CHECKS).map(([k,l])=>`<option value="${k}"${k===(v||'')?' selected':''}>${esc(l)}</option>`).join('');
  byId('au-ffields').innerHTML=A.cur.fields.map((x,i)=>`<div class="au-field"><div class="au-frow"><input type="text" data-au-ff="${i}" data-k="name" maxlength="80" value="${esc(x.name||'')}" placeholder="Название, например «Серия»" aria-label="Название поля">`+
    `<button class="btn ghost sm" type="button" data-au-ffdel="${i}" aria-label="Убрать поле" title="Убрать поле"><svg class="i i15"><use href="#i-x"/></svg></button></div>`+
    `<div class="au-frow"><input type="text" data-au-ff="${i}" data-k="hint" maxlength="300" value="${esc(x.hint||'')}" placeholder="Подсказка: в каком виде" aria-label="Подсказка"><select data-au-ff="${i}" data-k="check" aria-label="Проверка">${opts(x.check)}</select></div></div>`).join('');}
byId('au-fadd').addEventListener('click',()=>{A.cur.fields.push({name:'',hint:'',check:''});renderAuFFields();});
const auFF=e=>{const t=e.target;if(t.dataset.auFf!==undefined)A.cur.fields[+t.dataset.auFf][t.dataset.k]=t.value;};
byId('au-ffields').addEventListener('input',auFF);byId('au-ffields').addEventListener('change',auFF);
byId('au-ffields').addEventListener('click',e=>{const b=e.target.closest('[data-au-ffdel]');if(!b)return;A.cur.fields.splice(+b.dataset.auFfdel,1);renderAuFFields();});
byId('au-fields').addEventListener('input',e=>{const f=A.cur.template.fields,t=e.target;
  if(t.dataset.auFname!==undefined)f[+t.dataset.auFname].name=t.value;if(t.dataset.auFhint!==undefined)f[+t.dataset.auFhint].hint=t.value;});
byId('au-fields').addEventListener('click',e=>{const b=e.target.closest('[data-au-fdel]');if(!b)return;A.cur.template.fields.splice(+b.dataset.auFdel,1);renderAuFields();});
// The schedule: its kind's inputs and the values of the task's {{names}} it runs with.
function renderAuSched(){if(!A.cur||A.cur.kind!=='task')return;const s=A.cur.schedule,on=byId('au-sched-on').checked,every=byId('au-sched-every').value;
  byId('au-sched-body').hidden=!on;byId('au-sched-days').hidden=every!=='week';byId('au-sched-hours-wrap').hidden=every!=='hours';
  byId('au-sched-at-label').textContent=every==='hours'?'Начиная с':'Время';
  byId('au-sched-days').innerHTML=AU_DAYS.map((d,i)=>`<button type="button" aria-pressed="${(s.days||[]).includes(i+1)}" data-au-day="${i+1}">${d}</button>`).join('');
  const vars=auVars(byId('au-prompt').value);
  byId('au-vars').innerHTML=vars.length?'<span class="label">Значения для запуска по расписанию</span>'+vars.map(v=>`<div class="field"><label>${esc(v)}</label><input type="text" data-au-var="${esc(v)}" maxlength="300" value="${esc(A.cur.vars[v]||'')}"></div>`).join(''):'';}
['au-sched-on','au-sched-every'].forEach(id=>byId(id).addEventListener('change',renderAuSched));
byId('au-prompt').addEventListener('input',()=>{if(A.cur?.kind==='task')renderAuSched();});
byId('au-sched-days').addEventListener('click',e=>{const b=e.target.closest('[data-au-day]');if(!b)return;const d=+b.dataset.auDay,s=A.cur.schedule;
  s.days=(s.days||[]).includes(d)?s.days.filter(x=>x!==d):[...(s.days||[]),d].sort();renderAuSched();});
byId('au-vars').addEventListener('input',e=>{const t=e.target;if(t.dataset.auVar!==undefined)A.cur.vars[t.dataset.auVar]=t.value;});
function auForm(){const d=A.cur,k=d.kind,s=d.schedule;
  const f={kind:k,name:byId('au-name').value.trim(),emoji:byId('au-emoji').value.trim(),private:byId('au-private').checked,model:byId('au-model').value,
    prompt:k==='document'?'':byId('au-prompt').value,instructions:byId('au-instr').value,web:byId('au-web').checked,read:byId('au-read').checked,history:byId('au-history').checked};
  if(k==='task'){f.schedule={enabled:byId('au-sched-on').checked,every:byId('au-sched-every').value,at:byId('au-sched-at').value,days:s.days||[],hours:+byId('au-sched-hours').value||0};
    const vars={};for(const v of auVars(f.prompt))if(d.vars[v])vars[v]=d.vars[v];f.vars=vars;}
  if(k==='fields')f.fields=d.fields.filter(x=>x.name.trim());
  return f;}
byId('au-save').addEventListener('click',async()=>{const f=auForm();if(!f.name){notice('Назовите автоматизацию.',true);byId('au-name').focus();return;}
  const b=byId('au-save');b.disabled=true;
  try{if(!A.cur.id){const a=await api('/api/automations',{action:'create',...f,template:A.file||undefined});A.file=null;
      if(a.kind==='document'){openAuto(a);notice('Шаблон сохранён. Ищем поля…');await loadAuto();auAnalyze();return;}
      hideAll();notice('Автоматизация создана.');await loadAuto();return;}
    if(A.cur.kind==='document')f.fields=A.cur.template.fields;
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
function openRun(a,tab){A.run={a,files:[],values:null,text:''};byId('ar-title').innerHTML=`${auEmoji(a)}<span>${esc(a.name)}</span>`;byId('ar-policy').textContent=auPolicy(a);
  byId('ar-text').value='';byId('ar-status').textContent='';['ar-result','ar-answer'].forEach(id=>byId(id).hidden=true);byId('ar-done').hidden=true;renderRunFiles();
  byId('ar-text').placeholder=a.kind==='task'?'Дополнительные данные — необязательно: текст, фото, Word, Excel, CSV.':'Данные: текстом, или приложите фото, Word, Excel, CSV.';
  byId('ar-go').textContent=a.kind==='task'?'Запустить':'Распознать';
  byId('ar-vars').innerHTML=auVars(a.prompt).map(v=>`<div class="field"><label>${esc(v)}</label><input type="text" data-ar-var="${esc(v)}" maxlength="300" value="${esc(a.vars?.[v]||'')}"></div>`).join('');
  const sel=byId('ar-model');if(a.model){auModels(sel,a.model,a.private,null);sel.disabled=true;}else{sel.disabled=false;const chat=modelByKey(C.chosen);
    auModels(sel,a.private&&chat&&chat.source!=='local'?'':C.chosen,a.private,null);}
  byId('ar-tabs').hidden=!a.history;arTab(tab||'run');show(byId('au-run'));}
function arTab(t){$$('#ar-tabs [data-ar-tab]').forEach(b=>b.setAttribute('aria-pressed',String(b.dataset.arTab===t)));
  byId('ar-input').hidden=t!=='run';byId('ar-history').hidden=t!=='history';if(t==='history'){byId('ar-answer').hidden=true;byId('ar-result').hidden=true;loadHistory();}}
byId('ar-tabs').addEventListener('click',e=>{const b=e.target.closest('[data-ar-tab]');if(b)arTab(b.dataset.arTab);});
function renderRunFiles(){const r=A.run;byId('ar-files').innerHTML=(r?.files||[]).map((f,i)=>`<span class="chip">${esc(f.name)}<button type="button" data-ar-fdel="${i}" aria-label="Убрать файл"><svg class="i i15"><use href="#i-x"/></svg></button></span>`).join('');}
byId('ar-file').addEventListener('change',async e=>{const list=[...(e.target.files||[])];e.target.value='';
  for(const f of list){if(A.run.files.length>=10){notice('Не больше 10 файлов за раз.',true);break;}if(f.size>25*1024*1024){notice(`«${f.name}» больше 25 МБ.`,true);continue;}
    try{A.run.files.push({name:f.name,data:await readB64(f)});}catch(x){fail(x);}}renderRunFiles();});
byId('ar-files').addEventListener('click',e=>{const b=e.target.closest('[data-ar-fdel]');if(!b)return;A.run.files.splice(+b.dataset.arFdel,1);renderRunFiles();});
const arVarValues=()=>Object.fromEntries($$('#ar-vars [data-ar-var]').map(i=>[i.dataset.arVar,i.value]));
byId('ar-go').addEventListener('click',async()=>{const r=A.run;if(!r)return;const text=byId('ar-text').value.trim();
  if(r.a.kind!=='task'&&!text&&!r.files.length){notice('Дайте данные: текст, фото или файл.',true);return;}
  const b=byId('ar-go'),st=byId('ar-status');b.disabled=true;st.textContent=r.a.web?'Модель ищет и работает… Это может занять несколько минут.':'Модель работает… Это может занять минуту-две.';
  try{const d=await api('/api/automations',{action:'run',id:r.a.id,model:byId('ar-model').value,text,files:r.files,vars:arVarValues()});
    if(A.run!==r)return;st.textContent=d.run.cost_usd>0?`Стоимость: ${money(d.run.cost_usd)}`:'';showResult(d.run);}
  catch(e){st.textContent='';fail(e);}finally{b.disabled=false;}});
function showResult(run){const r=A.run;if(run.error&&!run.text&&!run.values){notice(run.error,true);return;}
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
  box.innerHTML=runs.map(x=>{const body=x.error?`<div class="err">${esc(x.error)}</div>`:x.values?.length?`<div class="au-hvals">${x.values.map(v=>`<span class="faint">${esc(v.field)}</span><span>${esc(v.value)}${v.warning?` <span class="warn-text">· ${esc(v.warning)}</span>`:''}</span>`).join('')}</div>`:`<div class="md">${md(x.text||'',x.sources||[])}</div>`;
    return `<details class="au-hrun"${runs[0]===x?' open':''}><summary>${esc(auWhen(x.created))} · ${x.trigger==='schedule'?'по расписанию':'вручную'}${x.error?' · <span class="warn-text">ошибка</span>':''}${x.seen?'':' <span class="pill iris">новое</span>'}</summary>${x.input?`<div class="small faint">Данные: ${esc(x.input.slice(0,300))}</div>`:''}${body}</details>`;}).join('')+
    '<div><button class="btn ghost sm danger-text" type="button" id="ar-hdel">Удалить историю</button></div>';}
byId('ar-history').addEventListener('click',async e=>{if(!e.target.closest('#ar-hdel')||!A.run)return;
  if(!await confirmDialog({title:'Удалить историю?',text:'Все результаты этой автоматизации будут удалены с ПК сразу, без корзины.'}))return;
  try{await api('/api/automations',{action:'runs-delete',id:A.run.a.id});renderHistory([]);loadAuto().catch(()=>{});}catch(x){fail(x);}});
byId('au-list').addEventListener('click',e=>{const r=e.target.closest('[data-au-run]'),h=e.target.closest('[data-au-hist]'),ed=e.target.closest('[data-au-edit]');
  const el=r||h||ed;if(!el)return;const id=el.dataset.auRun||el.dataset.auHist||el.dataset.auEdit,a=A.list?.find(x=>x.id===id);if(!a)return;
  if(ed)openAuto(a);else openRun(a,h?'history':'run');});
// Closing the run forgets its data, values and the link to the document in this page.
function auForget(){if(!A.run)return;A.run=null;byId('ar-text').value='';byId('ar-values').innerHTML='';byId('ar-done').innerHTML='';byId('ar-files').innerHTML='';byId('ar-md').innerHTML='';byId('ar-history').innerHTML='';}
PAGES.auto={show(){renderAuto();loadAuto().catch(fail);}};
