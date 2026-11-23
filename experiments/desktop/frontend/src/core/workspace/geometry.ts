// Pixel geometry for a layout tree. The workspace renders panes as a flat,
// keyed list positioned from these rectangles, so a pane keeps its DOM (and a
// terminal its state) when the tree around it changes.

import { defaultMetrics, isGroup, minSize } from './layout.ts';
import type { Dir, Edge, Metrics, Node, Rect, Size } from './layout.ts';

export interface PaneBox { id: string; rect: Rect }
// `header` is null for the root group of a tab, which has no handle.
export interface GroupBox { id: string; frame: Rect; header: Rect | null }
export interface DividerBox {
  groupId: string;
  index: number;
  dir: Dir;
  rect: Rect;
  // Current sizes and minimums of the two neighbors along the group's axis.
  sizes: [number, number];
  mins: [number, number];
}
export interface Placement { size: Size; panes: PaneBox[]; groups: GroupBox[]; dividers: DividerBox[] }

// Shares `total` by weight. A child that would fall under its minimum gets
// the minimum, and the rest share what is left.
export function distribute(total: number, weights: number[], mins: number[]): number[] {
  const out = weights.map(() => 0);
  const fixed = weights.map(() => false);
  let free = total;
  let sum = weights.reduce((a, b) => a + b, 0);
  for (let again = true; again;) {
    again = false;
    for (let i = 0; i < weights.length; i++) {
      if (fixed[i] || (free * weights[i]) / sum >= mins[i]) continue;
      fixed[i] = true; out[i] = mins[i]; free -= mins[i]; sum -= weights[i]; again = true;
      break;
    }
  }
  weights.forEach((w, i) => { if (!fixed[i]) out[i] = sum > 0 ? (free * w) / sum : 0; });
  return out;
}

// Lays a tab's root out in `space`. A root that cannot fit at the pane
// minimums gets its minimum size, and the caller scrolls.
export function place(root: Node, space: Size, m: Metrics = defaultMetrics): Placement {
  const min = minSize(root, true, m);
  const size = { w: Math.max(space.w, min.w), h: Math.max(space.h, min.h) };
  const out: Placement = { size, panes: [], groups: [], dividers: [] };
  walk(root, { x: 0, y: 0, w: size.w, h: size.h }, true, m, out);
  return out;
}

function walk(n: Node, box: Rect, top: boolean, m: Metrics, out: Placement) {
  if (!isGroup(n)) { out.panes.push({ id: n.id, rect: box }); return; }
  const edge = m.border / 2;
  const head = top ? 0 : m.header;
  out.groups.push({ id: n.id, frame: box, header: top ? null : { x: box.x + edge, y: box.y + edge, w: box.w - m.border, h: head } });
  const inner = { x: box.x + edge, y: box.y + edge + head, w: box.w - m.border, h: box.h - m.border - head };
  const cols = n.dir === 'cols';
  const key = cols ? 'w' : 'h';
  const mins = n.children.map((c) => minSize(c, false, m)[key]);
  const sizes = distribute(inner[key] - m.divider * (n.children.length - 1), n.weights, mins);
  let at = cols ? inner.x : inner.y;
  n.children.forEach((c, i) => {
    walk(c, cols ? { x: at, y: inner.y, w: sizes[i], h: inner.h } : { x: inner.x, y: at, w: inner.w, h: sizes[i] }, false, m, out);
    at += sizes[i];
    if (i === n.children.length - 1) return;
    out.dividers.push({
      groupId: n.id, index: i, dir: n.dir,
      rect: cols ? { x: at, y: inner.y, w: m.divider, h: inner.h } : { x: inner.x, y: at, w: inner.w, h: m.divider },
      sizes: [sizes[i], sizes[i + 1]], mins: [mins[i], mins[i + 1]],
    });
    at += m.divider;
  });
}

// The first neighbor's share after dragging a divider by `delta` pixels,
// held at both neighbors' minimums.
export function dragShare(d: DividerBox, delta: number): number {
  const both = d.sizes[0] + d.sizes[1];
  const first = Math.max(d.mins[0], Math.min(both - d.mins[1], d.sizes[0] + delta));
  return first / both;
}

const inside = (r: Rect, x: number, y: number) => x >= r.x && x <= r.x + r.w && y >= r.y && y <= r.y + r.h;

export function closestEdge(r: Rect, x: number, y: number): Edge {
  const d: [Edge, number][] = [
    ['left', (x - r.x) / r.w], ['right', (r.x + r.w - x) / r.w],
    ['top', (y - r.y) / r.h], ['bottom', (r.y + r.h - y) / r.h],
  ];
  return d.sort((a, b) => a[1] - b[1])[0][0];
}

// Docking target under the pointer. The workspace's outer band targets the
// whole tab and wins over the pane edge it overlaps.
export function hitStage(root: Node, placed: Placement, x: number, y: number, band = 30): { target: string; edge: Edge } {
  const whole = { x: 0, y: 0, w: placed.size.w, h: placed.size.h };
  const fromEdge = Math.min(x, whole.w - x, y, whole.h - y);
  const pane = fromEdge > band ? placed.panes.find((p) => inside(p.rect, x, y)) : undefined;
  return pane ? { target: pane.id, edge: closestEdge(pane.rect, x, y) } : { target: root.id, edge: closestEdge(whole, x, y) };
}

// Which part of a tab the pointer is over: its edges reorder, its center docks.
export function tabZone(left: number, width: number, x: number, edge = 17): 'before' | 'after' | 'center' {
  const zone = Math.min(edge, width / 5);
  return x < left + zone ? 'before' : x > left + width - zone ? 'after' : 'center';
}
