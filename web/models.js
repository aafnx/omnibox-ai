'use strict';
// Models: one list of local and OpenRouter models, a model's own settings, adding models.
const M={engineF:'all',capF:null,cloudQ:'',drawer:null,adv:{},ctx:0,advOn:false,applyKey:'',addEngine:'prismml',disk:null};
const PRESETS=[8,16,32,64,128,256].map(k=>k*1024);
const GIB=1073741824;
const CACHE_BYTES={f16:2,bf16:2,q8_0:34/32,q5_1:24/32,q5_0:22/32,q4_1:20/32,iq4_nl:18/32,q4_0:18/32};
const sys=()=>S.library?.system||{vram_bytes:16*GIB,ram_bytes:32*GIB,desktop_vram_bytes:0.75*GIB};

// ── advanced settings: what the owner sees, and how it is stored ──
const ADV=[
 {g:'Память и скорость',k:'gpu_layers',l:'Слоёв на видеокарте',t:'num',d:'все',p:[['все','вся модель на видеокарте — быстрее всего'],['40','если модель чуть не помещается'],['20','половина на процессоре — медленно'],['0','только процессор — очень медленно']],tip:'Сколько слоёв модели держать в видеопамяти, остальные считает процессор.\nВлияние: каждый слой на CPU замедляет ответ. «Все» — максимальная скорость, если модель помещается.'},
 {g:'Память и скорость',k:'n_cpu_moe',l:'Эксперты MoE в RAM',t:'num',d:'0',p:[['0','все эксперты на видеокарте'],['8','немного освободить видеопамять'],['16','больше места под контекст'],['все','все эксперты в RAM — максимум контекста']],tip:'Для MoE-моделей: сколько слоёв экспертов вынести в оперативную память.\nВлияние: освобождает видеопамять под контекст почти без потери скорости. Для обычных моделей не действует.'},
 {g:'Память и скорость',k:'cache_key',l:'Кеш контекста: ключи',t:'sel',o:['f16','q8_0','q4_0'],d:'q8_0',kv:1,tip:'Точность хранения ключей в кеше контекста.\nf16 — точнее, занимает вдвое больше памяти.\nq8_0 — почти без потерь, вдвое экономнее (рекомендуем).\nq4_0 — ещё вдвое меньше, качество длинного контекста заметно падает.'},
 {g:'Память и скорость',k:'cache_value',l:'Кеш контекста: значения',t:'sel',o:['f16','q8_0','q4_0'],d:'q8_0',kv:1,tip:'То же для значений кеша. Сжатие значений меньше влияет на качество, чем сжатие ключей.\nВлияние: q8_0 и q4_0 позволяют длиннее контекст в той же видеопамяти. Сжатый кеш требует Flash Attention.'},
 {g:'Память и скорость',k:'flash_attention',l:'Flash Attention',t:'sel',o:['авто','вкл','выкл'],d:'вкл',tip:'Быстрый расчёт внимания без промежуточных матриц.\nВлияние: меньше видеопамяти и быстрее на длинном контексте. Нужен для сжатого кеша.'},
 {g:'Память и скорость',k:'kv_offload',l:'Кеш на видеокарте',t:'sel',o:['да','нет'],d:'да',tip:'Где хранить кеш контекста.\nВлияние: «нет» переносит кеш в оперативную память — освобождает видеопамять, но генерация замедляется в несколько раз.'},
 {g:'Память и скорость',k:'batch',l:'Пакет чтения запроса',t:'num',d:'2048',p:[['512','экономнее по памяти'],['1024','для больших моделей'],['2048','баланс (рекомендуем)'],['4096','быстрее чтение длинных запросов']],tip:'Сколько токенов запроса обрабатывать за один шаг.\nВлияние: больше — быстрее читается длинный запрос, но нужно больше видеопамяти.'},
 {g:'Память и скорость',k:'ubatch',l:'Микропакет',t:'num',d:'512',p:[['128','минимум памяти'],['256','экономно'],['512','баланс для RTX 5080'],['1024','быстрее, больше пиковая память']],tip:'Размер части пакета, которую видеокарта считает за раз.\nВлияние: 512 — хороший баланс. Больше — чуть быстрее чтение, больше пиковая видеопамять.'},
 {g:'Память и скорость',k:'threads',l:'Потоки процессора',t:'num',d:'авто',p:[['авто','по числу физических ядер'],['4','оставить ядра другим программам'],['8','обычно оптимально'],['16','все ядра']],tip:'Сколько ядер CPU использовать для слоёв и экспертов в RAM.\nВлияние: важно, только если часть модели на процессоре.'},
 {g:'Память и скорость',k:'load_mode',l:'Загрузка модели',t:'sel',o:['с диска по мере нужды','целиком в память'],d:'с диска по мере нужды',tip:'Как читать файл модели.\n«По мере нужды» — быстрый старт, файл читается системой постепенно.\n«Целиком» — дольше запуск, но первый ответ без задержек на чтение с диска.'},
 {g:'Память и скорость',k:'idle_minutes',l:'Выгружать при простое',t:'num',d:'никогда',p:[['никогда','модель остаётся в памяти'],['15','через 15 минут без запросов'],['30','через полчаса'],['60','через час']],tip:'Через сколько минут без запросов модель выгружается из видеопамяти; движок продолжает работать.\nСледующий запрос загрузит её снова — первый ответ придёт на 10–30 секунд позже.'},
 {g:'Ответы',k:'temperature',l:'Температура',t:'num',d:'как в модели',p:[['0.2','точно: код, факты, расчёты'],['0.5','сдержанно'],['0.7','баланс для общения'],['1.0','творчески: тексты, идеи']],tip:'Насколько случайны ответы.\n0,2–0,5 — точнее и предсказуемее.\n0,8–1,0 — разнообразнее.\nНа скорость не влияет.'},
 {g:'Ответы',k:'top_p',l:'Top-P',t:'num',d:'как в модели',p:[['0.8','строго'],['0.9','чуть строже обычного'],['0.95','обычно'],['1.0','выключен']],tip:'Выбор слова только из самых вероятных, пока их суммарная вероятность не достигнет P.\nМеньше — строже ответы.'},
 {g:'Ответы',k:'top_k',l:'Top-K',t:'num',d:'как в модели',p:[['20','строго'],['40','обычно'],['64','разнообразнее'],['0','выключен']],tip:'Выбор только из K самых вероятных слов. 20–40 — обычные значения.'},
 {g:'Ответы',k:'min_p',l:'Min-P',t:'num',d:'как в модели',p:[['0','выключен'],['0.02','мягко'],['0.05','обычно'],['0.1','строго']],tip:'Отбрасывает слова, вероятность которых меньше доли P от самого вероятного.'},
 {g:'Ответы',k:'repeat_penalty',l:'Штраф за повтор',t:'num',d:'как в модели',p:[['1.0','выключен'],['1.05','мягко'],['1.1','против зацикливания'],['1.15','сильно — может портить текст']],tip:'Снижает вероятность слов, которые уже были в ответе.\n1,0 — выключен.'},
 {g:'Ответы',k:'presence_penalty',l:'Штраф за присутствие',t:'num',d:'0',p:[['0','выключен'],['0.5','чуть больше новых тем'],['1.0','заметно'],['1.5','сильно']],tip:'Подталкивает модель к новым темам, если слово уже встречалось.'},
 {g:'Ответы',k:'frequency_penalty',l:'Штраф за частоту',t:'num',d:'0',p:[['0','выключен'],['0.3','мягко'],['0.5','против повторов'],['1.0','сильно']],tip:'Чем чаще слово встречалось, тем сильнее штраф.'},
 {g:'Ответы',k:'max_tokens',l:'Предел длины ответа',t:'num',d:'без предела',p:[['без предела','как решит модель'],['1024','короткие ответы'],['4096','обычные ответы'],['16384','длинные тексты и код']],tip:'Максимум токенов в одном ответе.\nСлишком маленький обрежет ответ на полуслове.'},
 {g:'Ответы',k:'seed',l:'Зерно случайности',t:'num',d:'случайное',p:[['случайное','каждый ответ разный'],['42','повторяемые ответы для сравнения']],tip:'Одинаковое число даёт одинаковый ответ на одинаковый вопрос.'},
 {g:'Размышления',k:'reasoning_effort',l:'Глубина размышлений',t:'sel',o:['как в модели','низкая','средняя','высокая'],d:'как в модели',tip:'Сколько модель думает перед ответом для клиентов API (в чате OmniBox — шкала у поля ввода).\nВыше — точнее на сложных задачах, но ответ позже.'},
 {g:'Размышления',k:'reasoning_budget',l:'Бюджет размышлений',t:'num',d:'без предела',p:[['без предела','думает сколько нужно'],['1024','быстро'],['4096','обычно'],['8192','глубоко']],tip:'Максимум токенов на размышления.\nСлишком маленький бюджет обрывает рассуждение.'},
];
const GP={
 'Память и скорость':[{n:'Базовый',tip:'Подобрано под модель и видеокарту: всё на видеокарте, кеш q8_0, Flash Attention.',v:{}},
  {n:'Длинный контекст',tip:'Сжимает кеш значений до q4_0 и уменьшает пакет — помещается примерно на треть больше контекста.',v:{cache_value:'q4_0',batch:'1024',ubatch:'256'}},
  {n:'Максимум скорости',tip:'Большие пакеты и модель целиком в памяти: быстрее чтение запроса, но больше видеопамяти.',v:{batch:'4096',ubatch:'1024',load_mode:'целиком в память'}}],
 'Ответы':[{n:'Как в модели',tip:'Значения, которые рекомендовал автор модели.',v:{}},
  {n:'Точно',tip:'Для кода, фактов и расчётов.',v:{temperature:'0.2',top_p:'0.9',top_k:'20',min_p:'0.05',repeat_penalty:'1.05'}},
  {n:'Баланс',tip:'Для обычного общения.',v:{temperature:'0.7',top_p:'0.95',top_k:'40',min_p:'0.05',repeat_penalty:'1.05'}},
  {n:'Творчески',tip:'Для текстов и идей.',v:{temperature:'1.0',top_p:'0.95',top_k:'64',min_p:'0.02',repeat_penalty:'1.1',presence_penalty:'0.5'}}],
 'Размышления':[{n:'Как в модели',tip:'Модель думает столько, сколько задумал автор.',v:{}},
  {n:'Быстро',tip:'Короткие размышления: ответ раньше, сложные задачи хуже.',v:{reasoning_effort:'низкая',reasoning_budget:'1024'}},
  {n:'Глубоко',tip:'Долгие размышления: точнее, ответ заметно позже.',v:{reasoning_effort:'высокая',reasoning_budget:'без предела'}}]};
