import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  LayoutError, close, equalize, find, isGroup, joinTitle, leaves, minSize, neighbor, normalize, setDivider, setFocus, split, transform,
} from '../src/core/workspace/layout.ts';

const pane = (id, type = 'session') => ({ id, type });
const grp = (id, dir, children, weights, explicit) => ({ id, dir, children, weights: weights ?? children.map(() => 1), ...(explicit ? { explicit } : {}) });
const tab = (id, root, focus) => ({ id, root, focus: focus ?? leaves(root)[0].id });
function ids() { let n = 0; return () => `g${++n}`; }

// Compact shape of a tree: "A" for a pane, "cols(A,B)" for a group, with a
// "!" suffix on explicit groups.
const shape = (n) => (isGroup(n) ? `${n.dir}${n.explicit ? '!' : ''}(${n.children.map(shape).join(',')})` : n.id);
const weights = (n) => n.weights.map((w) => Math.round(w * 1000) / 1000);
const rootOf = (layout, tabId) => layout.tabs.find((t) => t.id === tabId).root;

// The spec's worked example: a tab with Codex and Tests side by side, and a Logs tab.
function example() {
  return {
    tabs: [tab('t1', grp('pair', 'cols', [pane('Codex'), pane('Tests')])), tab('t2', pane('Logs', 'logs'))],
    active: 't1',
  };
}

test('worked example: drop the Codex + Tests tab on Logs', async (t) => {
  const cases = [
    { edge: 'top', want: 'rows(cols!(Codex,Tests),Logs)' },
    { edge: 'right', want: 'cols(Logs,rows!(Codex,Tests))' },
    { edge: 'bottom', want: 'rows(Logs,cols!(Codex,Tests))' },
    { edge: 'left', want: 'cols(rows!(Codex,Tests),Logs)' },
  ];
  for (const c of cases) {
    await t.test(c.edge, () => {
      const before = example();
      const after = transform(before, 'pair', { target: 'Logs', edge: c.edge }, { newId: ids() });
      assert.equal(after.tabs.length, 1, 'the emptied source tab is removed');
      assert.equal(after.active, 't2');
      assert.equal(shape(after.tabs[0].root), c.want);
      assert.deepEqual(weights(after.tabs[0].root), [1, 1], 'incoming content takes half of the target');
      assert.equal(after.tabs[0].focus, 'Codex', 'focus follows the moved group');
    });
  }
});

test('a preview does not edit the committed layout', () => {
  const before = example();
  const snapshot = structuredClone(before);
  transform(before, 'pair', { target: 'Logs', edge: 'top' });
  transform(before, 'Tests', { gap: 2 });
  split(before, 'Logs', 'cols', pane('New'));
  close(before, 'Codex');
  equalize(before, 'pair');
  setDivider(before, 'pair', 0, 0.3);
  assert.deepEqual(before, snapshot);
});

test('moving back to the tab bar', async (t) => {
  const docked = transform(example(), 'pair', { target: 'Logs', edge: 'top' }, { newId: ids() });
  const pairId = docked.tabs[0].root.children[0].id;

  await t.test('the group handle carries both panes', () => {
    const after = transform(docked, pairId, { gap: 0 }, { newId: ids() });
    assert.deepEqual(after.tabs.map((x) => shape(x.root)), ['cols(Codex,Tests)', 'Logs']);
    assert.equal(after.active, after.tabs[0].id);
    assert.equal(after.tabs[0].root.explicit, undefined, 'a tab root is never explicit');
  });

  await t.test('a pane header carries one view', () => {
    const after = transform(docked, 'Tests', { gap: 1 }, { newId: ids() });
    assert.deepEqual(after.tabs.map((x) => shape(x.root)), ['rows(Codex,Logs)', 'Tests']);
    assert.equal(after.tabs[1].focus, 'Tests');
  });

  await t.test('a round trip through an edge changes the orientation', () => {
    const right = transform(example(), 'pair', { target: 'Logs', edge: 'right' }, { newId: ids() });
    const back = transform(right, right.tabs[0].root.children[1].id, { gap: 1 }, { newId: ids() });
    assert.deepEqual(back.tabs.map((x) => shape(x.root)), ['Logs', 'rows(Codex,Tests)']);
  });
});

