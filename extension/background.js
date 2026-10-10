const DEFAULT_SERVER = 'http://127.0.0.1:8090';
const TAB_KEY = 'omnimcp.boundTabId';
const READY_TIMEOUT_MS = 10000;
const READY_POLL_MS = 150;
const RECONNECT_ALARM = 'omnimcp.reconnect';
let socket;
let reconnectTimer;
let connectionGeneration = 0;
let lastConnectionError = '';
let connecting = false;
function serverBase(value) {
  const u = new URL(String(value || DEFAULT_SERVER).trim());
  if (!['http:', 'https:'].includes(u.protocol) || u.username || u.password || u.search || u.hash) throw new Error('服务器地址必须是 HTTP(S) 基础地址');
  u.pathname = u.pathname.replace(/\/+$/, '').replace(/\/mcp$/, '');
  return u.href.replace(/\/+$/, '');
}
function connectionStatus() {
  return {ok: true, connected: socket?.readyState === WebSocket.OPEN, connecting, error: lastConnectionError};
}

async function settings() {
  return chrome.storage.local.get({ server: DEFAULT_SERVER, token: '', pluginId: '', accountId: 'default' });
}

// Each platform (scope) drives its own tab, so one connection can serve several
// platforms without them navigating each other's page.
const tabKey = scope => `${TAB_KEY}.${scope || 'default'}`;
const tabArea = () => chrome.storage.session?.set ? chrome.storage.session : chrome.storage.local;

async function getBoundTabId(scope) {
  const key = tabKey(scope);
  const value = await tabArea().get(key);
  return Number.isInteger(value[key]) ? value[key] : undefined;
}

async function setBoundTabId(scope, tabId) {
  const area = tabArea();
  if (tabId == null) await area.remove(tabKey(scope));
  else await area.set({ [tabKey(scope)]: tabId });
}

async function post(path, body) {
  const s = await settings();
  const headers = { 'Content-Type': 'application/json' };
  if (s.token) headers.Authorization = `Bearer ${s.token}`;
  const res = await fetch(`${s.server}${path}`, { method: 'POST', headers, body: JSON.stringify(body) });
  if (!res.ok) throw new Error(`${res.status}: ${await res.text()}`);
  return res.json();
}

function sleep(ms) { return new Promise(resolve => setTimeout(resolve, ms)); }

async function ensureContentScript(tabId) {
  const deadline = Date.now() + READY_TIMEOUT_MS;
  let lastError;
  while (Date.now() < deadline) {
    try {
      const response = await chrome.tabs.sendMessage(tabId, { type: 'ping' });
      if (response?.ok) return;
    } catch (error) { lastError = error; }
    try {
      await chrome.scripting.executeScript({ target: { tabId }, files: ['zhihu-extractor.js', 'content.js'] });
    } catch (error) { lastError = error; }
    await sleep(READY_POLL_MS);
  }
  throw new Error(`页面内容脚本未就绪${lastError?.message ? `: ${lastError.message}` : ''}`);
}

async function currentTab(scope) {
  const boundId = await getBoundTabId(scope);
  if (boundId != null) {
    try {
      const tab = await chrome.tabs.get(boundId);
      if (tab?.id != null) return tab;
    } catch (_) { await setBoundTabId(scope, undefined); }
  }
  throw new Error('当前连接没有绑定标签页，请先执行 open');
}

async function sendTabCommand(scope, method, params) {
  const tab = await currentTab(scope);
  await ensureContentScript(tab.id);
  const result = await chrome.tabs.sendMessage(tab.id, { type: method === 'snapshot' ? 'snapshot' : method, ...params });
  if (!result?.ok) throw new Error(result?.error || `${method} 失败`);
  return result;
}

