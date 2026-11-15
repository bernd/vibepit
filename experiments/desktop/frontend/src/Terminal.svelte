<script lang="ts">
  import { onMount, tick } from 'svelte';
  import { WTerm, type SearchState, type ShellIntegrationState } from '@wterm/dom';
  import { GhosttyCore } from '@wterm/ghostty';
  import ghosttyWasm from '@wterm/ghostty/ghostty-vt.wasm?url';
  import '@wterm/dom/src/terminal.css';
  import { api, subscribe } from './bridge';
  import type { TerminalEditor } from './editing';
  import { terminalBytes, terminalPaste } from './terminal-input';
  import { workingDirectory, shellStatus } from './terminal-tools';

  let { connection, active, onerror, onexit, oneditor, onselection }: {
    connection: string; active: boolean; onerror: (error: unknown) => void; onexit: (message: string) => void;
    oneditor: (editor: TerminalEditor | undefined) => void; onselection: (text: string) => void;
  } = $props();
  let host: HTMLDivElement;
  let ready = $state(false);
  let terminal = $state.raw<WTerm>();
  let windowTitle = $state('');
  let directory = $state('');
  let shell = $state<ShellIntegrationState>({ phase: 'unknown', exitCode: null });
  let shellExited = $state(false);
  let finding = $state(false);
  let query = $state('');
  let caseSensitive = $state(false);
  let search = $state<SearchState>({ query: '', caseSensitive: false, count: 0, activeIndex: -1, searching: false, limited: false });
  let searchField = $state<HTMLInputElement>();
  let selectionButton: HTMLButtonElement;
  let capturedSelection: string | null = null;
  let selectionHint = $state('');
  const status = $derived(shellExited ? 'Shell exited' : shellStatus(shell));
  const matches = $derived(!query ? 'Search scrollback' : search.searching ? 'Searching…' : !search.count ? 'No matches' : `${search.activeIndex + 1} of ${search.count}${search.limited ? '+' : ''}`);

  $effect(() => { terminal?.setRenderingPaused(!active); });
  $effect(() => {
    if (terminal) {
      if (finding) terminal.search(query.slice(0, 1024), { caseSensitive });
      else terminal.clearSearch();
    }
  });

  async function find() {
    if (!ready || !active) return;
    finding = true;
    await tick();
    searchField?.focus(); searchField?.select();
  }
  function closeFind() { finding = false; terminal?.focus(); }
  function findKey(event: KeyboardEvent) {
    if (event.isComposing) return;
    if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); closeFind(); }
    if (event.key === 'Enter') {
      event.preventDefault();
      if (event.shiftKey) terminal?.findPrevious(); else terminal?.findNext();
    }
  }
  function shortcut(event: KeyboardEvent) {
    const mac = /Mac|iPhone|iPad/.test(navigator.platform);
    if (!active || !ready || event.defaultPrevented || event.isComposing || event.altKey) return;
    if (finding && event.key === 'Escape' && event.target instanceof Element && event.target.closest('.terminal-find')) {
      event.preventDefault(); event.stopImmediatePropagation(); closeFind(); return;
    }
    if (event.key.toLowerCase() !== 'f' || !(mac ? event.metaKey && !event.ctrlKey : event.ctrlKey && event.shiftKey && !event.metaKey)) return;
    event.preventDefault(); event.stopImmediatePropagation(); void find();
  }
  function captureSelection(event: PointerEvent) {
    // Read before wterm's document pointer handler clears its history selection.
    if (event.target instanceof Node && selectionButton?.contains(event.target)) {
      capturedSelection = terminal?.getSelectionText() ?? null;
      event.preventDefault();
    } else capturedSelection = null;
  }
  function sendSelection() {
    const text = capturedSelection ?? terminal?.getSelectionText() ?? '';
    capturedSelection = null;
    if (!text) { selectionHint = 'Select terminal output first.'; return; }
    selectionHint = '';
    onselection(text);
  }

  onMount(() => {
    const token = connection;
    let disposed = false;
    let exited = false;
    let term: WTerm | undefined;
    let core: GhosttyCore | undefined;
    let unsubscribe = () => {};
    // Serialize input and resize RPCs: transport completion order is not a PTY contract.
    let input = Promise.resolve();
    let resize = Promise.resolve();
    let opening = Promise.resolve();
    const report = (error: unknown) => { if (!disposed) onerror(error); };
    const send = (data: string | Uint8Array) => {
      const encoded = typeof data === 'string' ? data : terminalBytes(data);
      input = input.then(() => opening).then(() => {
        if (disposed || exited) return;
        return typeof data === 'string' ? api.writeTerminal(token, encoded) : api.writeTerminalBytes(token, encoded);
      }).catch(report);
    };
    const cleanup = () => {
      unsubscribe();
      term?.destroy();
      term = undefined;
      terminal = undefined;
      // WTerm leaves caller-supplied cores alive; each connection owns its instance.
      core?.dispose();
      core = undefined;
    };
    void (async () => {
      core = await GhosttyCore.load({ wasmPath: ghosttyWasm, scrollbackLines: 5000 });
      if (disposed) { cleanup(); return; }
      term = new WTerm(host, {
        core, cursorBlink: true, renderingPaused: !active,
        onData: send, onBinary: send,
        onTitle: title => { windowTitle = title; },
        onWorkingDirectory: uri => { directory = workingDirectory(uri); },
        onShellIntegration: state => { shell = state; },
        onSearchChange: state => { search = state; },
      });
      await term.init();
      if (disposed) return;
      terminal = term;
      unsubscribe = subscribe(event => {
        if (event.connection !== token) return;
        if (event.kind === 'terminal') {
          const bytes = Uint8Array.from(atob(event.data), c => c.charCodeAt(0));
          term?.write(bytes);
        }
        if (event.kind === 'terminal-exit') { exited = true; shellExited = true; onexit(event.data); }
      });
      const { cols, rows } = term;
      opening = api.openTerminal(token, cols, rows);
      await opening;
      if (disposed) return;
      term.onResize = (cols, rows) => {
        // Hidden panes can briefly measure one column; the SSH PTY requires two.
        if (cols < 2 || rows < 1) return;
        resize = resize.then(() => {
          if (!disposed && !exited) return api.resizeTerminal(token, cols, rows);
        }).catch(report);
      };
      // The window can resize while the SSH PTY is opening.
      if (term.cols !== cols || term.rows !== rows) term.onResize(term.cols, term.rows);
      oneditor({
        focused: () => {
          if (!host.getClientRects().length) return false;
          const active = document.activeElement;
          // wterm blurs its textarea when selecting words or logical lines.
          return host.contains(active) ||
            ((!active || active === document.body) && term?.getSelectionText() != null);
        },
        alive: () => !disposed && !exited,
        getSelection: () => term?.getSelectionText() ?? '',
        selectAll: () => { void term?.selectAll().catch(report); },
        paste: text => {
          if (!text || disposed || exited) return;
          term?.clearSelection();
          term?.focus();
          host.scrollTop = host.scrollHeight;
          send(terminalPaste(text, core?.bracketedPaste() ?? false));
        },
        focus: () => term?.focus(),
      });
      ready = true;
      if (active) term.focus();
    })().catch(error => { exited = true; cleanup(); report(error); });
    return () => { disposed = true; oneditor(undefined); cleanup(); };
  });
