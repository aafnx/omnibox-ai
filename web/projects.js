'use strict';
// Projects: chats grouped around one matter, with shared instructions and documents the model
// reads in every chat of the project. And the temporary chat, which is never written to disk.
const P={editing:null,busy:false,after:null};
const projById=id=>C.projects.find(p=>p.id===id);
const projIcon=p=>p?.emoji?`<span class="pemo">${esc(p.emoji)}</span>`:'<svg class="i"><use href="#i-folder"/></svg>';
const projChats=id=>C.threads.filter(t=>t.project_id===id).length;

// ── the list of chats: projects on top, a project opened in place of the list ──
function projRow(p){const n=projChats(p.id),f=p.files?.length||0;
  return `<li class="t proj"><a href="#chat" data-proj="${p.id}">${projIcon(p)}<span class="tt">${esc(p.name)}</span><small>${n} ${plural(n,'чат','чата','чатов')}${f?` · ${f} ${plural(f,'документ','документа','документов')}`:''}</small></a></li>`;}
function projGroup(){const key='t:Проекты',shut=collapsed.has(key);
  return `<li class="group pgroup"><button type="button" class="grp-btn tgrp" data-grp="${key}" aria-expanded="${!shut}"><span>Проекты</span><span class="grp-n">${C.projects.length||''}</span>${chev(key)}</button><button type="button" class="padd" data-proj-new title="Новый проект" aria-label="Новый проект"><svg class="i"><use href="#i-plus"/></svg></button></li>`+
    (shut?'':C.projects.map(projRow).join(''));}
function projHead(p){const f=p.files?.length||0;
  return `<li class="phead"><button type="button" class="btn ghost sm" data-proj-back><svg class="i i15"><use href="#i-left"/></svg>Все чаты</button>`+
    `<div class="pname-row">${projIcon(p)}<b>${esc(p.name)}</b><button type="button" class="padd" data-proj-edit="${p.id}" title="Указания и документы проекта" aria-label="Настроить проект"><svg class="i"><use href="#i-settings"/></svg></button></div>`+
    `<small class="faint">${f?`${f} ${plural(f,'документ','документа','документов')}`:'Без документов'}${p.instructions?' · есть указания':''}</small>`+
    `<button type="button" class="btn primary sm" data-proj-chat="${p.id}"><svg class="i i15"><use href="#i-plus"/></svg>Новый чат в проекте</button></li>`;}
function openProjectView(id){C.view=id||'';C.hits=null;byId('thread-search').value='';byId('thread-search').placeholder=id?'Поиск в проекте':'Поиск по чатам';renderThreads();if(!C.chat)renderChat();}

// ── the chat's head: its project and the temporary switch ──
const isTemp=()=>C.chat?!!C.chat.temporary:C.temp;
function chatProject(){return projById(C.chat?C.chat.project_id:C.view);}
function renderProjChip(){const b=byId('proj-chip'),p=chatProject();b.hidden=!p;if(!p)return;
  b.innerHTML=`${projIcon(p)}<span>${esc(p.name)}</span>`;b.dataset.chipProj=p.id;b.title=`Проект «${p.name}»: открыть его чаты`;}
// The switch is for a chat not yet started; a started temporary chat shows it on, fixed.
function renderTemp(){const b=byId('temp-btn'),on=isTemp();b.setAttribute('aria-pressed',String(on));b.classList.toggle('on',on);
  b.hidden=!!C.chat&&!C.chat.temporary;b.disabled=!!C.chat;byId('conv').classList.toggle('temp',on);renderProjChip();}
function emptyNote(){if(isTemp())return 'Не сохраняется на ПК и не попадает в журнал запросов. Исчезнет, когда вы откроете другой чат.';
  const p=chatProject();if(p)return `Чат в проекте «${esc(p.name)}»: модель получит указания${p.files?.length?' и документы':''} проекта.`;
  return 'Спросите что-нибудь. Можно приложить файлы и включить поиск в интернете.';}
// Leaving a temporary chat removes it from the service's memory at once.
function leaveTemp(){const c=C.chat;if(!c?.temporary)return;delete C.streams[c.id];api('/api/chats',{action:'delete',id:c.id}).catch(()=>{});}

// ── the project editor ──
function openProject(id){const p=id?projById(id):null;P.editing=p?.id||null;P.after=null;
  byId('pj-title').textContent=p?p.name:'Новый проект';byId('pj-emoji').value=p?.emoji||'';byId('pj-name').value=p?.name||'';byId('pj-instr').value=p?.instructions||'';
  byId('pj-delete').hidden=!p;countProj();renderProjFiles();show(byId('proj-drawer'));setTimeout(()=>byId('pj-name').focus(),50);}
function countProj(){const n=byId('pj-instr').value.length;const c=byId('pj-count');c.textContent=`${n} / 8000`;c.classList.toggle('danger-text',n>8000);}
function renderProjFiles(){const p=projById(P.editing),files=p?.files||[];
  byId('pj-files').innerHTML=files.length?files.map(f=>`<li><svg class="i i15"><use href="#${KIND_ICON[f.kind]||'i-file'}"/></svg><span class="fn">${esc(f.name)}</span><span class="fs">${fsize(f.size||0)} · ${fmtN(f.chars)} ${plural(f.chars,'знак','знака','знаков')}</span><button type="button" class="btn ghost sm" data-pj-fdel="${f.id}" aria-label="Удалить «${esc(f.name)}»" title="Удалить документ"><svg class="i i15"><use href="#i-trash"/></svg></button></li>`).join(''):'<li class="faint small">Документов пока нет.</li>';}