// Platforms append tracking parameters or redirect (e.g. to a login page), so
// navigation is done once the tab has loaded anything after our request.
async function waitForNavigation(tabId, requested) {
  const started = Date.now();
  const deadline = started + Math.max(READY_TIMEOUT_MS, 20000);
  let sawLoading = false;
  while (Date.now() < deadline) {
    const current = await chrome.tabs.get(tabId);
    if (current.status === 'loading') sawLoading = true;
    if (current.status === 'complete' && current.url && !current.url.startsWith('about:')) {
      if (current.url === requested.href || sawLoading) return current;
      // Fast navigations may finish between polls.
      if (Date.now() - started > 1500) return current;
    }
    await sleep(READY_POLL_MS);
  }
  throw new Error('页面导航超时');
}

// chrome.debugger produces isTrusted input events; the browser shows a
// "being debugged" bar while attached, so detach right after each action.
async function withDebugger(tabId, fn) {
  const target = { tabId };
  await chrome.debugger.attach(target, '1.3');
  try {
    return await fn((method, params) => chrome.debugger.sendCommand(target, method, params));
  } finally {
    await chrome.debugger.detach(target).catch(() => {});
  }
}

// Runs in the page's MAIN world; must stay self-contained.
function readGlobalInPage(path) {
  const unwrap = v => (v && typeof v === 'object' && v.__v_isRef) ? (v._value !== undefined ? v._value : v.value) : v;
  let cur = window;
  for (const key of path.split('.')) {
    cur = unwrap(cur);
    if (cur == null) return '';
    cur = cur[key];
  }
  cur = unwrap(cur);
  if (cur === undefined) return '';
  const seen = new WeakSet();
  return JSON.stringify(cur, (_k, v) => {
    v = unwrap(v);
    if (v && typeof v === 'object') { if (seen.has(v)) return undefined; seen.add(v); }
    return v;
  }) || '';
}

// Polls from the worker so waits survive navigations that reload the content script.
async function waitFor(scope, { selector = '', text = '', timeout_ms = 10000 }) {
  const deadline = Date.now() + Math.min(Number(timeout_ms) || 10000, 120000);
  let last = '';
  while (Date.now() < deadline) {
    try {
      const result = await sendTabCommand(scope, 'match', { selector, text });
      if (result.matched) return { ok: true };
    } catch (error) { last = error.message; }
    await sleep(300);
  }
  throw new Error(`wait_for 超时: selector=${selector} text=${text}${last ? ` (${last})` : ''}`);
}

async function handleCommand(command) {
  const scope = command.scope || 'default';
  if (command.method === 'open') {
    const requested = new URL(command.params.url);
    const boundId = await getBoundTabId(scope);
    let existing;
    if (boundId != null) {
      try { existing = await chrome.tabs.get(boundId); } catch (_) { /* Closed tab: create a new one. */ }
    }
    // Reuse only our bound tab, never another user tab.
    const tab = existing ? await chrome.tabs.update(boundId, {url: requested.href}) : await chrome.tabs.create({url: requested.href});
    if (tab.id == null) throw new Error('打开页面失败：没有标签页 ID');
    await setBoundTabId(scope, tab.id);
    const current = await waitForNavigation(tab.id, requested);
    await ensureContentScript(tab.id);
    return { tabId: tab.id, ready: true, url: current.url, redirected: current.url !== requested.href };
  }
  if (command.method === 'url') {
    const tab = await currentTab(scope);
    return { url: tab.url, title: tab.title };
  }
  if (command.method === 'read_global') {
    const tab = await currentTab(scope);
    const [res] = await chrome.scripting.executeScript({ target: { tabId: tab.id }, world: 'MAIN', func: readGlobalInPage, args: [String(command.params.path || '')] });
    return { json: res?.result || '' };
  }
  if (command.method === 'wait_for') return waitFor(scope, command.params || {});
  if (command.method === 'trusted_click') {
    const tab = await currentTab(scope);
    const { x, y } = await sendTabCommand(scope, 'locate', command.params);
    await withDebugger(tab.id, async send => {
      await send('Input.dispatchMouseEvent', { type: 'mouseMoved', x, y });
      await send('Input.dispatchMouseEvent', { type: 'mousePressed', x, y, button: 'left', clickCount: 1 });
      await send('Input.dispatchMouseEvent', { type: 'mouseReleased', x, y, button: 'left', clickCount: 1 });
    });
    return { ok: true };
  }
  if (command.method === 'trusted_key') {
    const keys = { Enter: [13, '\r'], Escape: [27, ''], Tab: [9, ''], Backspace: [8, ''] };
    const [code, text] = keys[command.params.key] || [];
    if (code == null) throw new Error(`unsupported key ${command.params.key}`);
    const tab = await currentTab(scope);
    await withDebugger(tab.id, async send => {
      const base = { key: command.params.key, code: command.params.key, windowsVirtualKeyCode: code, nativeVirtualKeyCode: code };
      await send('Input.dispatchKeyEvent', { type: 'keyDown', ...base, ...(text ? { text } : {}) });
      await send('Input.dispatchKeyEvent', { type: 'keyUp', ...base });
    });
    return { ok: true };
  }
  if (command.method === 'trusted_type') {
    const tab = await currentTab(scope);
    await sendTabCommand(scope, 'locate', { selector: command.params.selector, focus: true });
    await withDebugger(tab.id, send => send('Input.insertText', { text: String(command.params.value ?? '') }));
    return { ok: true };
  }
  if (command.method === 'cookies') {
    const cookies = await chrome.cookies.getAll({ domain: command.params.domain });
    return { cookie: cookies.map(c => `${c.name}=${c.value}`).join('; ') };
  }
  return sendTabCommand(scope, command.method, command.params || {});
}

