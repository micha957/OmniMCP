// The content script is intentionally small: platform plugins can add selectors
// and page workflows without duplicating MCP transport or account settings.
chrome.runtime.onMessage.addListener((message, _sender, sendResponse) => {
  if (message.type === 'snapshot') {
    sendResponse({ ok: true, url: location.href, title: document.title, text: document.body?.innerText?.slice(0, 20000) || '' });
  }
  if (message.type === 'click') {
    const node = document.querySelector(message.selector);
    if (!node) return sendResponse({ ok: false, error: `selector not found: ${message.selector}` });
    node.click(); sendResponse({ ok: true });
  }
  if (message.type === 'fill') {
    const node = document.querySelector(message.selector);
    if (!node) return sendResponse({ ok: false, error: `selector not found: ${message.selector}` });
    node.focus(); node.value = message.value; node.dispatchEvent(new Event('input', { bubbles: true })); node.dispatchEvent(new Event('change', { bubbles: true })); sendResponse({ ok: true });
  }
  if (message.type === 'upload') {
    const node = document.querySelector(message.selector);
    if (!node) return sendResponse({ ok: false, error: `selector not found: ${message.selector}` });
    const bytes = Uint8Array.from(atob(message.data), c => c.charCodeAt(0));
    const file = new File([bytes], message.name || 'upload.bin', { type: 'application/octet-stream' });
    const transfer = new DataTransfer(); transfer.items.add(file); node.files = transfer.files; node.dispatchEvent(new Event('change', { bubbles: true })); sendResponse({ ok: true });
  }
  return true;
});
