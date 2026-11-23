// Restores a saved layout. Saved preferences come from disk, so they are
// checked and repaired, never trusted.

import { find, isGroup, leaves, normalize } from './layout.ts';
import type { Layout, Node, PaneType, Tab } from './layout.ts';

const paneTypes: PaneType[] = ['session', 'monitor', 'logs'];
const text = (v: unknown): v is string => typeof v === 'string' && v.length > 0;
const record = (v: unknown): v is Record<string, unknown> => typeof v === 'object' && v !== null && !Array.isArray(v);

function node(v: unknown, seen: Set<string>): Node | null {
  if (!record(v) || !text(v.id) || seen.has(v.id)) return null;
  seen.add(v.id);
  if (!('children' in v)) {
    if (!paneTypes.includes(v.type as PaneType)) return null;
    if (v.type === 'session' && !text(v.sid)) return null;
    return v.type === 'session' ? { id: v.id, type: 'session', sid: v.sid as string } : { id: v.id, type: v.type as PaneType };
  }
  if (!Array.isArray(v.children) || (v.dir !== 'cols' && v.dir !== 'rows')) return null;
  const children: Node[] = [];
  const weights: number[] = [];
  v.children.forEach((c, i) => {
    const child = node(c, seen);
    if (!child) return;
    const w = Array.isArray(v.weights) ? v.weights[i] : undefined;
    children.push(child);
    weights.push(typeof w === 'number' && Number.isFinite(w) && w > 0 ? w : 1);
  });
  if (!children.length) return null;
  return { id: v.id, dir: v.dir, children, weights, ...(v.explicit === true ? { explicit: true } : {}) };
}

// Returns a valid layout, or null when nothing usable was saved. Damaged
// parts are dropped: a pane without a type, a duplicate id, a tab with no
// panes left.
export function restore(value: unknown): Layout | null {
  if (!record(value) || !Array.isArray(value.tabs)) return null;
  const seen = new Set<string>();
  const tabs: Tab[] = [];
  for (const t of value.tabs) {
    if (!record(t) || !text(t.id) || seen.has(t.id)) continue;
    seen.add(t.id);
    const parsed = node(t.root, seen);
    if (!parsed) continue;
    let root = normalize(parsed);
    if (isGroup(root) && root.explicit) { const { explicit: _explicit, ...rest } = root; root = rest; }
    const focus = text(t.focus) && find(root, t.focus) && !isGroup(find(root, t.focus) as Node) ? t.focus : leaves(root)[0].id;
    tabs.push({ id: t.id, root, focus });
  }
  const active = tabs.some((t) => t.id === value.active) ? (value.active as string) : tabs[0]?.id ?? null;
  return { tabs, active };
}