test('reordering a whole tab', async (t) => {
  const three = { tabs: [tab('a', pane('A')), tab('b', pane('B')), tab('c', pane('C'))], active: 'b' };
  const cases = [
    { name: 'first to end', src: 'A', gap: 3, want: ['b', 'c', 'a'] },
    { name: 'last to front', src: 'C', gap: 0, want: ['c', 'a', 'b'] },
    { name: 'into its own gap', src: 'B', gap: 1, want: ['a', 'b', 'c'] },
    { name: 'past its right neighbor', src: 'A', gap: 2, want: ['b', 'a', 'c'] },
  ];
  for (const c of cases) {
    await t.test(c.name, () => {
      const after = transform(three, c.src, { gap: c.gap });
      assert.deepEqual(after.tabs.map((x) => x.id), c.want);
      assert.equal(after.active, owner(after, c.src));
    });
  }
  function owner(layout, id) { return layout.tabs.find((x) => find(x.root, id)).id; }
});

test('nested example: only the direct children of the incoming group are reoriented', () => {
  const nested = grp('outer', 'cols', [pane('Shell'), grp('pair', 'cols', [pane('Codex'), pane('Tests')], [2, 1], true)]);
  const layout = { tabs: [tab('t1', nested), tab('t2', pane('Logs', 'logs'))], active: 't1' };
  const after = transform(layout, 'outer', { target: 'Logs', edge: 'right' }, { newId: ids() });
  assert.equal(shape(after.tabs[0].root), 'cols(Logs,rows!(Shell,cols!(Codex,Tests)))');
  assert.deepEqual(after.tabs[0].root.children[1].children[1].weights, [2, 1], 'nested proportions survive');
});

test('merging implicit groups', async (t) => {
  const row = () => ({ tabs: [tab('t1', grp('root', 'cols', [pane('A'), pane('B')])), tab('t2', pane('C'))], active: 't1' });
  const cases = [
    { name: 'outer right edge joins the row', dest: { target: 'root', edge: 'right' }, want: 'cols(A,B,C)', w: [0.5, 0.5, 1] },
    { name: 'right of a pane joins the row', dest: { target: 'A', edge: 'right' }, want: 'cols(A,C,B)', w: [0.5, 0.5, 1] },
    { name: 'left of a pane joins the row', dest: { target: 'B', edge: 'left' }, want: 'cols(A,C,B)', w: [1, 0.5, 0.5] },
    { name: 'below a pane nests a stacked group', dest: { target: 'A', edge: 'bottom' }, want: 'cols(rows(A,C),B)', w: [1, 1] },
    { name: 'outer top edge wraps the row', dest: { target: 'root', edge: 'top' }, want: 'rows(C,cols(A,B))', w: [1, 1] },
  ];
  for (const c of cases) {
    await t.test(c.name, () => {
      const after = transform(row(), 'C', c.dest, { newId: ids() });
      assert.equal(shape(rootOf(after, 't1')), c.want);
      assert.deepEqual(weights(rootOf(after, 't1')), c.w);
      assert.equal(after.tabs[0].focus, 'C', 'focus goes to the moved pane');
    });
  }

  await t.test('an explicit group does not merge', () => {
    const layout = { tabs: [tab('t1', grp('root', 'cols', [pane('A'), grp('pair', 'cols', [pane('B'), pane('C')], undefined, true)]))], active: 't1' };
    assert.equal(shape(normalize(layout.tabs[0].root)), 'cols(A,cols!(B,C))');
  });

  await t.test('an explicit group is promoted when one child is left', () => {
    const layout = { tabs: [tab('t1', grp('root', 'cols', [pane('A'), grp('pair', 'rows', [pane('B'), pane('C')], undefined, true)]))], active: 't1' };
    assert.equal(shape(close(layout, 'C').tabs[0].root), 'cols(A,B)');
  });
});

