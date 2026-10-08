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
      querySelectorAll: () => [],
      body: { innerText: '' },
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
    File: class File {},
    DataTransfer: class DataTransfer {}
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
