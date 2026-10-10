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

  // Light DOM first; fall back to open shadow roots (web components such as
  // publish widgets render their buttons there).
  const deepAll = selector => {
    const found = [...document.querySelectorAll(selector)];
    if (found.length) return found;
    const roots = [document];
    while (roots.length) {
      for (const el of roots.pop().querySelectorAll('*')) {
        if (!el.shadowRoot) continue;
        found.push(...el.shadowRoot.querySelectorAll(selector));
        roots.push(el.shadowRoot);
      }
    }
    return found;
  };

  const query = selector => {
    const nodes = deepAll(selector);
    const node = nodes[0];
    if (!node) return { exists: false, count: 0, visible: false, text: '' };
    const attrs = {};
    for (const a of node.attributes) attrs[a.name] = String(a.value).slice(0, 500);
    return { exists: true, count: nodes.length, visible: visible(node), text: (node.innerText || node.value || '').trim().slice(0, 2000), attrs };
  };

  const uploads = new Map();

  const fieldValue = (root, field) => {
    const el = field.selector ? root.querySelector(field.selector) : root;
    if (!el) return '';
    const attr = field.attr || 'text';
    if (attr === 'text') return (el.innerText || el.textContent || '').trim();
    if (attr === 'href') return el.href || el.getAttribute('href') || '';
    if (attr === 'src') return el.currentSrc || el.src || el.getAttribute('src') || '';
    return el.getAttribute(attr) || '';
  };

  const extractList = spec => {
    const limit = spec.limit > 0 ? spec.limit : 50;
    return [...document.querySelectorAll(spec.item)].slice(0, limit).map(node => {
      const out = {};
      for (const [name, field] of Object.entries(spec.fields || {})) out[name] = fieldValue(node, field);
      return out;
    });
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
      if (message.type === 'locate') {
        const matches = n => {
          if (!message.text) return true;
          const t = (n.innerText || n.textContent || '').trim();
          return message.exact ? t === message.text : t.includes(message.text);
        };
        const node = deepAll(message.selector).find(n => visible(n) && matches(n));
        if (!node) return sendResponse({ ok: false, error: `selector not found: ${message.selector}${message.text ? ` text=${message.text}` : ''}` });
        // Clicking a disabled control does nothing, so report it instead of
        // letting callers believe the click landed.
        const disabledClass = el => [...(el.classList || [])].some(c => /(^|[-_])disabled$/i.test(c));
        const disabled = !message.focus && (node.disabled || node.getAttribute('aria-disabled') === 'true' || disabledClass(node) || !!node.closest?.('[disabled],[aria-disabled="true"]'));
        if (disabled) return sendResponse({ ok: false, error: `element is disabled: ${message.selector}` });
        node.scrollIntoView({ block: 'center', inline: 'center' });
        const r = node.getBoundingClientRect();
        const ratio = message.x_ratio > 0 ? message.x_ratio : 0.5;
        if (message.focus) {
          node.focus();
          // Select existing content so the inserted text replaces it, matching fill.
          if (node.isContentEditable) {
            const range = document.createRange(); range.selectNodeContents(node);
            const sel = window.getSelection(); sel.removeAllRanges(); sel.addRange(range);
          } else if (typeof node.select === 'function') node.select();
        }
        return sendResponse({ ok: true, x: r.left + r.width * ratio, y: r.top + r.height / 2 });
      }
      if (message.type === 'extract_list') return sendResponse({ ok: true, items: extractList(message.spec || {}) });
      if (message.type === 'script_json') {
        const node = document.querySelector(message.selector);
        if (!node) return sendResponse({ ok: false, error: `selector not found: ${message.selector}` });
        return sendResponse({ ok: true, json: node.textContent || '' });
      }
      // Large files arrive in chunks: runtime messages are capped at 64MB.
      if (message.type === 'upload_begin') { uploads.set(message.id, []); return sendResponse({ ok: true }); }
      if (message.type === 'upload_chunk') {
        const parts = uploads.get(message.id);
        if (!parts) return sendResponse({ ok: false, error: 'upload was not started on this page' });
        parts.push(Uint8Array.from(atob(message.data), c => c.charCodeAt(0)));
        return sendResponse({ ok: true });
      }
      if (message.type === 'upload_commit') {
        const parts = uploads.get(message.id);
        uploads.delete(message.id);
        if (!parts) return sendResponse({ ok: false, error: 'upload was not started on this page' });
        const node = document.querySelector(message.selector);
        if (!node) return sendResponse({ ok: false, error: `selector not found: ${message.selector}` });
        const file = new File(parts, message.name || 'upload.bin', { type: message.mimeType || 'application/octet-stream' });
        const transfer = new DataTransfer(); transfer.items.add(file); node.files = transfer.files;
        node.dispatchEvent(new Event('input', { bubbles: true }));
        node.dispatchEvent(new Event('change', { bubbles: true }));
        return sendResponse({ ok: true, size: file.size });
      }
      if (message.type === 'query') return sendResponse({ ok: true, ...query(message.selector) });
      if (message.type === 'match') {
        let matched;
        if (message.selector) {
          const info = query(message.selector);
          matched = info.exists && (!message.text || info.text.includes(message.text));
        } else matched = !!message.text && (document.body?.innerText || '').includes(message.text);
        return sendResponse({ ok: true, matched });
      }
      if (message.type === 'scroll') {
        if (message.selector) {
          const node = document.querySelector(message.selector);
          if (!node) return sendResponse({ ok: false, error: `selector not found: ${message.selector}` });
          node.scrollIntoView({ block: 'center' });
        } else window.scrollTo(0, document.documentElement.scrollHeight);
        return sendResponse({ ok: true });
      }
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