function setProjects(list){C.projects=list||[];renderThreads();}
async function saveProject(){const body={action:'save',name:byId('pj-name').value,emoji:byId('pj-emoji').value,instructions:byId('pj-instr').value};if(P.editing)body.id=P.editing;
  const r=await api('/api/projects',body);P.editing=r.project.id;setProjects(r.projects);byId('pj-title').textContent=r.project.name;byId('pj-delete').hidden=false;return r.project;}
// A document needs the project saved first: a new project is saved with the first document.
async function addProjectFiles(list){if(P.busy)return;P.busy=true;const note=byId('pj-note');
  try{if(!P.editing){if(!byId('pj-name').value.trim()){notice('Сначала назовите проект.',true);return;}await saveProject();}
    for(const f of list){if(f.size>25*1024*1024){notice(`«${f.name}» больше 25 МБ.`,true);continue;}
      const k=kindOf(f.name);if(!k||KINDS[k].need){notice(`«${f.name}»: в проект добавляются документы — PDF, Word, Excel, CSV или текст.`,true);continue;}
      note.textContent=`Читаю «${f.name}»…`;
      try{const r=await api('/api/projects',{action:'file-add',id:P.editing,name:f.name,data:await readB64(f)});setProjects(r.projects);renderProjFiles();}catch(x){fail(x);}}}
  finally{P.busy=false;note.textContent='PDF, Word, Excel, CSV или текст, до 25 МБ. На ПК хранится только их текст; модель получает документы целиком, а если они не помещаются — подходящие к вопросу фрагменты.';}}
async function deleteProject(){const p=projById(P.editing);if(!p)return;
  const ok=await confirmDialog({title:'Удалить проект?',text:`«${p.name}» и текст его документов сразу сотрутся с диска. Чаты проекта останутся — в общем списке.`,yes:'Удалить'});if(!ok)return;
  const r=await api('/api/projects',{action:'delete',id:p.id});hideAll();if(C.view===p.id)C.view='';C.threads.forEach(t=>{if(t.project_id===p.id)t.project_id='';});if(C.chat?.project_id===p.id)C.chat.project_id='';setProjects(r.projects);renderTemp();renderChat();}

// ── moving a chat: the list's menu shows the projects in place of its actions ──
function moveMenu(id){const t=C.threads.find(x=>x.id===id);const m=byId('thread-menu');if(!t)return;
  setHTML(m,C.projects.filter(p=>p.id!==t.project_id).map(p=>`<button type="button" role="menuitem" data-move-to="${p.id}" data-move-chat="${id}">${projIcon(p)}${esc(p.name)}</button>`).join('')+
    (t.project_id?`<button type="button" role="menuitem" data-move-to="" data-move-chat="${id}"><svg class="i"><use href="#i-x"/></svg>Убрать из проекта</button>`:'')+
    `<button type="button" role="menuitem" data-move-new="${id}"><svg class="i"><use href="#i-plus"/></svg>Новый проект…</button>`);}
async function moveChat(id,pid){const s=await api('/api/chats',{action:'project',id,project_id:pid});const i=C.threads.findIndex(x=>x.id===id);if(i>=0)C.threads[i]=s;
  if(C.chat?.id===id)C.chat.project_id=s.project_id||'';renderThreads();renderTemp();notice(pid?`Чат перенесён в проект «${projById(pid)?.name||''}».`:'Чат убран из проекта.');}

// ── events ──
byId('pj-instr').addEventListener('input',countProj);
byId('pj-file').addEventListener('change',e=>{const list=[...(e.target.files||[])];e.target.value='';addProjectFiles(list).catch(fail);});
// A chat moved from the list's menu to a new project goes there once the project is saved.
byId('pj-save').addEventListener('click',()=>{if(!byId('pj-name').value.trim()){notice('Назовите проект.',true);byId('pj-name').focus();return;}
  saveProject().then(async p=>{hideAll();const id=P.after;P.after=null;
    if(id)await moveChat(id,p.id);else notice(`Проект «${p.name}» сохранён.`);renderTemp();if(!C.chat?.messages?.length)renderChat();}).catch(fail);});
byId('pj-delete').addEventListener('click',()=>deleteProject().catch(fail));
byId('pj-files').addEventListener('click',e=>{const b=e.target.closest('[data-pj-fdel]');if(!b||!P.editing)return;
  api('/api/projects',{action:'file-remove',id:P.editing,file_id:b.dataset.pjFdel}).then(r=>{setProjects(r.projects);renderProjFiles();}).catch(fail);});
byId('temp-btn').addEventListener('click',()=>{if(C.chat)return;C.temp=!C.temp;renderTemp();renderChat();byId('prompt').focus();});
document.addEventListener('click',e=>{
  if(e.target.closest('[data-proj-new]')){openProject(null);return;}
  const pv=e.target.closest('[data-proj]');if(pv){e.preventDefault();openProjectView(pv.dataset.proj);return;}
  if(e.target.closest('#proj-chip')){openProjectView(byId('proj-chip').dataset.chipProj);$('.threads').classList.add('open');return;}
  if(e.target.closest('[data-proj-back]')){openProjectView('');return;}
  const pe=e.target.closest('[data-proj-edit]');if(pe){openProject(pe.dataset.projEdit);return;}
  if(e.target.closest('[data-proj-chat]')){newChat();$('.threads').classList.remove('open');return;}
  const mm=e.target.closest('[data-move-menu]');if(mm){moveMenu(mm.dataset.moveMenu);return;}
  const mt=e.target.closest('[data-move-to]');if(mt){closeThreadMenu();moveChat(mt.dataset.moveChat,mt.dataset.moveTo).catch(fail);return;}
  const mn=e.target.closest('[data-move-new]');if(mn){closeThreadMenu();const id=mn.dataset.moveNew;openProject(null);P.after=id;return;}
});
