'use strict';
// ── automations: a Word template filled from the owner's data ──
// The template's places come from the service (and the model); a run's data, values and document
// live only in this page and the service's memory: closing the run forgets them.
const A={list:null,cur:null,file:null,run:null};
const auPolicy=a=>a.private?'Только ПК · без интернета · без журналов · файлы не хранятся':'Модель на ПК или в облаке · без интернета · файлы не хранятся';
const auEmoji=a=>`<span class="av emo">${emojiSVG(a.emoji||'📄')}</span>`;
async function loadAuto(){const d=await api('/api/automations');A.list=d.automations||[];renderAuto();}
function renderAuto(){const box=byId('au-list');if(!A.list){box.innerHTML='<div class="banner">Загружаем автоматизации…</div>';return;}
  if(!A.list.length){box.innerHTML='<div class="banner"><span>Пока нет автоматизаций. Загрузите шаблон Word — договор с пустыми местами «____» или с данными образца: модель найдёт, что в нём меняется.</span></div>';return;}
  box.innerHTML=A.list.map(a=>{const n=a.template?.fields?.length||0,m=a.model?(modelByKey(a.model)?.name||a.model.slice(a.model.indexOf(':')+1)):'модель чата';
    return `<div class="au-card">${auEmoji(a)}<div class="au-main"><b>${esc(a.name)}</b><span class="small muted">${esc(a.template?.name||'')} · ${n} ${plural(n,'поле','поля','полей')} · ${esc(m)}</span>`+
      `<span class="au-policy${a.private?' priv':''}">${a.private?'<svg class="i i15"><use href="#i-lock"/></svg>':''}${esc(auPolicy(a))}</span></div>`+
      `<div class="au-acts"><button class="btn primary sm" type="button" data-au-run="${esc(a.id)}">Запустить</button><button class="btn ghost sm" type="button" data-au-edit="${esc(a.id)}">Настроить</button></div></div>`;}).join('');}

// The model list: models on the PC always, cloud ones (the favourites) only when not private.
function auModels(sel,cur,priv,chatLabel){const list=models().filter(m=>m.source==='local'||(!priv&&m.favorite));
  if(cur&&!list.some(m=>m.key===cur)&&modelByKey(cur))list.push(modelByKey(cur));
  sel.innerHTML=(chatLabel!==null?`<option value="">${esc(chatLabel)}</option>`:'')+list.map(m=>`<option value="${esc(m.key)}">${esc(m.name)}${m.source==='local'?'':m.source==='chatgpt'?' · ChatGPT':' · облако'}${m.caps?.vision?' · видит фото':''}</option>`).join('');
  sel.value=cur||'';}

// ── settings drawer ──
function openAuto(a,file){A.cur=JSON.parse(JSON.stringify(a));A.file=file||null;const d=A.cur;
  byId('au-title').textContent=d.id?d.name:'Новая автоматизация';byId('au-name').value=d.name||'';byId('au-emoji').value=d.emoji||'';
  byId('au-private').checked=!!d.private;byId('au-instr').value=d.instructions||'';byId('au-delete').hidden=!d.id;
  const tpl=byId('au-tpl');tpl.hidden=!d.id;if(d.id){tpl.href='/api/automations/file?template='+encodeURIComponent(d.id);tpl.setAttribute('download',d.template?.name||'шаблон.docx');}
  byId('au-tpl-name').textContent=d.template?.name||'';renderAuModel();renderAuFields();show(byId('au-drawer'));}
function renderAuModel(){const priv=byId('au-private').checked;auModels(byId('au-model'),A.cur.model,priv,'Модель, выбранная в чате');
  byId('au-model-hint').textContent=priv?'Конфиденциально: только модели на этом ПК. Для фото нужна модель со зрением.':'Если не выбрана — модель, выбранная в чате. Облачная модель получит данные через OpenRouter или ChatGPT.';}
// A place is shown in its sentence, the text to replace marked.
function auPlace(p){const t=p.text||'',c=t.indexOf(p.context),f=c<0?-1:c+p.context.indexOf(p.find);
  const body=f<0?esc(t):esc(t.slice(0,f))+'<mark>'+esc(p.find)+'</mark>'+esc(t.slice(f+p.find.length));
  return `<div class="au-place"><span class="small faint">${esc(p.where||'абзац '+(p.para+1))}${p.all?' · во всех совпадениях':''}</span><span class="au-ptext">${body}</span></div>`;}
function renderAuFields(){const f=A.cur.template?.fields||[],saved=!!A.cur.id;
  byId('au-fields-title').textContent=`Поля · ${f.length}`;byId('au-analyze').disabled=!saved;
  byId('au-fields-hint').textContent=saved?'Модель прочитает шаблон и предложит места для заполнения. Метки вида {{ФИО}} — поля сразу. Лишнее уберите, названия поправьте.':'Сохраните — OmniBox прочитает шаблон, а модель найдёт поля.';
  byId('au-fields').innerHTML=f.length?f.map((x,i)=>`<div class="au-field"><div class="au-frow"><input type="text" data-au-fname="${i}" maxlength="80" value="${esc(x.name)}" aria-label="Название поля"><button class="btn ghost sm" type="button" data-au-fdel="${i}" aria-label="Убрать поле" title="Убрать поле"><svg class="i i15"><use href="#i-x"/></svg></button></div>`+
    `<input type="text" data-au-fhint="${i}" maxlength="300" value="${esc(x.hint||'')}" placeholder="Что вписать и в каком виде" aria-label="Подсказка модели">${(x.places||[]).map(auPlace).join('')}</div>`).join(''):'';}
