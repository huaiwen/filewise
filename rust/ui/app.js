'use strict';
(async () => {
const $ = s => document.querySelector(s);
const node = (tag, text, cls) => { const e = document.createElement(tag); if (text != null) e.textContent = text; if (cls) e.className = cls; return e; };
const messages = await (await fetch('/ui/strings.json', {credentials:'omit'})).json();
let preference = localStorage.getItem('filewise-locale') || 'auto';
let i18n, data = {folders:[],jobs:[]}, editing = null, detailJob = null, refreshing = false, connected = false, startupSaved = false;
let token = sessionStorage.getItem('filewise-local-token') || '';
const errors = new Map();
const t = (key, params) => i18n.t(key, params);
const button = (text, action, cls = '') => { const b = node('button', text, cls); b.type = 'button'; b.onclick = () => action().catch(notice); return b; };
const errorText = error => {
  const key = 'error.' + (error.code || '');
  if (Object.hasOwn(messages.en, key)) return t(key) + (error.message && (error.code.startsWith('model_') || error.code==='document_extract') ? '\n' + t('error.details',{message:error.message}) : '');
  return error.message || String(error) || t('error.operation');
};
function showError(selector, error) { errors.set(selector,error); $(selector).textContent=errorText(error); $(selector).hidden=false; }
function notice(error) { showError('#notice', error); }
const localError = code => Object.assign(new Error(''), {code});
if (location.hash.startsWith('#token=')) { token = decodeURIComponent(location.hash.slice(7)); sessionStorage.setItem('filewise-local-token', token); history.replaceState(null, '', location.pathname); }
async function api(operation, body) {
  const r = await fetch('/api/local/' + operation, {method:body === undefined ? 'GET':'POST', headers:{Authorization:'Bearer ' + token, 'Content-Type':'application/json'}, body:body === undefined ? undefined:JSON.stringify(body), credentials:'omit'});
  const v = await r.json();
  if (!r.ok) { if (r.status === 401 && !$('#login-dialog').open) $('#login-dialog').showModal(); throw Object.assign(new Error(v.error || t('error.operation')), {code:v.code}); }
  return v;
}
function empty(title, text) { const e = node('div', null, 'empty'); e.append(node('strong',title),node('p',text)); return e; }
function badge(status, text) { return node('span', text || t('state.' + status), 'badge ' + status); }
async function configure(folder, rules) { await api('configure',{project:folder.project,revision:folder.revision,rules}); await refresh(); }
async function action(job, name) {
  if (name === 'approve' && !confirm(t('confirm.rename',{path:job.proposed_path}))) return;
  if (name === 'undo' && !confirm(t('confirm.undo',{path:job.path}))) return;
  await api('action',{job:job.id,action:name}); await refresh();
}
function render() {
  $('#folder-count').textContent = i18n.number(data.folders.filter(f => f.rules.enabled).length);
  $('#file-count').textContent = i18n.number(data.folders.reduce((n,f)=>n+f.files,0));
  $('#review-count').textContent = i18n.number(data.jobs.filter(j=>j.status==='review').length);
  $('#error-count').textContent = i18n.number(data.jobs.filter(j=>['failed','undo_failed'].includes(j.status)).length + data.folders.filter(f=>f.error).length);
  const folders = data.folders.map(f => {
    const el = node('article', null, 'folder'), top = node('div',null,'folder-top'), bottom = node('div',null,'folder-bottom');
    top.append(node('span','▱','folder-icon'), node('h3',f.name));
    bottom.append(badge(f.error?'failed':'',f.error?t('folder.error'):f.rules.enabled?t('folder.monitored',{count:f.files}):t('folder.paused')),button(t('folder.rules'),async()=>openFolder(f)),button(t(f.rules.enabled?'folder.pause':'folder.resume'),()=>configure(f,{...f.rules,enabled:!f.rules.enabled})));
    el.append(top,node('p',f.root,'path'),bottom);
    if (f.error) el.append(node('p',errorText({message:f.error,code:f.error_code}),'error'));
    return el;
  });
  $('#folders').replaceChildren(...(folders.length?folders:[empty(t('folders.empty.title'),t('folders.empty.description'))]));
  const filter = $('#filter').value;
  const jobs = data.jobs.filter(j=>filter==='all'||j.status===filter).map(j=>{
    const el=node('article',null,'job'), info=node('div'), actions=node('div',null,'job-actions');
    info.append(node('p',j.output_path || j.path || t('jobs.protected'),'job-name'));
    const folder=data.folders.find(f=>f.project===j.project);
    info.append(node('div',(folder?.name || '')+' · '+(j.detected_at?i18n.date(j.detected_at):''),'job-sub'));
    if (j.status==='review') info.append(node('div',t('jobs.proposal',{path:j.proposed_path}),'job-sub'));
    if (j.error) info.append(node('div',errorText({message:j.error,code:j.error_code}),'job-sub'));
    if (j.analysis?.coverage==='basic_only') info.append(node('div',t('jobs.basic'),'job-sub'));
    if (j.analysis?.extraction?.status==='partial') info.append(node('div',t('jobs.partial'),'job-sub'));
    actions.append(badge(j.status));
    if (j.path) actions.append(button(t('jobs.details'),async()=>detail(j)));
    if (j.status==='review') actions.append(button(t('jobs.approve'),()=>action(j,'approve'),'primary'));
    if (['failed','undo_failed','superseded','review'].includes(j.status)) actions.append(button(t('jobs.retry'),()=>action(j,'retry')));
    if (j.status==='done' && j.output_path!==j.path) actions.append(button(t('jobs.undo'),()=>action(j,'undo')));
    el.append(info,actions); return el;
  });
  $('#jobs').replaceChildren(...(jobs.length?jobs:[empty(t('jobs.empty.title'),t('jobs.empty.description'))]));
}
async function refresh() {
  if (!token || refreshing) return;
  refreshing=true;
  try { data=await api('overview'); render(); connected=true; $('#notice').hidden=true; }
  catch(e) { connected=false; notice(e); }
  finally { refreshing=false; service(); }
}
function service() { $('#service').textContent=t(connected?'service.connected':'service.disconnected'); $('#light').classList.toggle('live',connected); }
const form=$('#folder-form'), field = name => form.elements.namedItem(name), split = value => value.split(/[,，]/).map(s=>s.trim()).filter(Boolean);
function options() {
  $('#model-options').hidden=field('engine').value!=='ollama';
  field('model').required=field('engine').value==='ollama';
  $('#rename-consent').hidden=field('naming').value!=='auto';
  field('consent').required=field('naming').value==='auto';
}
function formLabels() { $('#form-title').textContent=t(editing?'folder.edit':'folder.new'); $('#save-folder').textContent=t(editing?'folder.save':'folder.start'); }
function openFolder(folder=null) {
  editing=folder; form.reset(); $('#form-error').hidden=true; formLabels();
  for (const name of ['name','root','includes','excludes','process_existing']) field(name).disabled=!!folder;
  $('#pick').disabled=!!folder;
  if (folder) {
    field('name').value=folder.name; field('root').value=folder.root; field('includes').value=folder.includes.join(', '); field('excludes').value=folder.excludes.join(', ');
    for (const [key,value] of Object.entries(folder.rules)) { const e=field(key); if (!e || ['tags','fields'].includes(key)) continue; if(e.type==='checkbox') e.checked=value; else e.value=value; }
    field('tags').value=folder.rules.tags.join(', '); field('fields').value=Object.entries(folder.rules.fields).map(([n,p])=>n+'='+p).join('\n'); field('consent').checked=folder.rules.naming==='auto';
  }
  options(); $('#folder-dialog').showModal();
}
form.onsubmit=async event=>{
  event.preventDefault(); $('#save-folder').disabled=true; $('#form-error').hidden=true;
  try {
    const fields={}; for (const line of field('fields').value.split('\n').filter(s=>s.trim())) { const i=line.indexOf('='); if(i<1) throw localError('field.syntax'); const name=line.slice(0,i).trim(); if(Object.hasOwn(fields,name)) throw localError('field.duplicate'); Object.defineProperty(fields,name,{value:line.slice(i+1).trim(),enumerable:true}); }
    const rules={enabled:editing?.rules.enabled ?? true,process_existing:field('process_existing').checked,poll_seconds:+field('poll_seconds').value,settle_seconds:+field('settle_seconds').value,summary:field('summary').checked,tags:split(field('tags').value),fields,naming:field('naming').value,template:field('template').value,engine:field('engine').value,model:field('model').value,endpoint:field('endpoint').value};
    if (editing) await configure(editing,rules); else await api('register',{name:field('name').value,root:field('root').value,includes:split(field('includes').value),excludes:split(field('excludes').value),rules});
    $('#folder-dialog').close(); await refresh();
  } catch(e) { showError('#form-error',e); }
  finally { $('#save-folder').disabled=false; }
};
function detail(j) {
  detailJob=j;
  const el=$('#detail'); el.replaceChildren(node('h3',j.path),badge(j.status));
  const value=(label,text)=>{el.append(node('p',t(label),'detail-label'),node('div',text,'detail-value'));};
  value('detail.hash',j.sha256); value('detail.source',j.version);
  if(j.analysis) {
    value('detail.name',j.analysis.title); value('detail.coverage',t('coverage.'+j.analysis.coverage));
    if (j.analysis.extraction) {
      const extraction=j.analysis.extraction;
      value('detail.extraction',t('extraction.'+extraction.status));
      if(extraction.notes.length) value('detail.extraction_notes',extraction.notes.map(note=>{
        const page=/^page:(\d+):(no_text|extraction_failed)$/.exec(note);
        if(page) return t('extraction.page_'+page[2],{page:i18n.number(+page[1])});
        return Object.hasOwn(messages.en,'extraction.'+note)?t('extraction.'+note):note;
      }).join('\n'));
    }
    value('detail.summary',j.analysis.summary || t('detail.no_summary')); const tags=node('div',null,'tags'); tags.append(...j.analysis.tags.map(tag=>badge('',tag))); el.append(tags); el.append(node('pre',j.analysis_fields_json || JSON.stringify(j.analysis.fields,null,2)));
  }
  if(j.proposed_path) value('detail.proposal',j.proposed_path);
  if(j.output_version) value('detail.saved',j.output_version);
  if(j.error) el.append(node('p',errorText({message:j.error,code:j.error_code}),'error'));
  el.append(node('p',t('detail.gate'),'hint'));
  if(!$('#detail-dialog').open) $('#detail-dialog').showModal();
}
function language() {
  i18n=FilewiseI18n.create(messages,FilewiseI18n.resolve(preference,navigator.languages));
  document.documentElement.lang=i18n.locale;
  FilewiseI18n.apply(document,i18n);
  for(const selector of document.querySelectorAll('#language, [data-language]')) selector.value=['auto','en','zh-CN'].includes(preference)?preference:'auto';
  render(); formLabels(); service();
  for(const [selector,error] of errors) if(!$(selector).hidden) $(selector).textContent=errorText(error);
  if(startupSaved) $('#autostart-note').textContent=t('settings.autostart.saved');
  if($('#detail-dialog').open && detailJob) detail(detailJob);
}
for(const heading of document.querySelectorAll('.dialog-heading')) {
  const selector=$('#language').cloneNode(true); selector.removeAttribute('id'); selector.dataset.language=''; selector.className='dialog-language';
  heading.insertBefore(selector,heading.querySelector('.close'));
}
for(const selector of document.querySelectorAll('#language, [data-language]')) selector.onchange=()=>{preference=selector.value;localStorage.setItem('filewise-locale',preference);language();};
window.addEventListener('languagechange',()=>{if(preference==='auto')language();});
$('#add').onclick=()=>openFolder(); $('#filter').onchange=render;
field('engine').onchange=options; field('naming').onchange=options;
$('#pick').onclick=async()=>{ $('#pick').disabled=true; try { const v=await api('pick',{locale:i18n.locale}); field('root').value=v.root; if(!field('name').value) field('name').value=v.root.replace(/\/$/,'').split('/').pop(); } catch(e){showError('#form-error',e);} finally{$('#pick').disabled=false;} };
for(const e of document.querySelectorAll('[data-close]')) e.onclick=()=>document.getElementById(e.dataset.close).close();
$('#settings').onclick=async()=>{ try {const v=await api('autostart'); $('#autostart').checked=v.enabled;$('#autostart').disabled=!v.supported;$('#settings-dialog').showModal();}catch(e){notice(e);} };
$('#autostart').onchange=async()=>{const e=$('#autostart');e.disabled=true;try{const v=await api('autostart',{enabled:e.checked});e.checked=v.enabled;startupSaved=true;$('#autostart-note').textContent=t('settings.autostart.saved');}catch(error){e.checked=!e.checked;notice(error);}finally{e.disabled=false;}};
$('#logout').onclick=()=>{sessionStorage.removeItem('filewise-local-token');token='';connected=false;service();$('#settings-dialog').close();$('#login-dialog').showModal();};
$('#login-form').onsubmit=async e=>{e.preventDefault();token=$('#token').value.trim();try{await api('status');sessionStorage.setItem('filewise-local-token',token);$('#token').value='';$('#login-dialog').close();await refresh();}catch(error){showError('#login-error',error);}};
language();
if(!token) $('#login-dialog').showModal(); else refresh();
setInterval(refresh,2500);
})().catch(error=>{const notice=document.querySelector('#notice');notice.textContent=error.message;notice.hidden=false;});
