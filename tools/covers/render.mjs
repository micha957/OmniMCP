#!/usr/bin/env node
// Renders a cover to a 1080x1440 PNG in a temporary tab of the shared Chrome, so
// layout and Chinese type come from a real browser and no extra Chrome is started.
//
//   node tools/covers/render.mjs <template> <out.png> '{"title":"...","image":"/abs/a.png"}'
//
// Templates live in tools/covers/templates; see docs/CONTENT_PLAYBOOK.md for when
// to use which.
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const [, , template, out, rawVars = '{}'] = process.argv;
if (!template || !out) {
  console.error('usage: render.mjs <template> <out.png> <json vars>');
  process.exit(2);
}
const here = path.dirname(fileURLToPath(import.meta.url));
const port = process.env.OMNIMCP_CHROME_PORT || '9333';
const vars = JSON.parse(rawVars);
const escapeHTML = s => String(s).replace(/[&<>"]/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]));

// Images become data URLs: an http(s) page cannot load file:// and the temporary
// tab should not depend on anything being served.
function dataURL(file) {
  const type = { '.png': 'image/png', '.jpg': 'image/jpeg', '.jpeg': 'image/jpeg', '.webp': 'image/webp' }[path.extname(file).toLowerCase()];
  if (!type) throw new Error(`unsupported image: ${file}`);
  return `data:${type};base64,${fs.readFileSync(file).toString('base64')}`;
}

let html = fs.readFileSync(path.join(here, 'templates', `${template}.html`), 'utf8');
html = html.replace(/\{\{(\w+)\}\}/g, (_, key) => {
  const value = vars[key] ?? '';
  if (/image/.test(key)) return value ? dataURL(value) : '';
  // Authors break lines with \n; a * pair marks the highlighted words.
  return escapeHTML(value).replace(/\n/g, '<br>').replace(/\*(.+?)\*/g, '<em>$1</em>');
});

const target = await fetch(`http://127.0.0.1:${port}/json/new?about:blank`, { method: 'PUT' }).then(r => r.json());
const ws = new WebSocket(target.webSocketDebuggerUrl);
let seq = 0;
const pending = new Map();
const call = (method, params = {}) => new Promise((resolve, reject) => {
  const id = ++seq;
  pending.set(id, { resolve, reject });
  ws.send(JSON.stringify({ id, method, params }));
});
ws.onmessage = event => {
  const msg = JSON.parse(event.data);
  const p = msg.id && pending.get(msg.id);
  if (!p) return;
  pending.delete(msg.id);
  msg.error ? p.reject(new Error(msg.error.message)) : p.resolve(msg.result);
};
await new Promise(resolve => { ws.onopen = resolve; });

let failed = false;
try {
  await call('Emulation.setDeviceMetricsOverride', { width: 1080, height: 1440, deviceScaleFactor: 1, mobile: false });
  const { frameTree } = await call('Page.getFrameTree');
  await call('Page.setDocumentContent', { frameId: frameTree.frame.id, html });
  await call('Runtime.evaluate', { expression: 'document.fonts.ready.then(()=>Promise.all([...document.images].map(i=>i.decode().catch(()=>{}))))', awaitPromise: true });
  // A cover with clipped text is worse than none, so overflow stops the render.
  // CJK glyphs report a few pixels of harmless overhang, hence the tolerance.
  const overflow = await call('Runtime.evaluate', { returnByValue: true, expression: `[...document.querySelectorAll('[data-fit]')].filter(e=>e.scrollHeight>e.clientHeight+16||e.scrollWidth>e.clientWidth+16).map(e=>e.dataset.fit+' '+e.scrollWidth+'x'+e.scrollHeight+' in '+e.clientWidth+'x'+e.clientHeight)` });
  if (overflow.result.value.length) throw new Error(`text does not fit: ${overflow.result.value.join(', ')}`);
  const shot = await call('Page.captureScreenshot', { format: 'png', clip: { x: 0, y: 0, width: 1080, height: 1440, scale: 1 } });
  fs.writeFileSync(out, Buffer.from(shot.data, 'base64'));
  console.log(out);
} catch (err) {
  failed = true;
  console.error(`render failed: ${err.message}`);
} finally {
  await fetch(`http://127.0.0.1:${port}/json/close/${target.id}`);
  ws.close();
}
process.exit(failed ? 1 : 0);
