import { test } from 'node:test';
import assert from 'node:assert/strict';
import { terminalPaste, terminalBytes } from '../src/terminal-input.ts';

test('native clipboard paste follows bracketed-paste mode without allowing an embedded terminator', () => {
  assert.equal(terminalPaste('héllo 🌍\nnext', false), 'héllo 🌍\nnext');
  assert.equal(terminalPaste('héllo 🌍\nnext', true), '\x1b[200~héllo 🌍\nnext\x1b[201~');
  assert.equal(terminalPaste('before\x1b[201~after', true), '\x1b[200~before[201~after\x1b[201~');
  assert.equal(terminalPaste('', true), '');
});

test('binary mouse reports survive the JSON transport without UTF-8 re-encoding', () => {
  assert.equal(terminalBytes(Uint8Array.of(0x1b, 0x5b, 0x4d, 32, 200, 255)), 'G1tNIMj/');
});
