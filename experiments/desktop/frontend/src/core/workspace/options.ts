// The choices a move has. The drag markers and the move dialog both list
// them from here, so they offer the same results as a drop.

import { LayoutError, find, isGroup, leaves, owner, sameLayout, transform } from './layout.ts';
import type { Destination, Edge, Layout, Node, Options } from './layout.ts';

export const edges: Edge[] = ['left', 'top', 'right', 'bottom'];

// ok: a real change. same: everything would stay where it is.
// blocked: not possible, for the reason in `error`.
export type OptionState = 'ok' | 'same' | 'blocked';
export interface Outcome { state: OptionState; layout: Layout | null; error: string }
export interface DockOption extends Outcome { target: string; edge: Edge }

export function outcome(layout: Layout, src: string, dest: Destination, opts: Options = {}): Outcome {
  try {
    const next = transform(layout, src, dest, opts);
    return sameLayout(next, layout) ? { state: 'same', layout: null, error: '' } : { state: 'ok', layout: next, error: '' };
  } catch (e) {
    if (e instanceof LayoutError) return { state: 'blocked', layout: null, error: e.message };
    throw e;
  }
}

// The four edges of each target, for the markers shown while dragging.
export function dockOptions(layout: Layout, src: string, targets: string[], opts: Options = {}): DockOption[] {
  return targets.flatMap((target) => edges.map((edge) => ({ target, edge, ...outcome(layout, src, { target, edge }, opts) })));
}

export interface MoveOption {
  dest: Destination;
  layout: Layout;
  label: string;
  // The tab the source lands in, or null for a position in the tab bar.
  tab: string | null;
}

const sides: Record<Edge, string> = { left: 'Left of', top: 'Above', right: 'Right of', bottom: 'Below' };

// Every distinct result of moving `src`, each listed once. Positions that
// change nothing are left out, and `blocked` counts those that cannot fit.
export function moveOptions(layout: Layout, src: string, title: (n: Node) => string, opts: Options = {}): { options: MoveOption[]; blocked: number } {
  const home = owner(layout, src);
  const source = home && find(home.root, src);
  const options: MoveOption[] = [];
  let blocked = 0;
  if (!home || !source) return { options, blocked };
  const add = (dest: Destination, label: string, tab: string | null) => {
    const result = outcome(layout, src, dest, opts);
    if (result.state === 'blocked') blocked++;
    if (!result.layout || options.some((o) => sameLayout(o.layout, result.layout as Layout))) return;
    options.push({ dest, layout: result.layout, label, tab });
  };
  // The source's own tab comes first. Panes come before the whole tab, so a
  // result reachable both ways is named after the pane.
  for (const tab of [home, ...layout.tabs.filter((t) => t !== home)]) {
    const panes = leaves(tab.root).filter((p) => !find(source, p.id));
    for (const p of panes) for (const edge of edges) add({ target: p.id, edge }, `${sides[edge]} ${title(p)}`, tab.id);
    if (isGroup(tab.root) && tab.root.id !== src) for (const edge of edges) add({ target: tab.root.id, edge }, `${sides[edge]} all panes`, tab.id);
  }
  const whole = home.root.id === src;
  layout.tabs.forEach((t, i) => add({ gap: i }, `${whole ? 'Before' : 'New tab before'} ${title(t.root)}`, null));
  add({ gap: layout.tabs.length }, whole ? 'Last in the tab bar' : 'New tab at the end', null);
  return { options, blocked };
}
