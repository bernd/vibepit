<script lang="ts">
  import { find } from '../core/workspace/layout.ts';
  import type { MoveOption } from '../core/workspace/options.ts';
  import type { Workspace } from './state.svelte.ts';
  import Diagram from './Diagram.svelte';
  import Icon from './Icon.svelte';

  // The keyboard path for docking. It shows every distinct result of the
  // move as a small diagram, and choosing one commits it. The results are
  // the same ones a drag can reach.
  let { ws }: { ws: Workspace } = $props();
  let dialog = $state<HTMLDialogElement | null>(null);
  let grid = $state<HTMLElement | null>(null);
  let shownGroup = $state('');

  const source = $derived.by(() => {
    const tab = ws.moving ? ws.layout.tabs.find((t) => find(t.root, ws.moving as string)) : undefined;
    const node = tab && find(tab.root, ws.moving as string);
    return tab && node ? { tab, node } : null;
  });
  const choices = $derived(source ? ws.choices(source.node.id) : { options: [], blocked: 0 });
  // One group per destination: the source's own tab, each other tab, and the tab bar.
  const groups = $derived.by(() => {
    const out: { key: string; label: string; options: MoveOption[] }[] = [];
    for (const o of choices.options) {
      const key = o.tab ?? 'bar';
      let g = out.find((x) => x.key === key);
      if (!g) {
        const tab = ws.layout.tabs.find((t) => t.id === o.tab);
        g = { key, label: !tab ? 'Tab bar' : tab === source?.tab ? 'This tab' : ws.title(tab.root), options: [] };
        out.push(g);
      }
      g.options.push(o);
    }
    return out;
  });
  const current = $derived(groups.find((g) => g.key === shownGroup) ?? groups[0]);

  $effect(() => {
    if (ws.moving && dialog && !dialog.open) {
      shownGroup = groups[0]?.key ?? '';
      dialog.showModal();
      grid?.querySelector<HTMLElement>('button')?.focus();
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
  function pick(o: MoveOption) {
    done();
    ws.commit(o.layout);
  }
  function arrows(e: KeyboardEvent) {
    const step = { ArrowRight: 1, ArrowDown: 1, ArrowLeft: -1, ArrowUp: -1 }[e.key];
    if (!step || !grid) return;
    const items = [...grid.querySelectorAll<HTMLElement>('button')];
    const i = items.indexOf(document.activeElement as HTMLElement);
    if (i < 0) return;
    e.preventDefault();
    items[(i + step + items.length) % items.length].focus();
  }
  const resultRoot = (o: MoveOption) => o.layout.tabs.find((t) => t.id === o.layout.active)?.root;
</script>

<dialog class="movedialog" bind:this={dialog} {onclose} aria-labelledby="move-title">
  {#if source}
    <div class="dialoghead">
      <h2 id="move-title">Move {ws.title(source.node)}</h2>
      <button type="button" class="icon" aria-label="Close dialog" onclick={done}><Icon name="close" /></button>
    </div>
    {#if groups.length > 1}
      <div class="chips" role="group" aria-label="Destination">
        {#each groups as g (g.key)}
          <button type="button" aria-pressed={g === current} onclick={() => (shownGroup = g.key)}>{g.label}</button>
        {/each}
      </div>
    {/if}
    {#if current}
      <!-- svelte-ignore a11y_no_static_element_interactions -->
      <div class="options" bind:this={grid} onkeydown={arrows}>
        {#each current.options as o (o.label + JSON.stringify(o.dest))}
          {@const root = resultRoot(o)}
          <button type="button" class="option" onclick={() => pick(o)}>
            <span class="thumb">
              {#if o.tab && root}
                <Diagram {ws} node={root} incoming={source.node.id} />
              {:else}
                <span class="tabsketch">
                  {#each o.layout.tabs as t (t.id)}<span class:incoming={t.id === o.layout.active}>{ws.title(t.root)}</span>{/each}
                </span>
              {/if}
            </span>
            <span class="optionlabel">{o.label}</span>
          </button>
        {/each}
      </div>
    {:else}
      <p class="note">There is no other position for it.</p>
    {/if}
    {#if choices.blocked}
      <p class="note">{choices.blocked} more {choices.blocked === 1 ? 'position needs' : 'positions need'} a larger window.</p>
    {/if}
  {/if}
</dialog>
