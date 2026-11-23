<script lang="ts">
  import { isGroup } from '../core/workspace/layout.ts';
  import type { Tab } from '../core/workspace/layout.ts';
  import type { MenuEntry, Workspace } from './state.svelte.ts';
  import Icon from './Icon.svelte';

  let { ws }: { ws: Workspace } = $props();

  const gap = $derived(ws.drag?.dest && 'gap' in ws.drag.dest ? ws.drag.dest.gap : -1);
  // The tab whose workspace is shown while dragging.
  const revealed = $derived(ws.drag?.reveal);

  function tabMenu(tab: Tab, anchor: HTMLElement) {
    const items: MenuEntry[] = [{ heading: ws.fullTitle(tab.root) }, { label: 'Move tab…', run: () => (ws.moving = tab.root.id) }];
    if (isGroup(tab.root)) items.push({ label: 'Equalize sizes', run: () => ws.equalize(tab.root.id) });
    items.push('separator', { label: 'Close tab', run: () => ws.closeNode(tab.root.id) });
    ws.menu = { anchor, items };
  }
  function typeMenu(anchor: HTMLElement) {
    ws.menu = { anchor, items: [
      { heading: 'New tab' },
      { label: 'Session', run: () => ws.addTab('session') },
      { label: 'Monitor', run: () => ws.addTab('monitor') },
      { label: 'Logs', run: () => ws.addTab('logs') },
    ] };
  }
</script>

<nav class="tabbar" aria-label="Workspace tabs">
  <div class="strip" role="tablist" aria-label="Tabs">
    {#each ws.layout.tabs as tab, i (tab.id)}
      {@const on = tab.id === ws.layout.active}
      {@const full = ws.fullTitle(tab.root)}
      <div class="tab" class:on class:reveal={tab.id === revealed} class:ins={gap === i} class:endins={gap === ws.layout.tabs.length && i === gap - 1} data-tab={tab.id}>
        <button type="button" class="tab-name" role="tab" aria-selected={on} title={full} aria-label={full} data-grab={tab.root.id} onclick={() => ws.selectTab(tab.id)}>
          <Icon name={isGroup(tab.root) ? 'group' : tab.root.type} /><span class="tablabel">{ws.title(tab.root)}</span>
        </button>
        <button type="button" class="tabmenu" aria-haspopup="menu" aria-label="Tab actions for {full}" title="Tab actions for {full}" onclick={(e) => tabMenu(tab, e.currentTarget)}><Icon name="more" /></button>
      </div>
    {/each}
  </div>
  <button type="button" class="icon" aria-label="New Session tab" title="New Session tab" onclick={() => ws.addTab('session')}><Icon name="plus" /></button>
  <button type="button" class="icon" aria-haspopup="menu" aria-label="Open another kind of tab" title="Open another kind of tab" onclick={(e) => typeMenu(e.currentTarget)}><Icon name="chevron" /></button>
</nav>
