import { test } from 'node:test';
import assert from 'node:assert/strict';
import { closestEdge, distribute, dragShare, hitStage, place, tabZone } from '../src/core/workspace/geometry.ts';
import { restore } from '../src/core/workspace/persist.ts';

const pane = (id, type = 'session') => ({ id, type, ...(type === 'session' ? { sid: `s-${id}` } : {}) });
const grp = (id, dir, children, weights) => ({ id, dir, children, weights: weights ?? children.map(() => 1) });
const m = { pane: { w: 100, h: 50 }, divider: 10, header: 30, border: 2 };
const rectOf = (placed, id) => placed.panes.find((p) => p.id === id).rect;

test('distribute', async (t) => {
  const cases = [
    { name: 'by weight', total: 300, weights: [1, 2], mins: [10, 10], want: [100, 200] },
    { name: 'holds a minimum and shares the rest', total: 300, weights: [1, 9, 10], mins: [60, 10, 10], want: [60, 240 * 9 / 19, 240 * 10 / 19] },
    { name: 'a second child falls under its minimum after the first is held', total: 200, weights: [1, 2, 7], mins: [80, 50, 10], want: [80, 50, 70] },
    { name: 'overflows at the minimums when nothing fits', total: 100, weights: [1, 1], mins: [80, 80], want: [80, 80] },
  ];
  for (const c of cases) {
    await t.test(c.name, () => {
      const got = distribute(c.total, c.weights, c.mins);
      got.forEach((v, i) => assert.ok(Math.abs(v - c.want[i]) < 1e-9, `${got} != ${c.want}`));
    });
  }
});

test('place', async (t) => {
  await t.test('a single pane fills the space', () => {
    const placed = place(pane('A'), { w: 800, h: 600 }, m);
    assert.deepEqual(placed.panes, [{ id: 'A', rect: { x: 0, y: 0, w: 800, h: 600 } }]);
    assert.deepEqual(placed.groups, []);
  });

  await t.test('a root row has a frame, no header, and one divider', () => {
    const placed = place(grp('root', 'cols', [pane('A'), pane('B')], [1, 3]), { w: 812, h: 602 }, m);
    assert.deepEqual(placed.groups, [{ id: 'root', frame: { x: 0, y: 0, w: 812, h: 602 }, header: null }]);
    assert.deepEqual(rectOf(placed, 'A'), { x: 1, y: 1, w: 200, h: 600 });
    assert.deepEqual(rectOf(placed, 'B'), { x: 211, y: 1, w: 600, h: 600 });
    assert.deepEqual(placed.dividers, [{ groupId: 'root', index: 0, dir: 'cols', rect: { x: 201, y: 1, w: 10, h: 600 }, sizes: [200, 600], mins: [100, 100] }]);
  });

  await t.test('a nested group has a header above its panes', () => {
    const root = grp('root', 'cols', [pane('A'), grp('pair', 'rows', [pane('B'), pane('C')])]);
    const placed = place(root, { w: 412, h: 302 }, m);
    const pair = placed.groups.find((g) => g.id === 'pair');
    assert.deepEqual(pair.frame, { x: 211, y: 1, w: 200, h: 300 });
    assert.deepEqual(pair.header, { x: 212, y: 2, w: 198, h: 30 });
    assert.deepEqual(rectOf(placed, 'B'), { x: 212, y: 32, w: 198, h: 129 });
    assert.deepEqual(rectOf(placed, 'C'), { x: 212, y: 171, w: 198, h: 129 });
  });

  await t.test('too little space gives the minimum size, for the caller to scroll', () => {
    const placed = place(grp('root', 'cols', [pane('A'), pane('B')]), { w: 50, h: 20 }, m);
    assert.deepEqual(placed.size, { w: 212, h: 52 });
    assert.equal(rectOf(placed, 'A').w, 100);
  });
});

test('dragShare holds both neighbors at their minimums', async (t) => {
  const d = { groupId: 'g', index: 0, dir: 'cols', rect: { x: 0, y: 0, w: 10, h: 10 }, sizes: [200, 600], mins: [100, 150] };
  const cases = [
    { name: 'no movement', delta: 0, want: 0.25 },
    { name: 'to the right', delta: 200, want: 0.5 },
    { name: 'past the first minimum', delta: -500, want: 0.125 },
    { name: 'past the second minimum', delta: 5000, want: 650 / 800 },
  ];
  for (const c of cases) await t.test(c.name, () => assert.equal(dragShare(d, c.delta), c.want));
});

test('closestEdge', async (t) => {
  const r = { x: 100, y: 100, w: 400, h: 200 };
  const cases = [
    { x: 110, y: 200, want: 'left' }, { x: 490, y: 200, want: 'right' },
    { x: 300, y: 105, want: 'top' }, { x: 300, y: 295, want: 'bottom' },
  ];
  for (const c of cases) await t.test(c.want, () => assert.equal(closestEdge(r, c.x, c.y), c.want));
});

