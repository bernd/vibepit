import { hitStage, place, tabZone } from '../core/workspace/geometry.ts';
import type { Hit } from '../core/workspace/geometry.ts';
import { find, owner } from '../core/workspace/layout.ts';
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

  function destinationAt(x: number, y: number, src: string): { dest: Destination | null; reveal?: string } {
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
    const prev: Hit | undefined = last && 'target' in last ? last : undefined;
    return { dest: hitStage(tab.root, place(tab.root, ws.space), x - r.left, y - r.top, prev), reveal: tab.id };
  }

  function withinSource(src: string, target: string): boolean {
    const tab = owner(ws.layout, src);
    const node = tab && find(tab.root, src);
    return !!node && !!find(node, target);
  }

  function update(x: number, y: number) {
    if (!active || !ws.drag) return;
    active.x = x; active.y = y;
    const hit = destinationAt(x, y, ws.drag.src);
    if (hit.reveal) active.reveal = hit.reveal;
    // Over the source itself there is nothing to preview: a release there
    // leaves the layout as it is.
    const dest = hit.dest && 'target' in hit.dest && withinSource(ws.drag.src, hit.dest.target) ? null : hit.dest;
    last = dest;
    const sig = JSON.stringify(dest);
    if (sig === active.sig) return;
    active.sig = sig;
    const { layout, error } = dest ? ws.preview(ws.drag.src, dest) : { layout: null, error: '' };
    ws.drag = { src: ws.drag.src, dest, result: layout, error };
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
    if (apply && result) ws.commit(result);
    else if (prior instanceof HTMLElement && prior.isConnected) prior.focus({ preventScroll: true });
    else void ws.focusPane();
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
      ws.drag = { src: pending.src, dest: null, result: null, error: '' };
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
