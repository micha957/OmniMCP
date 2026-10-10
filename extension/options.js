const ids = ['server', 'token', 'pluginId', 'accountId'];
const status = document.getElementById('status');
chrome.storage.local.get({server:'http://127.0.0.1:8090',token:'',pluginId:'*',accountId:'default'}).then(values => {
  ids.forEach(id => document.getElementById(id).value = values[id] || '');
});
document.getElementById('save').onclick = async () => {
  try {
    const settings = Object.fromEntries(ids.map(id => [id, document.getElementById(id).value.trim()]));
    const result = await chrome.runtime.sendMessage({type:'save_settings',settings});
    if (!result?.ok) throw new Error(result?.error || '保存失败');
    status.textContent = '设置已保存，连接中';
  } catch(error) { status.textContent = error.message; }
};
setInterval(async () => {
  try {
    const result = await chrome.runtime.sendMessage({type:'connection_status'});
    status.textContent = result.connected ? '已连接到 OmniMCP' : result.error || (result.connecting ? '正在连接' : '未连接到 OmniMCP');
  } catch(error) { status.textContent = error.message; }
}, 1500);
