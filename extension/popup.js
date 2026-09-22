const ids=['server','token','pluginId','accountId'];
const $=id=>document.getElementById(id);
function show(text,kind=''){ $('status').textContent=text; $('status').className='status '+kind; }
async function load(){
  const v=await chrome.storage.local.get({server:'http://127.0.0.1:8090',token:'',pluginId:'bilibili',accountId:'main'});
  ids.forEach(id=>$(id).value=v[id]||'');
  const state=await chrome.runtime.sendMessage({type:'connection_status'});
  show(state.connected?'已连接到 OmniMCP':'未连接到 OmniMCP',state.connected?'ok':'error');
}
$('save').onclick=async()=>{try{const settings=Object.fromEntries(ids.map(id=>[id,$(id).value.trim()]));await chrome.runtime.sendMessage({type:'save_settings',settings});show('已保存并连接','ok')}catch(e){show('连接失败：'+e.message,'error')}};
$('login').onclick=async()=>{try{const urls={xiaohongshu:'https://www.xiaohongshu.com/explore',bilibili:'https://www.bilibili.com/',wechat:'https://mp.weixin.qq.com/'};await chrome.tabs.create({url:urls[$('pluginId').value]||'https://www.xiaohongshu.com/'});show('登录页面已打开，请在当前 Chrome 中完成登录','ok')}catch(e){show('打开失败：'+e.message,'error')}};
load();
