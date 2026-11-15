import { beforeEach, test } from 'node:test';
import assert from 'node:assert/strict';
import { createEditor, shortcutCommand } from '../src/editing.ts';

class Field extends EventTarget {
  visible = true;
  getClientRects() { return this.visible ? [{}] : []; }
  value = 'hello world'; selectionStart = 6; selectionEnd = 11;
  readOnly = false; disabled = false; isConnected = true;
  setRangeText(text, start, end) { this.value = this.value.slice(0, start) + text + this.value.slice(end); this.selectionStart = this.selectionEnd = start + text.length; }
  select() { this.selectionStart = 0; this.selectionEnd = this.value.length; }
}
class TextInput extends Field {}
globalThis.HTMLTextAreaElement = Field;
globalThis.HTMLInputElement = TextInput;
globalThis.InputEvent = class extends Event { constructor(type, options) { super(type, options); this.inputType = options.inputType; this.data = options.data; } };
beforeEach(() => { globalThis.document = { activeElement: null, execCommand: () => false, getSelection: () => ({ toString: () => 'transcript selection' }) }; });
function harness() {
  let text = '', context = 'terminal:1', focused = true, alive = true;
  const pastes = []; let selections = 0;
  const terminal = { focused: () => focused, alive: () => alive, getSelection: () => 'terminal selection', selectAll: () => selections++, paste: value => pastes.push(value), focus() {} };
  const clipboard = { readText: async () => text, writeText: async value => { text = value; } };
  const edit = createEditor({ clipboard, terminal: () => terminal, context: () => context });
  return { edit, clipboard, terminal, pastes, setText: value => text = value, text: () => text, selections: () => selections, blur: () => focused = false, close: () => alive = false, changeContext: () => context += 'x' };
}
const key = (key, modifiers = {}) => ({ key, ctrlKey: false, metaKey: false, shiftKey: false, altKey: false, isComposing: false, defaultPrevented: false, ...modifiers });
test('terminal shortcuts do not steal shell control keys or editing keys', () => {
  for (const k of ['c', 'v', 'a', 'z', 'Backspace']) assert.equal(shortcutCommand(key(k, { ctrlKey: true }), false), undefined);
  assert.equal(shortcutCommand(key('C', { ctrlKey: true, shiftKey: true }), false), 'copy');
  assert.equal(shortcutCommand(key('V', { ctrlKey: true, shiftKey: true }), false), 'paste');
  assert.equal(shortcutCommand(key('c', { metaKey: true }), true), 'copy');
  assert.equal(shortcutCommand(key('Z', { metaKey: true, shiftKey: true }), true), 'redo');
  assert.equal(shortcutCommand(key('v', { metaKey: true, defaultPrevented: true }), true), undefined);
  assert.equal(shortcutCommand(key('v', { metaKey: true, isComposing: true }), true), undefined);
});
test('terminal paste uses the terminal paste method; Cut never deletes terminal output', async () => {
  const h = harness(); h.setText('line one\nline two');
  await h.edit('paste'); assert.deepEqual(h.pastes, ['line one\nline two']);
  await h.edit('cut'); assert.equal(h.text(), 'terminal selection'); assert.equal(h.pastes.length, 1);
  await h.edit('select-all'); assert.equal(h.selections(), 1);
  await h.edit('undo'); await h.edit('redo'); assert.equal(h.pastes.length, 1);
});
test('async terminal paste is cancelled on tab change, focus change, or terminal exit', async () => {
  for (const invalidate of ['changeContext', 'blur', 'close']) {
    const h = harness(); let resolve;
    h.clipboard.readText = () => new Promise(r => resolve = r);
    const pending = h.edit('paste'); h[invalidate](); resolve('late paste');
    await assert.rejects(pending, /target changed/); assert.deepEqual(h.pastes, []);
  }
});
test('field paste and cut dispatch input events for Svelte bindings', async () => {
  const h = harness(); h.blur(); const field = new Field(); document.activeElement = field;
  const events = []; field.addEventListener('input', event => events.push([field.value, event.inputType]));
  h.setText('Claude'); await h.edit('paste'); assert.equal(field.value, 'hello Claude');
  field.selectionStart = 0; field.selectionEnd = 6; await h.edit('cut');
  assert.equal(h.text(), 'hello '); assert.equal(field.value, 'Claude');
  assert.deepEqual(events, [['hello Claude', 'insertFromPaste'], ['Claude', 'deleteByCut']]);
});
test('field uses native editing history when execCommand is supported', async () => {
  const h = harness(); h.blur(); document.activeElement = new Field(); const calls = [];
  document.execCommand = (...args) => { calls.push(args); return true; };
  h.setText('insert'); await h.edit('paste'); await h.edit('undo'); await h.edit('redo');
  assert.deepEqual(calls, [['insertText', false, 'insert'], ['undo'], ['redo']]);
});
test('delayed field paste does not overwrite newer input or another selection', async () => {
  for (const change of [f => f.value += '!', f => f.selectionStart = 0, f => f.isConnected = false, () => document.activeElement = new Field()]) {
    const h = harness(); h.blur(); const field = new Field(); document.activeElement = field; let resolve;
    h.clipboard.readText = () => new Promise(r => resolve = r);
    const pending = h.edit('paste'); change(field); resolve('late');
    await assert.rejects(pending, /target changed/); assert.equal(field.value.includes('late'), false);
  }
});
test('read-only selections copy but cannot cut or paste; transcript selection copies', async () => {
  const h = harness(); h.blur(); const field = new Field(); field.readOnly = true; document.activeElement = field;
  await h.edit('cut'); await h.edit('paste'); assert.equal(field.value, 'hello world'); assert.equal(h.text(), 'world');
  document.activeElement = null; await h.edit('copy'); assert.equal(h.text(), 'transcript selection');
});
test('clipboard errors reach caller without changing the destination', async () => {
  const h = harness(); h.clipboard.readText = async () => { throw new Error('Clipboard permission denied'); };
  await assert.rejects(h.edit('paste'), /permission denied/); assert.deepEqual(h.pastes, []);
});

test('empty clipboard paste is a no-op even with a selected field range', async () => {
  const h = harness(); h.blur(); const field = new Field(); document.activeElement = field;
  document.execCommand = () => { throw new Error('Must not edit'); };
  await h.edit('paste'); assert.equal(field.value, 'hello world');
  assert.equal(field.selectionStart, 6); assert.equal(field.selectionEnd, 11);
});

test('a hidden field (including a concealed Ghostty textarea) cannot receive paste', async () => {
  const h = harness(); h.blur(); const field = new Field(); field.visible = false; document.activeElement = field;
  h.clipboard.readText = async () => { throw new Error('Must not read without a destination'); };
  await h.edit('paste'); assert.equal(field.value, 'hello world');
});

test('an exited terminal can copy output but cannot fall back to editing its internal textarea', async () => {
  const h = harness(); h.close(); const field = new Field(); document.activeElement = field;
  h.clipboard.readText = async () => { throw new Error('Must not paste after exit'); };
  await h.edit('paste'); await h.edit('copy'); assert.equal(h.text(), 'terminal selection');
  assert.equal(field.value, 'hello world');
});
