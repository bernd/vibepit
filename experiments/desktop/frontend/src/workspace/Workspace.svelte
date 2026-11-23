<script lang="ts">
  import { attachDocking } from './docking.ts';
  import { Workspace } from './state.svelte.ts';
  import Menu from './Menu.svelte';
  import MoveDialog from './MoveDialog.svelte';
  import Stage from './Stage.svelte';
  import TabBar from './TabBar.svelte';

  const ws = new Workspace();
  let inner = $state<HTMLElement | null>(null);

  $effect(() => attachDocking(ws, () => inner));

  // A refusal stays until the next action.
  const clear = () => { if (ws.message) ws.message = ''; };
</script>

<svelte:document onpointerdowncapture={clear} onkeydowncapture={clear} />

<div class="workspace">
  <header class="appbar">
    <div class="brand">
      <svg width="28" height="28" viewBox="0 0 32 32" aria-hidden="true"><rect width="32" height="32" rx="7" fill="#d4e7a8" /><path d="m5 9 11 6 11-6M5 15l11 6 11-6M9 21l7 4 7-4" fill="none" stroke="#243223" stroke-width="2.4" stroke-linejoin="round" /></svg>
      <span>Vibepit</span>
    </div>
    <span class="slash" aria-hidden="true">/</span>
    <div class="sandbox"><b>vibepit</b><span class="path">~/projects/vibepit</span></div>
  </header>
  <TabBar {ws} />
  <Stage {ws} bind:inner />
  <footer>
    <span>Workspace playground · simulated sandbox</span>
    <span class="footdiv"></span>
    <button type="button" class="footbutton" onclick={() => ws.reset()}>Restore example tabs</button>
  </footer>
  <Menu {ws} />
  <MoveDialog {ws} />
  <div class="toast" role="status">{ws.message}</div>
</div>
