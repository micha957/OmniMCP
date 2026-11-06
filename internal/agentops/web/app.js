'use strict';

const $ = id => document.getElementById(id);
let data, page = 'overview', filter = 'all', settingsDirty = false, toastTimer;
const navItems = [['overview', '◫', '概览'], ['queue', '✓', '待确认'], ['opportunities', '⌕', '参与机会'], ['drafts', '▤', '内容草稿'], ['calendar', '▦', '内容日历'], ['threads', '◌', '对话跟进'], ['metrics', '↗', '数据复盘'], ['activity', '↻', '运行记录'], ['settings', '⚙', '角色设置']];
const labels = {note:'笔记',comment:'评论',reply:'回复',draft:'草稿',ready:'内容已整理',archived:'已归档',pending:'待判断',drafted:'草稿已备',skipped:'已跳过',attention:'需要你处理',running:'运行中',completed:'已完成',failed:'未完成',interrupted:'已中断',sent:'已发送',answered:'已回复',executing:'执行中',succeeded:'已成功',unknown:'结果不明',rejected:'已驳回',expired:'已过期'};
const approvalLabels = {pending:'待你确认',executing:'正在执行',succeeded:'已成功',failed:'执行失败',unknown:'结果不明',rejected:'已驳回',expired:'已过期'};
const platformName = id => (data.platforms||[]).find(p=>p.id===(id||'xiaohongshu'))?.name || id;

function h(tag, attrs = {}, ...children) {
  const node = document.createElement(tag);
  for (const [key, value] of Object.entries(attrs)) {
    if (key.startsWith('on')) node.addEventListener(key.slice(2), value);
    else if (key === 'value') node.value = value;
    else if (key === 'checked') node.checked = value;
    else if (value !== false && value != null) node.setAttribute(key, value === true ? '' : value);
  }
  for (const child of children.flat(Infinity)) {
    if (child != null) node.append(child instanceof Node ? child : document.createTextNode(String(child)));
  }
  return node;
}

function button(text, fn, style = '', disabled = false) {
  return h('button', {class:'button '+style, disabled, onclick:async e => {
    const target = e.currentTarget;
    target.disabled = true;
    try { await fn(); } catch (err) { toast(err.message); }
    finally { if (target.isConnected) target.disabled = false; }
  }}, text);
}

