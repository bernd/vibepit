<script lang="ts">
  import { tick } from 'svelte';
  import type { MenuEntry, Workspace } from './state.svelte.ts';

  let { ws }: { ws: Workspace } = $props();
  let pop = $state<HTMLElement | null>(null);
  let position = $state({ left: 0, top: 0 });

  $effect(() => {
    const menu = ws.menu;
    if (!menu) return;
    void tick().then(() => {
      if (!pop) return;
      const r = menu.anchor.getBoundingClientRect();
      position = {
        left: Math.max(4, Math.min(window.innerWidth - pop.offsetWidth - 5, r.left)),
        top: Math.max(4, Math.min(window.innerHeight - pop.offsetHeight - 5, r.bottom + 2)),
      };
      pop.querySelector<HTMLElement>('button')?.focus();
    });
  });

  // A menu returns focus to the control that opened it.
  function closeMenu(restore: boolean) {
    const anchor = ws.menu?.anchor;
    ws.menu = null;
    if (restore && anchor?.isConnected) anchor.focus({ preventScroll: true });
  }
  function choose(item: Extract<MenuEntry, { label: string }>) {
    closeMenu(true);
    item.run();
  }
  function onkeydown(e: KeyboardEvent) {
    const items = [...(pop?.querySelectorAll<HTMLElement>('button') ?? [])];
    const i = items.indexOf(document.activeElement as HTMLElement);
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') { e.preventDefault(); items[(i + (e.key === 'ArrowDown' ? 1 : items.length - 1)) % items.length]?.focus(); }
    else if (e.key === 'Home' || e.key === 'End') { e.preventDefault(); items.at(e.key === 'Home' ? 0 : -1)?.focus(); }
    else if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); closeMenu(true); }
    else if (e.key === 'Tab') closeMenu(true);
  }
  function outside(e: PointerEvent) {
    if (ws.menu && pop && !pop.contains(e.target as globalThis.Node) && !ws.menu.anchor.contains(e.target as globalThis.Node)) closeMenu(false);
  }
</script>

<svelte:document onpointerdowncapture={outside} />

{#if ws.menu}
  <div class="pop" role="menu" tabindex="-1" bind:this={pop} style:left="{position.left}px" style:top="{position.top}px" {onkeydown}>
    {#each ws.menu.items as item, i (i)}
      {#if item === 'separator'}
        <hr />
      {:else if 'heading' in item}
        <div class="poplabel">{item.heading}</div>
      {:else}
        <button type="button" role="menuitem" class:danger={item.danger} onclick={() => choose(item)}>{item.label}</button>
      {/if}
    {/each}
  </div>
{/if}
