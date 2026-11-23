<script lang="ts">
  import type { Pane, Rect } from '../core/workspace/layout.ts';
  import type { MenuEntry, Workspace } from './state.svelte.ts';
  import Icon from './Icon.svelte';

  // One view. It stays mounted while the layout around it changes, so its
  // content keeps its state. `rect` is undefined while the pane is not shown.
  let { ws, pane, rect, multi, solo }: { ws: Workspace; pane: Pane; rect: Rect | undefined; multi: boolean; solo: boolean } = $props();

  const title = $derived(ws.paneTitle(pane));
  const focused = $derived(ws.active?.focus === pane.id);
  const zoomed = $derived(ws.zoomed === pane.id && !ws.drag);

  function paneMenu(anchor: HTMLElement) {
    const items: MenuEntry[] = [
      { heading: title },
      { label: 'Split right', run: () => ws.splitPane(pane.id, 'cols') },
      { label: 'Split below', run: () => ws.splitPane(pane.id, 'rows') },
      'separator',
      { label: 'Move pane…', run: () => (ws.moving = pane.id) },
    ];
    if (multi) items.push({ label: 'Move to new tab', run: () => ws.move(pane.id, { gap: ws.layout.tabs.length }) });
    items.push('separator', { label: 'Close pane', run: () => ws.closeNode(pane.id) });
    ws.menu = { anchor, items };
  }
</script>

<section
  id="pane-{pane.id}" class="pane" class:focus={focused} class:solo role="group" aria-label={title} hidden={!rect}
  style:left="{rect?.x ?? 0}px" style:top="{rect?.y ?? 0}px" style:width="{rect?.w ?? 0}px" style:height="{rect?.h ?? 0}px"
  onfocusin={() => ws.focus(pane.id)} onpointerdowncapture={() => ws.focus(pane.id)}
>
  <div class="phead" data-grab={pane.id}>
    <span class="grip" aria-hidden="true"><Icon name="grip" /></span><Icon name={pane.type} /><strong>{title}</strong>
    <span class="spacer"></span>
    {#if multi}
      <button type="button" class="zoom-toggle" data-nodrag aria-label="{zoomed ? 'Restore layout' : 'Zoom pane'} · {title}" onclick={() => ws.toggleZoom(pane.id)}>{zoomed ? 'Restore layout' : 'Zoom'}</button>
    {/if}
    <button type="button" class="more" data-nodrag data-primary={pane.type === 'session' ? undefined : ''} aria-haspopup="menu" aria-label="Pane actions for {title}" title="Pane actions for {title}" onclick={(e) => paneMenu(e.currentTarget)}><Icon name="more" /></button>
  </div>
  <div class="body">
    {#if pane.type === 'session'}
      <div class="scroll terminal">
        <div class="mut">~/projects/vibepit  ·  experiment/desktop</div>
        <div class="mut">Placeholder for {title}. The terminal is added in a later stage.</div>
      </div>
      <form class="prompt" onsubmit={(e) => e.preventDefault()}>
        <span aria-hidden="true">❯</span>
        <input data-primary aria-label="Command in {title}" placeholder="Type to leave a draft" autocomplete="off" spellcheck="false" bind:value={() => ws.drafts[pane.id] ?? '', (v) => (ws.drafts[pane.id] = v)} />
      </form>
    {:else}
      <div class="scroll">
        <p class="mut">{pane.type === 'monitor' ? 'Held connections, logs, hosts and rules are added in the next stage.' : 'The network event log is added in the next stage.'}</p>
      </div>
    {/if}
  </div>
</section>