const WORDS={flash_attention:{'авто':'auto','вкл':'on','выкл':'off'},kv_offload:{'да':'on','нет':'off'},load_mode:{'целиком в память':'none'},reasoning_effort:{'низкая':'low','средняя':'medium','высокая':'high'}};
const AUTO={gpu_layers:'все',n_cpu_moe:'все'};
// Stored settings → the form's words; only values that differ from the base are kept.
function fromConfig(s){const a={};for(const x of ADV){let v=s?.[x.k];if(v===undefined)continue;
  const w=WORDS[x.k];if(w){const back=Object.entries(w).find(([,c])=>c===v);v=back?back[0]:v;}
  if(x.k==='gpu_layers'&&+v>=99)v='все';if(x.k==='n_cpu_moe'&&+v>=999)v='все';if(x.k==='reasoning_budget'&&v==='-1')v='без предела';
  if(v!==x.d)a[x.k]=String(v);}return a;}
function toConfig(a){const s={};for(const x of ADV){const v=a[x.k];if(v===undefined||v===x.d)continue;const w=WORDS[x.k];
  if(w){if(w[v]!==undefined)s[x.k]=w[v];continue;}
  if(v===AUTO[x.k]){s[x.k]=x.k==='gpu_layers'?'999':'999';continue;}
  if(['как в модели','без предела','случайное','авто','никогда'].includes(v))continue;s[x.k]=String(v).replace(',','.');}return s;}

