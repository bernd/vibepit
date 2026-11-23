<script lang="ts">
  import { crossRects, dragShare, place } from '../core/workspace/geometry.ts';
  import type { DividerBox } from '../core/workspace/geometry.ts';
  import { find, isGroup, leaves, parentOf } from '../core/workspace/layout.ts';
  import type { Edge, Rect } from '../core/workspace/layout.ts';
  import type { Workspace } from './state.svelte.ts';
  import Icon from './Icon.svelte';
  import PaneView from './PaneView.svelte';

  let { ws, inner = $bindable(null) }: { ws: Workspace; inner?: HTMLElement | null } = $props();
  let stage = $state<HTMLElement | null>(null);

  // While dragging, the workspace of the tab the pointer rested on is shown.
  const tab = $derived(ws.layout.tabs.find((t) => t.id === ws.drag?.reveal) ?? ws.active);
  // A drag previews the full layout, so zoom is ignored while one is in progress.
  const visible = $derived(tab ? (ws.zoomed && !ws.drag && find(tab.root, ws.zoomed)) || tab.root : null);
  const placed = $derived(visible && ws.space.w > 0 ? place(visible, ws.space) : null);
  const rects = $derived(new Map<string, Rect>(placed?.panes.map((p) => [p.id, p.rect])));
  // Every pane of every tab stays mounted, keyed by its id. Panes of
  // background tabs are hidden, and a move never remounts a pane.
  const panes = $derived(ws.layout.tabs.flatMap((t) => leaves(t.root).map((pane) => ({ pane, multi: isGroup(t.root) }))));
  // Markers for the edges: wide ones on the border for the whole tab, and a
  // cross of four arrows in the middle of the pane under the pointer. An
  // unavailable arrow of the cross stays as a faint outline, so the cross
  // keeps its shape. An unavailable border marker is left out.
  const marks = $derived.by(() => {
    const d = ws.drag;
    if (!d || !placed || !tab) return [];
    const out: { key: string; rect: Rect; edge: Edge; whole: boolean; active: boolean; off: boolean }[] = [];
    for (const m of d.marks) {
      const whole = m.target === tab.root.id;
      const off = m.state !== 'ok';
      const active = !!d.dest && 'target' in d.dest && d.dest.target === m.target && d.dest.edge === m.edge;
      if (whole) {
        if (off) continue;
        const across = m.edge === 'left' || m.edge === 'right';
        const w = across ? 22 : 44;
        const h = across ? 44 : 22;
        const x = m.edge === 'left' ? 4 : m.edge === 'right' ? placed.size.w - 4 - w : (placed.size.w - w) / 2;
        const y = m.edge === 'top' ? 4 : m.edge === 'bottom' ? placed.size.h - 4 - h : (placed.size.h - h) / 2;
        out.push({ key: `${m.target}:${m.edge}`, rect: { x, y, w, h }, edge: m.edge, whole, active, off });
        continue;
      }
      const r = rects.get(m.target);
      const cross = r && crossRects(r);
      if (cross) out.push({ key: `${m.target}:${m.edge}`, rect: cross[m.edge], edge: m.edge, whole, active, off });
    }
    return out;
  });
  // Where the dragged content will sit after the drop, with its real size.
  const landing = $derived.by(() => {
    const d = ws.drag;
    const next = d?.result?.tabs.find((t) => t.id === d.result?.active);
    if (!d?.dest || !('target' in d.dest) || !next) return null;
    const carried = ws.carried;
    const after = place(next.root, ws.space);
    // A moved group lands as its frame, handle included. A pane lands as itself.
    const frame = after.groups.find((g) => g.id === d.src)?.frame;
    const boxes = frame ? [frame] : after.panes.filter((p) => carried.has(p.id)).map((p) => p.rect);
    if (!boxes.length) return null;
    const x = Math.min(...boxes.map((b) => b.x));
    const y = Math.min(...boxes.map((b) => b.y));
    const source = ws.layout.tabs.map((t) => find(t.root, d.src)).find(Boolean);
    // The group the content will belong to, when that is not the tab itself.
    // It is outlined with its future handle, so it is clear which panes will
    // move together afterwards.
    const parent = parentOf(next.root, d.src);
    const frame2 = parent && parent.id !== next.root.id ? after.groups.find((g) => g.id === parent.id)?.frame : undefined;
    // A group that exists already is named as it is now, without the newcomer.
    const existing = parent && ws.layout.tabs.map((t) => find(t.root, parent.id)).find(Boolean);
    const joins = parent && frame2 ? { rect: frame2, label: existing ? `Joins group · ${ws.fullTitle(existing)}` : `New group · ${ws.fullTitle(parent)}` } : null;
    return { rect: { x, y, w: Math.max(...boxes.map((b) => b.x + b.w)) - x, h: Math.max(...boxes.map((b) => b.y + b.h)) - y }, title: source ? ws.title(source) : '', joins };
  });
  const blocked = $derived(ws.drag?.dest && 'target' in ws.drag.dest && !ws.drag.result ? ws.drag.error : '');

  $effect(() => {
    if (!stage) return;
    const measure = () => {
      const style = getComputedStyle(stage as HTMLElement);
      const w = (stage as HTMLElement).clientWidth - parseFloat(style.paddingLeft) - parseFloat(style.paddingRight);
      const h = (stage as HTMLElement).clientHeight - parseFloat(style.paddingTop) - parseFloat(style.paddingBottom);
      if (w !== ws.space.w || h !== ws.space.h) ws.space = { w, h };
    };
    measure();
    if (typeof ResizeObserver === 'undefined') {
      window.addEventListener('resize', measure);
      return () => window.removeEventListener('resize', measure);
    }
    const observer = new ResizeObserver(measure);
    observer.observe(stage);
    return () => observer.disconnect();
  });

  const box = (r: Rect) => `left:${r.x}px;top:${r.y}px;width:${r.w}px;height:${r.h}px`;
  const groupNode = (id: string) => (tab ? find(tab.root, id) : undefined);

  function dividerDown(e: PointerEvent, d: DividerBox) {
    const el = e.currentTarget as HTMLElement;
    const start = d.dir === 'cols' ? e.clientX : e.clientY;
    el.setPointerCapture(e.pointerId);
    el.onpointermove = (f) => ws.divider(d.groupId, d.index, dragShare(d, (d.dir === 'cols' ? f.clientX : f.clientY) - start), false);
    el.onpointerup = el.onpointercancel = () => {
      el.onpointermove = el.onpointerup = el.onpointercancel = null;
      ws.divider(d.groupId, d.index, currentShare(d), true);
    };
    e.preventDefault();
  }
  // The share the divider holds now, read back from the committed weights.
  function currentShare(d: DividerBox): number {
    const g = groupNode(d.groupId);
    return g && isGroup(g) ? g.weights[d.index] / (g.weights[d.index] + g.weights[d.index + 1]) : 0.5;
  }
  function dividerKey(e: KeyboardEvent, d: DividerBox) {
    if (e.ctrlKey || e.metaKey || e.altKey) return;
    const delta = (d.dir === 'cols' ? { ArrowLeft: -20, ArrowRight: 20 } : { ArrowUp: -20, ArrowDown: 20 } as Record<string, number>)[e.key];
    if (!delta) return;
    e.preventDefault();
    ws.divider(d.groupId, d.index, dragShare(d, delta), true);
  }