test('hitStage', async (t) => {
  const root = grp('root', 'cols', [pane('A'), pane('B')]);
  const placed = place(root, { w: 812, h: 602 }, m);
  const cases = [
    { name: 'the outer band targets the whole tab', x: 10, y: 300, want: { target: 'root', edge: 'left' } },
    { name: 'the outer band wins over the pane edge', x: 400, y: 590, want: { target: 'root', edge: 'bottom' } },
    { name: 'inside a pane targets its nearest edge', x: 380, y: 300, want: { target: 'A', edge: 'right' } },
    { name: 'the other pane', x: 440, y: 300, want: { target: 'B', edge: 'left' } },
    { name: 'on a divider falls back to the whole tab', x: 406, y: 100, want: { target: 'root', edge: 'top' } },
  ];
  for (const c of cases) await t.test(c.name, () => assert.deepEqual(hitStage(root, placed, c.x, c.y), c.want));

  await t.test('a single-pane tab is its own target', () => {
    const one = pane('A');
    assert.deepEqual(hitStage(one, place(one, { w: 800, h: 600 }, m), 400, 100), { target: 'A', edge: 'top' });
  });
});

test('tabZone', async (t) => {
  const cases = [
    { name: 'left edge reorders before', x: 105, width: 200, want: 'before' },
    { name: 'right edge reorders after', x: 295, width: 200, want: 'after' },
    { name: 'center docks', x: 200, width: 200, want: 'center' },
    { name: 'a narrow tab keeps a center', x: 125, width: 50, want: 'center' },
  ];
  for (const c of cases) await t.test(c.name, () => assert.equal(tabZone(100, c.width, c.x), c.want));
});

test('restore', async (t) => {
  const good = { tabs: [{ id: 't1', root: grp('g', 'cols', [pane('A'), pane('L', 'logs')], [2, 1]), focus: 'L' }, { id: 't2', root: pane('M', 'monitor'), focus: 'M' }], active: 't2' };

  await t.test('a valid layout round-trips through JSON', () => {
    assert.deepEqual(restore(JSON.parse(JSON.stringify(good))), good);
  });

  const cases = [
    { name: 'not an object', value: 'x', want: null },
    { name: 'no tabs array', value: { active: 't1' }, want: null },
    { name: 'an unknown active tab falls back to the first', value: { ...good, active: 'gone' }, check: (r) => assert.equal(r.active, 't1') },
    { name: 'a missing focus falls back to the first pane', value: { tabs: [{ ...good.tabs[0], focus: 'gone' }], active: 't1' }, check: (r) => assert.equal(r.tabs[0].focus, 'A') },
    { name: 'a group cannot hold focus', value: { tabs: [{ ...good.tabs[0], focus: 'g' }], active: 't1' }, check: (r) => assert.equal(r.tabs[0].focus, 'A') },
    { name: 'bad weights reset to equal', value: { tabs: [{ id: 't1', root: { ...good.tabs[0].root, weights: [0, 'x'] }, focus: 'A' }], active: 't1' }, check: (r) => assert.deepEqual(r.tabs[0].root.weights, [1, 1]) },
    { name: 'a pane with an unknown type is dropped and its sibling promoted', value: { tabs: [{ id: 't1', root: grp('g', 'cols', [pane('A'), { id: 'X', type: 'claude' }]), focus: 'A' }], active: 't1' }, check: (r) => assert.equal(r.tabs[0].root.id, 'A') },
    { name: 'a session pane needs a session id', value: { tabs: [{ id: 't1', root: { id: 'A', type: 'session' }, focus: 'A' }], active: 't1' }, check: (r) => assert.deepEqual(r, { tabs: [], active: null }) },
    { name: 'a duplicate pane id is dropped', value: { tabs: [{ id: 't1', root: grp('g', 'cols', [pane('A'), pane('A')]), focus: 'A' }], active: 't1' }, check: (r) => assert.equal(r.tabs[0].root.id, 'A') },
    { name: 'same-direction nesting is merged', value: { tabs: [{ id: 't1', root: grp('g', 'cols', [pane('A'), grp('h', 'cols', [pane('B'), pane('C')])]), focus: 'A' }], active: 't1' }, check: (r) => assert.equal(r.tabs[0].root.children.length, 3) },
    { name: 'a root is never explicit', value: { tabs: [{ id: 't1', root: { ...grp('g', 'cols', [pane('A'), pane('B')]), explicit: true }, focus: 'A' }], active: 't1' }, check: (r) => assert.equal(r.tabs[0].root.explicit, undefined) },
  ];
  for (const c of cases) {
    await t.test(c.name, () => {
      const got = restore(c.value);
      if (c.check) c.check(got); else assert.equal(got, c.want);
    });
  }
});
