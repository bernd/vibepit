<script lang="ts">
  import { all, find, isGroup, leaves, sameLayout } from '../core/workspace/layout.ts';
  import type { Destination, Edge } from '../core/workspace/layout.ts';
  import type { Workspace } from './state.svelte.ts';
  import Diagram from './Diagram.svelte';
  import Icon from './Icon.svelte';

  // The keyboard path for docking. It offers the same sources, targets,
  // edges and new-tab choices as a drag, and shows the same diagram.
  let { ws }: { ws: Workspace } = $props();
  let dialog = $state<HTMLDialogElement | null>(null);
  let choice = $state('');
  let edge = $state<Edge>('right');
  const edges: [Edge, string][] = [['left', 'Left'], ['top', 'Above'], ['right', 'Right'], ['bottom', 'Below']];

  const source = $derived.by(() => {
    const tab = ws.moving ? ws.layout.tabs.find((t) => find(t.root, ws.moving as string)) : undefined;
    const node = tab && find(tab.root, ws.moving as string);
    return tab && node ? { tab, node } : null;
  });

  const options = $derived.by(() => {
    if (!source) return [];
    const omit = new Set(all(source.node).map((n) => n.id));
    const out: { value: string; label: string; other: boolean }[] = [];
    ws.layout.tabs.forEach((t, i) => {
      const other = t !== source.tab;
      out.push({ value: `gap:${i}`, label: `Tab bar · before ${ws.title(t.root)}`, other: false });
      if (!omit.has(t.root.id)) out.push({ value: t.root.id, label: `Whole tab · ${ws.title(t.root)}`, other });
      if (isGroup(t.root)) for (const p of leaves(t.root)) if (!omit.has(p.id)) out.push({ value: p.id, label: `Pane · ${ws.paneTitle(p)} (${ws.title(t.root)})`, other });
    });
    out.push({ value: `gap:${ws.layout.tabs.length}`, label: 'Tab bar · at end', other: false });
    return out;
  });

  const gap = $derived(choice.startsWith('gap:'));
  const dest = $derived<Destination | null>(!choice ? null : gap ? { gap: Number(choice.slice(4)) } : { target: choice, edge });
  const preview = $derived(source && dest ? ws.preview(source.node.id, dest) : { layout: null, error: '' });
  const unchanged = $derived(!!preview.layout && sameLayout(preview.layout, ws.layout));
  const shown = $derived(preview.layout?.tabs.find((t) => t.id === preview.layout?.active)?.root);

  $effect(() => {
    if (ws.moving && dialog && !dialog.open) {
      choice = (options.find((o) => o.other) ?? options.at(-1))?.value ?? '';
      edge = 'right';
      dialog.showModal();
    }
  });

  // The close event arrives later, so the source is cleared here. Otherwise a
  // layout change in between would reopen the dialog.
  function done() {
    ws.moving = null;
    if (dialog?.open) dialog.close();
  }
  function onclose() {
    ws.moving = null;
    // The dialog returns focus to its opener. If that element is gone, focus goes to the active pane.
    setTimeout(() => { if (document.activeElement === document.body || !document.activeElement) void ws.focusPane(); }, 0);
  }
  function apply() {
    const next = preview.layout;
    done();
    if (next) ws.commit(next);
  }
</script>

<dialog bind:this={dialog} {onclose} aria-labelledby="move-title">
  {#if source}
    <div class="dialoghead">
      <h2 id="move-title">Move {ws.title(source.node)}</h2>
      <button type="button" class="icon" aria-label="Close dialog" onclick={done}><Icon name="close" /></button>
    </div>
    <label class="formline">Destination
      <select bind:value={choice}>
        {#each options as o (o.value)}<option value={o.value}>{o.label}</option>{/each}
      </select>
    </label>
    {#if !gap}
      <div class="chips" role="group" aria-label="Edge">
        {#each edges as [side, label] (side)}
          <button type="button" aria-pressed={edge === side} onclick={() => (edge = side)}>{label}</button>
        {/each}
      </div>
    {/if}
    <div class="miniwrap">
      {#if shown}<Diagram {ws} node={shown} incoming={source.node.id} target={gap ? undefined : choice} />{/if}
    </div>
    <p class="note" aria-live="polite">
      {preview.error || (unchanged ? 'It is already there.' : gap ? 'Places the source in the tab bar at this position.' : 'The dashed outline marks the incoming pane or group.')}
    </p>
    <div class="dialogfoot">
      <button type="button" onclick={done}>Cancel</button>
      <button type="button" class="primary" disabled={!preview.layout || unchanged} onclick={apply}>Move</button>
    </div>
  {/if}
</dialog>