byId('au-private').addEventListener('change',()=>{A.cur.model=byId('au-model').value;renderAuModel();});
byId('au-model').addEventListener('change',e=>{A.cur.model=e.target.value;});
byId('au-fields').addEventListener('input',e=>{const f=A.cur.template.fields,t=e.target;
  if(t.dataset.auFname!==undefined)f[+t.dataset.auFname].name=t.value;if(t.dataset.auFhint!==undefined)f[+t.dataset.auFhint].hint=t.value;});
byId('au-fields').addEventListener('click',e=>{const b=e.target.closest('[data-au-fdel]');if(!b)return;A.cur.template.fields.splice(+b.dataset.auFdel,1);renderAuFields();});
function auForm(){return {name:byId('au-name').value.trim(),emoji:byId('au-emoji').value.trim(),private:byId('au-private').checked,model:byId('au-model').value,instructions:byId('au-instr').value};}
byId('au-save').addEventListener('click',async()=>{const f=auForm();if(!f.name){notice('Назовите автоматизацию.',true);byId('au-name').focus();return;}
  const b=byId('au-save');b.disabled=true;
  try{if(!A.cur.id){const a=await api('/api/automations',{action:'create',...f,template:A.file});A.file=null;openAuto(a);notice('Шаблон сохранён. Ищем поля…');await loadAuto();auAnalyze();return;}
    const a=await api('/api/automations',{action:'save',id:A.cur.id,...f,fields:A.cur.template.fields});A.cur=a;hideAll();notice('Автоматизация сохранена.');await loadAuto();}
  catch(e){fail(e);}finally{b.disabled=false;}});
async function auAnalyze(){const b=byId('au-analyze');if(!A.cur?.id)return;b.disabled=true;const hint=byId('au-fields-hint');hint.textContent='Модель читает шаблон… Это может занять минуту-две.';
  try{const d=await api('/api/automations',{action:'analyze',id:A.cur.id,model:byId('au-model').value||C.chosen||''});A.cur.template.fields=d.fields||[];renderAuFields();
    hint.textContent=`Найдено полей: ${A.cur.template.fields.length}${d.dropped?` · отброшено мест, которых нет в шаблоне: ${d.dropped}`:''}. Проверьте и сохраните.`;}
  catch(e){fail(e);renderAuFields();}finally{b.disabled=false;}}
byId('au-analyze').addEventListener('click',auAnalyze);
byId('au-delete').addEventListener('click',async()=>{if(!A.cur?.id||!await confirmDialog({title:'Удалить автоматизацию?',text:`«${A.cur.name}» и её шаблон будут удалены с ПК сразу, без корзины.`}))return;
  try{await api('/api/automations',{action:'delete',id:A.cur.id});hideAll();notice('Автоматизация удалена.');await loadAuto();}catch(e){fail(e);}});
byId('au-new').addEventListener('change',async e=>{const f=e.target.files?.[0];e.target.value='';if(!f)return;
  if(!/\.docx$/i.test(f.name)){notice('Шаблон — файл Word .docx. Файл .doc сохраните в Word как .docx.',true);return;}
  if(f.size>25*1024*1024){notice('Шаблон больше 25 МБ.',true);return;}
  try{const data=await readB64(f);openAuto({name:f.name.replace(/\.docx$/i,''),emoji:'📄',private:true,model:'',instructions:'',template:{name:f.name,fields:[]}},{name:f.name,data});}catch(x){fail(x);}});

// ── run ──
function openRun(a){A.run={a,files:[],values:null};byId('ar-title').innerHTML=`${auEmoji(a)}<span>${esc(a.name)}</span>`;byId('ar-policy').textContent=auPolicy(a);
  byId('ar-text').value='';byId('ar-status').textContent='';byId('ar-result').hidden=true;byId('ar-done').hidden=true;renderRunFiles();
  const sel=byId('ar-model');if(a.model){auModels(sel,a.model,a.private,null);sel.disabled=true;}else{sel.disabled=false;const chat=modelByKey(C.chosen);
    auModels(sel,a.private&&chat&&chat.source!=='local'?'':C.chosen,a.private,null);}
  show(byId('au-run'));byId('ar-text').focus();}
function renderRunFiles(){const r=A.run;byId('ar-files').innerHTML=(r?.files||[]).map((f,i)=>`<span class="chip">${esc(f.name)}<button type="button" data-ar-fdel="${i}" aria-label="Убрать файл"><svg class="i i15"><use href="#i-x"/></svg></button></span>`).join('');}
byId('ar-file').addEventListener('change',async e=>{const list=[...(e.target.files||[])];e.target.value='';
  for(const f of list){if(A.run.files.length>=10){notice('Не больше 10 файлов за раз.',true);break;}if(f.size>25*1024*1024){notice(`«${f.name}» больше 25 МБ.`,true);continue;}
    try{A.run.files.push({name:f.name,data:await readB64(f)});}catch(x){fail(x);}}renderRunFiles();});