</script>

<svelte:window onkeydowncapture={shortcut} onpointerdowncapture={captureSelection} />

<div class="terminal-surface">
  <div class="terminal-tools">
    <div class="shell-context">
      {#if windowTitle}<span class="terminal-title" title={windowTitle} aria-label="Terminal window title" dir="auto">{windowTitle}</span>{/if}
      <div class="shell-details">
        <span class={['shell-status', { failed: !!shell.exitCode && shell.phase !== 'running' }]} title="Reported by the shell. Existing sessions may need shell integration enabled.">{status}</span>
        <span class="working-directory" title={directory ? `Shell-reported directory: ${directory}` : 'The shell has not reported its current directory.'}>{directory || 'Directory unavailable'}</span>
      </div>
    </div>
    <div class="terminal-actions">
      <button onclick={() => void find()} disabled={!ready} aria-expanded={finding} title="Find in terminal (Ctrl+Shift+F / ⌘F)">Find</button>
      <button bind:this={selectionButton} onclick={sendSelection} disabled={!ready} title="Append selected output to the Claude draft">Send selection to Claude</button>
    </div>
  </div>
  {#if finding}
    <div class="terminal-find" role="search" aria-label="Find in terminal">
      <input bind:this={searchField} bind:value={query} onkeydown={findKey} maxlength="1024" aria-label="Find terminal output" placeholder="Find in terminal" />
      <button class={['match-case', { enabled: caseSensitive }]} aria-pressed={caseSensitive} onclick={() => caseSensitive = !caseSensitive} aria-label="Match case" title="Match case">Aa</button>
      <span class="match-count" role="status">{matches}</span>
      <button onclick={() => terminal?.findPrevious()} disabled={!search.count} aria-label="Previous match" title="Previous match (Shift+Enter)">Previous</button>
      <button onclick={() => terminal?.findNext()} disabled={!search.count} aria-label="Next match" title="Next match (Enter)">Next</button>
      <button onclick={closeFind} aria-label="Close terminal search" title="Close search (Escape)">Close</button>
    </div>
  {/if}
  {#if selectionHint}<p class="selection-hint" role="status">{selectionHint}</p>{/if}
  <div class="terminal-viewport">
    <div class="terminal-host" bind:this={host} aria-label="Sandbox terminal"></div>
    {#if !ready}<p class="terminal-loading" role="status">Opening terminal…</p>{/if}
  </div>
</div>

<style>
  .terminal-surface { height: 100%; display: flex; flex-direction: column; min-width: 0; }
  .terminal-tools { display: flex; flex-wrap: wrap; align-items: center; gap: 10px 18px; padding: 10px 18px; border-bottom: 1px solid var(--line); }
  .shell-context { display: flex; flex: 1; flex-direction: column; gap: 4px; min-width: 160px; overflow: hidden; font-size: 12px; }
  .terminal-title { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 600; }
  .shell-details { display: flex; align-items: center; gap: 16px; min-width: 0; }
  .shell-status { flex-shrink: 0; color: var(--muted); }
  .shell-status.failed { color: #edbcad; }
  .working-directory { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-family: 'DejaVu Sans Mono', 'SFMono-Regular', Consolas, monospace; }
  .terminal-actions { display: flex; gap: 8px; }
  .terminal-tools button, .terminal-find button { font-size: 12px; padding: 6px 9px; }
  .terminal-find { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; padding: 10px 18px; border-bottom: 1px solid var(--line); background: var(--panel); }
  .terminal-find input { flex: 1; min-width: 130px; width: 180px; padding: 6px 9px; }
  .match-case.enabled { border-color: var(--accent); color: var(--accent); }
  .match-count { color: var(--muted); font-size: 12px; font-variant-numeric: tabular-nums; min-width: 85px; }
  .selection-hint { padding: 8px 18px; color: var(--muted); font-size: 12px; }
  .terminal-viewport { position: relative; flex: 1; min-height: 0; }
  .terminal-host {
    position: absolute; inset: 18px; overflow: auto; padding: 0;
    border-radius: 0; box-shadow: none;
    --term-bg: #141b19; --term-fg: #dbe5dd; --term-cursor: #d6e8a4;
    --term-font-family: "DejaVu Sans Mono", "SFMono-Regular", Consolas, monospace;
    --term-font-size: 14px;
  }
  .terminal-loading { position: absolute; bottom: 16px; left: 24px; color: var(--muted); pointer-events: none; }
</style>