test('moves inside one tab', async (t) => {
  await t.test('the target group dissolves when its child leaves', () => {
    const layout = { tabs: [tab('t1', grp('root', 'cols', [grp('left', 'rows', [pane('A'), pane('B')]), pane('C')]))], active: 't1' };
    const after = transform(layout, 'A', { target: 'left', edge: 'right' }, { newId: ids() });
    assert.equal(shape(after.tabs[0].root), 'cols(B,A,C)');
  });

  await t.test('focus stays on a pane that did not move', () => {
    const layout = { tabs: [tab('t1', grp('root', 'cols', [pane('A'), pane('B'), pane('C')]), 'B')], active: 't1' };
    const after = transform(layout, 'A', { gap: 1 }, { newId: ids() });
    assert.equal(after.tabs.find((x) => find(x.root, 'B')).focus, 'B');
  });

  await t.test('the source tab refocuses when its focused pane leaves', () => {
    const layout = { tabs: [tab('t1', grp('root', 'cols', [pane('A'), pane('B')]), 'A')], active: 't1' };
    const after = transform(layout, 'A', { gap: 1 }, { newId: ids() });
    assert.equal(after.tabs[0].focus, 'B');
  });
});

test('rejected moves', async (t) => {
  const layout = { tabs: [tab('t1', grp('root', 'cols', [pane('A'), grp('pair', 'rows', [pane('B'), pane('C')])])), tab('t2', pane('D'))], active: 't1' };
  const cases = [
    { name: 'into the source itself', run: () => transform(layout, 'pair', { target: 'pair', edge: 'left' }), code: 'into-self' },
    { name: 'into a descendant of the source', run: () => transform(layout, 'pair', { target: 'B', edge: 'left' }), code: 'into-self' },
    { name: 'a source that is gone', run: () => transform(layout, 'nope', { gap: 0 }), code: 'missing-source' },
    { name: 'a target that is gone', run: () => transform(layout, 'A', { target: 'nope', edge: 'left' }), code: 'missing-target' },
    { name: 'too little width', run: () => transform(layout, 'D', { target: 'A', edge: 'left' }, { space: { w: 300, h: 600 } }), code: 'no-space' },
    { name: 'too little height', run: () => transform(layout, 'D', { target: 'B', edge: 'top' }, { space: { w: 900, h: 200 } }), code: 'no-space' },
    { name: 'a split that cannot fit', run: () => split(layout, 'A', 'cols', pane('E'), { space: { w: 300, h: 600 } }), code: 'no-space' },
  ];
  for (const c of cases) {
    await t.test(c.name, () => {
      assert.throws(c.run, (e) => e instanceof LayoutError && e.code === c.code);
    });
  }

  await t.test('a move that fits is accepted', () => {
    assert.doesNotThrow(() => transform(layout, 'D', { target: 'A', edge: 'left' }, { space: { w: 900, h: 600 } }));
  });
});

test('split', async (t) => {
  const one = { tabs: [tab('t1', pane('A', 'logs'))], active: 't1' };
  const right = split(one, 'A', 'cols', pane('S1'), { newId: ids() });

  await t.test('opens beside the pane and takes focus', () => {
    assert.equal(shape(right.tabs[0].root), 'cols(A,S1)');
    assert.equal(right.tabs[0].focus, 'S1');
  });

  await t.test('the same direction joins the row with half of the split pane', () => {
    const again = split(right, 'S1', 'cols', pane('S2'), { newId: ids() });
    assert.equal(shape(again.tabs[0].root), 'cols(A,S1,S2)');
    assert.deepEqual(weights(again.tabs[0].root), [1, 0.5, 0.5]);
  });

  await t.test('the other direction nests', () => {
    const below = split(right, 'S1', 'rows', pane('S2'), { newId: ids() });
    assert.equal(shape(below.tabs[0].root), 'cols(A,rows(S1,S2))');
  });
});

test('close', async (t) => {
  const layout = { tabs: [tab('t1', grp('root', 'cols', [pane('A'), grp('pair', 'rows', [pane('B'), pane('C')])]), 'C'), tab('t2', pane('D')), tab('t3', pane('E'))], active: 't1' };

  await t.test('a pane promotes its last sibling and moves focus', () => {
    const after = close(layout, 'C');
    assert.equal(shape(after.tabs[0].root), 'cols(A,B)');
    assert.equal(after.tabs[0].focus, 'A');
  });

  await t.test('the active tab hands over to its neighbor', () => {
    assert.equal(close(layout, 'root').active, 't2');
    assert.equal(close({ ...layout, active: 't3' }, 'E').active, 't2');
  });

  await t.test('a background tab leaves the active tab alone', () => {
    assert.equal(close(layout, 'D').active, 't1');
  });

  await t.test('the last tab leaves an empty window', () => {
    assert.deepEqual(close({ tabs: [tab('t1', pane('A'))], active: 't1' }, 'A'), { tabs: [], active: null });
  });
});