// ── where a context fits: weights + KV cache + buffers against the GPU's memory ──
function kvBytes(m,ctx,a){const mem=m.memory;if(!mem)return 0;const ck=a.cache_key||'q8_0',cv=a.cache_value||'q8_0';return ctx*(mem.kv_key_per_token*CACHE_BYTES[ck]+mem.kv_value_per_token*CACHE_BYTES[cv]);}
function fit(m,ctx,a=M.adv){const s=sys(),usable=s.vram_bytes-(s.desktop_vram_bytes||0),ramFree=Math.max(0,s.ram_bytes-6*GIB);
  if(!m.memory)return {level:'unknown',short:'оценка недоступна',text:'Размер кеша неизвестен: заголовок модели не прочитан.'};
  if(m.context&&ctx>m.context)return {level:'over',short:'больше обученного',text:`Модель обучена на ${kfmt(m.context)} токенов: больший контекст не поможет, ответы станут хуже.`};
  const kv=kvBytes(m,ctx,a),w=m.memory.weights_bytes,buf=0.5*GIB,need=w+kv+buf,vr=gb(s.vram_bytes/GIB);
  if(need<=usable)return {level:'gpu',vram:need,kv,w,short:'в видеопамяти',text:`Поместится в видеопамять: ${gb(need/GIB)} из ${vr} ГБ. Полная скорость.`};
  if(m.memory.experts>1){const v=w*0.15+kv+buf;return v<=usable?{level:'moe',vram:v,kv,w:w*0.15,short:'эксперты в RAM',text:`Эксперты MoE в оперативной памяти, остальное на видеокарте: ${gb(v/GIB)} из ${vr} ГБ. Скорость почти не страдает.`}
    :{level:'ram',vram:v,kv,w:w*0.15,short:'медленнее',text:`Даже без экспертов нужно ${gb(v/GIB)} ГБ видеопамяти из ${vr}. Часть кеша уйдёт в RAM, ответы замедлятся.`};}
  if(need<=usable+ramFree)return {level:'ram',vram:need,kv,w,short:'медленнее',text:`Не поместится в видеопамять: нужно ${gb(need/GIB)} ГБ из ${vr}. Часть уйдёт в оперативную память, ответы будут в 3–5 раз медленнее.`};
  return {level:'none',vram:need,kv,w,short:'не хватит памяти',text:`Не хватит памяти: нужно ${gb(need/GIB)} ГБ, а видеопамяти и свободной RAM вместе около ${gb((usable+ramFree)/GIB)} ГБ. Модель не запустится.`};}
const best=(m,a)=>PRESETS.filter(k=>(!m.context||k<=m.context)&&['gpu','moe'].includes(fit(m,k,a).level)).pop()||8192;
const MARK={gpu:'✓',moe:'✓',ram:'⚠',none:'✕',over:'—',unknown:''};
function curCtx(m){const own=+m.settings?.context;if(own)return own;const b=(S.status?.backends||[]).find(x=>x.name===m.engine);
  if(m.running&&+b?.settings?.context)return +b.settings.context;return m.engine==='ollama'?(+b?.settings?.context||8192):best(m,fromConfig(m.settings));}

// ── the list ──
function ctxOptions(m){const cur=curCtx(m),a=fromConfig(m.settings),list=PRESETS.filter(k=>!m.context||k<=m.context);if(!list.includes(cur))list.push(cur);list.sort((x,y)=>x-y);const rec=m.engine==='prismml'&&m.memory?best(m,a):0;
  return list.map(k=>{const f=fit(m,k,a);return `<option value="${k}"${k===cur?' selected':''}>${kfmt(k)} ${MARK[f.level]} ${f.short}${k===rec?' · рекомендуем':''}</option>`;}).join('')+'<option value="custom">Своё значение…</option>';}
const star=m=>`<button class="btn ghost sm starb${m.favorite?' on':''}" type="button" data-star="${esc(m.key)}" aria-pressed="${!!m.favorite}" title="${m.favorite?'Убрать из избранного':'В избранное'}" aria-label="${m.favorite?'Убрать из избранного':'В избранное'}"><svg class="i"><use href="#i-star"/></svg></button>`;
function localRow(m){const f=fit(m,curCtx(m),fromConfig(m.settings));const busy=startingKey===m.key||LOAD.key===m.key;
  return `<div class="model${m.running?' live':''}" data-model="${esc(m.key)}" tabindex="0">
    <div><div class="name"><b>${esc(m.name)}</b>${m.running?'<span class="pill ok"><span class="dot"></span>Работает</span>':''}<span class="pill">${engineLabel(m.engine)}</span></div>
      <div class="sub"><span>${[m.params,m.quant,m.bytes?bytesGb(m.bytes):''].filter(Boolean).map(esc).join(' · ')}</span>${m.memory?`<span class="fit ${f.level}">${f.short}</span>`:''}</div></div>
    <div class="acts">${busy?'<span class="small muted">Запускается…</span>':m.running?'<button class="btn sm" type="button" data-stop-model>Остановить</button>':'<button class="btn sm primary" type="button" data-start-model>Запустить</button>'}<button class="btn ghost sm" type="button" data-open-model>Настроить</button>${star(m)}</div>
    ${busy?'<div class="load" data-load></div>':''}<div class="caps">${allChips(m)}<label class="ctxsel" title="Длина контекста: сколько текста модель держит в разговоре"><svg class="i"><use href="#i-ctx"/></svg>Контекст<select data-ctx-model="${esc(m.key)}" aria-label="Контекст ${esc(m.name)}">${ctxOptions(m)}</select></label></div></div>`;}
function cloudRow(m){return `<div class="model cloud" data-cloud="${esc(m.key)}">
    <div><div class="name"><b>${esc(m.name)}</b><span class="pill sky"><svg class="i"><use href="#i-cloud"/></svg>OpenRouter</span></div>
      <div class="sub"><span>${esc(m.provider||'')}</span><span title="Цена за миллион токенов: запрос / ответ">${price(m.prompt_usd)} / ${price(m.completion_usd)} за 1M токенов</span></div></div>
    <div class="acts"><button class="btn sm" type="button" data-chat-model="${esc(m.key)}">Открыть в чате</button>${star(m)}</div>
    <div class="caps">${allChips(m)}${ctxChip(m)}</div></div>`;}
