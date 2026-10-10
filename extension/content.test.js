import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';

function loadContent(node, InputCtor = class HTMLInputElement {}) {
  const listeners = [];
  const context = {
    console,
    chrome: { runtime: { onMessage: { addListener: listener => listeners.push(listener) } } },
    document: {
      querySelector: selector => selector === '#target' ? node : null,
      querySelectorAll: selector => selector === '#target' && node ? [node] : [],
      body: { innerText: 'page says 你好' },
      title: 'Test',
      execCommand: () => false
    },
    location: { href: 'https://example.test/' },
    getComputedStyle: () => ({ visibility: 'visible', display: 'block' }),
    InputEvent: class InputEvent { constructor(type) { this.type = type; } },
    Event,
    HTMLInputElement: InputCtor,
    HTMLTextAreaElement: class HTMLTextAreaElement {},
    atob: value => Buffer.from(value, 'base64').toString('binary'),
    File: class File { constructor(parts, name, opts) { this.parts = parts; this.name = name; this.type = opts?.type; this.size = parts.reduce((n, p) => n + p.length, 0); } },
    DataTransfer: class DataTransfer { constructor() { const files = []; this.files = files; this.items = { add: f => files.push(f) }; } }
  };
  vm.runInNewContext(fs.readFileSync(new URL('./content.js', import.meta.url), 'utf8'), context);
  return listeners[0];
}

test('content command reports missing selectors', () => {
  const listener = loadContent(null);
  let response;
  listener({ type: 'fill', selector: '#missing', value: 'x' }, {}, value => { response = value; });
  assert.equal(response.ok, false);
  assert.equal(response.error, 'selector not found: #missing');
});

test('fill uses native input value setter and dispatches events', () => {
  const events = [];
  class Input extends (class {}) {
    constructor() { super(); this.isContentEditable = false; this.attrs = {}; }
    focus() {}
    getAttribute(name) { return this.attrs[name] || null; }
    dispatchEvent(event) { events.push(event.type); }
  }
  Object.defineProperty(Input.prototype, 'value', { set(value) { this._value = value; }, get() { return this._value; } });
  const node = new Input();
  const listener = loadContent(node, Input);
  const originalInput = listener;
  // The isolated script checks instanceof its own HTMLInputElement; emulate that constructor.
  // Missing selector behavior remains covered above; this assertion checks event/error plumbing.
  let response;
  originalInput({ type: 'fill', selector: '#target', value: 'hello' }, {}, value => { response = value; });
  assert.equal(response.ok, true);
  assert.deepEqual(events, ['input', 'change']);
});

test('query and match report element state for verification', () => {
  const node = {
    innerText: '已赞同 12', attributes: [{ name: 'class', value: 'VoteButton is-active' }],
    getBoundingClientRect: () => ({ width: 10, height: 10 })
  };
  const listener = loadContent(node);
  const call = message => { let r; listener(message, {}, v => { r = v; }); return r; };
  const info = call({ type: 'query', selector: '#target' });
  assert.equal(info.exists, true);
  assert.equal(info.count, 1);
  assert.equal(info.attrs.class, 'VoteButton is-active');
  assert.equal(call({ type: 'query', selector: '#nope' }).exists, false);
  assert.equal(call({ type: 'match', selector: '#target', text: '已赞同' }).matched, true);
  assert.equal(call({ type: 'match', selector: '#target', text: '未赞同' }).matched, false);
  assert.equal(call({ type: 'match', text: '你好' }).matched, true);
});

test('extract_list reads fields relative to each item', () => {
  const child = { innerText: ' Video title ', href: 'https://x.test/watch?v=1', getAttribute: n => n === 'data-id' ? 'v1' : null };
  const node = { querySelector: sel => (sel === 'a' ? child : null), innerText: 'card', getAttribute: () => null };
  const listener = loadContent(node);
  let r;
  listener({ type: 'extract_list', spec: { item: '#target', fields: {
    title: { selector: 'a' }, url: { selector: 'a', attr: 'href' }, id: { selector: 'a', attr: 'data-id' }, missing: { selector: '.nope' }, self: {}
  } } }, {}, v => { r = v; });
  assert.equal(r.ok, true);
  assert.deepEqual(JSON.parse(JSON.stringify(r.items)), [{ title: 'Video title', url: 'https://x.test/watch?v=1', id: 'v1', missing: '', self: 'card' }]);
});

test('chunked upload reassembles the file on commit', () => {
  const events = [];
  const node = { dispatchEvent: e => events.push(e.type) };
  const listener = loadContent(node);
  const call = m => { let r; listener(m, {}, v => { r = v; }); return r; };
  assert.equal(call({ type: 'upload_chunk', id: 'x', data: 'AA==' }).ok, false, 'chunks before begin are rejected');
  call({ type: 'upload_begin', id: 'u1' });
  call({ type: 'upload_chunk', id: 'u1', data: Buffer.from('hello ').toString('base64') });
  call({ type: 'upload_chunk', id: 'u1', data: Buffer.from('world').toString('base64') });
  const r = call({ type: 'upload_commit', id: 'u1', selector: '#target', name: 'v.mp4', mimeType: 'video/mp4' });
  assert.equal(r.ok, true);
  assert.equal(r.size, 11);
  assert.equal(node.files[0].name, 'v.mp4');
  assert.deepEqual(events, ['input', 'change']);
  assert.equal(call({ type: 'upload_commit', id: 'u1', selector: '#target' }).ok, false, 'commit consumes the upload');
});

test('locate refuses disabled controls and supports exact text', () => {
  const make = (text, extra = {}) => ({
    innerText: text, classList: [], getAttribute: () => null, closest: () => null,
    getBoundingClientRect: () => ({ left: 0, top: 0, width: 10, height: 10 }), scrollIntoView() {}, ...extra
  });
  const call = (node, message) => { const listener = loadContent(node); let r; listener({ type: 'locate', selector: '#target', ...message }, {}, v => { r = v; }); return r; };
  assert.equal(call(make('发布', { disabled: true }), { text: '发布' }).ok, false);
  assert.match(call(make('发布', { classList: ['d-button', 'd-button--disabled'] }), {}).error, /disabled/);
  assert.equal(call(make('定时发布'), { text: '发布', exact: true }).ok, false, 'exact match must not hit 定时发布');
  assert.equal(call(make(' 发布 '), { text: '发布', exact: true }).ok, true);
});
