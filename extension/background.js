const DEFAULT_SERVER = 'http://127.0.0.1:8090';
let socket;

async function settings() {
  const s = await chrome.storage.local.get({ server: DEFAULT_SERVER, token: '', pluginId: '', accountId: 'default' });
  return s;
}

async function post(path, body) {
  const s = await settings();
  const headers = { 'Content-Type': 'application/json' };
  if (s.token) headers.Authorization = `Bearer ${s.token}`;
  const res = await fetch(`${s.server}${path}`, { method: 'POST', headers, body: JSON.stringify(body) });
  if (!res.ok) throw new Error(`${res.status}: ${await res.text()}`);
  return res.json();
}

async function connect() {
  const s = await settings();
  const wsURL = s.server.replace(/^http/, 'ws') + `/extension/ws?plugin_id=${encodeURIComponent(s.pluginId)}&account_id=${encodeURIComponent(s.accountId)}${s.token ? `&token=${encodeURIComponent(s.token)}` : ''}`;
  if (!s.pluginId || socket?.readyState === WebSocket.OPEN) return;
  socket = new WebSocket(wsURL);
  socket.onmessage = async event => {
    const command = JSON.parse(event.data);
    if (command.type !== 'command') return;
    try {
      let result;
      if (command.method === 'open') { const tab = await chrome.tabs.create({ url: command.params.url }); result = { tabId: tab.id }; }
      else if (command.method === 'cookies') { const cookies = await chrome.cookies.getAll({ domain: command.params.domain }); result = { cookie: cookies.map(c => `${c.name}=${c.value}`).join('; ') }; }
      else { const tabs = await chrome.tabs.query({ active: true, lastFocusedWindow: true }); if (!tabs[0]?.id) throw new Error('没有找到活动标签页'); result = await chrome.tabs.sendMessage(tabs[0].id, { type: command.method === 'snapshot' ? 'snapshot' : command.method, ...command.params }); }
      socket.send(JSON.stringify({ id: command.id, result }));
    } catch (error) { socket.send(JSON.stringify({ id: command.id, error: error.message })); }
  };
  socket.onclose = () => { socket = undefined; setTimeout(connect, 3000); };
}

chrome.runtime.onMessage.addListener((message, sender, sendResponse) => {
  (async () => {
    if (message.type === 'ping') return { ok: true, extension: 'omnimcp' };
    if (message.type === 'connection_status') return { connected: socket?.readyState === WebSocket.OPEN };
    if (message.type === 'save_settings') { await chrome.storage.local.set(message.settings); if (socket) socket.close(); await connect(); return { ok: true }; }
    if (message.type === 'open_login') {
      const s = await settings(); const headers = { 'Content-Type': 'application/json' }; if (s.token) headers.Authorization = `Bearer ${s.token}`;
      const res = await fetch(`${s.server}/api/accounts/login`, { method: 'POST', headers, body: JSON.stringify({ plugin_id: s.pluginId, account_id: s.accountId }) });
      if (!res.ok) throw new Error(`${res.status}: ${await res.text()}`); return res.json();
    }
    if (message.type === 'page_snapshot') {
      return { ok: true, url: sender.tab?.url || location.href, title: sender.tab?.title || '' };
    }
    if (message.type === 'mcp_call') return post('/api/tools/call', { name: message.name, arguments: message.arguments || {} });
    throw new Error('unknown message');
  })().then(sendResponse).catch(error => sendResponse({ ok: false, error: error.message }));
  return true;
});

chrome.runtime.onStartup.addListener(connect);
chrome.runtime.onInstalled.addListener(connect);
connect();