let startingKey='';
function renderModels(){const box=byId('model-list');if(!S.library){setHTML(box,'<div class="banner">Загружаем список моделей…</div>');return;}
  const capOk=m=>!M.capF||has(m,M.capF);const q=M.cloudQ;
  const loc=models().filter(m=>m.source==='local'&&capOk(m)),cl=models().filter(m=>m.source==='cloud'&&capOk(m)&&(!q||(m.name+' '+m.id+' '+(m.provider||'')).toLowerCase().includes(q)));
  const row=m=>m.source==='local'?localRow(m):cloudRow(m);const cnt=b=>(b.match(/class="model/g)||[]).length;
  const sec=(title,body,extra='')=>{const key='m:'+title;const shut=collapsed.has(key);return `<div class="msec"><div class="msec-head"><button type="button" class="grp-btn" data-grp="${key}" aria-expanded="${!shut}"><h2>${title}</h2><span class="grp-n">${cnt(body)}</span>${chev(key)}</button>${shut||!extra?'':`<div class="msec-extra">${extra}</div>`}</div>${shut?'':body||'<div class="banner">Нет моделей с таким фильтром.</div>'}</div>`;};
  const bal=S.account?`<span class="chatcost">Баланс ${usd2(S.account.balance)}</span><a class="btn sm" href="https://openrouter.ai/settings/credits" target="_blank" rel="noopener noreferrer">Пополнить ↗</a>`:'';
  const cloudExtra=S.cloud?.connected?`${bal}<input type="search" id="cloud-q" value="${esc(q)}" placeholder="Поиск среди ${cl.length&&!q?cl.length:models().filter(m=>m.source==='cloud').length} моделей OpenRouter" aria-label="Поиск моделей OpenRouter">`:'';
  const cloudBody=S.cloud?.connected?(S.library.cloud?.error?`<div class="banner">${esc(S.library.cloud.error)}</div>`:'')+cl.filter(m=>M.engineF!=='all'||!m.favorite).map(row).join(''):'<div class="banner"><span>Подключите OpenRouter, чтобы пользоваться облачными моделями.</span><a class="btn sm" href="#settings" data-go="settings" data-jump-after="s-cloud">Добавить ключ</a></div>';
  let html='';
  if(M.engineF==='fav')html=sec('Избранные',[...loc,...cl].filter(m=>m.favorite).map(row).join(''));
  else{if(M.engineF==='all'){const favs=[...loc,...cl].filter(m=>m.favorite).map(row).join('');if(favs)html+=sec('Избранные',favs);}
    if(M.engineF!=='cloud')html+=sec('Локальные модели',loc.filter(m=>M.engineF!=='all'||!m.favorite).map(row).join('')||'<div class="banner"><span>Локальных моделей пока нет.</span><button class="btn sm" type="button" data-open-add>Добавить модель</button></div>');
    if(M.engineF!=='local')html+=sec('OpenRouter',cloudBody,cloudExtra);}
  if(M.applyKey){const m=modelByKey(M.applyKey);if(m?.running)html=`<div class="banner"><span>Настройки ${esc(m.name)} сохранены. Работающая модель применит их после перезапуска.</span><button class="btn sm primary" type="button" data-apply-now>Перезапустить сейчас</button></div>`+html;}
  const focus=document.activeElement?.id==='cloud-q'?document.activeElement.selectionStart:null;setHTML(box,html);
  if(focus!==null){const i=byId('cloud-q');i?.focus();i?.setSelectionRange(focus,focus);}}

async function startModel(m){startingKey=m.key;loadBegin(m);renderModels();try{const op=await operation({action:'model-start',model:m.key});if(op.state==='done')M.applyKey='';await refreshLibrary();}finally{startingKey='';loadEnd();renderModels();}}
async function stopModel(m){await operation({action:'stop',backend:m.engine});await refreshLibrary();}
async function saveSettings(m,settings,advanced){await api('/api/library',{action:'settings',model:m.key,settings,advanced:!!advanced});await refreshLibrary();
  if(modelByKey(m.key)?.running){M.applyKey=m.key;renderModels();}else notice(`Настройки ${m.name} сохранены: применятся при запуске.`);}

// ── the model's drawer ──
const DESC={vision:'Понимает изображения: скриншоты, фото, сканы. В чате OmniBox — вложения; в Open WebUI и Jan — тоже.',reasoning:'Думает перед ответом. Полезно для кода, логики и математики; ответ приходит позже.',
  tools:'Вызывает инструменты, когда они нужны. В чате OmniBox это веб-поиск; в Open WebUI и Jan — их функции и MCP-серверы.',moe:'Mixture of Experts: на каждый токен работает часть весов — быстро даже частично в RAM.'};
function openModel(key){const m=modelByKey(key);if(!m||m.source!=='local')return;M.drawer=key;M.adv=fromConfig(m.settings);M.ctx=curCtx(m);M.advOn=!!m.advanced||Object.keys(M.adv).length>0;
  byId('d-engine').textContent=engineLabel(m.engine)+(m.running?' · работает':'');byId('d-name').textContent=m.name;byId('d-file').textContent=m.file||'';
  setHTML(byId('d-caps'),['vision','reasoning','tools','moe'].filter(k=>has(m,k)).map(k=>`<div class="capx"><svg><use href="#${(CAP[k]||{icon:'i-chip'}).icon}"/></svg><b>${k==='moe'?'MoE':CAP[k].name}</b><p>${DESC[k]}</p></div>`).join('')+(m.context?`<div class="capx"><svg><use href="#i-ctx"/></svg><b>Контекст до ${kfmt(m.context)}</b><p>Сколько текста модель держит в разговоре. Длиннее контекст — больше видеопамяти.</p></div>`:''));
  byId('d-img').textContent=has(m,'vision')?'Да':'Нет';byId('d-vid').textContent='Нет';
  const prism=m.engine==='prismml';byId('d-vision-row').hidden=!prism||!m.projector;byId('d-vision').checked=m.settings?.mmproj!=='off';
  byId('d-alias-row').hidden=!prism;byId('d-alias').value=m.settings?.alias||'';byId('d-alias').placeholder=m.alias||'';
  byId('d-adv-row').hidden=!prism;byId('adv-on').checked=M.advOn;byId('adv').hidden=!prism||!M.advOn;byId('d-custom').value='';byId('d-custom-msg').textContent='';
  setHTML(byId('d-files'),`<div class="line small"><span class="muted">Модель</span><span>${[m.bytes?bytesGb(m.bytes):'',m.quant].filter(Boolean).map(esc).join(' · ')}</span></div>${m.projector?`<div class="line small"><span class="muted">Проектор изображений</span><span>${esc(m.projector.split(/[\\/]/).pop())}</span></div>`:''}${m.page?`<div class="line small"><span class="muted">Источник</span><a class="ink" href="${esc(m.page)}" target="_blank" rel="noopener noreferrer">${esc(m.page.replace(/^https:\/\/(huggingface\.co|ollama\.com)\//,''))} ↗</a></div>`:''}${m.engine==='prismml'&&!m.projector&&/^https:\/\/huggingface\.co\/[^?]+$/.test(m.page||'')?'<div><button class="btn sm" type="button" id="d-projector">Найти проектор изображений</button><span class="hint"> Если у модели есть проектор mmproj в том же репозитории, она начнёт понимать изображения.</span></div>':''}<div><button class="btn ghost sm danger-text" type="button" id="d-delete">Удалить модель с диска</button></div><div class="confirm" id="d-del-confirm" hidden><span></span><button class="btn danger sm" type="button">Удалить</button><button class="btn ghost sm" type="button">Отмена</button></div>`);
  renderCtx();renderAdv();show(byId('drawer'));}
const dm=()=>modelByKey(M.drawer);
function renderCtx(){const m=dm();if(!m)return;const a=M.adv,rec=m.engine==='prismml'&&m.memory?best(m,a):0;
  setHTML(byId('d-ctx'),PRESETS.map(k=>{const f=fit(m,k,a);return `<button type="button" data-k="${k}" class="${f.level}" aria-pressed="${k===M.ctx}"${f.level==='over'?' disabled':''}>${kfmt(k)}<small>${f.level==='over'?'больше обученного':f.short}</small></button>`;}).join(''));
  const f=fit(m,M.ctx,a),total=sys().vram_bytes,usable=total-(sys().desktop_vram_bytes||0);byId('d-bar').hidden=!m.memory;$('#drawer .legend').hidden=!m.memory;
  if(m.memory){const w=Math.min(f.w||0,total),over=Math.max(0,(f.vram||0)-usable),kv=Math.max(0,Math.min(f.kv||0,total-w));
    setHTML(byId('d-bar'),`<i class="w" data-w="${w/total*100}%"></i><i class="k" data-w="${Math.max(0,kv-over)/total*100}%"></i><i class="x" data-w="${Math.min(over,total)/total*100}%"></i>`);}
  byId('d-verdict').className='verdict '+f.level;byId('d-verdict').textContent=`${kfmt(M.ctx)}: ${f.text}`;
  byId('d-rec').innerHTML=!rec?(m.engine==='ollama'?'Ollama сама распределяет модель между видеопамятью и RAM.':'Оценка памяти недоступна: заголовок файла модели не прочитан.'):M.ctx===rec?`Это самый длинный контекст без замедления для вашей видеокарты.${m.context?` Модель обучена максимум на ${kfmt(m.context)}.`:''}`:`Рекомендуем <b>${kfmt(rec)}</b>: самый длинный контекст без замедления для вашей видеокарты. <button class="btn sm" type="button" data-k="${rec}">Поставить ${kfmt(rec)}</button>`;
  byId('d-fit').textContent=f.short;applyCheck();}
function presetRow(g){const ks=new Set(ADV.filter(x=>x.g===g).map(x=>x.k));const cur=Object.fromEntries(Object.entries(M.adv).filter(([k])=>ks.has(k)));
  const act=(GP[g]||[]).find(p=>JSON.stringify(Object.entries(p.v).sort())===JSON.stringify(Object.entries(cur).sort()))?.n;
  return (GP[g]||[]).map(p=>`<button type="button" class="gp" data-gp="${g}" data-name="${p.n}" aria-pressed="${act===p.n}" title="${esc(p.tip)}">${p.n}</button>`).join('')+(act?'':'<span class="gp own">Своё</span>');}
function renderAdv(){const groups=[...new Set(ADV.map(x=>x.g))];
  setHTML(byId('adv-groups'),groups.map(g=>`<div class="agroup"><h3>${g}</h3><div class="gpresets" data-group="${g}" role="group" aria-label="Заготовки: ${g}">${presetRow(g)}</div><div class="agrid">${ADV.filter(x=>x.g===g).map(x=>{const v=M.adv[x.k]??x.d;const ch=v!==x.d;
    const ctl=x.t==='sel'?`<select id="a-${x.k}" data-adv="${x.k}">${x.o.map(o=>`<option${o===v?' selected':''}>${o}</option>`).join('')}</select>`:`<div class="combo"><input id="a-${x.k}" data-adv="${x.k}" type="text" value="${ch?esc(v):''}" placeholder="${x.d}" autocomplete="off">${x.p?`<button type="button" class="combo-btn" data-combo="${x.k}" aria-label="Варианты: ${x.l}"><svg class="i"><use href="#i-down"/></svg></button><div class="combo-menu" hidden>${x.p.map(([val,lab])=>`<button type="button" data-pick="${x.k}" data-val="${val}"><b>${val}</b><span>${lab}</span></button>`).join('')}</div>`:''}</div>`;
    return `<div class="afield${ch?' changed':''}" data-field="${x.k}"><span class="lab"><label for="a-${x.k}">${x.l}</label><span class="qi" tabindex="0" role="note" aria-label="${esc(x.tip.replace(/\n/g,' '))}" data-tip="${esc(x.tip)}">?</span></span>${ctl}<span class="amsg" id="m-${x.k}" aria-live="polite"></span></div>`;}).join('')}</div></div>`).join(''));
  updateMeta();}
function updateMeta(){ADV.forEach(x=>{const el=document.querySelector(`[data-field="${x.k}"]`);if(el)el.classList.toggle('changed',M.adv[x.k]!==undefined&&M.adv[x.k]!==x.d);});
  $$('.gpresets').forEach(r=>{r.innerHTML=presetRow(r.dataset.group);});const n=Object.keys(M.adv).length;
  byId('adv-count').textContent=n?`Изменено для этой модели: ${n}. Остальное — базовые значения.`:'Все значения базовые: подобраны под модель и видеокарту.';byId('adv-reset').hidden=!n;applyCheck();}
// Impossible values are errors and block saving; legal but doubtful ones are warnings.
function check(m,a,ctx){const E={},layers=m.memory?.layers||999,cores=navigator.hardwareConcurrency||16;
  const err=(k,t)=>{E[k]={lv:'err',t};},warn=(k,t)=>{if(!E[k])E[k]={lv:'warn',t};};
  const word=k=>{const d=ADV.find(x=>x.k===k).d;return a[k]===undefined||a[k]===d||['все','авто','как в модели','без предела','случайное','никогда'].includes(a[k]);};
  const val=k=>Number(String(a[k]).replace(',','.'));
  const int=(k,lo,hi,why)=>{if(word(k))return null;const x=val(k);if(!Number.isFinite(x)){err(k,'Нужно целое число');return null;}if(!Number.isInteger(x)){err(k,'Нужно целое число, без дробной части');return null;}if(x<lo){err(k,`Не меньше ${fmtN(lo)}`);return null;}if(x>hi){err(k,`Не больше ${fmtN(hi)}${why?' — '+why:''}`);return null;}return x;};
  const real=(k,lo,hi,open)=>{if(word(k))return null;const x=val(k);if(!Number.isFinite(x)){err(k,'Нужно число, например 0.7');return null;}if(open?x<=lo:x<lo){err(k,open?`Больше ${lo}`:`Не меньше ${lo}`);return null;}if(x>hi){err(k,`Не больше ${hi}`);return null;}return x;};
  const f=fit(m,ctx,a);
  const gl=int('gpu_layers',0,layers,`у модели ${layers} слоёв`);if(gl===0)warn('gpu_layers','Только процессор: ответы в 10 и более раз медленнее.');else if(gl!==null&&f.level==='gpu')warn('gpu_layers','Модель помещается в видеопамять целиком — с этим значением она будет медленнее.');
  const moe=int('n_cpu_moe',0,layers,`у модели ${layers} слоёв`);if(!(m.memory?.experts>1)&&!word('n_cpu_moe')&&!E.n_cpu_moe&&moe!==0)warn('n_cpu_moe','У этой модели нет экспертов MoE: настройка ни на что не влияет.');
  const b=int('batch',32,8192),ub=int('ubatch',32,8192),bb=b??2048;if(ub!==null&&ub>bb)err('ubatch',`Не больше пакета чтения запроса (${fmtN(bb)})`);
  int('threads',1,256,'');int('idle_minutes',1,1440,'это сутки');const t=real('temperature',0,2);if(t===0)warn('temperature','Ответ всегда одинаковый — подходит только для тестов.');else if(t>1.3)warn('temperature','Ответы могут стать бессвязными.');
  const tp=real('top_p',0,1,true);if(tp!==null&&tp<.5)warn('top_p','Очень строго: ответы станут однообразными.');
  const tk=int('top_k',0,1000);if(tk!==null&&tk>0&&tk<=5)warn('top_k','Почти не остаётся вариантов: ответы шаблонные.');
  const mp=real('min_p',0,1);if(mp>.3)warn('min_p','Слишком строго: ответы обедняются.');
  const rp=real('repeat_penalty',.8,2);if(rp!==null&&rp<1)warn('repeat_penalty','Меньше 1 поощряет повторы.');else if(rp>1.3)warn('repeat_penalty','Сильный штраф портит текст.');
  ['presence_penalty','frequency_penalty'].forEach(k=>{const x=real(k,-2,2);if(x!==null&&Math.abs(x)>1.5)warn(k,'Слишком сильно: текст становится странным.');});
  const mt=int('max_tokens',16,ctx,'это вся длина контекста');int('seed',0,4294967295);
  const rb=int('reasoning_budget',0,ctx,'это вся длина контекста');if(rb!==null&&mt!==null&&rb>=mt)warn('reasoning_budget','Бюджет размышлений не меньше предела ответа: модель может не успеть ответить.');
  if(a.flash_attention==='выкл'&&((a.cache_value||'q8_0')!=='f16'||(a.cache_key||'q8_0')!=='f16'))err('flash_attention','Сжатый кеш (q8_0, q4_0) работает только с Flash Attention. Включите его или выберите кеш f16.');
  if(a.cache_key==='q4_0')warn('cache_key','Качество длинного контекста заметно падает. Чаще сжимают значения, а ключи оставляют q8_0.');
  if(a.kv_offload==='нет')warn('kv_offload','Кеш в оперативной памяти: генерация замедлится в несколько раз.');
  if(f.level==='none')err('ctx','Не хватит памяти: модель не запустится. Уменьшите контекст или сожмите кеш.');
  return E;}
function applyCheck(){const m=dm();if(!m)return;const E=check(m,M.advOn?M.adv:{},M.ctx);let errs=0,warns=0;
  ADV.forEach(x=>{const el=document.querySelector(`[data-field="${x.k}"]`);if(!el)return;const e=E[x.k];el.classList.toggle('err',e?.lv==='err');el.classList.toggle('warn',e?.lv==='warn');el.querySelector('.amsg').textContent=e?e.t:'';
    const inp=el.querySelector('input,select');if(inp)inp.setAttribute('aria-invalid',e?.lv==='err'?'true':'false');if(e?.lv==='err')errs++;else if(e)warns++;});
  if(E.ctx)errs++;if(byId('d-custom').getAttribute('aria-invalid')==='true')errs++;
  byId('d-save').disabled=errs>0;byId('d-save-note').textContent=errs?`Исправьте: ${errs} ${plural(errs,'поле','поля','полей')}`:warns?`Предупреждений: ${warns}`:'';byId('d-save-note').className='small '+(errs?'bad':'faint');}
async function saveDrawer(){const m=dm();if(!m)return;const s=m.engine==='prismml'?{...(M.advOn?toConfig(M.adv):{})}:{};s.context=String(M.ctx);
  if(m.engine==='prismml'){const alias=byId('d-alias').value.trim();if(alias)s.alias=alias;if(m.projector&&!byId('d-vision').checked)s.mmproj='off';}
  await saveSettings(m,s,M.advOn);hideAll();}

// ── adding models ──
async function catalogSearch(q){const box=byId('add-results');setHTML(box,'<div class="busy"><span>Ищем на Hugging Face…</span><div class="meter run"><i></i></div></div>');
  try{const d=await api('/api/catalog?q='+encodeURIComponent(q));
    setHTML(box,(d.models||[]).map(r=>`<div class="pick"><div><b>${esc(r.name)}</b> <span class="small muted">${fmtN(r.downloads)} скачиваний${r.gated?' · нужен доступ автора':''}</span></div><button class="btn sm" type="button" data-repo="${esc(r.id)}">Выбрать файл</button><div class="caps">${capIcons({caps:Object.fromEntries((r.capabilities||[]).map(c=>[c,true]))})}<a class="small ink" href="${esc(r.url)}" target="_blank" rel="noopener noreferrer">Описание ↗</a></div></div>`).join('')||'<div class="banner">Ничего не найдено. Измените запрос.</div>');}
  catch(e){setHTML(box,`<div class="errbox">${esc(e.message)}</div>`);}}
let repoFiles=[];
async function repoOpen(repo){const box=byId('add-results');setHTML(box,'<div class="busy"><span>Получаем файлы репозитория…</span><div class="meter run"><i></i></div></div>');
  try{const d=await api('/api/catalog?repo='+encodeURIComponent(repo));repoFiles=d.files||[];const proj=pickProjector(repoFiles);const ollama=M.addEngine==='ollama';
    setHTML(box,`<div class="line"><a class="ink" href="${esc(d.url)}" target="_blank" rel="noopener noreferrer">${esc(repo)} ↗</a><span>${capIcons({caps:Object.fromEntries((d.capabilities||[]).map(c=>[c,true])),context:d.context})}</span></div>`+
      repoFiles.map((f,i)=>{const off=f.split||(ollama&&(f.prism_only||f.projector));return `<div class="pick"><div><b>${esc(f.name)}</b> <span class="small muted">${f.bytes?bytesGb(f.bytes):'размер не указан'}${f.projector?' · проектор изображений':''}${!f.projector&&proj&&!ollama?' · вместе с проектором':''}${f.prism_only?' · только PrismML':''}${f.split?' · многотомный файл':''}</span></div><button class="btn sm" type="button" data-file="${i}"${off?' disabled':''}>Скачать</button>${f.fit?`<div><span class="fit ${esc(f.fit.level)} small muted">${esc(f.fit.text)}</span></div>`:''}</div>`;}).join('')||'<div class="banner">Подходящих GGUF нет.</div>');}
  catch(e){setHTML(box,`<div class="errbox">${esc(e.message)}</div>`);}
  byId('add-results').dataset.repo=repo;}
function pickProjector(files){const l=(files||[]).filter(f=>f.projector&&!f.split);return l.find(f=>/f16/i.test(f.name)&&!/bf16/i.test(f.name))||l.find(f=>/bf16/i.test(f.name))||l[0]||null;}
async function download(i){const f=repoFiles[i];const repo=byId('add-results').dataset.repo;let body;
  if(M.addEngine==='ollama')body={action:'pull',backend:'ollama',model:`hf.co/${repo}:${f.name}`};
  else{const p=f.projector?null:pickProjector(repoFiles);body={action:'download',backend:'prismml',url:f.url,filename:f.filename,sha256:f.sha256||'',size:f.bytes?String(f.bytes):''};
    if(p)Object.assign(body,{mmproj_url:p.url,mmproj_filename:p.filename,mmproj_sha256:p.sha256||'',mmproj_size:p.bytes?String(p.bytes):''});}
  const {done}=await runAction(body);notice('Скачивание началось. Прогресс — в этом окне и на странице «Модели».');watchAddOps();const op=await done;notice(op.message,op.state==='failed');refreshLibrary().catch(()=>{});}
// A model without its projector gets the one from its repository; the panel picks it for the model.
async function downloadProjector(m){const repo=m.page.replace('https://huggingface.co/','');const d=await api('/api/catalog?repo='+encodeURIComponent(repo));const p=pickProjector(d.files);
  if(!p){notice(`В репозитории ${repo} нет проектора mmproj: эта модель изображения не понимает.`,true);return;}
  hideAll();go('models');const op=await operation({action:'download',backend:'prismml',url:p.url,filename:p.filename,sha256:p.sha256||'',size:p.bytes?String(p.bytes):'',for_model:m.id});if(op.state==='done')await refreshLibrary();}
function watchAddOps(){renderOps(byId('add-ops'),['download','pull']);renderOps(byId('models-ops'),['download','pull','model-delete']);}
on('status',()=>{watchAddOps();if(S.page==='models'&&!startingKey)renderModels();});
on('library',()=>{if(S.page==='models')renderModels();});

// ── events ──
function seg(id,fn){byId(id).addEventListener('click',e=>{const b=e.target.closest('button');if(!b)return;const was=b.getAttribute('aria-pressed')==='true';byId(id).querySelectorAll('button').forEach(x=>x.setAttribute('aria-pressed','false'));fn(b,was);});}
seg('engine-filter',b=>{b.setAttribute('aria-pressed','true');M.engineF=b.dataset.engine;renderModels();});
seg('cap-filter',(b,was)=>{M.capF=was?null:b.dataset.cap;if(!was)b.setAttribute('aria-pressed','true');renderModels();});
seg('add-engine',b=>{b.setAttribute('aria-pressed','true');M.addEngine=b.dataset.addEngine;const ol=M.addEngine==='ollama';byId('direct-prism').hidden=ol;byId('direct-ollama').hidden=!ol;
  byId('hf').placeholder=ol?'Модели с Hugging Face для Ollama, или имя из библиотеки ниже':'Название на Hugging Face: gemma, qwen, bonsai…';if(byId('add-results').dataset.repo)repoOpen(byId('add-results').dataset.repo);});
byId('model-list').addEventListener('input',e=>{if(e.target.id==='cloud-q'){M.cloudQ=e.target.value.toLowerCase();renderModels();}});
byId('model-list').addEventListener('change',e=>{const sel=e.target.closest('[data-ctx-model]');if(!sel)return;const m=modelByKey(sel.dataset.ctxModel);if(!m)return;
  if(sel.value==='custom'){openModel(m.key);setTimeout(()=>byId('d-custom').focus(),50);return;}
  saveSettings(m,{...(m.settings||{}),context:sel.value},m.advanced).catch(fail);});
byId('model-list').addEventListener('keydown',e=>{if(e.key==='Enter'&&e.target.matches('[data-model]'))openModel(e.target.dataset.model);});
byId('add-open').addEventListener('click',()=>{show(byId('add'));byId('hf').focus();watchAddOps();});
byId('hf-form').addEventListener('submit',e=>{e.preventDefault();const q=byId('hf').value.trim();if(q)catalogSearch(q);});
byId('direct-form').addEventListener('submit',e=>{e.preventDefault();const ol=M.addEngine==='ollama';
  const body=ol?{action:'pull',backend:'ollama',model:byId('direct-ollama').value.trim()}:{action:'download',backend:'prismml',url:byId('direct-url').value.trim(),filename:byId('direct-name').value.trim(),sha256:byId('direct-sha').value.trim()};
  runAction(body).then(({done})=>{notice('Скачивание началось.');watchAddOps();return done;}).then(op=>{notice(op.message,op.state==='failed');refreshLibrary();}).catch(fail);});
byId('drawer').addEventListener('click',e=>{const b=e.target.closest('[data-k]');if(b){M.ctx=+b.dataset.k;byId('d-custom').value='';byId('d-custom').setAttribute('aria-invalid','false');byId('d-custom-msg').textContent='';renderCtx();return;}
  if(e.target.closest('#d-projector')){downloadProjector(dm()).catch(fail);return;}
  if(e.target.closest('#d-delete')){const m=dm();const box=byId('d-del-confirm');ask(box,m.running?`Остановить ${m.name} и удалить с диска? Восстановить можно только скачав заново.`:`Удалить ${m.name} с диска? Восстановить можно только скачав заново.`).then(async yes=>{if(!yes)return;
    if(m.running)await operation({action:'stop',backend:m.engine},{quiet:true});await operation({action:'model-delete',backend:m.engine,model:m.id});hideAll();await refreshLibrary();}).catch(fail);}});
// Safari on iPhone may skip 'change' on a styled switch, so every way of toggling it syncs the panel.
// The groups open below the fold on a phone, so the drawer scrolls to them.
function syncAdv(){const on=byId('adv-on').checked;if(on===M.advOn&&byId('adv').hidden===!on)return;M.advOn=on;byId('adv').hidden=!on;renderCtx();
  if(on)byId('d-adv-row').scrollIntoView({behavior:'smooth',block:'start'});}
['change','input','click'].forEach(t=>byId('adv-on').addEventListener(t,()=>setTimeout(syncAdv)));
// The whole row toggles, not only the small switch.
byId('d-adv-row').addEventListener('click',e=>{if(!e.target.closest('.switch')){const c=byId('adv-on');c.checked=!c.checked;}setTimeout(syncAdv);});
byId('adv-groups').addEventListener('input',e=>{const el=e.target.closest('input[data-adv]');if(!el)return;const x=ADV.find(a=>a.k===el.dataset.adv);const v=el.value.trim();if(!v||v===x.d)delete M.adv[x.k];else M.adv[x.k]=v;applyCheck();});
byId('adv-groups').addEventListener('change',e=>{const el=e.target.closest('[data-adv]');if(!el)return;const x=ADV.find(a=>a.k===el.dataset.adv);const v=el.value.trim()||x.d;if(v===x.d)delete M.adv[x.k];else M.adv[x.k]=v;updateMeta();if(x.kv)renderCtx();});
byId('adv-groups').addEventListener('click',e=>{
  const cb=e.target.closest('[data-combo]');if(cb){const menu=cb.nextElementSibling;const open=menu.hidden;$$('.combo-menu').forEach(x=>x.hidden=true);menu.hidden=!open;return;}
  const pk=e.target.closest('[data-pick]');if(pk){const x=ADV.find(a=>a.k===pk.dataset.pick);if(pk.dataset.val===x.d)delete M.adv[x.k];else M.adv[x.k]=pk.dataset.val;renderAdv();if(x.kv)renderCtx();return;}
  const gp=e.target.closest('[data-gp]');if(gp){const g=gp.dataset.gp;ADV.filter(x=>x.g===g).forEach(x=>delete M.adv[x.k]);Object.assign(M.adv,GP[g].find(p=>p.n===gp.dataset.name).v);renderAdv();renderCtx();}});
document.addEventListener('click',e=>{if(!e.target.closest('.combo'))$$('.combo-menu').forEach(x=>x.hidden=true);});
byId('adv-reset').addEventListener('click',()=>{M.adv={};renderAdv();renderCtx();});
byId('d-custom').addEventListener('input',e=>{const el=e.target,raw=el.value.trim(),m=dm(),msg=byId('d-custom-msg');
  const bad=t=>{msg.textContent=t;el.setAttribute('aria-invalid','true');applyCheck();};
  if(!raw){msg.textContent='';el.setAttribute('aria-invalid','false');applyCheck();return;}
  if(!/^\d[\d\s]*$/.test(raw))return bad('Только целое число токенов, например 48000.');
  const n=parseInt(raw.replace(/\s/g,''),10);if(n<1024)return bad('Не меньше 1024 токенов: иначе не поместится даже короткий диалог.');
  if(m.context&&n>m.context)return bad(`Не больше ${fmtN(m.context)}: модель обучена на ${kfmt(m.context)}.`);if(n>1048576)return bad('Не больше 1 048 576 токенов.');
  msg.textContent='';el.setAttribute('aria-invalid','false');M.ctx=n;renderCtx();});
byId('d-save').addEventListener('click',()=>saveDrawer().catch(fail));
document.addEventListener('click',e=>{if(S.page!=='models'&&!e.target.closest('#add'))return;
  const st=e.target.closest('[data-star]');if(st){const m=modelByKey(st.dataset.star);api('/api/library',{action:'favorite',model:st.dataset.star,on:!m?.favorite}).then(()=>refreshLibrary()).catch(fail);return;}
  const cm=e.target.closest('[data-chat-model]');if(cm){go('chat');setChosen(cm.dataset.chatModel);return;}
  if(e.target.closest('[data-start-model]')){const m=modelByKey(e.target.closest('[data-model]').dataset.model);startModel(m).catch(fail);return;}
  if(e.target.closest('[data-stop-model]')){const m=modelByKey(e.target.closest('[data-model]').dataset.model);stopModel(m).catch(fail);return;}
  if(e.target.closest('[data-apply-now]')){const m=modelByKey(M.applyKey);if(m)startModel(m).catch(fail);return;}
  if(e.target.closest('[data-open-add]')){byId('add-open').click();return;}
  const grp=e.target.closest('[data-grp^="m:"]');if(grp){toggleGroup(grp.dataset.grp);renderModels();return;}
  const rp=e.target.closest('[data-repo]');if(rp){repoOpen(rp.dataset.repo);return;}
  const hf=e.target.closest('[data-hf]');if(hf){byId('hf').value=hf.dataset.hf;catalogSearch(hf.dataset.hf);return;}
  const fl=e.target.closest('[data-file]');if(fl){download(+fl.dataset.file).catch(fail);return;}
  const om=e.target.closest('[data-open-model]');if(om){openModel(om.closest('[data-model]').dataset.model);return;}
  if(e.target.closest('.ctxsel,#cloud-q,.cloud,.acts'))return;
  const row=e.target.closest('[data-model]');if(row)openModel(row.dataset.model);});
async function refreshDisk(){try{const u=await api('/api/storage');M.disk=u;const place=(u.models||[]).find(x=>x.name==='prismml')||(u.models||[])[0];const d=place&&(u.drives||[]).find(x=>x.name===place.drive);
  byId('models-disk').textContent=d?`Свободно на диске ${d.name} ${bytesGb(d.free)}`:'';}catch(e){}}
PAGES.models={show(){renderModels();refreshLibrary().catch(fail);refreshDisk();watchAddOps();}};