async function connect() {
  if (socket?.readyState === WebSocket.OPEN || connecting) return;
  const generation = ++connectionGeneration;
  // Acquire before the first await: startup, alarms and settings may overlap.
  connecting = true;
  lastConnectionError = '';
  try {
    const s = await settings();
    if (generation !== connectionGeneration) return;
    if (!s.pluginId || !s.accountId) {
      connecting = false;
      lastConnectionError = '请选择平台并填写账号标识';
      return;
    }
    s.server = serverBase(s.server);
    const headers = {'Content-Type': 'application/json'};
    if (s.token) headers.Authorization = `Bearer ${s.token}`;
    const res = await fetch(`${s.server}/mcp`, {method: 'POST', headers,
      body: JSON.stringify({jsonrpc:'2.0', id:'connection-check', method:'tools/list', params:{}}), signal: AbortSignal.timeout(5000)});
    if (res.status === 401) throw new Error('认证失败（401）：请核对 API Token；本地无认证服务请留空');
    if (!res.ok) throw new Error(`服务检查失败（HTTP ${res.status}）：请填写服务基础地址`);
    const payload = await res.json();
    if (!Array.isArray(payload.result?.tools)) throw new Error('该地址没有返回 OmniMCP 工具列表');
    if (generation !== connectionGeneration) return;
    const wsURL = s.server.replace(/^http/, 'ws') + `/extension/ws?plugin_id=${encodeURIComponent(s.pluginId)}&account_id=${encodeURIComponent(s.accountId)}${s.token ? `&token=${encodeURIComponent(s.token)}` : ''}`;
    await new Promise(resolve => {
      const ws = new WebSocket(wsURL);
      socket = ws;
      let heartbeat;
      const finish = () => { clearTimeout(timer); if (generation === connectionGeneration) connecting = false; resolve(); };
      const timer = setTimeout(() => {
        if (generation === connectionGeneration) lastConnectionError = 'WebSocket 连接超时，请检查浏览器对本地服务的访问';
        ws.close(); finish();
      }, 8000);
      ws.onopen = () => {
        if (generation !== connectionGeneration) { ws.close(); finish(); return; }
        lastConnectionError = '';
        // A periodic message keeps Manifest V3's service worker/socket alive.
        heartbeat = setInterval(() => { if (ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify({type:'ping'})); }, 20000);
        finish();
      };
      ws.onmessage = async event => {
        try {
          const command = JSON.parse(event.data);
          if (command.type !== 'command') return;
          try {
            const result = await handleCommand(command);
            if (ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify({id:command.id,result}));
          } catch(error) {
            if (ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify({id:command.id,error:error.message}));
          }
        } catch (_) { /* Ignore non-protocol messages without exposing connection credentials. */ }
      };
      ws.onerror = () => { if (generation === connectionGeneration) lastConnectionError = 'MCP HTTP 可达，但 WebSocket 握手失败；请检查代理和 Token'; finish(); };
      ws.onclose = event => {
        clearInterval(heartbeat);
        if (socket === ws) socket = undefined;
        finish();
        if (generation !== connectionGeneration) return;
        if (!lastConnectionError) lastConnectionError = `连接已断开（${event.code}），正在重试`;
        clearTimeout(reconnectTimer);
        reconnectTimer = setTimeout(() => { reconnectTimer = undefined; connect(); }, 3000);
      };
    });
  } catch(error) {
    if (generation !== connectionGeneration) return;
    connecting = false;
    lastConnectionError = error.name === 'TimeoutError' ? '连接本地 MCP 服务超时' : error.message === 'Failed to fetch' ? '浏览器无法访问 MCP 服务，请检查地址、代理或本地网络权限' : error.message;
    clearTimeout(reconnectTimer);
    reconnectTimer = setTimeout(() => { reconnectTimer = undefined; connect(); }, 3000);
  }
}

