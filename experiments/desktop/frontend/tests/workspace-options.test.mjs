import { test } from 'node:test';
import assert from 'node:assert/strict';
import { dockOptions, moveOptions, outcome } from '../src/core/workspace/options.ts';
import { isGroup, leaves } from '../src/core/workspace/layout.ts';

const pane = (id) => ({ id, type: 'session', sid: `s-${id}` });
const grp = (id, dir, children, weights) => ({ id, dir, children, weights: weights ?? children.map(() => 1) });
const tab = (id, root) => ({ id, root, focus: leaves(root)[0].id });
const title = (n) => (isGroup(n) ? leaves(n).map((p) => p.id).join(' + ') : n.id);
const three = () => ({ tabs: [tab('t1', grp('root', 'cols', [pane('A'), pane('B'), pane('C')]))], active: 't1' });
const states = (list) => Object.fromEntries(list.map((o) => [`${o.target}:${o.edge}`, o.state]));

test('outcome', async (t) => {
  const cases = [
    { name: 'a real change', dest: { target: 'A', edge: 'left' }, want: 'ok' },
    { name: 'already there', dest: { target: 'A', edge: 'right' }, want: 'same' },
    { name: 'into itself', dest: { target: 'B', edge: 'left' }, want: 'blocked' },
    { name: 'too little space', dest: { target: 'A', edge: 'top' }, opts: { space: { w: 2000, h: 100 } }, want: 'blocked' },
  ];
  for (const c of cases) await t.test(c.name, () => assert.equal(outcome(three(), 'B', c.dest, c.opts).state, c.want));
});

test('dockOptions marks each edge of each target', () => {
  assert.deepEqual(states(dockOptions(three(), 'B', ['root', 'A'])), {
    'root:left': 'ok', 'root:top': 'ok', 'root:right': 'ok', 'root:bottom': 'ok',
    'A:left': 'ok', 'A:top': 'ok', 'A:right': 'same', 'A:bottom': 'ok',
  });
});

test('moveOptions', async (t) => {
  await t.test('the middle of three shells has ten distinct moves', () => {
    const { options, blocked } = moveOptions(three(), 'B', title);
    assert.deepEqual(options.map((o) => o.label), [
      'Left of A', 'Above A', 'Below A',
      'Above C', 'Right of C', 'Below C',
      'Above all panes', 'Below all panes',
      'New tab before A + B + C', 'New tab at the end',
    ]);
    assert.equal(blocked, 0);
  });

  await t.test('a result reachable from a pane and from the whole tab is listed once, under the pane', () => {
    const labels = moveOptions(three(), 'B', title).options.map((o) => o.label);
    assert.ok(labels.includes('Left of A') && !labels.includes('Left of all panes'));
  });

  await t.test('other tabs follow the source tab, and options name their tab', () => {
    const layout = { tabs: [tab('t1', grp('root', 'cols', [pane('A'), pane('B')])), tab('t2', pane('L'))], active: 't1' };
    const { options } = moveOptions(layout, 'B', title);
    assert.deepEqual(options.filter((o) => o.tab === 't2').map((o) => o.label), ['Left of L', 'Above L', 'Right of L', 'Below L']);
    assert.ok(options.findIndex((o) => o.tab === 't1') < options.findIndex((o) => o.tab === 't2'));
  });

  await t.test('a whole tab can be reordered or docked, never into itself', () => {
    const layout = { tabs: [tab('t1', pane('A')), tab('t2', pane('B')), tab('t3', pane('C'))], active: 't1' };
    const { options } = moveOptions(layout, 'A', title);
    assert.deepEqual(options.filter((o) => o.tab === null).map((o) => o.label), ['Before C', 'Last in the tab bar']);
    assert.equal(options.filter((o) => o.tab === 't1').length, 0);
    assert.equal(options.filter((o) => o.tab === 't2').length, 4);
  });

  await t.test('positions that cannot fit are counted, not offered', () => {
    const { options, blocked } = moveOptions(three(), 'B', title, { space: { w: 2000, h: 100 } });
    assert.deepEqual(options.filter((o) => o.tab === 't1').map((o) => o.label), ['Left of A', 'Right of C']);
    assert.equal(blocked, 6);
  });
});
