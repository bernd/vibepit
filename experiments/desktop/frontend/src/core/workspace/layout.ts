// Workspace layout tree. Every operation is pure: it returns a new layout and
// leaves its input untouched, so a docking preview can run the same code as
// the commit without editing the committed layout.

export type Dir = 'cols' | 'rows';
export type Edge = 'left' | 'right' | 'top' | 'bottom';
export type PaneType = 'session' | 'monitor' | 'logs';

export interface Pane { id: string; type: PaneType; sid?: string }
export interface Group {
  id: string;
  dir: Dir;
  children: Node[];
  weights: number[];
  // Set when the user moved this group in as a unit. An explicit group never
  // merges into its parent, so it stays movable.
  explicit?: boolean;
}
export type Node = Pane | Group;
export interface Tab { id: string; root: Node; focus: string }
export interface Layout { tabs: Tab[]; active: string | null }

// A move ends in a tab-bar gap (a new or reordered tab) or at an edge of a
// pane, group, or whole tab.
export type Destination = { gap: number } | { target: string; edge: Edge };

export interface Size { w: number; h: number }
export interface Metrics { pane: Size; divider: number; header: number; border: number }
// Prototype tuning values, kept in one place.
export const defaultMetrics: Metrics = { pane: { w: 124, h: 86 }, divider: 7, header: 30, border: 2 };

export interface Options {
  newId?: () => string;
  // Space available to the tab's root. When given, a result that cannot fit
  // at the pane minimums is rejected.
  space?: Size;
  metrics?: Metrics;
}

export type LayoutErrorCode = 'missing-source' | 'missing-target' | 'into-self' | 'no-space';
export class LayoutError extends Error {
  code: LayoutErrorCode;
  constructor(code: LayoutErrorCode, message: string) {
    super(message);
    this.name = 'LayoutError';
    this.code = code;
  }
}

let counter = 0;
const fallbackId = () => `n${Date.now().toString(36)}${(counter++).toString(36)}`;

export const isGroup = (n: Node): n is Group => 'children' in n;
export const leaves = (n: Node): Pane[] => (isGroup(n) ? n.children.flatMap(leaves) : [n]);
export const all = (n: Node): Node[] => (isGroup(n) ? [n, ...n.children.flatMap(all)] : [n]);
export const find = (n: Node, id: string): Node | undefined => all(n).find((x) => x.id === id);
export const owner = (layout: Layout, id: string): Tab | undefined => layout.tabs.find((t) => find(t.root, id));
export const parentOf = (root: Node, id: string): Group | undefined =>
  all(root).find((n): n is Group => isGroup(n) && n.children.some((c) => c.id === id));

export function group(dir: Dir, children: Node[], newId: () => string = fallbackId): Group {
  return { id: newId(), dir, children, weights: children.map(() => 1) };
}

// A group made by a dock or split is implicit and merges into a parent of the
// same orientation, keeping each child's share of the space. A group left
// with one child is replaced by that child.
export function normalize(n: Node): Node {
  if (!isGroup(n)) return n;
  const children: Node[] = [];
  const weights: number[] = [];
  n.children.forEach((child, i) => {
    const c = normalize(child);
    if (isGroup(c) && c.dir === n.dir && !c.explicit) {
      const sum = c.weights.reduce((a, b) => a + b, 0);
      c.children.forEach((g, j) => { children.push(g); weights.push((n.weights[i] * c.weights[j]) / sum); });
    } else {
      children.push(c);
      weights.push(n.weights[i]);
    }
  });
  return children.length === 1 ? children[0] : { ...n, children, weights };
}

function remove(n: Node, id: string): Node | null {
  if (n.id === id) return null;
  if (!isGroup(n)) return n;
  const children: Node[] = [];
  const weights: number[] = [];
  n.children.forEach((c, i) => {
    const kept = remove(c, id);
    if (kept) { children.push(kept); weights.push(n.weights[i]); }
  });
  return { ...n, children, weights };
}

function replace(n: Node, id: string, next: Node): Node {
  if (n.id === id) return next;
  return isGroup(n) ? { ...n, children: n.children.map((c) => replace(c, id, next)) } : n;
}

export function minSize(n: Node, top = false, m: Metrics = defaultMetrics): Size {
  if (!isGroup(n)) return m.pane;
  const c = n.children.map((x) => minSize(x, false, m));
  const gaps = m.divider * (c.length - 1);
  const sum = (k: 'w' | 'h') => c.reduce((s, x) => s + x[k], 0);
  const max = (k: 'w' | 'h') => Math.max(...c.map((x) => x[k]));
  // The root group of a tab has no header: the tab is its handle.
  return {
    w: (n.dir === 'cols' ? sum('w') + gaps : max('w')) + m.border,
    h: (n.dir === 'rows' ? sum('h') + gaps : max('h')) + m.border + (top ? 0 : m.header),
  };
}

