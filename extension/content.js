// The listener is guarded because background.js may inject this file after a navigation.
if (!globalThis.__omnimcpContentInstalled) {
  globalThis.__omnimcpContentInstalled = true;

  const visible = node => {
    const style = getComputedStyle(node);
    const rect = node.getBoundingClientRect();
    return style.visibility !== 'hidden' && style.display !== 'none' && rect.width > 0 && rect.height > 0;
  };

  const describe = node => ({
    tag: node.tagName.toLowerCase(),
    type: node.getAttribute('type') || undefined,
    name: node.getAttribute('name') || undefined,
    id: node.id || undefined,
    text: (node.innerText || node.value || node.getAttribute('aria-label') || '').trim().slice(0, 200),
    placeholder: node.getAttribute('placeholder') || undefined,
    href: node.tagName === 'A' ? node.href : undefined
  });

  const snapshot = () => {
    const nodes = selector => [...document.querySelectorAll(selector)].filter(visible).map(describe).slice(0, 200);
    const links = nodes('a');
    const inputs = nodes('input, textarea, [contenteditable="true"]');
    const buttons = nodes('button, [role="button"], input[type="submit"]');
    const metadata = [
      'Links:', ...links.map(item => JSON.stringify(item)),
      'Visible inputs:', ...inputs.map(item => JSON.stringify(item)),
      'Visible buttons:', ...buttons.map(item => JSON.stringify(item))
    ].join('\n');
    return { ok: true, url: location.href, title: document.title, text: `${document.body?.innerText?.slice(0, 20000) || ''}\n\n${metadata}` };
  };

  const fill = (node, value) => {
    node.focus();
    if (node.isContentEditable || node.getAttribute('contenteditable') === 'true') {
      const selection = window.getSelection();
      const range = document.createRange();
      range.selectNodeContents(node);
      selection.removeAllRanges();
      selection.addRange(range);
      if (!document.execCommand('insertText', false, value)) node.textContent = value;
    } else {
      const proto = node instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
      const setter = Object.getOwnPropertyDescriptor(proto, 'value')?.set;
      if (setter) setter.call(node, value); else node.value = value;
    }
    node.dispatchEvent(new InputEvent('input', { bubbles: true, inputType: 'insertText', data: value }));
    node.dispatchEvent(new Event('change', { bubbles: true }));
  };

  chrome.runtime.onMessage.addListener((message, _sender, sendResponse) => {
    try {
      if (message.type === 'read_zhihu') {
        (async () => {
          if (!/(^|\.)zhihu\.com$/.test(location.hostname)) throw new Error('not a Zhihu page');
          if (typeof OmniZhihuExtract !== 'function') throw new Error('reload OmniMCP extension to enable structured extraction');
          if (message.mode === 'author_scan') {
            if (!/^\/people\/[A-Za-z0-9_-]+\/posts\/?$/.test(location.pathname)) throw new Error('not an author posts page');
            const end = Date.now() + 20000;
            let count = -1, unchangedSince = Date.now(), result;
            do {
              result = OmniZhihuExtract(document, location.href, 'author', 2000);
              if (result.status === 'login_required' || /安全验证|访问异常|请求过于频繁/.test(document.body.innerText)) {
                return {ok:true, data:{...result, status:'blocked', complete:false, stop_reason:'access_check'}};
              }
              if (result.expected_total !== null && result.loaded_count >= result.expected_total) {
                return {ok:true, data:{...result, scope:'author_catalog', complete:true, stop_reason:'advertised_total_reached'}};
              }
              if (result.loaded_count !== count) { count = result.loaded_count; unchangedSince = Date.now(); }
              if (count >= 2000 || Date.now() - unchangedSince > 6000) break;
              document.querySelector('.ArticleItem:last-child')?.scrollIntoView({block:'end'});
              window.scrollTo(0, document.documentElement.scrollHeight);
              await new Promise(resolve => setTimeout(resolve, 1000));
            } while (Date.now() < end);
            return {ok:true, data:{...result, complete:false, stop_reason:count >= 2000 ? 'limit_reached' : Date.now() - unchangedSince > 6000 ? 'stalled' : 'round_budget'}};
          }
          const deadline = Date.now() + 12000;
          let previous = '', stable = 0, result;
          do {
            result = OmniZhihuExtract(document, location.href, message.mode, message.limit);
            const signature = JSON.stringify([result.title, result.body, result.items]);
            stable = signature === previous ? stable + 1 : 0;
            previous = signature;
            if (result.status === 'login_required' || result.status === 'partial' || (result.status === 'ok' && stable >= 2)) break;
            await new Promise(resolve => setTimeout(resolve, 350));
          } while (Date.now() < deadline);
          return {ok: true, data: result};
        })().then(sendResponse).catch(e => sendResponse({ok: false, error: e.message}));
        return true;
      }
      if (message.type === 'ping') return sendResponse({ ok: true });
      if (message.type === 'snapshot') return sendResponse(snapshot());
      if (message.type === 'click') {
        const node = document.querySelector(message.selector);
        if (!node) return sendResponse({ ok: false, error: `selector not found: ${message.selector}` });
        node.click(); return sendResponse({ ok: true });
      }
      if (message.type === 'fill') {
        const node = document.querySelector(message.selector);
        if (!node) return sendResponse({ ok: false, error: `selector not found: ${message.selector}` });
        fill(node, String(message.value ?? '')); return sendResponse({ ok: true });
      }
      if (message.type === 'upload') {
        const node = document.querySelector(message.selector);
        if (!node) return sendResponse({ ok: false, error: `selector not found: ${message.selector}` });
        const bytes = Uint8Array.from(atob(message.data), c => c.charCodeAt(0));
        const file = new File([bytes], message.name || 'upload.bin', { type: message.mimeType || 'application/octet-stream' });
        const transfer = new DataTransfer(); transfer.items.add(file); node.files = transfer.files;
        node.dispatchEvent(new Event('change', { bubbles: true })); return sendResponse({ ok: true });
      }
      return sendResponse({ ok: false, error: `unknown content command: ${message.type}` });
    } catch (error) { return sendResponse({ ok: false, error: error.message }); }
  });
}