test('equalize keeps nested proportions', () => {
  const layout = { tabs: [tab('t1', grp('root', 'cols', [pane('A'), grp('pair', 'rows', [pane('B'), pane('C')], [3, 1])], [5, 1]))], active: 't1' };
  const after = equalize(layout, 'root');
  assert.deepEqual(after.tabs[0].root.weights, [1, 1]);
  assert.deepEqual(after.tabs[0].root.children[1].weights, [3, 1]);
});

test('setDivider moves space between two neighbors only', () => {
  const layout = { tabs: [tab('t1', grp('root', 'cols', [pane('A'), pane('B'), pane('C')], [1, 1, 2]))], active: 't1' };
  assert.deepEqual(weights(setDivider(layout, 'root', 0, 0.25).tabs[0].root), [0.5, 1.5, 2]);
  assert.equal(setDivider(layout, 'root', 2, 0.5), layout, 'no divider after the last child');
  assert.equal(setDivider(layout, 'root', 0, 1), layout, 'a share must leave room for both');
});

test('setFocus selects the pane and its tab', () => {
  const after = setFocus(example(), 'Logs');
  assert.equal(after.active, 't2');
  assert.equal(setFocus(example(), 'Tests').tabs[0].focus, 'Tests');
  const layout = example();
  assert.equal(setFocus(layout, 'pair'), layout, 'a group cannot take focus');
});

test('minSize', async (t) => {
  const m = { pane: { w: 100, h: 50 }, divider: 10, header: 30, border: 2 };
  const row = grp('g', 'cols', [pane('A'), pane('B')]);
  const cases = [
    { name: 'pane', node: pane('A'), top: false, want: { w: 100, h: 50 } },
    { name: 'row with a header', node: row, top: false, want: { w: 212, h: 82 } },
    { name: 'root row has no header', node: row, top: true, want: { w: 212, h: 52 } },
    { name: 'column in a root row', node: grp('r', 'cols', [pane('A'), grp('c', 'rows', [pane('B'), pane('C')])]), top: true, want: { w: 214, h: 144 } },
  ];
  for (const c of cases) await t.test(c.name, () => assert.deepEqual(minSize(c.node, c.top, m), c.want));
});

test('neighbor follows the visible layout', async (t) => {
  // A | B over C, with D spanning the bottom.
  const root = grp('root', 'rows', [grp('top', 'cols', [pane('A'), grp('right', 'rows', [pane('B'), pane('C')])]), pane('D')]);
  const cases = [
    { from: 'A', edge: 'right', want: 'B' },
    { from: 'A', edge: 'bottom', want: 'D' },
    { from: 'A', edge: 'left', want: undefined },
    { from: 'B', edge: 'bottom', want: 'C' },
    { from: 'C', edge: 'bottom', want: 'D' },
    { from: 'C', edge: 'left', want: 'A' },
    { from: 'D', edge: 'top', want: 'A' },
    { from: 'B', edge: 'top', want: undefined },
  ];
  for (const c of cases) await t.test(`${c.from} ${c.edge}`, () => assert.equal(neighbor(root, c.from, c.edge), c.want));

  await t.test('prefers the larger overlap', () => {
    const wide = grp('root', 'rows', [grp('top', 'cols', [pane('A'), pane('B')], [1, 3]), pane('D')]);
    assert.equal(neighbor(wide, 'D', 'top'), 'B');
  });
});

test('joinTitle', async (t) => {
  const cases = [
    { names: ['Codex'], want: 'Codex' },
    { names: ['Codex', 'Tests'], want: 'Codex + Tests' },
    { names: ['Codex', 'Tests', 'Logs'], want: 'Codex + 2' },
  ];
  for (const c of cases) await t.test(c.want, () => assert.equal(joinTitle(c.names), c.want));
});
