import { test } from 'node:test';
import assert from 'node:assert/strict';
import { workingDirectory, shellStatus, appendTerminalSelection } from '../src/terminal-tools.ts';

test('working directories decode shell URIs without treating them as local paths or links', () => {
  assert.equal(workingDirectory('file://sandbox/home/code/a%20b/%E4%B8%96%E7%95%8C'), 'sandbox:/home/code/a b/世界');
  assert.equal(workingDirectory('file:///tmp/work'), '/tmp/work');
  for (const uri of ['', 'https://host/project', 'file://user:pass@host/tmp', 'file://host:22/tmp', 'file://host/tmp?x', 'file://host/tmp#x', 'file://host/%1b[31m', 'file://host/%0anewline', 'file://host/%FF']) {
    assert.equal(workingDirectory(uri), '', uri);
  }
});

test('shell status distinguishes absent reports, running commands, and reported failures', () => {
  assert.equal(shellStatus({phase:'unknown',exitCode:null}), 'Shell status unavailable');
  assert.equal(shellStatus({phase:'running',exitCode:null}), 'Running');
  assert.equal(shellStatus({phase:'input',exitCode:0}), 'Ready');
  assert.equal(shellStatus({phase:'prompt',exitCode:7}), 'Ready · last exit 7');
  assert.equal(shellStatus({phase:'complete',exitCode:7}), 'Exit 7');
  assert.equal(shellStatus({phase:'complete',exitCode:null}), 'Finished');
});

test('selection appends to the existing draft and cannot close its own Markdown fence', () => {
  assert.equal(appendTerminalSelection('Explain this', 'error: build failed'), 'Explain this\n\nTerminal selection:\n```text\nerror: build failed\n```');
  assert.equal(appendTerminalSelection('', '```\ntext'), 'Terminal selection:\n````text\n```\ntext\n````');
  assert.equal(appendTerminalSelection('keep draft', ''), 'keep draft');
});