function checkSpace(root: Node, opts: Options, message: string) {
  if (!opts.space) return;
  const min = minSize(root, true, opts.metrics);
  if (min.w > opts.space.w || min.h > opts.space.h) throw new LayoutError('no-space', message);
}

function implicit(n: Node): Node {
  if (!isGroup(n) || !n.explicit) return n;
  const { explicit: _explicit, ...rest } = n;
  return rest;
}

const withFocus =(root: Node, focus: string) => (find(root, focus) ? focus : leaves(root)[0].id);

// Moves a pane, a group, or a whole tab. The source id sets the unit of
// movement: a tab's root carries the tab, a group carries its subtree, and a
// pane carries one view.
export function transform(layout: Layout, src: string, dest: Destination, opts: Options = {}): Layout {
  const newId = opts.newId ?? fallbackId;
  const start = owner(layout, src);
  if (!start) throw new LayoutError('missing-source', 'Source no longer exists.');
  const found = find(start.root, src) as Node;
  const whole = found === start.root;
  const former = layout.tabs.indexOf(start);
  if ('target' in dest && find(found, dest.target)) throw new LayoutError('into-self', 'Choose a target outside the moving group.');

  // Focus follows the moved pane, or the moved group's last-focused pane.
  const focus = withFocus(found, start.focus);
  const originalTarget = 'target' in dest ? find(start.root, dest.target) : undefined;

  let tabs = layout.tabs.slice();
  let rest: Tab | undefined;
  if (whole) tabs.splice(former, 1);
  else {
    const root = normalize(remove(start.root, src) as Node);
    rest = { ...start, root, focus: withFocus(root, start.focus) };
    tabs[former] = rest;
  }

  if ('gap' in dest) {
    // A move into the tab bar keeps the internal layout. A tab's root is
    // never explicit: the tab is its handle.
    const tab: Tab = { id: whole ? start.id : newId(), root: implicit(found), focus };
    const index = dest.gap - (whole && former < dest.gap ? 1 : 0);
    tabs.splice(Math.max(0, Math.min(index, tabs.length)), 0, tab);
    return { tabs, active: tab.id };
  }

  // Taking the source out of its own tab can dissolve the target group. The
  // part of it that survives becomes the target.
  let targetId = dest.target;
  if (rest && originalTarget && isGroup(originalTarget) && !find(rest.root, targetId)) {
    const survivor = originalTarget.children.find((c) => find((rest as Tab).root, c.id));
    if (survivor) targetId = survivor.id;
  }
  const at = tabs.findIndex((t) => find(t.root, targetId));
  if (at < 0) throw new LayoutError('missing-target', 'Target no longer exists.');
  const target = find(tabs[at].root, targetId) as Node;

  // At a left or right edge the incoming group's direct children stack. At
  // the top or bottom they run side by side. Deeper groups keep their own
  // orientation.
  const horizontal = dest.edge === 'left' || dest.edge === 'right';
  const moving: Node = isGroup(found) ? { ...found, dir: horizontal ? 'rows' : 'cols', explicit: true } : found;
  // Incoming content takes half of its target. The target keeps its own
  // weights in the other half.
  const first = dest.edge === 'left' || dest.edge === 'top';
  const pair = group(horizontal ? 'cols' : 'rows', first ? [moving, target] : [target, moving], newId);
  const root = normalize(replace(tabs[at].root, target.id, pair));
  checkSpace(root, opts, 'Too little space for these panes at this edge.');
  tabs[at] = { ...tabs[at], root, focus };
  return { tabs, active: tabs[at].id };
}

// Opens a fresh pane next to an existing one in the same tab. The new pane
// takes half of the split pane's space and receives focus.
export function split(layout: Layout, paneId: string, dir: Dir, fresh: Pane, opts: Options = {}): Layout {
  const tab = owner(layout, paneId);
  if (!tab) throw new LayoutError('missing-source', 'Pane no longer exists.');
  const pane = find(tab.root, paneId) as Node;
  const root = normalize(replace(tab.root, paneId, group(dir, [pane, fresh], opts.newId)));
  checkSpace(root, opts, 'Too little space in this tab for another pane.');
  return { tabs: layout.tabs.map((t) => (t === tab ? { ...t, root, focus: fresh.id } : t)), active: tab.id };
}