byId('ar-files').addEventListener('click',e=>{const b=e.target.closest('[data-ar-fdel]');if(!b)return;A.run.files.splice(+b.dataset.arFdel,1);renderRunFiles();});
byId('ar-go').addEventListener('click',async()=>{const r=A.run;if(!r)return;const text=byId('ar-text').value.trim();
  if(!text&&!r.files.length){notice('Дайте данные: текст, фото или файл.',true);return;}
  const b=byId('ar-go'),st=byId('ar-status');b.disabled=true;st.textContent='Модель читает данные и подбирает значения… Это может занять минуту-две.';
  try{const d=await api('/api/automations',{action:'run',id:r.a.id,model:byId('ar-model').value,text,files:r.files});
    if(A.run!==r)return;r.values=d.values||[];renderValues();st.textContent=d.cost_usd>0?`Стоимость: ${money(d.cost_usd)}`:'';}
  catch(e){st.textContent='';fail(e);}finally{b.disabled=false;}});
function renderValues(){const r=A.run;byId('ar-result').hidden=false;byId('ar-done').hidden=true;
  byId('ar-values').innerHTML=r.values.map((v,i)=>`<div class="ar-row${v.missing?' miss':''}"><label for="ar-v${i}">${esc(v.field)}</label><textarea id="ar-v${i}" data-ar-v="${i}" rows="1">${esc(v.value)}</textarea>`+
    `<span class="small ${v.missing?'warn-text':'faint'}">${esc(v.missing?'Нет в данных — впишите или оставьте место пустым':v.source||'')}</span></div>`).join('');}
byId('ar-values').addEventListener('input',e=>{const t=e.target;if(t.dataset.arV!==undefined){const v=A.run.values[+t.dataset.arV];v.value=t.value;v.missing=!t.value.trim();}});
const auValues=()=>Object.fromEntries((A.run?.values||[]).map(v=>[v.field,v.value]));
byId('ar-build').addEventListener('click',async()=>{const r=A.run;if(!r?.values)return;const b=byId('ar-build');b.disabled=true;
  try{const d=await api('/api/automations',{action:'build',id:r.a.id,values:auValues()});const href='/api/automations/file?result='+encodeURIComponent(d.result);
    const done=byId('ar-done');done.hidden=false;done.innerHTML=`<a class="btn" href="${esc(href)}" download="${esc(d.name)}"><svg class="i i15"><use href="#i-download"/></svg>${esc(d.name)}</a><span class="hint">Документ хранится в памяти службы час и на диск не записывается.</span>`;
    done.querySelector('a').click?.();}catch(e){fail(e);}finally{b.disabled=false;}});
// The values in other formats are made here, in the browser: nothing more goes to the service.
function auSave(name,type,text){const url=URL.createObjectURL(new Blob([text],{type}));const a=document.createElement('a');a.href=url;a.download=name;document.body.append(a);a.click?.();a.remove?.();setTimeout(()=>URL.revokeObjectURL(url),1000);}
const csvCell=s=>/[";\n\r]/.test(s)?`"${s.replace(/"/g,'""')}"`:s;
function auOut(kind){const r=A.run;if(!r?.values)return;const vals=r.values,base=r.a.name.replace(/[<>:"/\\|?*]/g,' ').trim()||'Данные';
  if(kind==='text'){const text=vals.map(v=>`${v.field}: ${v.value}`).join('\n');
    (navigator.clipboard?.writeText?navigator.clipboard.writeText(text):Promise.reject()).then(()=>notice('Значения скопированы.'),()=>auSave(base+'.txt','text/plain;charset=utf-8',text));return text;}
  if(kind==='csv'){const text='﻿'+vals.map(v=>csvCell(v.field)).join(';')+'\r\n'+vals.map(v=>csvCell(v.value)).join(';')+'\r\n';auSave(base+'.csv','text/csv;charset=utf-8',text);return text;}
  const text=JSON.stringify(auValues(),null,2);auSave(base+'.json','application/json',text);return text;}
byId('au-run').addEventListener('click',e=>{const b=e.target.closest('[data-ar-out]');if(b)auOut(b.dataset.arOut);});
byId('au-list').addEventListener('click',e=>{const r=e.target.closest('[data-au-run]'),ed=e.target.closest('[data-au-edit]');
  const a=A.list?.find(x=>x.id===(r||ed)?.dataset[r?'auRun':'auEdit']);if(!a)return;r?openRun(a):openAuto(a);});
// Closing the run forgets its data, values and the link to the document in this page.
function auForget(){if(!A.run)return;A.run=null;byId('ar-text').value='';byId('ar-values').innerHTML='';byId('ar-done').innerHTML='';byId('ar-files').innerHTML='';}
PAGES.auto={show(){renderAuto();loadAuto().catch(fail);}};
