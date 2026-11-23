import { tick } from 'svelte';
import { hitStage, paneAt, place, tabZone } from '../core/workspace/geometry.ts';
import type { Hit } from '../core/workspace/geometry.ts';
import { find, owner, sameLayout } from '../core/workspace/layout.ts';
import type { Destination } from '../core/workspace/layout.ts';
import type { Workspace } from './state.svelte.ts';

// Pointer docking. The drag source sets the unit of movement through its
// data-grab attribute: a tab's root, a group, or a pane. Dragging inside
// pane content stays a selection gesture, because only headers carry it.
export function attachDocking(ws: Workspace, stageInner: () => HTMLElement | null): () => void {
  let pending: { src: string; x: number; y: number; pid: number; prior: Element | null } | null = null;
  let active: { pid: number; prior: Element | null; reveal: string | null; sig: string; x: number; y: number } | null = null;
  // A tab becomes the docking target only after the pointer rests on it, so
  // crossing other tabs on the way to the workspace does not retarget.
  const tabDwell = 150;
  let hover: { tab: string; since: number; timer: ReturnType<typeof setTimeout> } | null = null;
  let last: Destination | null = null;
  const leaveTab = () => { if (hover) clearTimeout(hover.timer); hover = null; };
  let swallowClick = false;

  interface Found { dest: Destination | null; reveal?: string; over?: string | null }

  function destinationAt(x: number, y: number, src: string): Found {
    const at = document.elementFromPoint(x, y);
    const tabs = ws.layout.tabs;
    const tabEl = at?.closest<HTMLElement>('[data-tab]');
    if (tabEl) {
      const index = tabs.findIndex((t) => t.id === tabEl.dataset.tab);
      const r = tabEl.getBoundingClientRect();
      const zone = tabZone(r.left, r.width, x);
      if (zone !== 'center') { leaveTab(); return { dest: { gap: index + (zone === 'after' ? 1 : 0) } }; }
      const tab = tabs[index];
      if (tab.id !== active?.reveal) {
        if (hover?.tab !== tab.id) {
          leaveTab();
          hover = { tab: tab.id, since: performance.now(), timer: setTimeout(() => { if (active) update(active.x, active.y); }, tabDwell) };
        }
        if (performance.now() - hover.since < tabDwell) return { dest: last };
      }
      leaveTab();
      // Releasing over the center of another tab defaults to its right edge.
      return tab.root.id === src ? { dest: null, reveal: tab.id } : { dest: { target: tab.root.id, edge: 'right' }, reveal: tab.id };
    }
    leaveTab();
    if (at?.closest('.tabbar')) return { dest: { gap: tabs.length } };
    const inner = stageInner();
    const tab = tabs.find((t) => t.id === active?.reveal) ?? ws.active;
    if (!inner || !tab) return { dest: null };
    const r = inner.getBoundingClientRect();
    // A release outside the workspace and the tab bar has no destination, which cancels the move.
    if (x < r.left || x > r.right || y < r.top || y > r.bottom) return { dest: null };
    const placed = place(tab.root, ws.space);
    const prev: Hit | undefined = last && 'target' in last ? last : undefined;
    return { dest: hitStage(tab.root, placed, x - r.left, y - r.top, prev), reveal: tab.id, over: paneAt(placed, x - r.left, y - r.top) ?? null };
  }

  function withinSource(src: string, target: string): boolean {
    const tab = owner(ws.layout, src);
    const node = tab && find(tab.root, src);
    return !!node && !!find(node, target);
  }

  function update(x: number, y: number) {
    if (!active || !ws.drag) return;
    active.x = x; active.y = y;
    const src = ws.drag.src;
    const hit = destinationAt(x, y, src);
    if (hit.reveal) active.reveal = hit.reveal;
    // Over the source itself there is nothing to offer: a release there
    // leaves the layout as it is.
    let dest = hit.dest && 'target' in hit.dest && withinSource(src, hit.dest.target) ? null : hit.dest;
    last = dest;
    const over = hit.over && !withinSource(src, hit.over) ? hit.over : null;
    const sig = JSON.stringify([dest, active.reveal, over, hit.over !== undefined]);
    if (sig === active.sig) return;
    active.sig = sig;
    let { layout, error } = dest ? ws.preview(src, dest) : { layout: null, error: '' };
    // A destination that would leave everything where it is offers nothing.
    if (layout && sameLayout(layout, ws.layout)) { dest = null; layout = null; error = ''; }
    // Markers show which edges of the whole tab and of the pane under the
    // pointer are available, so the choices are visible without exploring.
    const shown = ws.layout.tabs.find((t) => t.id === active?.reveal);
    const targets = hit.over === undefined || !shown ? [] : [...new Set([shown.root.id, ...(over ? [over] : [])])].filter((id) => !withinSource(src, id));
    ws.drag = { src, dest, result: layout, error, reveal: active.reveal, over, marks: ws.marks(src, targets) };
  }

  function stop(apply: boolean) {
    pending = null;
    if (!active || !ws.drag) return;
    const { result } = ws.drag;
    const { prior } = active;
    active = null;
    last = null;
    leaveTab();
    ws.drag = null;
    document.body.style.userSelect = '';
    // Only the click that this release produces is swallowed, never a later one.
    swallowClick = true;
    setTimeout(() => { swallowClick = false; }, 0);
    if (apply && result) { ws.commit(result); return; }
    // The revealed tab is swapped back first, so the element that had focus is visible again.
    void tick().then(() => {
      if (prior instanceof HTMLElement && prior.isConnected && prior.offsetParent !== null) prior.focus({ preventScroll: true });
      else void ws.focusPane();
    });
  }

  const down = (e: PointerEvent) => {
    swallowClick = false;
    const target = e.target as Element | null;
    const handle = target?.closest<HTMLElement>('[data-grab]');
    if (!handle || e.button !== 0 || target?.closest('[data-nodrag]') || ws.moving) return;
    pending = { src: handle.dataset.grab as string, x: e.clientX, y: e.clientY, pid: e.pointerId, prior: document.activeElement };
  };
  const move = (e: PointerEvent) => {
    if (!active && pending && e.pointerId === pending.pid && Math.hypot(e.clientX - pending.x, e.clientY - pending.y) > 7) {
      active = { pid: pending.pid, prior: pending.prior, reveal: ws.layout.active, sig: '', x: e.clientX, y: e.clientY };
      ws.menu = null;
      ws.drag = { src: pending.src, dest: null, result: null, error: '', reveal: ws.layout.active, over: null, marks: [] };
      pending = null;
      document.body.style.userSelect = 'none';
    }
    if (active && e.pointerId === active.pid) { e.preventDefault(); update(e.clientX, e.clientY); }
  };
  const up = (e: PointerEvent) => {
    if (active && e.pointerId === active.pid) update(e.clientX, e.clientY);
    stop(true);
  };
  const cancel = () => stop(false);
  const key = (e: KeyboardEvent) => { if (e.key === 'Escape' && active) { e.preventDefault(); stop(false); } };
  // The click that ends a drag must not also select the tab under the pointer.
  const click = (e: MouseEvent) => { if (swallowClick) { swallowClick = false; e.preventDefault(); e.stopImmediatePropagation(); } };

  document.addEventListener('pointerdown', down, true);
  document.addEventListener('pointermove', move, { passive: false });
  document.addEventListener('pointerup', up);
  document.addEventListener('pointercancel', cancel);
  document.addEventListener('keydown', key, true);
  document.addEventListener('click', click, true);
  window.addEventListener('blur', cancel);
  return () => {
    document.removeEventListener('pointerdown', down, true);
    document.removeEventListener('pointermove', move);
    document.removeEventListener('pointerup', up);
    document.removeEventListener('pointercancel', cancel);
    document.removeEventListener('keydown', key, true);
    document.removeEventListener('click', click, true);
    window.removeEventListener('blur', cancel);
  };
}