function badge(status) { return h('span', {class:'badge '+(['ready','drafted','completed','sent','answered','succeeded'].includes(status)?'good':['attention','failed','unknown','pending'].includes(status)?'warm':'muted')}, labels[status]||status); }
function approvalBadge(status) { return h('span',{class:'badge '+(status==='succeeded'?'good':['pending','failed','unknown'].includes(status)?'warm':'muted')}, approvalLabels[status]||status); }
function date(value) { if (!value || value.startsWith('0001')) return '—'; return new Intl.DateTimeFormat('zh-CN',{timeZone:'Asia/Shanghai',month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit',hour12:false}).format(new Date(value)); }
function toast(message) { clearTimeout(toastTimer); $('toast').textContent=message; $('toast').hidden=false; toastTimer=setTimeout(()=>$('toast').hidden=true,5500); }
async function api(path, method='GET', body) {
  const response = await fetch(path,{method,headers:body===undefined?{}:{'Content-Type':'application/json'},body:body===undefined?undefined:JSON.stringify(body)});
  const out = await response.json();
  if (!response.ok) throw new Error(out.error || '操作未完成，请重试');
  return out;
}
async function refresh(force=false) {
  data = await api('/api/state');
  if (force || (!$('dialog').open && !(page==='settings'&&settingsDirty))) render();
}
async function navigate(next) {
  if (settingsDirty && !confirm('角色设置还没有保存，确定离开吗？')) return;
  settingsDirty=false; page=next; filter='all'; render(); window.scrollTo({top:0});
}
function heading(title, subtitle, actions=[]) { return h('div',{class:'page-heading'},h('div',{},h('div',{class:'eyebrow'},'AGENT WORKSPACE · '+(data.state.config.stage==='live'?'LIVE':'PRELAUNCH')),h('h1',{},title),h('div',{class:'sub'},subtitle)),h('div',{class:'heading-actions'},actions)); }
function empty(title, text, actions=[]) { return h('div',{class:'empty'},h('span',{class:'empty-icon'},'◌'),h('h3',{},title),h('p',{},text),h('div',{class:'row',style:undefined},actions)); }
function section(title, subtitle, actions=[]) { return h('div',{class:'section-head'},h('div',{},h('h2',{},title),subtitle?h('div',{class:'sub'},subtitle):null),h('div',{class:'row'},actions)); }
function field(id,label,value='',type='input',hint='') {
  const control = h(type,{id,value,rows:type==='textarea'?4:undefined,type:type==='input'?'text':undefined});
  return h('div',{class:'field'},h('label',{for:id},label),control,hint?h('small',{},hint):null);
}
function openDialog(title, ...nodes) { $('dialog-title').textContent=title; $('dialog-body').replaceChildren(...nodes); $('dialog').showModal(); }
function closeDialog() { $('dialog').close(); }

function render() {
  const s = data.state;
  document.title=s.config.product+' · 运营工作台';
  $('product-name').textContent=s.config.product;
  $('breadcrumb').textContent='工作空间 / '+navItems.find(n=>n[0]===page)[2];
  $('model-status').textContent=data.model_configured?'模型已配置':'模型待配置';
  $('model-status').className='pill'+(data.model_configured?' good':'');
  $('model-status').title=data.model_configured?data.model:'初始内容可直接编辑；配置模型后可处理新帖子与回复';
  $('run-button').textContent=data.running?'正在准备…':'准备下一轮';
  $('run-button').disabled=data.running;
  const waiting=s.drafts.filter(d=>d.approval?.status==='pending').length;
  $('nav').replaceChildren(...navItems.map(([id,icon,label])=>h('button',{class:'nav-item'+(page===id?' active':''),onclick:()=>navigate(id)},h('span',{class:'nav-icon'},icon),label,id==='queue'&&waiting?h('span',{class:'nav-count'},waiting):null)));
  $('content').replaceChildren(...({overview:overview,queue:queue,calendar:calendar,metrics:metrics,drafts:drafts,opportunities:opportunities,threads:threads,activity:activity,settings:settings}[page])().flat(Infinity).filter(Boolean));
  if(data.runtime_error) $('content').prepend(h('div',{class:'error-banner'},data.runtime_error));
}

function overview() {
  const s=data.state, active=s.drafts.filter(d=>d.status!=='archived');
    const waiting=s.drafts.filter(d=>d.approval?.status==='pending').length;
  const needs=data.attention||[];
  const stats=[['待你确认',waiting,'确认后才会发到平台'],['内容草稿',active.length,'笔记、评论与回复'],['待跟进对话',s.threads.filter(t=>t.status==='pending').length,'优先接住已有的回复'],['待处理事项',needs.length,'账号、失败与需要判断的内容']];
  const flow=[['01','发现值得回答的问题','读正文和讨论，确认产品能帮上什么忙。'],['02','准备具体、有依据的回答','先解答，适合时说明开发者身份与产品能力。'],['03','跟进对话，积累内容','记住上下文，把反复出现的问题写成下一篇笔记。']];
  return [heading(s.config.product+'的运营工作台','AI 准备内容和回复，每一次平台操作都由你确认。',[button('↻ 同步平台',()=>startRun('sync'),'',data.running)]),needs.length?h('section',{class:'panel attention'},h('h2',{},'需要你处理'),needs.map(n=>h('div',{class:'attention-item'},n.text))):null,h('div',{class:'stats'},stats.map(([label,n,sub])=>h('div',{class:'stat'},h('div',{class:'stat-label'},label,h('span',{},'↗')),h('div',{class:'stat-number'},String(n).padStart(2,'0')),h('small',{},sub)))),
    section('首批内容，已经有了起点','按当前开发版本整理，配图清单随草稿保存。',[button('查看全部 →',()=>navigate('drafts'),'quiet')]),h('div',{class:'cards'},active.filter(d=>d.kind==='note').slice(0,3).map(draftCard)),
    h('div',{class:'two-columns'},h('section',{class:'panel'},h('h2',{},'这个角色怎么工作'),flow.map(([n,title,text])=>h('div',{class:'flow-item'},h('div',{class:'step'},n),h('div',{},h('strong',{},title),h('p',{},text))))),h('section',{class:'panel'},h('h2',{},'产品知识，有据可查'),s.config.facts.slice(0,4).map(f=>h('div',{class:'fact'},h('span',{class:'fact-mark'},'✓'),h('span',{},f.text))))),
    h('p',{class:'footer-sub'},s.config.automation?'自动准备已开启 · 下一轮 '+date(s.next_run_at):'自动准备尚未开启 · 你可以先编辑内容，或添加真实帖子后手动运行。')];
}

function draftCard(d,index=0) {
  const text=d.kind==='note'?['▦  ◉  ▦','PHOTO TO PATTERN']:d.kind==='reply'?['◌  ↗  ◌','KEEP THE CONVERSATION']:['⌕  ·  ◌','A USEFUL ANSWER'];
  return h('article',{class:'card'},h('div',{class:'cover '+(['','mint','peach'][index%3])},h('span',{},text[0]),text[1]),h('div',{class:'card-top'},h('span',{class:'card-meta'},platformName(d.platform)+' · '+labels[d.kind]+' · '+(d.origin==='starter'?'初始内容':'模型草稿')),d.approval?approvalBadge(d.approval.status):badge(d.status)),h('h3',{},d.title),h('p',{class:'clamp'},d.body),h('div',{class:'card-footer'},h('span',{class:'card-meta'},d.assets?.length?d.assets.length+' 项配图建议':date(d.updated_at)),button('打开草稿 ↗',()=>editDraft(d),'quiet')));
}

function drafts() {
  const all=data.state.drafts.filter(d=>d.origin!=='decision');
  const visible=all.filter(d=>(filter==='all'?d.status!=='archived':filter==='archived'?d.status==='archived':d.status!=='archived'&&d.kind===filter)).slice().reverse();
  return [heading('内容草稿','笔记、评论和后续回复都在这里，编辑后会保存在本机。',[h('a',{class:'button',href:'/api/export',download:'agentops-drafts.md'},'↓ 导出内容')]),h('div',{class:'toolbar'},h('div',{class:'filters'},[['all','全部'],['note','笔记'],['comment','评论'],['reply','回复'],['archived','归档']].map(([id,label])=>h('button',{class:'filter'+(filter===id?' active':''),onclick:()=>{filter=id;render();}},label))),h('span',{class:'sub'},visible.length+' 份内容')),
    visible.length?h('div',{class:'cards'},visible.map(draftCard)):empty('这里还没有这类草稿','添加相关帖子或新回复，连接模型后运行，判断适合参与的内容会生成草稿。',[button('寻找参与机会',()=>navigate('opportunities'))])];
}

function localInput(value) { if(!value||value.startsWith('0001'))return ''; const t=new Date(value); return new Date(t.getTime()-t.getTimezoneOffset()*60000).toISOString().slice(0,16); }
function draftBody(extra={}) { const at=$('edit-schedule').value; return {hypothesis:$('edit-hypothesis').value,title:$('edit-title').value,body:$('edit-body').value,status:$('edit-status').value,platform:$('edit-platform').value,asset_paths:$('edit-assets').value.split('\n').map(x=>x.trim()).filter(Boolean),scheduled_at:at?new Date(at).toISOString():'',...extra}; }
function fileBase64(file) { return new Promise((resolve,reject)=>{const r=new FileReader();r.onload=()=>resolve(String(r.result).split(',')[1]);r.onerror=()=>reject(new Error('读取文件失败'));r.readAsDataURL(file);}); }

function editDraft(d) {
  const locked=d.status==='sent'||['pending','executing'].includes(d.approval?.status);
  const save=()=>api('/api/drafts/'+encodeURIComponent(d.id),'PATCH',draftBody());
  const title=field('edit-title','标题',d.title);
  const body=field('edit-body','正文',d.body,'textarea');body.classList.add('large');
  const status=h('select',{id:'edit-status'},[['draft','草稿'],['ready','内容已整理'],['archived','归档']].concat(d.status==='sent'?[['sent','已发送']]:[]).map(([value,label])=>h('option',{value},label)));status.value=d.status;
  const platform=h('select',{id:'edit-platform'},data.platforms.map(p=>h('option',{value:p.id},p.name)));platform.value=d.platform||'xiaohongshu';
  const picker=h('input',{type:'file',accept:'image/*',multiple:true,onchange:async e=>{try{for(const file of e.target.files){const out=await api('/api/assets','POST',{name:file.name,data:await fileBase64(file)});$('edit-assets').value=($('edit-assets').value.trim()+'\n'+out.path).trim();}toast('素材已加入');}catch(err){toast(err.message);}}});
  const adaptTargets=data.platforms.filter(p=>p.id!==(d.platform||'xiaohongshu'));
  const adapt=h('select',{id:'adapt-platform'},adaptTargets.map(p=>h('option',{value:p.id},p.name)));
  const approval=d.approval?h('div',{class:'note'},'确认状态：'+(approvalLabels[d.approval.status]||d.approval.status)+(d.approval.error?' · '+d.approval.error:'')+(d.approval.reason?' · 驳回原因：'+d.approval.reason:'')):null;
  const receipt=d.sent_at&&!d.sent_at.startsWith('0001')?h('div',{class:'note'},'已发送 '+date(d.sent_at)+(d.platform_id?' · 平台 ID '+d.platform_id:'')+(d.receipt?' · '+d.receipt:'')):null;
  openDialog('编辑'+labels[d.kind],approval,receipt,d.schedule_error?h('div',{class:'error-banner'},d.schedule_error):null,data.style?.[d.id]?h('div',{class:'note'},'可能有 AI 感：'+data.style[d.id].join('；')):null,h('div',{class:'form-grid'},h('div',{class:'field'},h('label',{for:'edit-platform'},'发布平台'),platform),h('div',{class:'field'},h('label',{for:'edit-status'},'内容状态'),status)),title,body,
    field('edit-hypothesis','这篇想验证什么',d.hypothesis||'','input','写清楚假设，发布后按 24 小时、72 小时、7 天记分。'),field('edit-assets','真实素材（本机绝对路径，每行一个）',(d.asset_paths||[]).join('\n'),'textarea','发布笔记需要图片，视频平台需要视频。较大的视频可直接放进素材目录。'),h('div',{class:'field'},h('label',{},'上传图片到素材库'),picker),
    h('div',{class:'field'},h('label',{for:'edit-schedule'},'排期（到时间进入确认队列，不会自动发布；需要状态为“内容已整理”）'),h('input',{id:'edit-schedule',type:'datetime-local',value:localInput(d.scheduled_at)})),
    d.assets?.length?h('div',{},h('h3',{},'待准备的真实素材'),h('ul',{class:'assets'},d.assets.map(a=>h('li',{},a)))):h('div'),d.reason?h('p',{class:'sub'},d.reason):h('div'),
    d.kind==='note'&&!locked?h('div',{class:'row'},adapt,button('改写到该平台',async()=>{await api('/api/drafts/'+encodeURIComponent(d.id)+'/adapt','POST',{platform:$('adapt-platform').value});closeDialog();await refresh(true);toast('已生成新平台的草稿');})):null,
    h('div',{class:'dialog-footer'},button('复制正文',async()=>{await navigator.clipboard.writeText($('edit-body').value);toast('正文已复制');}),locked?null:button('保存草稿',async()=>{await save();closeDialog();await refresh(true);toast('草稿已保存');}),
      locked?null:d.kind==='note'?button('提交确认：存平台草稿',async()=>{await save();await api('/api/drafts/'+encodeURIComponent(d.id)+'/submit','POST',{mode:'draft'});closeDialog();await refresh(true);toast('已进入待确认，尚未发送');}):null,
      locked?null:button('提交确认：发布',async()=>{await save();await api('/api/drafts/'+encodeURIComponent(d.id)+'/submit','POST',{mode:'publish'});closeDialog();await refresh(true);toast('已进入待确认，尚未发送');},'primary')));
}

function queueCard(d) {
  const a=d.approval, id=encodeURIComponent(a.id);
  const decide=(action,payload,done)=>button(({approve:'确认执行',reject:'驳回',retry:'已核对，重新执行'})[action],async()=>{
    let body=payload;
    if(action==='reject'){const reason=prompt('驳回原因（可留空）','');if(reason===null)return;body={reason};}
    if(action==='retry'&&!confirm('确定重新执行？如果上次其实已生效，内容会重复出现。'))return;
    await api('/api/approvals/'+id+'/'+action,'POST',body);await refresh(true);toast(done);},action==='approve'?'primary':'');
  return h('article',{class:'card list-card'},h('div',{class:'card-top'},h('span',{class:'card-meta'},platformName(d.platform)+' · '+labels[d.kind]+' · '+(a.mode==='draft'?'存平台草稿':'发布')+' · 账号 '+(a.account||'')),approvalBadge(a.status)),h('h3',{},d.title),h('p',{class:'pre'},d.body),
    d.asset_paths?.length?h('ul',{class:'assets'},d.asset_paths.map(x=>h('li',{},x))):null,d.source_url?h('a',{href:d.source_url,target:'_blank',rel:'noopener noreferrer'},'查看原帖 ↗'):null,d.reason?h('div',{class:'reason'},d.reason):null,
    a.status==='unknown'?h('div',{class:'error-banner'},'可能已经在平台生效。请先到平台核对，不要直接重试。'):null,a.error?h('div',{class:'error-banner'},a.error):null,
    h('div',{class:'card-footer'},h('span',{class:'card-meta'},'提交于 '+date(a.submitted_at)),h('div',{class:'row'},a.status==='pending'?[decide('approve',{hash:a.hash},'已确认，正在执行'),decide('reject',{},'已驳回，可修改后重新提交')]:null,['failed','unknown'].includes(a.status)?decide('retry',{hash:a.hash},'已重新开始'):null)));
}

function queue() {
  const list=data.state.drafts.filter(d=>d.approval);
  const order={pending:0,unknown:1,failed:2,executing:3};
  const open=list.filter(d=>d.approval.status in order).sort((a,b)=>order[a.approval.status]-order[b.approval.status]);
  const done=list.filter(d=>!(d.approval.status in order)).slice(-10).reverse();
  return [heading('待确认','这里的内容只有你确认后才会发到平台。结果会自动写回草稿。',data.approvals_url?[h('a',{class:'button',href:data.approvals_url,target:'_blank',rel:'noopener noreferrer'},'完整操作记录 ↗')]:[]),
    open.length?h('div',{class:'list'},open.map(queueCard)):empty('现在没有等你确认的内容','在草稿里点“提交确认”，或到排期时间后，内容会出现在这里。',[button('查看草稿',()=>navigate('drafts'))]),
    done.length?[section('最近处理','已结束的确认记录。'),h('div',{class:'list'},done.map(queueCard))]:null];
}

function calendar() {
  const list=data.state.drafts.filter(d=>d.origin!=='decision'&&d.status!=='archived');
  const planned=list.filter(d=>d.scheduled_at&&!d.scheduled_at.startsWith('0001')).sort((a,b)=>a.scheduled_at.localeCompare(b.scheduled_at));
  const day=v=>new Intl.DateTimeFormat('zh-CN',{timeZone:'Asia/Shanghai',month:'long',day:'numeric',weekday:'short'}).format(new Date(v));
  const groups=new Map();planned.forEach(d=>{const k=day(d.scheduled_at);groups.set(k,[...(groups.get(k)||[]),d]);});
  const row=d=>h('div',{class:'cal-row'},h('span',{class:'card-meta'},d.scheduled_at?date(d.scheduled_at):platformName(d.platform)),h('strong',{},d.title),h('span',{class:'card-meta'},platformName(d.platform)),d.approval?approvalBadge(d.approval.status):badge(d.status),button('打开',()=>editDraft(d),'quiet'));
  const unplanned=list.filter(d=>d.kind==='note'&&d.status!=='sent'&&!planned.includes(d));
  return [heading('内容日历','到排期时间，内容进入待确认队列并等你确认，不会自动发布。'),
    planned.length?[...groups].map(([k,items])=>h('section',{class:'panel'},h('h2',{},k),items.map(row))):empty('还没有排期','打开一份笔记草稿，设置排期并把状态改为“内容已整理”。'),
    unplanned.length?h('section',{class:'panel'},h('h2',{},'尚未排期的笔记'),unplanned.map(row)):null];
}

function metrics() {
  const s=data.state, samples=s.samples||[];
  const groups=new Map();samples.forEach(m=>{const k=m.platform+'|'+(m.watch||'');groups.set(k,[...(groups.get(k)||[]),m]);});
  const health=Object.entries(s.health||{}).map(([p,x])=>h('div',{class:'fact'},h('span',{class:'fact-mark'},x.logged_in?'✓':'!'),h('span',{},platformName(p)+'：'+(x.error||(x.logged_in?'已登录 '+(x.nickname||''):'未登录'))+' · '+date(x.checked_at))));
  const inbox=Object.entries(s.inbox||{}).map(([p,x])=>h('div',{class:'fact'},h('span',{class:'fact-mark'},x.error?'!':x.complete?'✓':'…'),h('span',{},platformName(p)+'通知：'+(x.error||('读取 '+x.loaded+' 条'+(x.complete?'，已读完':'，还有更早的未读取')))+' · 最后成功 '+date(x.last_success))));
  const delta=(now,before)=>before==null||now===before?'':(now>before?' +':' ')+(now-before);
  const card=([k,list])=>{
    const latest=list[list.length-1], prev=list.length>1?list[list.length-2]:null;
    const before=id=>prev?.notes?.find(n=>n.id===id);
    const notes=(latest.notes||[]).slice().sort((a,b)=>(b.likes+2*b.collects+3*b.comments)-(a.likes+2*a.collects+3*a.comments));
    const cell=(n,key)=>h('td',{},String(n[key]||0),h('small',{},delta(n[key]||0,before(n.id)?.[key]||(before(n.id)?0:null))));
    return h('section',{class:'panel'},h('h2',{},(latest.watch?'对标 · '+latest.watch:platformName(latest.platform))),h('p',{class:'sub'},'采样 '+date(latest.at)+' · 共 '+list.length+' 次'+(latest.stats?' · '+Object.entries(latest.stats).map(([a,b])=>({fans:'粉丝',follows:'关注',interaction:'获赞与收藏'}[a]||a)+' '+b).join(' · '):'')),
      notes.length?h('div',{class:'table-wrap'},h('table',{},h('thead',{},h('tr',{},['内容','浏览','赞','藏','评','转'].map(x=>h('th',{},x)))),h('tbody',{},notes.map(n=>h('tr',{},h('td',{},n.title||n.id),cell(n,'views'),cell(n,'likes'),cell(n,'collects'),cell(n,'comments'),cell(n,'shares')))))):h('p',{class:'sub'},'这次采样没有读到内容。'));
  };
  const point=(c,k)=>{const p=c.points?.[k];return h('td',{},p?`看${p.views||0} 赞${p.likes} 评${p.comments} 藏${p.collects}`:'—');};
  const scores=(data.scorecards||[]).length?h('section',{class:'panel'},h('h2',{},'发布实验'),h('p',{class:'sub'},'同一发布时长下比较；还没到时间或没采到的留空，不做估算。'),h('div',{class:'table-wrap'},h('table',{},h('thead',{},h('tr',{},['内容','24 小时','72 小时','7 天','当前'].map(x=>h('th',{},x)))),h('tbody',{},data.scorecards.map(c=>h('tr',{},h('td',{},c.title,c.hypothesis?h('small',{},' · '+c.hypothesis):null),point(c,'24h'),point(c,'72h'),point(c,'7d'),h('td',{},c.latest?`看${c.latest.views||0} 赞${c.latest.likes} 评${c.latest.comments} 藏${c.latest.collects}`:'—'))))))):null;
  return [heading('数据复盘','数据来自平台实际可读取的内容，并记录采样时间。',[button('↻ 同步平台',()=>startRun('sync'),'',data.running)]),
    scores,health.length||inbox.length?h('section',{class:'panel'},h('h2',{},'账号与通知'),health,inbox):null,
    groups.size?[...groups].map(card):empty('还没有数据','在角色设置绑定账号后点“同步平台”。没有读到的数据不会被估算。')];
}

function opportunities() {
  const list=data.state.opportunities.slice().reverse();
  return [heading('参与机会','找到真实问题，再决定能不能提供有用的信息。',[button('⌕ 从平台搜索',()=>startRun('discover'),'',data.running),button('＋ 添加帖子',addOpportunity,'primary')]),h('div',{class:'note'},'当前搜索方向：'+data.state.config.queries.join(' · ')),
    list.length?h('div',{class:'list'},list.map(o=>h('article',{class:'card list-card'},h('div',{class:'card-top'},h('span',{class:'card-meta'},(o.origin==='platform'?'平台读取':'手动收集')+' · '+date(o.created_at)),badge(o.status)),h('h3',{},o.title),h('details',{},h('summary',{},'阅读帖子上下文'),h('div',{class:'context'},o.body)),o.reason?h('div',{class:'reason'},o.reason):null,h('div',{class:'card-footer'},h('a',{href:o.url,target:'_blank',rel:'noopener noreferrer'},'打开原帖 ↗'),button('准备回答',()=>startRun('prepare'),'quiet',data.running))))):empty('把值得回答的帖子收集起来','可以粘贴真实帖子，也可以绑定浏览器账号后搜索。系统会结合正文、产品知识和角色规则判断是否参与。',[button('添加第一个帖子',addOpportunity,'primary')])];
}

function addOpportunity() {
  openDialog('收集一个相关帖子',field('opp-url','帖子链接','','input','完整的小红书帖子链接'),field('opp-title','帖子标题'),field('opp-body','正文与已有讨论','','textarea','保留对方的实际问题和必要上下文，最多 8000 字。'),h('div',{class:'dialog-footer'},button('保存帖子',async()=>{await api('/api/opportunities','POST',{url:$('opp-url').value,title:$('opp-title').value,body:$('opp-body').value});closeDialog();await refresh(true);toast('帖子已加入待判断列表');},'primary')));
}

function threads() {
  const list=data.state.threads.slice().sort((a,b)=>b.updated_at.localeCompare(a.updated_at));
  return [heading('对话跟进','把后续问题接住，让每次回答记得之前聊过什么。',[button('↻ 同步平台',()=>startRun('sync'),'',data.running),button('＋ 记录一段对话',()=>addThread(),'primary')]),h('div',{class:'note'},'绑定账号后，“同步平台”会把新评论导入这里；也可以手动记录。回复草稿提交确认后才会发送。'),
    list.length?h('div',{class:'list'},list.map(t=>{
      const latest=t.messages[t.messages.length-1];
      const draft=data.state.drafts.slice().reverse().find(d=>d.source_id===t.id&&d.status!=='archived');
      return h('article',{class:'card list-card'},h('div',{class:'card-top'},h('span',{class:'card-meta'},t.messages.length+' 条已记录消息 · '+date(t.updated_at)),badge(t.status)),h('h3',{},t.title),h('div',{class:'message'},h('small',{},platformName(t.platform)+' · '+latest.author),latest.text),h('details',{},h('summary',{},'完整上下文'),h('p',{class:'context'},t.context),t.messages.map(m=>h('div',{class:'message'},h('small',{},m.author+' · '+date(m.created_at)),m.text))),t.reason?h('div',{class:'reason'},t.reason):null,h('div',{class:'card-footer'},h('a',{href:t.url,target:'_blank',rel:'noopener noreferrer'},'打开讨论 ↗'),h('div',{class:'row'},draft?button(t.status==='pending'?'查看上一版草稿':'查看回复草稿',()=>editDraft(draft),'quiet'):null,button('记录新回复',()=>addMessage(t)),button('准备回答',()=>startRun('prepare'),'primary',data.running))));
    })):empty('持续跟进，从第一段对话开始','收集对方的新问题和此前语境。已经准备过的回复会保留；新消息到来后，再接着生成下一版回答。',[button('记录对话',()=>addThread(),'primary')])];
}

function addThread() {
  openDialog('记录一段需要跟进的对话',field('thread-url','原帖链接'),field('thread-title','这段对话的主题'),field('thread-context','此前聊过什么','','textarea'),h('div',{class:'form-grid'},field('thread-author','对方称呼'),field('thread-message','最新收到的消息','','textarea')),h('div',{class:'dialog-footer'},button('保存对话',async()=>{await api('/api/threads','POST',{url:$('thread-url').value,title:$('thread-title').value,context:$('thread-context').value,author:$('thread-author').value,message:$('thread-message').value});closeDialog();await refresh(true);toast('对话已加入跟进列表');},'primary')));
}
function addMessage(t) {
  openDialog('记录新收到的回复',field('message-author','对方称呼',t.messages[t.messages.length-1].author),field('message-text','回复内容','','textarea'),h('div',{class:'dialog-footer'},button('保存新回复',async()=>{await api('/api/threads/'+encodeURIComponent(t.id)+'/messages','POST',{author:$('message-author').value,text:$('message-text').value});closeDialog();await refresh(true);toast('新回复已记录，可以准备下一版回答');},'primary')));
}

function activity() {
  const runs=data.state.runs.slice().reverse();
  return [heading('运行记录','每一轮读到了什么、准备了什么、哪里需要处理，都留下结果。',data.running?[button('停止本轮',async()=>{await api('/api/cancel','POST',{});toast('已请求停止');},'danger')]:[]),
    runs.length?h('section',{class:'panel'},runs.map(r=>h('article',{class:'run'},h('span',{class:'run-dot '+(r.status==='failed'?'failed':'')},r.status==='running'?'↻':r.status==='completed'?'✓':'!'),h('div',{},h('h3',{},r.trigger==='schedule'?'定时运行':'手动运行'),h('p',{},r.summary||'正在读取上下文并准备内容…'),h('small',{},date(r.started_at)+' · '+r.drafts+' 份草稿 · '+r.skipped+' 项跳过 · '+r.escalated+' 项转交'+(r.tokens?' · '+r.tokens+' tokens':''))),badge(r.status)))):empty('还没有运行记录','可以先编辑三篇初始内容。收集真实问题并连接模型后，开始第一轮内容准备。',[button('准备下一轮',()=>startRun('prepare'),'primary')])];
}

function settings() {
  const c=data.state.config;
  const stage=h('select',{id:'cfg-stage'},[['prelaunch','上线前：只按开发中表述'],['live','已上线：可说明入口']].map(([value,label])=>h('option',{value},label)));stage.value=c.stage;
  const root=h('div',{class:'settings'},h('section',{class:'panel'},h('h2',{},'角色与目标'),h('div',{class:'field'},h('label',{for:'cfg-stage'},'运营阶段'),stage),h('div',{class:'form-grid'},field('cfg-name','角色名称',c.name),field('cfg-product','产品名称',c.product)),field('cfg-persona','这个角色如何表达',c.persona,'textarea'),field('cfg-goal','当前运营目标',c.goal,'textarea'),field('cfg-entry','预留的产品访问说明',c.entry,'input','当前产品尚未上线，可以留空。')),
    h('section',{class:'panel'},h('h2',{},'发现需求与内容方向'),field('cfg-account','小红书账号标识',c.account_id,'input','与 OmniMCP 里配置的账号标识一致；填写后可读取真实平台内容。'),field('cfg-accounts','其他平台账号',(c.accounts||[]).map(a=>a.platform+' | '+a.account_id).join('\n'),'textarea','每行一个：平台 | 账号标识。平台可选 '+data.platforms.map(p=>p.id).join('、')+'。'),field('cfg-watch','对标账号（目前支持小红书）',(c.watch||[]).map(w=>[w.name,w.platform,w.user_id,w.xsec_token||''].join(' | ')).join('\n'),'textarea','每行一个：名称 | 平台 | 用户 ID | xsec_token。'),field('cfg-queries','搜索词',c.queries.join('\n'),'textarea','每行一个，优先描述用户的问题。'),field('cfg-topics','内容选题',c.topics.map(t=>t.title+' | '+t.brief).join('\n'),'textarea','每行一个选题：标题 | 需要说明的内容。修改标题会作为新选题处理。')),
    h('section',{class:'panel'},h('h2',{},'行动规则'),field('cfg-rules','每一轮都遵守的规则',c.rules.join('\n'),'textarea','每行一条；系统会同时提供已核对的产品知识。')),
    h('section',{class:'panel'},h('h2',{},'自动准备'),h('label',{class:'check'},h('input',{id:'cfg-auto',type:'checkbox',checked:c.automation}),'按计划自动准备本地草稿'),h('label',{class:'check'},h('input',{id:'cfg-discover',type:'checkbox',checked:c.discover}),'每轮先从平台收集相关帖子'),h('div',{class:'form-grid'},field('cfg-interval','运行间隔（分钟）',c.interval_minutes),field('cfg-max','每轮最多处理的任务',c.max_tasks)),h('p',{class:'sub'},'已有对话优先，其次是候选帖子，再准备尚无草稿的选题。每轮最多搜索 3 个关键词、新增 5 个帖子。')),
    h('div',{class:'savebar'},h('span',{class:'sub'},data.model_configured?'模型已配置 · '+data.model:'初始内容可直接编辑，模型待配置。'),button('保存角色设置',saveSettings,'primary')));
  root.addEventListener('input',()=>settingsDirty=true);
  return [heading('角色设置','把人设、产品知识和运营节奏放在同一个地方。'),root];
}

async function saveSettings() {
  const c=structuredClone(data.state.config);
  c.name=$('cfg-name').value;c.product=$('cfg-product').value;c.persona=$('cfg-persona').value;c.goal=$('cfg-goal').value;c.entry=$('cfg-entry').value;c.account_id=$('cfg-account').value.trim();
  const lines=id=>$(id).value.split('\n').map(x=>x.trim()).filter(Boolean);
  c.queries=lines('cfg-queries');c.rules=lines('cfg-rules');
  c.topics=lines('cfg-topics').map((line,i)=>{const [title,...brief]=line.split('|');const old=c.topics.find(t=>t.title===title.trim());return {id:old?.id||'topic-'+Date.now()+'-'+i,title:title.trim(),brief:brief.join('|').trim()};});
  c.stage=$('cfg-stage').value;
  c.accounts=lines('cfg-accounts').map(line=>{const [platform,account_id]=line.split('|').map(x=>x.trim());return {platform,account_id};});
  c.watch=lines('cfg-watch').map(line=>{const [name,platform,user_id,xsec_token]=line.split('|').map(x=>x.trim());return {name,platform,user_id,xsec_token};});
  c.automation=$('cfg-auto').checked;c.discover=$('cfg-discover').checked;c.interval_minutes=Number($('cfg-interval').value);c.max_tasks=Number($('cfg-max').value);
  await api('/api/config','PUT',c);settingsDirty=false;await refresh(true);toast('角色、规则和运行安排已保存');
}
async function startRun(mode) { await api('/api/run','POST',{mode});await refresh(true);toast(mode==='discover'?'正在读取平台内容':mode==='sync'?'正在同步通知、账号和数据':'已开始准备，结果会保存在草稿和运行记录里'); }
$('dialog-close').addEventListener('click',closeDialog);
$('dialog').addEventListener('click',e=>{if(e.target===$('dialog') && (e.clientX<$('dialog').getBoundingClientRect().left||e.clientX>$('dialog').getBoundingClientRect().right))closeDialog();});
$('run-button').addEventListener('click',()=>startRun('prepare').catch(e=>toast(e.message)));
refresh().catch(e=>{$('content').replaceChildren(h('div',{class:'error-banner'},e.message));});
setInterval(()=>{if(data?.running||(page==='queue'&&!document.hidden))refresh().catch(e=>toast(e.message));},3000);
