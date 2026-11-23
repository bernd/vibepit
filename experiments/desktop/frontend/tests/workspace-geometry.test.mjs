import { test } from 'node:test';
import assert from 'node:assert/strict';
import { closestEdge, crossRects, distribute, dragShare, edgeZone, hitStage, place, tabZone } from '../src/core/workspace/geometry.ts';
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

test('edgeZone', async (t) => {
  const r = { x: 0, y: 0, w: 300, h: 300 };
  const cases = [
    { name: 'pushing toward a side picks it', x: 30, y: 150, want: 'left' },
    { name: 'the middle has no edge', x: 150, y: 150, want: null },
    { name: 'just outside the outer third', x: 110, y: 150, want: null },
    { name: 'the previous edge stays in the middle', x: 150, y: 150, prev: 'left', want: null },
    { name: 'a corner keeps the previous edge', x: 40, y: 30, prev: 'left', want: 'left' },
    { name: 'the same corner without history takes the nearer edge', x: 40, y: 30, want: 'top' },
    { name: 'a clearly deeper edge replaces the previous one', x: 80, y: 20, prev: 'left', want: 'top' },
    { name: 'the previous edge is dropped once the pointer leaves its side', x: 200, y: 20, prev: 'left', want: 'top' },
  ];
  for (const c of cases) await t.test(c.name, () => assert.equal(edgeZone(r, c.x, c.y, c.prev), c.want));
});

test('hitStage', async (t) => {
  const root = grp('root', 'cols', [pane('A'), pane('B')]);
  const placed = place(root, { w: 812, h: 602 }, m);
  const cases = [
    { name: 'the outer band targets the whole tab', x: 10, y: 300, want: { target: 'root', edge: 'left' } },
    { name: 'the outer band wins over the pane edge', x: 400, y: 590, want: { target: 'root', edge: 'bottom' } },
    { name: 'pushing toward a pane side targets that edge', x: 380, y: 300, want: { target: 'A', edge: 'right' } },
    { name: 'the other pane', x: 440, y: 300, want: { target: 'B', edge: 'left' } },
    { name: 'on a divider falls back to the whole tab', x: 406, y: 100, want: { target: 'root', edge: 'top' } },
    { name: 'the middle of a pane keeps the previous edge', x: 150, y: 220, prev: { target: 'A', edge: 'right' }, want: { target: 'A', edge: 'right' } },
    { name: 'history from another target does not carry over', x: 80, y: 300, prev: { target: 'B', edge: 'right' }, want: { target: 'A', edge: 'left' } },
    { name: 'a whole-tab target reaches a little deeper', x: 36, y: 300, prev: { target: 'root', edge: 'left' }, want: { target: 'root', edge: 'left' } },
    { name: 'a pane target reaches a little closer to the border', x: 24, y: 300, prev: { target: 'A', edge: 'left' }, want: { target: 'A', edge: 'left' } },
  ];
  for (const c of cases) await t.test(c.name, () => assert.deepEqual(hitStage(root, placed, c.x, c.y, c.prev), c.want));

  await t.test('small movements in the middle of a pane do not change the result', () => {
    let hit = hitStage(root, placed, 380, 300);
    const seen = new Set();
    for (const [dx, dy] of [[0, 0], [8, -9], [-7, 6], [5, 11], [-10, -4], [9, 9], [-3, -12]]) {
      hit = hitStage(root, placed, 150 + dx, 220 + dy, hit);
      seen.add(`${hit.target}:${hit.edge}`);
    }
    assert.deepEqual([...seen], ['A:right']);
  });

  await t.test('a single-pane tab is its own target', () => {
    const one = pane('A');
    assert.deepEqual(hitStage(one, place(one, { w: 800, h: 600 }, m), 400, 100), { target: 'A', edge: 'top' });
  });
});

test('crossRects', async (t) => {
  await t.test('four arrows around the center of the pane', () => {
    assert.deepEqual(crossRects({ x: 100, y: 0, w: 400, h: 300 }), {
      left: { x: 257, y: 137, w: 26, h: 26 }, right: { x: 317, y: 137, w: 26, h: 26 },
      top: { x: 287, y: 107, w: 26, h: 26 }, bottom: { x: 287, y: 167, w: 26, h: 26 },
    });
  });
  await t.test('a pane too small for the cross has none', () => {
    assert.equal(crossRects({ x: 0, y: 0, w: 124, h: 90 }), null);
  });
});

test('hitStage: pointing at an arrow of the cross chooses its edge', async (t) => {
  const root = grp('root', 'cols', [pane('A'), pane('B')]);
  const placed = place(root, { w: 812, h: 602 }, m);
  // Pane A spans x 1..401 and y 1..601, so its center is (201, 301).
  const cases = [
    { name: 'left arrow', x: 171, y: 301, want: 'left' },
    { name: 'right arrow', x: 231, y: 301, want: 'right' },
    { name: 'top arrow', x: 201, y: 271, want: 'top' },
    { name: 'bottom arrow', x: 201, y: 331, want: 'bottom' },
  ];
  for (const c of cases) await t.test(c.name, () => assert.deepEqual(hitStage(root, placed, c.x, c.y, { target: 'A', edge: 'top' === c.want ? 'left' : 'top' }), { target: 'A', edge: c.want }));

  await t.test('the gap in the middle of the cross keeps the previous edge', () => {
    assert.deepEqual(hitStage(root, placed, 201, 301, { target: 'A', edge: 'bottom' }), { target: 'A', edge: 'bottom' });
  });
  await t.test('between the cross and the outer third the previous edge stays', () => {
    assert.deepEqual(hitStage(root, placed, 150, 220, { target: 'A', edge: 'right' }), { target: 'A', edge: 'right' });
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
