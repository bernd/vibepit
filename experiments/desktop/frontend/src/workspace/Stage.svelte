<script lang="ts">
  import { dragShare, place } from '../core/workspace/geometry.ts';
  import type { DividerBox } from '../core/workspace/geometry.ts';
  import { find, isGroup, leaves } from '../core/workspace/layout.ts';
  import type { Rect } from '../core/workspace/layout.ts';
  import type { Workspace } from './state.svelte.ts';
  import Diagram from './Diagram.svelte';
  import Icon from './Icon.svelte';
  import PaneView from './PaneView.svelte';

  let { ws, inner = $bindable(null) }: { ws: Workspace; inner?: HTMLElement | null } = $props();
  let stage = $state<HTMLElement | null>(null);

  const tab = $derived(ws.active);
  // A drag previews the full layout, so zoom is ignored while one is in progress.
  const visible = $derived(tab ? (ws.zoomed && !ws.drag && find(tab.root, ws.zoomed)) || tab.root : null);
  const placed = $derived(visible && ws.space.w > 0 ? place(visible, ws.space) : null);
  const rects = $derived(new Map<string, Rect>(placed?.panes.map((p) => [p.id, p.rect])));
  // Every pane of every tab stays mounted, keyed by its id. Panes of
  // background tabs are hidden, and a move never remounts a pane.
  const panes = $derived(ws.layout.tabs.flatMap((t) => leaves(t.root).map((pane) => ({ pane, multi: isGroup(t.root) }))));
  const preview = $derived.by(() => {
    const d = ws.drag;
    if (!d?.dest || !('target' in d.dest)) return null;
    return { root: d.result?.tabs.find((t) => t.id === d.result?.active)?.root, error: d.error, src: d.src, target: d.dest.target };
  });

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
  {#if preview}
    <div class="projection" class:invalid={!preview.root}>
      {#if preview.root}<Diagram {ws} node={preview.root} incoming={preview.src} target={preview.target} />{:else}{preview.error}{/if}
    </div>
  {/if}
</div>