// Removes a pane, a group, or a whole tab from the layout. Ending or
// detaching the sessions behind it is the caller's concern.
export function close(layout: Layout, id: string): Layout {
  const tab = owner(layout, id);
  if (!tab) return layout;
  const index = layout.tabs.indexOf(tab);
  if (tab.root.id === id) {
    const tabs = layout.tabs.filter((t) => t !== tab);
    const active = layout.active === tab.id ? tabs[Math.min(index, tabs.length - 1)]?.id ?? null : layout.active;
    return { tabs, active };
  }
  const root = normalize(remove(tab.root, id) as Node);
  return { ...layout, tabs: layout.tabs.map((t) => (t === tab ? { ...t, root, focus: withFocus(root, t.focus) } : t)) };
}

function updateGroup(layout: Layout, groupId: string, change: (g: Group) => Group): Layout {
  const tab = owner(layout, groupId);
  const g = tab && find(tab.root, groupId);
  if (!tab || !g || !isGroup(g)) return layout;
  const next = change(g);
  if (next === g) return layout;
  return { ...layout, tabs: layout.tabs.map((t) => (t === tab ? { ...t, root: replace(t.root, groupId, next) } : t)) };
}

// Shares space equally among a group's direct children. Nested groups keep
// their internal weights.
export function equalize(layout: Layout, groupId: string): Layout {
  return updateGroup(layout, groupId, (g) => ({ ...g, weights: g.children.map(() => 1) }));
}

// Moves the divider after child `index`. `share` is the first child's part of
// the space the two neighbors hold together.
export function setDivider(layout: Layout, groupId: string, index: number, share: number): Layout {
  return updateGroup(layout, groupId, (g) => {
    if (index < 0 || index >= g.children.length - 1 || !(share > 0 && share < 1)) return g;
    const weights = g.weights.slice();
    const both = weights[index] + weights[index + 1];
    weights[index] = both * share;
    weights[index + 1] = both - weights[index];
    return { ...g, weights };
  });
}

export function setFocus(layout: Layout, paneId: string): Layout {
  const tab = owner(layout, paneId);
  if (!tab || isGroup(find(tab.root, paneId) as Node)) return layout;
  return { tabs: layout.tabs.map((t) => (t === tab ? { ...t, focus: paneId } : t)), active: tab.id };
}

export interface Rect { x: number; y: number; w: number; h: number }

// Pane rectangles in a unit square, from the weights alone. Enough to decide
// which pane lies in a direction without measuring the DOM.
export function rects(root: Node, box: Rect = { x: 0, y: 0, w: 1, h: 1 }, out = new Map<string, Rect>()): Map<string, Rect> {
  if (!isGroup(root)) { out.set(root.id, box); return out; }
  const sum = root.weights.reduce((a, b) => a + b, 0);
  let offset = 0;
  root.children.forEach((c, i) => {
    const part = root.weights[i] / sum;
    rects(c, root.dir === 'cols'
      ? { x: box.x + box.w * offset, y: box.y, w: box.w * part, h: box.h }
      : { x: box.x, y: box.y + box.h * offset, w: box.w, h: box.h * part }, out);
    offset += part;
  });
  return out;
}

// The pane that directional focus moves to: the nearest one on that side
// that overlaps the current pane, preferring the larger overlap.
export function neighbor(root: Node, paneId: string, edge: Edge): string | undefined {
  const map = rects(root);
  const here = map.get(paneId);
  if (!here) return;
  const eps = 1e-9;
  const across = edge === 'left' || edge === 'right';
  let best: { id: string; gap: number; overlap: number } | undefined;
  for (const [id, r] of map) {
    if (id === paneId) continue;
    const gap = edge === 'left' ? here.x - (r.x + r.w) : edge === 'right' ? r.x - (here.x + here.w)
      : edge === 'top' ? here.y - (r.y + r.h) : r.y - (here.y + here.h);
    if (gap < -eps) continue;
    const overlap = across
      ? Math.min(r.y + r.h, here.y + here.h) - Math.max(r.y, here.y)
      : Math.min(r.x + r.w, here.x + here.w) - Math.max(r.x, here.x);
    if (overlap <= eps) continue;
    if (!best || gap < best.gap - eps || (Math.abs(gap - best.gap) <= eps && overlap > best.overlap)) best = { id, gap, overlap };
  }
  return best?.id;
}

// A tab or group title joins the names of its panes. Beyond two panes it
// shows the first name and a count.
export function joinTitle(names: string[]): string {
  return names.length > 2 ? `${names[0]} + ${names.length - 1}` : names.join(' + ');
}