</script>

<div class="stage" bind:this={stage} role="tabpanel" aria-label={tab ? `${ws.fullTitle(tab.root)} layout` : 'Workspace'}>
  <div class="stage-inner" bind:this={inner} style:width="{placed?.size.w ?? 0}px" style:height="{placed?.size.h ?? 0}px">
    {#if placed}
      {#each placed.groups as g (g.id)}
        {@const node = groupNode(g.id)}
        <div class="frame" class:root={!g.header} style={box(g.frame)}></div>
        {#if g.header && node}
          <div class="gin" data-grab={g.id} style={box(g.header)}>
            <span class="grip" aria-hidden="true"><Icon name="grip" /></span><Icon name="group" /><b>{ws.title(node)}</b>
            <span class="spacer"></span>
            <button type="button" class="more" data-nodrag aria-haspopup="menu" aria-label="Group actions for {ws.fullTitle(node)}" title="Group actions for {ws.fullTitle(node)}"
              onclick={(e) => (ws.menu = { anchor: e.currentTarget, items: [{ heading: ws.fullTitle(node) }, { label: 'Move group…', run: () => (ws.moving = g.id) }, { label: 'Equalize sizes', run: () => ws.equalize(g.id) }] })}><Icon name="more" /></button>
          </div>
        {/if}
      {/each}
      {#each placed.dividers as d (`${d.groupId}:${d.index}`)}
        <!-- svelte-ignore a11y_no_noninteractive_tabindex, a11y_no_noninteractive_element_interactions -->
        <div class="divider {d.dir}" role="separator" tabindex="0" aria-label="Resize adjacent panes" aria-orientation={d.dir === 'cols' ? 'vertical' : 'horizontal'}
          aria-valuenow={Math.round((100 * d.sizes[0]) / (d.sizes[0] + d.sizes[1]))} aria-valuemin="0" aria-valuemax="100"
          style={box(d.rect)} onpointerdown={(e) => dividerDown(e, d)} onkeydown={(e) => dividerKey(e, d)}></div>
      {/each}
    {/if}
    {#each marks as m (m.key)}
      <div class="dropmark {m.edge}" class:whole={m.whole} class:active={m.active} class:off={m.off} style={box(m.rect)}><Icon name="chevron" /></div>
    {/each}
    {#if landing}
      {#if landing.joins}<div class="groupmark" style={box(landing.joins.rect)}><span><Icon name="group" />{landing.joins.label}</span></div>{/if}
      <div class="landing" style={box(landing.rect)}><span>{landing.title}</span></div>
    {/if}
    {#each panes as { pane, multi } (pane.id)}
      <PaneView {ws} {pane} {multi} rect={rects.get(pane.id)} solo={!!visible && !isGroup(visible)} />
    {/each}
  </div>
  {#if !tab}
    <div class="empty">
      <p>No tabs are open in this sandbox.</p>
      <div class="choices">
        <button type="button" class="primary" onclick={() => ws.addTab('session')}>Open a session</button>
        <button type="button" onclick={() => ws.addTab('monitor')}>Open Monitor</button>
      </div>
    </div>
  {/if}
  {#if blocked}<div class="dropnote">{blocked}</div>{/if}
</div>
