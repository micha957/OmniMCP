const ids=['server','token','pluginId','accountId'];
const $=id=>document.getElementById(id);
function show(text,kind=''){ $('status').textContent=text; $('status').className='status '+kind; }
async function load(){
  const v=await chrome.storage.local.get({server:'http://127.0.0.1:8090',token:'',pluginId:'zhihu',accountId:'default'});
  ids.forEach(id=>$(id).value=v[id]||'');
  const state=await chrome.runtime.sendMessage({type:'connection_status'});
  show(state?.ok !== false && state.connected?'已连接到 OmniMCP':'未连接到 OmniMCP',state?.ok !== false && state.connected?'ok':'error');
}
$('save').onclick=async()=>{try{const settings=Object.fromEntries(ids.map(id=>[id,$(id).value.trim()]));const result=await chrome.runtime.sendMessage({type:'save_settings',settings});if(result?.ok===false)throw new Error(result.error||'保存失败');show(result.connected?'已保存并连接':result.error||'正在连接',result.connected?'ok':'error')}catch(e){show('连接失败：'+e.message,'error')}};
$('login').onclick=async()=>{try{const urls={xiaohongshu:'https://www.xiaohongshu.com/explore',bilibili:'https://www.bilibili.com/',wechat:'https://mp.weixin.qq.com/',zhihu:'https://www.zhihu.com/signin'};await chrome.tabs.create({url:urls[$('pluginId').value]||'https://www.xiaohongshu.com/'});show('登录页面已打开，请在当前 Chrome 中完成登录','ok')}catch(e){show('打开失败：'+e.message,'error')}};
load();

setInterval(async()=>{try{const r=await chrome.runtime.sendMessage({type:'connection_status'});show(r.connected?'已连接到 OmniMCP':r.error||(r.connecting?'正在连接':'未连接'),r.connected?'ok':'error')}catch(e){show(e.message,'error')}},1500);
