<script lang="ts">
  import { tick } from 'svelte';
  import { matchingCommands, insertCommand } from './claude-stream';
  import type { Command } from './claude-stream';
  let { prompt = $bindable(''), commands, busy, exited, onsend, oninterrupt }: {
    prompt?: string; commands: Command[]; busy: boolean; exited: boolean; onsend: () => void; oninterrupt: () => void;
  } = $props();
  let field: HTMLTextAreaElement;
  let browsing = $state(false);
  let search = $state('');
  let dismissed = $state(false);
  let selected = $state(0);
  const matches = $derived(matchingCommands(commands, browsing ? '/' + search : prompt));
  const open = $derived((browsing || (!dismissed && /^\/\S*$/.test(prompt))) && !exited);
  async function choose(name: string) {
    prompt = browsing && !prompt.startsWith('/') ? `/${name} ${prompt}` : insertCommand(prompt,name); dismissed = true; browsing = false;
    await tick(); field?.focus(); field?.setSelectionRange(prompt.length,prompt.length);
  }
  function keydown(event: KeyboardEvent) {
    if(event.isComposing) return;
    if(event.key === 'Enter' && (event.metaKey || event.ctrlKey)) { event.preventDefault(); onsend(); return; }
    if(!open) return;
    if(event.key === 'Escape') { event.preventDefault(); dismissed=true; browsing=false; }
    if((event.key === 'ArrowDown' || event.key === 'ArrowUp') && matches.length) {
      event.preventDefault(); selected=(selected + (event.key === 'ArrowDown'?1:-1) + matches.length)%matches.length;
      void tick().then(()=>document.getElementById(`command-${selected}`)?.scrollIntoView({block:'nearest'}));
    }
    if((event.key === 'Enter' || event.key === 'Tab') && matches.length) { event.preventDefault(); void choose(matches[Math.min(selected,matches.length-1)].name); }
  }
</script>
<form class="composer" onsubmit={event => { event.preventDefault(); onsend(); }}>
  <div class="composer-label"><label for="prompt">Message Claude</label><button type="button" class="command-toggle" disabled={exited} onclick={() => { browsing=!browsing; search=''; selected=0; }}>Commands ({commands.length})</button></div>
  {#if open}
    <div class="command-menu" aria-label="Available Claude commands">
      <div class="command-help">Choose a command, add arguments, then send. ↑ ↓ to browse · Enter to insert · Esc to close</div>
      {#if browsing}<label class="command-search">Search commands<input aria-label="Search commands" bind:value={search} oninput={() => selected=0} onkeydown={keydown} /></label>{/if}
      <p class="sr-only" aria-live="polite">{matches.length ? `Selected command: /${matches[Math.min(selected, matches.length - 1)].name}` : 'No matching commands'}</p>
      <div class="command-options" id="command-options">
        {#each matches as command, index (command.name)}
          <button type="button" id={`command-${index}`} class:selected={index === selected} onclick={() => choose(command.name)}>
            <strong>/{command.name}</strong><span>{command.description || 'Available in this Claude session'}</span>
          </button>
        {:else}<p>{commands.length ? 'No matching commands.' : 'No commands advertised yet. Start Claude to load available commands.'}</p>{/each}
      </div>
    </div>
  {/if}
  <textarea id="prompt" bind:this={field} bind:value={prompt} oninput={() => { dismissed=false; selected=0; }} placeholder="Ask Claude, or type / for commands" rows="3" disabled={exited} onkeydown={keydown} aria-controls={open ? 'command-options' : undefined}></textarea>
  <div class="composer-footer"><span>Ctrl / ⌘ + Enter to send</span>{#if busy}<button type="button" onclick={oninterrupt}>Interrupt</button>{/if}<button class="primary" disabled={busy || !prompt.trim() || exited}>Send message</button></div>
</form>
<style>
  .sr-only { position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); }
  .composer { position: relative; }
  .composer-label { display: flex; align-items: center; justify-content: space-between; margin-bottom: 8px; }
  .composer-label label { margin: 0; }
  .command-toggle { font-size: 11px; padding: 3px 8px; background: transparent; color: var(--muted); }
  .command-menu { position: absolute; bottom: calc(100% - 16px); left: 24px; right: 24px; z-index: 3; background: var(--panel); border: 1px solid var(--line); border-radius: 5px; box-shadow: 0 8px 24px #0005; }
  .command-help { padding: 10px 12px; font-size: 11px; color: var(--muted); border-bottom: 1px solid var(--line); }
  .command-search { display: block; padding: 8px 12px; font-size: 11px; }
  .command-search input { display: block; width: 100%; margin-top: 5px; padding: 7px; }
  .command-options { max-height: 250px; overflow: auto; padding: 5px; }
  .command-options button { display: block; width: 100%; text-align: left; background: transparent; border-color: transparent; padding: 9px; }
  .command-options button.selected { background: #ffffff0c; border-color: var(--line); }
  .command-options strong { display: block; color: var(--accent); font-size: 12px; font-weight: 500; }
  .command-options span, .command-options p { display: block; color: var(--muted); font-size: 11px; margin-top: 4px; line-height: 1.5; overflow-wrap: anywhere; }
</style>
