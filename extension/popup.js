const ids=['server','token','pluginId','accountId'];
const $=id=>document.getElementById(id);
function show(text,kind=''){ $('status').textContent=text; $('status').className='status '+kind; }
let loginURLs={xiaohongshu:'https://www.xiaohongshu.com/explore',bilibili:'https://www.bilibili.com/',zhihu:'https://www.zhihu.com/signin',douyin:'https://www.douyin.com',kuaishou:'https://www.kuaishou.com',weibo:'https://weibo.com',youtube:'https://accounts.google.com/ServiceLogin?service=youtube&continue=https://www.youtube.com/',tiktok:'https://www.tiktok.com/login'};
// Platforms come from the server so new plugins appear without editing the popup.
async function loadPlatforms(server,token){
  try{
    const res=await fetch(server.replace(/\/+$/,'')+'/api/plugins',{headers:token?{Authorization:`Bearer ${token}`}:{}});
    if(!res.ok)return;
    const list=(await res.json()).filter(p=>p.requires_browser&&p.browser_mode!=='managed').sort((a,b)=>a.id.localeCompare(b.id));
    if(!list.length)return;
    $('pluginId').innerHTML='<option value="*">全部平台</option>';
    for(const p of list){const o=document.createElement('option');o.value=p.id;o.textContent=p.name;$('pluginId').append(o);if(p.login_url)loginURLs[p.id]=p.login_url;}
  }catch(_){/* Keep the built-in list when the server is unreachable. */}
}
async function load(){
  const v=await chrome.storage.local.get({server:'http://127.0.0.1:8090',token:'',pluginId:'*',accountId:'default'});
  await loadPlatforms(v.server,v.token);
  ids.forEach(id=>$(id).value=v[id]||'');
  const state=await chrome.runtime.sendMessage({type:'connection_status'});
  show(state?.ok !== false && state.connected?'已连接到 OmniMCP':'未连接到 OmniMCP',state?.ok !== false && state.connected?'ok':'error');
}
$('save').onclick=async()=>{try{const settings=Object.fromEntries(ids.map(id=>[id,$(id).value.trim()]));const result=await chrome.runtime.sendMessage({type:'save_settings',settings});if(result?.ok===false)throw new Error(result.error||'保存失败');show(result.connected?'已保存并连接':result.error||'正在连接',result.connected?'ok':'error')}catch(e){show('连接失败：'+e.message,'error')}};
$('login').onclick=async()=>{try{const url=loginURLs[$('pluginId').value];if(!url)throw new Error('请先选择具体平台再打开登录页');await chrome.tabs.create({url});show('登录页面已打开，请在当前 Chrome 中完成登录','ok')}catch(e){show('打开失败：'+e.message,'error')}};
load();

setInterval(async()=>{try{const r=await chrome.runtime.sendMessage({type:'connection_status'});show(r.connected?'已连接到 OmniMCP':r.error||(r.connecting?'正在连接':'未连接'),r.connected?'ok':'error')}catch(e){show(e.message,'error')}},1500);
