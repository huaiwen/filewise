'use strict';
const $ = id => document.getElementById(id);
const state = {token:'', me:null, demo:null, projects:[], project:null, snapshot:null, path:null, file:null, view:'files', watch:null, seenEvent:null, setup:null, connections:null};
const el = (tag, text, cls) => { const n=document.createElement(tag); if(text!==undefined)n.textContent=text; if(cls)n.className=cls; return n; };
const short = value => value ? value.slice(0,10) : '—';
const stamp = value => new Date(value).toLocaleString('zh-CN',{hour12:false});
const enc = value => encodeURIComponent(value);
const has = role => state.me?.roles.includes(role);
function notice(message, error=false){$('notice').textContent=message;$('notice').className=error?'error':'';$('notice').hidden=false;}
async function api(path, method='GET', body, token=state.token){
  const headers={Authorization:'Bearer '+token};
  if(body && !(body instanceof FormData))headers['Content-Type']='application/json';
  const response=await fetch('/api/'+path,{method,headers,body:body?(body instanceof FormData?body:JSON.stringify(body)):undefined});
  const data=await response.json();
  if(!response.ok)throw new Error(typeof data.detail==='string'?data.detail:JSON.stringify(data.detail));
  return data;
}
const run = fn => async event => {try{await fn(event);}catch(error){notice(error.message,true);}};
function on(id, fn){$(id).addEventListener('click',run(fn));}
function projectPath(){return 'projects/'+enc(state.project.id);}
function snapshotPath(){return projectPath()+'/snapshots/'+enc(state.snapshot.release_id);}
function badge(text, kind){return el('span',text,'badge '+(kind||''));}
function empty(title,message){const box=el('div',undefined,'empty');box.append(el('span','▤','empty-icon'),el('h2',title),el('p',message));return box;}
async function connect(token){
  const me=await api('me','GET',undefined,token);
  if(me.audience==='agent')throw new Error('工作台需要操作员凭据。Agent 凭据请用于 filewise agent CLI。');
  state.token=token;state.me=me;$('identity-label').textContent=me.id;$('connect').textContent='切换凭据';
  sessionStorage.setItem('filewise-token',token);
  $('new-project').disabled=!has('editor');
  state.setup=await api('setup');$('begin-setup').hidden=!state.setup.local_setup||!has('editor');
  await refresh();
}
async function refresh(preferred){
  if(!state.token){$('login-dialog').showModal();return;}
  state.projects=await api('projects');
  const id=preferred||state.project?.id||state.projects[0]?.id;
  const select=$('project-select');select.replaceChildren();
  for(const p of state.projects){const o=el('option',p.name);o.value=p.id;select.append(o);}
  if(!state.projects.length){state.project=null;state.snapshot=null;$('content').replaceChildren(empty('先选一个关注文件夹','连接常用 Agent 后，照常工作；修改会在这里等待检查和审核。'));renderGate();if(state.setup?.local_setup)openSetup(true);return;}
  select.value=id;
  await selectProject(id);
}
async function selectProject(id){
  if(state.project?.id!==id){state.path=null;state.file=null;state.view='files';$('file-filter').value='';}
  state.project=await api('projects/'+enc(id));
  $('project-name').textContent=state.project.name;
  $('project-origin').textContent=state.project.source==='local'?'已注册本地目录':'上传的文件项目';
  $('project-description').textContent=state.project.active_release?'当前发布 '+short(state.project.active_release)+' · 新变更经过检查与审核后启用':'尚未发布 · 查看文件版本，审核后发布';
  $('upload').disabled=!has('editor')||state.project.source!=='upload';
  $('sync').disabled=!has('editor')||state.project.source!=='local';
  $('watch-settings').disabled=state.project.source!=='local';
  $('agent-connections').disabled=state.project.source!=='local';
  await refreshConnections();
  await refreshWatch();
  $('snapshot-select').replaceChildren();
  for(const s of state.project.snapshots){const o=el('option',stamp(s.created_at)+' · '+short(s.release_id)+(s.release_id===state.project.active_release?' · 当前':''));o.value=s.release_id;$('snapshot-select').append(o);}
  if(state.project.snapshots.length){
    const selected=state.snapshot?.project_id===id&&state.project.snapshots.some(s=>s.release_id===state.snapshot.release_id)?state.snapshot.release_id:state.project.snapshots[0].release_id;
    await selectSnapshot(selected);
  }else{
    state.snapshot=null;state.file=null;state.path=null;$('file-tree').replaceChildren();$('file-count').textContent='0';$('content').replaceChildren(empty('准备第一个文件版本','上传完整文件集；本地目录项目可直接点击同步目录。'));$('snapshot-select').append(el('option','暂无版本'));renderGate();
  }
}
async function selectSnapshot(id){
  $('agent-result').replaceChildren(el('p','点击验证以检查此刻的读取资格。','fine'));
  state.snapshot=await api(projectPath()+'/snapshots/'+enc(id));
  $('snapshot-select').value=id;
  if(state.watch?.last_event?.release_id===id){state.seenEvent=id;$('watch-event').hidden=true;}
  if(!state.snapshot.files.some(f=>f.path===state.path))state.path=state.snapshot.report.changes.find(c=>c.kind!=='removed')?.path||state.snapshot.files[0]?.path;
  state.file=null;renderTree();renderGate();
  if(state.path)await loadFile(state.path);else renderView();
}
function renderTree(){
  const tree=$('file-tree');tree.replaceChildren();const query=$('file-filter').value.toLowerCase();
  $('file-count').textContent=state.snapshot?.files.length||0;
  for(const file of state.snapshot?.files||[]){
    if(!file.path.toLowerCase().includes(query))continue;
    const button=el('button',undefined,'file-row'+(file.path===state.path?' active':''));
    button.title=file.path;button.setAttribute('aria-current',file.path===state.path?'true':'false');
    button.append(el('span',file.path.split('.').pop().slice(0,4).toUpperCase(),'file-icon'),el('span',file.path));
    button.addEventListener('click',run(async()=>{state.view='files';await loadFile(file.path);}));tree.append(button);
  }
}
async function loadFile(path){
  state.path=path;state.file=null;renderTree();
  const snapshot=state.snapshot.release_id;
  let file;
  if(has('editor')||has('reviewer')||has('publisher'))file=await api(snapshotPath()+'/preview?path='+enc(path));
  else {const session=await api(projectPath()+'/sessions?release_id='+enc(snapshot),'POST');file=await api('sessions/'+session.session_id+'/read?path='+enc(path));}
  if(state.path!==path||state.snapshot.release_id!==snapshot)return;
  state.file=file;
  $('file-details').replaceChildren(el('p',file.path),el('p',(file.size/1024).toFixed(1)+' KiB · '+file.extraction),el('p','来源 '+short(file.source_id)),el('code','SHA-256 '+file.sha256),el('p','预览用于人工审核；Agent 读取另行经过发布检查。'));
  renderView();
}
function renderGate(){
  const snap=state.snapshot, gate=$('gate');gate.replaceChildren();
  $('approve').disabled=true;$('publish').disabled=true;$('recover').hidden=true;$('publish').textContent='发布此版本';$('agent-read').disabled=!state.project?.active_release;
  if(!snap){
    $('version-state').textContent='未发布';$('version-state').className='badge';
    $('change-count').textContent='0';$('check-count').textContent='';
    $('approval-note').textContent='先上传或同步文件，再提交审核。';
    $('file-details').textContent='选择文件查看定位信息。';$('agent-result').replaceChildren();
    for(const button of document.querySelectorAll('[data-view]'))button.setAttribute('aria-selected',String(button.dataset.view===state.view));
    $('content').setAttribute('aria-labelledby','tab-'+state.view);
    gate.append(el('p','尚无候选版本。','small muted'));return;
  }
  const pass=snap.verification.decision==='PASS';
  const active=snap.release_id===state.project.active_release;
  const status=snap.revoked?'已撤销':active?'当前发布':snap.approver?'已审核':pass?'待审核':'阻止发布';
  $('version-state').textContent=status;$('version-state').className='badge '+(snap.revoked||!pass?'blocked':active?'pass':'review');
  $('change-count').textContent=snap.report.changes.length;
  const title=el('div',undefined,'gate-title');title.append(badge(snap.verification.decision,pass?'pass':'blocked'),el('span',pass?'检查通过':'需要修复'));gate.append(title);
  gate.append(el('p',snap.report.assurance==='file_integrity_only'?'仅验证文件完整性。未声明业务约束；语义适用性仍须人工审核。':'验证已声明的业务约束与文件完整性。语义提示须由审核者确认。','gate-description muted'));
  $('check-count').textContent=snap.verification.tests.length+' 项';
  for(const check of snap.verification.tests){const row=el('div',undefined,'check-row'+(check.passed?'':' fail'));row.append(el('strong',(check.passed?'✓ ':'! ')+(check.id==='file-manifest'?'文件集完整性':check.id)));row.append(el('div','实际 '+JSON.stringify(check.actual)+' / 要求 '+JSON.stringify(check.expected),'check-result'));gate.append(row);}
  for(const issue of snap.verification.issues){gate.append(el('p',issue.reason+(issue.object_id?' · '+issue.object_id:''),'fine'));}
  $('approve').disabled=!has('reviewer')||!pass||snap.revoked||Boolean(snap.approver);
  $('publish').disabled=!has('publisher')||!pass||snap.revoked||!snap.approver||active;
  if(snap.writeback){
    const recovery=['applying','recovery_required'].includes(snap.writeback.status);
    $('publish').textContent=snap.writeback.status==='applied'?'已写回原文件':'写回原文件并发布';
    $('publish').disabled||=snap.writeback.status!=='pending';
    $('recover').hidden=!recovery||!has('publisher');
    gate.append(el('p',recovery?'写回尚未完成，需要恢复后继续。':snap.writeback.status==='applied'?'这次修改已经写回原件。':'模型仅修改了副本；审核通过后才写回原件。','fine'));
    if(snap.writeback.error)gate.append(el('p',snap.writeback.error,'fine'));
  }
  $('approval-note').textContent=snap.approver?'审核人 '+snap.approver+' · 发布者可启用此版本。':'需要独立审核。提交者不能审核自己的版本。';
  if(snap.report.skipped.length)gate.append(el('p','已排除 '+snap.report.skipped.length+' 个路径：'+snap.report.skipped.slice(0,5).map(s=>s.path).join('、'),'fine'));
}
function renderView(){
  for(const button of document.querySelectorAll('[data-view]'))button.setAttribute('aria-selected',String(button.dataset.view===state.view));
  const content=$('content');content.setAttribute('aria-labelledby','tab-'+state.view);content.replaceChildren();
  if(!state.snapshot)return;
  if(state.view==='changes'){renderChanges(content);return;}
  if(state.view==='impact'){renderImpact(content);return;}
  if(state.view==='history'){renderHistory(content);return;}
  if(!state.snapshot.files.length){content.append(empty('文件集为空','查看“变更”中的删除记录。空文件集无法发布。'));return;}
  if(!state.file){content.append(empty('正在读取文件…',''));return;}
  const file=state.file, title=el('div',undefined,'file-title'), names=el('div');
  names.append(el('h2',file.path),el('p','版本 '+short(state.snapshot.release_id)+' · 审核预览'));
  const download=el('button','下载原文件');download.addEventListener('click',()=>{
    const bytes=Uint8Array.from(atob(file.base64),c=>c.charCodeAt(0));const url=URL.createObjectURL(new Blob([bytes],{type:'application/octet-stream'}));
    const a=el('a');a.href=url;a.download=file.path.split('/').pop();a.click();setTimeout(()=>URL.revokeObjectURL(url),1000);
  });title.append(names,download);content.append(title);
  if(file.path.endsWith('.xlsx')||file.path.endsWith('.csv')){renderSheet(content,file.fragments);return;}
  if(file.text!==null){const code=el('div',undefined,'text-preview');file.text.split('\n').slice(0,3000).forEach((line,i)=>{const row=el('div',undefined,'text-line');row.append(el('span',String(i+1),'line-number'),el('span',line,'line-text'));code.append(row);});content.append(code);if(file.text.split('\n').length>3000)content.append(el('p','界面显示前 3,000 行；下载可获取完整文件。','fine'));return;}
  for(const fragment of file.fragments.slice(0,1000)){const item=el('div',undefined,'fragment');item.append(el('span',fragment.locator,'locator'),el('p',fragment.text));content.append(item);}
  if(!file.fragments.length)content.append(empty('原文件已保存','此格式没有可用的文本提取结果。可下载原文件，内容完整性仍受版本管理。'));
  if(file.fragments.length>1000)content.append(el('p','界面显示前 1,000 个文本片段。','fine'));
}
function renderSheet(content,fragments){
  const sheets=new Map();
  for(const f of fragments){let m=f.locator.match(/^sheet:(\d+)\/cell:([A-Z]+)(\d+)$/),sheet,row,col;
    if(m){sheet=m[1];row=Number(m[3]);col=0;for(const c of m[2])col=col*26+c.charCodeAt(0)-64;}
    else {m=f.locator.match(/^row:(\d+)\/col:(\d+)$/);if(!m)continue;sheet='1';row=Number(m[1]);col=Number(m[2]);}
    if(!sheets.has(sheet))sheets.set(sheet,new Map());if(!sheets.get(sheet).has(row))sheets.get(sheet).set(row,new Map());sheets.get(sheet).get(row).set(col,f);
  }
  for(const [sheet,rows] of sheets){content.append(el('h3','工作表 '+sheet));const table=el('table',undefined,'sheet');
    const columns=[...new Set([...rows.values()].flatMap(r=>[...r.keys()]))].sort((a,b)=>a-b).slice(0,30);
    const header=el('tr');header.append(el('th','#','row-index'));for(const col of columns){let n=col,name='';while(n){name=String.fromCharCode(65+(n-1)%26)+name;n=Math.floor((n-1)/26);}header.append(el('th',name));}table.append(header);
    for(const [row,cells] of [...rows].slice(0,200)){const tr=el('tr');tr.append(el('th',String(row),'row-index'));for(const col of columns){const td=el('td',cells.get(col)?.text||'');td.title=cells.get(col)?.locator||'';tr.append(td);}table.append(tr);}content.append(table);
  }
  content.append(el('p','显示有内容的单元格，最多 200 行 × 30 列 / 表。公式按原文展示，未执行计算。','fine'));
}
function renderChanges(content){
  const changes=state.snapshot.report.changes;
  if(!changes.length){content.append(empty('相对当前发布无内容变化','相同内容复用原文件修订；审批记录独立保留。'));return;}
  content.append(el('p','与构建基线 '+(state.snapshot.report.base_release?short(state.snapshot.report.base_release):'空版本')+' 对比。语义提示来自确定性规则，供人工审核。','fine'));
  for(const change of changes){const block=el('article',undefined,'change-file'),header=el('div',undefined,'change-header');const name=el('button',change.path);name.disabled=change.kind==='removed';name.addEventListener('click',run(async()=>{state.view='files';await loadFile(change.path);}));header.append(name,badge({added:'新增',modified:'修改',removed:'删除'}[change.kind]));block.append(header);
    for(const c of change.locations.slice(0,100)){const location=el('div',undefined,'change-location');if(c.before!==null)location.append(el('div','− '+c.before_locator+'  '+c.before,'diff-line before'));if(c.after!==null)location.append(el('div','+ '+c.after_locator+'  '+c.after,'diff-line after'));block.append(location,el('p',c.summary+' · '+(state.snapshot.approver?'已由 '+state.snapshot.approver+' 审核':'待审核'),'change-meaning'));}
    if(change.binary_changed)block.append(el('p','原文件字节发生变化，暂无可定位的文本差分。','small muted'));
    else if(!change.locations.length)block.append(el('p','原文件字节发生变化，但提取内容一致。变化可能来自格式或文档元数据，需检查原文件。','small muted'));
    if(change.locations.length>100)block.append(el('p','界面显示前 100 处变化，API 提供完整报告。','fine'));content.append(block);
  }
}
function renderImpact(content){
  content.append(el('h2','这次变化影响哪些文件'),el('p','沿项目声明的依赖传播。覆盖范围以这些依赖和校验规则为界。','fine'));
  const paths=Object.entries(state.snapshot.report.impact);
  if(!paths.length)content.append(empty('没有内容变化传播','当前版本与发布基线内容一致。'));
  for(const [file,path] of paths){const row=el('div',undefined,'impact-row');row.append(el('strong',file));const chain=el('div',undefined,'impact-path');path.forEach((p,i)=>{if(i)chain.append(el('i','→'));chain.append(el('span',p));});row.append(chain);content.append(row);}
}
function renderHistory(content){
  content.append(el('h2','发布记录'),el('p','回退切换当前发布指针；原文件和历史版本仍保留。目录项目读取时还会检查实际文件是否匹配。','fine'));
  for(const snapshot of state.project.snapshots){const row=el('div',undefined,'history-row'),button=el('button',short(snapshot.release_id));button.title=snapshot.release_id;button.addEventListener('click',run(async()=>{await selectSnapshot(snapshot.release_id);}));row.append(button,document.createTextNode('　'),badge(snapshot.revoked?'已撤销':snapshot.release_id===state.project.active_release?'当前发布':snapshot.approver?'已审核':'候选版本'));
    row.append(el('p',stamp(snapshot.created_at)+' · 提交 '+snapshot.author+(snapshot.approver?' · 审核 '+snapshot.approver:'')));
    if(has('publisher')&&!snapshot.revoked){const actions=el('div',undefined,'history-actions');if(snapshot.approver&&snapshot.release_id!==state.project.active_release){const rollback=el('button','回退至此版本');rollback.addEventListener('click',run(async()=>{await api('releases/'+enc(snapshot.release_id)+'/rollback','POST',{expected_active:state.project.active_release});notice('已切换发布指针。');await refresh();}));actions.append(rollback);}const revoke=el('button','撤销使用权限');revoke.addEventListener('click',run(async()=>{if(!confirm('撤销此版本后，绑定它的 Agent 读取会立即被拒绝。继续？'))return;await api('releases/'+enc(snapshot.release_id)+'/revoke','POST');notice('已撤销该版本。');await refresh();}));actions.append(revoke);row.append(actions);}content.append(row);
  }
}
on('connect',()=>{$('login-dialog').showModal();});on('empty-connect',()=>{$('login-dialog').showModal();});
$('login-form').addEventListener('submit',run(async event=>{event.preventDefault();await connect($('token').value);$('token').value='';$('login-dialog').close();notice('已连接工作区。');}));
on('refresh',()=>refresh());on('new-project',()=>{if(state.setup?.local_setup)openSetup(true);else $('project-dialog').showModal();});on('upload',()=>{$('upload-dialog').showModal();});
for(const button of document.querySelectorAll('[data-close]'))button.addEventListener('click',()=>$(button.dataset.close).close());
$('project-select').addEventListener('change',run(()=>selectProject($('project-select').value)));
$('snapshot-select').addEventListener('change',run(()=>selectSnapshot($('snapshot-select').value)));
$('file-filter').addEventListener('input',renderTree);
for(const button of document.querySelectorAll('[data-view]'))button.addEventListener('click',()=>{state.view=button.dataset.view;renderView();});
$('project-form').addEventListener('submit',run(async event=>{
  event.preventDefault();const values=Object.fromEntries(new FormData(event.target));const data={id:values.id,name:values.name,dependencies:{},checks:[]};
  if(Boolean(values.dependent)!==Boolean(values.dependency))throw new Error('请同时填写使用文件和依据文件。');
  if(values.dependent)data.dependencies[values.dependent]=[values.dependency];
  if(values.check_file||values.reference_file){if(!values.check_file||!values.reference_file)throw new Error('请同时填写检查文件和比较文件。');data.checks.push({id:'file-consistency',file:values.check_file,pointer:values.check_pointer,reference_file:values.reference_file,reference_pointer:values.reference_pointer});}
  await api('projects','POST',data);$('project-dialog').close();event.target.reset();state.snapshot=null;await refresh(data.id);notice('项目已创建。请上传完整文件集。');
}));
for(const id of ['upload-files','upload-directory'])$(id).addEventListener('change',run(async event=>{
  const files=[...event.target.files];if(!files.length)return;const body=new FormData();for(const file of files){const path=file.webkitRelativePath?file.webkitRelativePath.split('/').slice(1).join('/'):file.name;body.append('files',file,path);}
  const snap=await api(projectPath()+'/upload','POST',body);state.snapshot=snap;$('upload-dialog').close();event.target.value='';await refresh();notice('已建立候选版本，请查看变更和发布检查。');
}));
on('sync',async()=>{state.snapshot=await api(projectPath()+'/sync','POST');await refresh();notice('已同步目录并建立候选版本。');});
on('approve',async()=>{await api(snapshotPath()+'/approve','POST');await refresh();notice('已记录独立审核；等待发布者启用。');});
on('publish',async()=>{const guarded=Boolean(state.snapshot.writeback);await api(snapshotPath()+(guarded?'/apply':'/activate'),'POST',{expected_active:state.project.active_release});await refresh();notice(guarded?'已将审核后的修改写回原文件并发布。':'版本已发布，Agent 可以建立新的读取会话。');});
on('recover',async()=>{await api(snapshotPath()+'/recover','POST');await refresh();notice('已完成写回恢复，请查看当前版本状态。');});
on('agent-read',async()=>{
  const result=$('agent-result');result.replaceChildren(el('p','正在验证当前发布与实际文件…'));
  const token=state.demo?.identities.agent.token||state.token;
  try{const session=await api(projectPath()+'/sessions','POST',undefined,token);const data=await api('sessions/'+session.session_id+'/read?path='+enc(state.path),'GET',undefined,token);
    result.replaceChildren(badge('PASS · 已获准读取','pass'),el('p',state.path+' · '+(data.text!==null?'文本':'原文件 + 定位片段')),el('pre','release  '+short(session.release_id)+'\nsource   '+short(data.source_id)+'\nsha256   '+data.sha256+'\nactor    '+data.receipt.actor+'\nreceipt  '+short(data.receipt.id)),el('p','文件内容已通过专用网关读取，操作记录进入审计。','fine'));
  }catch(error){result.replaceChildren(badge('拒绝读取','blocked'),el('p',error.message));}
});
async function refreshWatch(){
  const project=state.project;
  $('watch-bar').hidden=project?.source!=='local';
  if(project?.source!=='local'){state.watch=null;return;}
  const watch=await api('projects/'+enc(project.id)+'/watch');
  if(state.project?.id!==project.id)return;
  state.watch=watch;
  const config=watch.config;
  $('watch-status').textContent=watch.error?'关注异常：'+watch.error:!config.enabled?'关注已暂停':!watch.worker_running?'关注服务未运行':config.mode==='guard'?'保存后分析未启用 · 请查看 Agent 连接状态':'保存后自动分析正在运行';
  const event=watch.last_event;
  $('watch-event').hidden=!event||state.seenEvent===event.release_id||state.snapshot?.release_id===event.release_id;
  if(event)$('watch-event').textContent=(event.kind==='before_write'?'有待审核修改':'发现文件变更')+' · '+event.paths.length+' 个文件 · 查看';
}
on('watch-settings',async()=>{
  await refreshWatch();$('watch-enabled').checked=state.watch.config.enabled;$('watch-mode').value=state.watch.config.mode;
  $('watch-save').disabled=!has('editor');$('watch-enabled').disabled=!has('editor');$('watch-mode').disabled=!has('editor');
  $('watch-scope').textContent='关注范围：'+(state.project.spec.includes||['**']).join('、')+'。范围在首次选择文件夹时设定；默认排除凭据、版本库和 Agent 配置。';
  $('watch-dialog').showModal();
});
$('watch-form').addEventListener('submit',run(async event=>{
  event.preventDefault();await api(projectPath()+'/watch','PUT',{...state.watch.config,enabled:$('watch-enabled').checked,mode:$('watch-mode').value});
  $('watch-dialog').close();await refreshWatch();notice('关注设置已保存。');
}));
on('watch-event',async()=>{
  const id=state.watch.last_event.release_id;await refresh();await selectSnapshot(id);state.view='changes';renderView();
});
let watching=false;
setInterval(async()=>{
  if(watching||document.hidden||!state.token||state.project?.source!=='local')return;
  watching=true;try{await refreshWatch();await refreshConnections();}catch(error){$('watch-status').textContent='无法连接关注服务：'+error.message;}finally{watching=false;}
},2000);
function setupMessage(text,error=false){$('setup-message').textContent=text;$('setup-message').className=error?'error':'fine';}
function openSetup(folder=false){
  const choose=folder||!state.project||state.project.source!=='local';
  $('folder-step').hidden=!choose;$('connection-step').hidden=choose;
  $('setup-step-folder').className=choose?'current':'complete';$('setup-step-agent').className=choose?'':'current';
  setupMessage('');if(!$('setup-dialog').open)$('setup-dialog').showModal();
  if(!choose)renderConnections();
}
async function refreshConnections(){
  const project=state.project;
  if(project?.source!=='local'){state.connections=null;$('connection-summary').hidden=true;return;}
  const data=await api('projects/'+enc(project.id)+'/connections');
  if(state.project?.id!==project.id)return;
  const changed=JSON.stringify(data)!==JSON.stringify(state.connections);state.connections=data;
  $('connection-summary').hidden=false;
  const verified=data.agents.filter(a=>a.state==='verified'), configured=data.agents.filter(a=>a.configured);
  $('connection-summary').textContent=verified.length?'已收到 '+verified.map(a=>a.name).join('、')+' 的受控工具请求 · 修改会进入审核流程':configured.length?'已安装 Agent 配置，等待在 Agent 中新开任务验证':'Agent 尚未连接 · 普通保存会被观察，写入前处理需要先连接';
  if(changed&&$('setup-dialog').open&&!$('connection-step').hidden)renderConnections();
}
function renderConnections(){
  const data=state.connections;if(!data)return;
  $('connection-root').textContent='关注目录：'+data.root;
  $('connection-coverage').textContent=data.coverage;
  $('add-another-folder').hidden=!state.setup?.local_setup;
  const list=$('connection-list');list.replaceChildren();
  const labels={not_connected:'尚未连接',waiting:'配置已安装 · 等待新任务',loaded:'Agent 已加载 · 等待文件操作',verified:'已收到受控工具请求',error:'需要处理'};
  for(const item of data.agents){
    const row=el('div',undefined,'connection-row'),text=el('div'),actions=el('div',undefined,'connection-actions');
    text.append(el('strong',item.name),el('p',labels[item.state]+(item.available?'':' · 未在本机 PATH 中发现 CLI'),'small'));
    if(item.last_tool)text.append(el('p','最近操作：'+item.last_tool+' · '+stamp(item.last_seen),'fine'));
    if(item.self_test?.passed)text.append(el('p','连接自检通过 · '+stamp(item.self_test.at),'fine'));
    if(item.error)text.append(el('p',item.error,'error small'));
    function action(label,fn,primary=false){const b=el('button',label,primary?'primary':'');b.disabled=!has('editor')||!state.setup?.local_setup||!data.supported;b.addEventListener('click',async()=>{b.disabled=true;try{await fn();await refreshConnections();}catch(error){setupMessage(error.message,true);}finally{b.disabled=false;}});actions.append(b);}
    if(!item.configured)action(item.installed?'修复连接':'连接',async()=>{await api(projectPath()+'/connections/'+item.agent,'POST');setupMessage('已安装 '+item.name+' 的项目接入配置。请运行连接自检，然后在 Agent 中新开任务。');},true);
    if(item.configured)action('连接自检',async()=>{setupMessage('正在检查配置、工具路径和原文件隔离…');await api(projectPath()+'/connections/'+item.agent+'/check','POST');setupMessage('自检通过。现在去 '+item.name+' 打开这个文件夹并新开任务，接入状态会自动更新。');});
    if(item.installed)action('断开',async()=>{await api(projectPath()+'/connections/'+item.agent,'DELETE');setupMessage('已移除 Filewise 接入配置。已有候选和版本保留，请重载 Agent 配置。');});
    action('查看配置',async()=>{const preview=await api(projectPath()+'/connections/'+item.agent+'/preview');$('config-preview').hidden=false;$('config-preview').open=true;$('config-preview-body').textContent=preview.entry+'\n'+(typeof preview.addition==='string'?preview.addition:JSON.stringify(preview.addition,null,2));});
    row.append(text,actions);list.append(row);
  }
  const verified=data.agents.some(a=>a.state==='verified');
  const configured=data.agents.some(a=>a.configured);
  $('setup-step-agent').className=configured?'complete':'current';
  $('setup-step-check').className=verified?'complete':configured?'current':'';
  $('connection-next').replaceChildren(el('strong',verified?'可以继续在 Agent 中工作':'接下来：在 Agent 中验证一次'),el('p',verified?'修改将出现在工作台的“待审核修改”中。查看差异和检查后，点击“审核通过”，再“写回原文件并发布”。':'在上方目录中新开一个 Agent 任务，让它读取或修改一个关注文件。若 Agent 提示信任本项目的钩子或扩展，请审核后启用一次；Pi 也可执行 /reload。仅点击自检不会标记为实际接入。'));
  if(!data.supported)setupMessage('本机原生受控连接目前仅支持 macOS；保存后分析仍可使用。',true);
  else if(!state.setup?.local_setup)setupMessage('此服务没有开启本机配置功能。请用 filewise start 打开连接向导。',true);
}
on('begin-setup',()=>openSetup(!state.project));on('agent-connections',async()=>{await refreshConnections();openSetup();});on('add-another-folder',()=>openSetup(true));
$('folder-form').addEventListener('submit',async event=>{
  event.preventDefault();const button=event.target.querySelector('button[type=submit]');button.disabled=true;
  try{setupMessage('正在建立文件基线…');const includes=$('folder-includes').value.split('\n').map(p=>p.trim()).filter(Boolean);const project=await api('setup/project','POST',{root:$('folder-path').value.trim(),name:$('folder-name').value.trim(),includes:includes.length?includes:['**']});state.snapshot=null;await refresh(project.id);openSetup();setupMessage('文件夹已开始关注。再连接你的常用 Agent，启用写入前处理。');}
  catch(error){setupMessage(error.message,true);}finally{button.disabled=false;}
});
async function demoStep(step){
  if(!has('editor'))throw new Error('请先将演示身份切换为编辑者。');
  state.snapshot=await api('demo/'+step,'POST');state.path='requirement.json';await refresh(state.demo.project_id);state.view='changes';renderView();notice(step==='change'?'需求已改为 120 kPa；检验计划仍为 100，发布被阻止。':'Excel 检验值已修复为 120。请切换审核者确认，再由发布者启用。');
}
on('demo-change',()=>demoStep('change'));on('demo-repair',()=>demoStep('repair'));
$('demo-role').addEventListener('change',run(async()=>{await connect(state.demo.identities[$('demo-role').value].token);notice('已切换为 '+$('demo-role').selectedOptions[0].textContent+'。');}));
(async()=>{try{const token=new URLSearchParams(location.hash.slice(1)).get('connect');if(token){history.replaceState(null,'',location.pathname);await connect(token);return;}const response=await fetch('/demo');if(response.ok){state.demo=await response.json();$('demo-banner').hidden=false;$('demo-role').hidden=false;await connect(state.demo.identities.editor.token);}else if(sessionStorage.getItem('filewise-token'))await connect(sessionStorage.getItem('filewise-token'));}catch(error){sessionStorage.removeItem('filewise-token');notice(error.message,true);}})();