chrome.runtime.onMessage.addListener((message, _sender, sendResponse) => {
  (async () => {
    if (message.type === 'ping') return { ok: true, extension: 'omnimcp' };
    if (message.type === 'connection_status') return connectionStatus();
    if (message.type === 'save_settings') {
      const updated = {...message.settings, server: serverBase(message.settings.server)};
      await chrome.storage.local.set(updated);
      connectionGeneration++;
      connecting = false;
      clearTimeout(reconnectTimer);
      reconnectTimer = undefined;
      const oldSocket = socket;
      socket = undefined;
      if (oldSocket && oldSocket.readyState !== WebSocket.CLOSED) oldSocket.close(1000, 'settings changed');
      await connect();
      return connectionStatus();
    }
    if (message.type === 'open_login') {
      const s = await settings();
      const headers = { 'Content-Type': 'application/json' };
      if (s.token) headers.Authorization = `Bearer ${s.token}`;
      const res = await fetch(`${s.server}/api/accounts/login`, { method: 'POST', headers, body: JSON.stringify({ plugin_id: s.pluginId, account_id: s.accountId }) });
      if (!res.ok) throw new Error(`${res.status}: ${await res.text()}`);
      return res.json();
    }
    if (message.type === 'page_snapshot') return { ok: true, url: message.url || _sender.tab?.url || '', title: _sender.tab?.title || '' };
    if (message.type === 'mcp_call') return post('/api/tools/call', { name: message.name, arguments: message.arguments || {} });
    throw new Error('unknown message');
  })().then(sendResponse).catch(error => sendResponse({ ok: false, error: error.message }));
  return true;
});

chrome.tabs.onRemoved.addListener(async tabId => {
  const all = await tabArea().get(null);
  const stale = Object.keys(all).filter(k => k.startsWith(TAB_KEY) && all[k] === tabId);
  if (stale.length) await tabArea().remove(stale);
});
// Browser-owned alarms survive worker suspension; setTimeout alone does not.
chrome.alarms.onAlarm.addListener(alarm => {
  if (alarm.name === RECONNECT_ALARM) return connect();
});
chrome.alarms.create(RECONNECT_ALARM, {periodInMinutes: 1}).catch(error => {
  lastConnectionError = `自动重连唤醒失败：${error.message}`;
});
chrome.runtime.onStartup.addListener(connect);
chrome.runtime.onInstalled.addListener(connect);
connect();
