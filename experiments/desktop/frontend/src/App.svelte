<script lang="ts">
  import { onMount, tick } from 'svelte';
  import Terminal from './Terminal.svelte';
  import ClaudeSettings from './ClaudeSettings.svelte';
  import ClaudeUsage from './ClaudeUsage.svelte';
  import ToolActivity from './ToolActivity.svelte';
  import TurnDetails from './TurnDetails.svelte';
  import Markdown from './Markdown.svelte';
  import ClaudeComposer from './ClaudeComposer.svelte';
  import { createConversation, beginTurn, applyFrame, endProgress, phase } from './claude-stream';
  import ClaudeDebug from './ClaudeDebug.svelte';
  import DesktopControls from './DesktopControls.svelte';
  import { api, initializeWorkspace, subscribe, subscribeCommands } from './bridge';
  import { createEditor, shortcutCommand } from './editing';
  import type { DesktopCommand, EditCommand, TerminalEditor } from './editing';
  import { appendTerminalSelection } from './terminal-tools';
  import type { ClaudeConfig, AgentDebugMessage, AgentFrame, BackendEvent, NetworkEntry, Sandbox, StreamLogStatus, ToolRequest, WindowInfo } from './bridge';


  let workspace = $state<WindowInfo | null>(null);
  let initializing = $state(true);
  let needsRecovery = $state(false);
  let sandboxes = $state<Sandbox[]>([]);
  let selected = $state('');
  let connection = $state('');
  let current = $state<Sandbox | null>(null);
  let working = $state(false);
  let tab = $state<'terminal' | 'agent' | 'debug'>('terminal');
  let debugMessages = $state<AgentDebugMessage[]>([]);
  let debugDropped = $state(0);
  let streamLog = $state<StreamLogStatus | null>(null);
  let debugBytes = 0;
  let error = $state('');
  let terminalExit = $state('');
  let networkStatus = $state('Disconnected');
  let holds = $state<NetworkEntry[]>([]);
  let logs = $state<string[]>([]);
  let decisions = $state<number[]>([]);
  let tools = $state<ToolRequest[]>([]);
  let toolDecisions = $state<string[]>([]);
  let claudeConfig = $state<ClaudeConfig | null>(null);
  let configuring = $state(false);
  let yolo = $state(false);
  let agentStatus = $state('Not started');
  let prompt = $state('');
  let sending = $state(false);
  let conversation = $state(createConversation());
  let diagnostics = $state<string[]>([]);
  let now = $state(Date.now());
  let transcript = $state<HTMLDivElement>();
  let terminalEditor: TerminalEditor | undefined;
  let editRevision = 0;
  const edit = createEditor({ clipboard: api.clipboard, terminal: () => terminalEditor, context: () => `${connection}:${tab}:${editRevision}` });

  function changeView(view: 'terminal' | 'agent' | 'debug') {
    editRevision++;
    tab = view;
    void tick().then(() => {
      if (tab === 'terminal') terminalEditor?.focus();
      else if (tab === 'agent') document.getElementById('prompt')?.focus();
    });
  }
  function desktopCommand(command: DesktopCommand) {
    if (command === 'view-terminal') changeView('terminal');
    else if (command === 'view-claude') changeView('agent');
    else if (command === 'view-debug') changeView('debug');
    else void edit(command).catch(fail);
  }
  function keyboard(event: KeyboardEvent) {
    // The native menu owns accelerators. Browser previews need their own fallback.
    if (workspace?.native) return;
    const command = shortcutCommand(event, /Mac|iPhone|iPad/.test(navigator.platform));
    if (!command) return;
    event.preventDefault();
    event.stopImmediatePropagation();
    desktopCommand(command);
  }
  function clipboardEvent(event: ClipboardEvent) {
    // WebKit's ordinary field shortcuts may emit clipboard events instead of menu commands.
    if (!workspace?.native || event.defaultPrevented) return;
    event.preventDefault();
    event.stopImmediatePropagation();
    void edit(event.type as EditCommand).catch(fail);
  }

  const busy = $derived(sending || configuring || agentStatus === 'Running');
  const activeHolds = $derived(holds.filter(h => new Date(h.deadline ?? 0).getTime() > now));
  const windowTitle = $derived(`${current?.project || (needsRecovery ? workspace?.project : '') || 'Vibepit'} · ${workspace?.title || 'Desktop Lab'}`);

  function fail(value: unknown) { error = value instanceof Error ? value.message : String(value); }
  function log(text: string) { logs = [text, ...logs].slice(0, 40); }
  function format(value: unknown) { return typeof value === 'string' ? value : JSON.stringify(value, null, 2); }
  function clearDebug() { debugMessages = []; debugDropped = 0; debugBytes = 0; }
  function captureDebug(message: AgentDebugMessage) {
    debugMessages.push(message);
    debugBytes += message.raw.length * 2;
    while (debugMessages.length > 1000 || debugBytes > 16 * 1024 * 1024) {
      debugBytes -= debugMessages.shift()!.raw.length * 2;
      debugDropped++;
    }
  }

  async function refresh() {
    if (!workspace || needsRecovery) return;
    working = true; error = '';
    try {
      sandboxes = await api.sessions();
      if (!sandboxes.some(s => s.id === selected)) selected = sandboxes[0]?.id ?? '';
    } catch (e) { fail(e); }
    finally { working = false; }
  }

  async function refreshWindowInfo() {
    try { workspace = await api.windowInfo(); }
    catch (e) {
      const detail = e instanceof Error ? e.message : String(e);
      error = [error, `Window details could not be refreshed: ${detail}`].filter(Boolean).join('\n');
    }
  }

  async function connect() {
    if (!workspace || needsRecovery) return;
    working = true; error = '';
    try {
      const token = await api.connect(selected);
      current = sandboxes.find(s => s.id === selected) ?? null;
      holds = []; logs = []; tools = []; conversation = createConversation(); diagnostics = []; terminalExit = '';
      clearDebug();
      streamLog = null; claudeConfig = null; configuring = false; yolo = false; sending = false;
      agentStatus = 'Not started'; networkStatus = 'Connecting'; tab = 'terminal';
      connection = token;
      await api.watchNetwork(token);
    } catch (e) { fail(e); }
    finally {
      if (connection) await refreshWindowInfo();
      working = false;
    }
  }

  async function disconnect() {
    working = true;
    try {
      await api.disconnect();
      needsRecovery = false;
      connection = ''; current = null; holds = []; tools = []; networkStatus = 'Disconnected'; endProgress(conversation, 'Interrupted');
      error = '';
      await refreshWindowInfo();
    } catch (e) { fail(e); }
    finally { working = false; }
  }

  async function recoverWorkspace() {
    await disconnect();
    if (!needsRecovery) await refresh();
  }

  async function scrollIfFollowing(follow: boolean) {
    if (!follow) return;
    await tick();
    if (transcript) transcript.scrollTop = transcript.scrollHeight;
  }

  function agentEvent(frame: AgentFrame) {
    const following = !transcript || transcript.scrollHeight - transcript.scrollTop - transcript.clientHeight < 100;
    if (frame.type === 'control_request' && frame.request?.subtype === 'can_use_tool') {
      tools = [...tools.filter(t => t.id !== frame.request_id), { id: frame.request_id!, name: frame.request.tool_name, input: frame.request.input, toolID: frame.request.tool_use_id }];
    } else if (frame.type === 'control_cancel_request') {
      tools = tools.filter(t => t.id !== frame.request_id);
    } else if (frame.type === 'result') {
      tools = [];
      if (frame.is_error) diagnostics = [...diagnostics, frame.errors?.join('\n') || frame.result || 'Claude returned an error.'].slice(-30);
    }
    applyFrame(conversation, frame);
    void scrollIfFollowing(following);
  }

  function receive(event: BackendEvent) {
    if (!connection || event.connection !== connection) return;
    switch (event.kind) {
      case 'network-status': networkStatus = event.data; break;
      case 'network-reset': holds = []; break;
      case 'network-hold':
        holds = [...holds.filter(h => h.id !== event.data.id && `${h.domain}:${h.port}` !== `${event.data.domain}:${event.data.port}`), event.data];
        break;
      case 'network-end': {
        const held = holds.find(h => h.id === event.data.id);
        log(`${held?.domain ?? 'Request'} · ${event.data.outcome}`);
        holds = holds.filter(h => h.id !== event.data.id);
        break;
      }
      case 'network-log': log(`${event.data.domain}${event.data.port ? ':' + event.data.port : ''} · ${event.data.action}${event.data.reason ? ' · ' + event.data.reason : ''}`); break;
      case 'agent-config': claudeConfig = event.data; break;
      case 'agent-event': agentEvent(event.data); break;
      case 'agent-debug': captureDebug(event.data); break;
      case 'agent-log-status':
        if (streamLog?.path !== event.data.path) clearDebug();
        streamLog = event.data;
        if (event.data.error) fail(event.data.error);
        break;
      case 'agent-status': agentStatus = event.data; if (event.data === 'Exited') { tools = []; endProgress(conversation, 'Interrupted'); } break;
      case 'agent-error': diagnostics = [...diagnostics, event.data].slice(-30); break;
    }
  }

  async function decideNetwork(id: number, allow: boolean) {
    decisions.push(id);
    try { await api.decideNetwork(connection, id, allow); }
    catch (e) { fail(e); }
    finally { decisions = decisions.filter(n => n !== id); }
  }

  async function decideTool(id: string, allow: boolean) {
    toolDecisions.push(id);
    try {
      const token = connection;
      const toolID = tools.find(t => t.id === id)?.toolID;
      await api.decideTool(token, id, allow);
      if (connection !== token) return;
      const row = conversation.messages.find(m => m.id === toolID);
      if (row?.tool?.state === 'Waiting for approval') row.tool.state = allow ? 'Running' : 'Reported error';
      delete conversation.permissions[id];
      tools = tools.filter(t => t.id !== id);
      if (!tools.length && conversation.progress?.label === 'Waiting for your tool decision') phase(conversation, 'Waiting for Claude', Date.now());
    }
    catch (e) { fail(e); }
    finally { toolDecisions = toolDecisions.filter(n => n !== id); }
  }

  function openClaude() { changeView('agent'); }

  function useTerminalSelection(text: string) {
    prompt = appendTerminalSelection(prompt, text);
    changeView('agent');
  }

  async function startClaude() {
    if (!connection || claudeConfig || configuring) return;
    const token = connection;
    configuring = true;
    try {
      const config = await api.startClaude(token, yolo);
      if (connection === token) { claudeConfig = config; agentStatus = 'Ready'; }
    } catch (e) { if (connection === token) fail(e); }
    finally { if (connection === token) configuring = false; }
  }

  async function changeClaudeOption(option: 'model' | 'effort', value: string) {
    const token = connection;
    configuring = true; error = '';
    try {
      const config = await api.setClaudeOption(token, option, value);
      if (connection === token) claudeConfig = config;
    } catch (e) { if (connection === token) fail(e); }
    finally { if (connection === token) configuring = false; }
  }

  async function send() {
    if (!prompt.trim() || busy || !connection) return;
    const token = connection;
    const text = prompt; prompt = ''; sending = true; error = '';
    beginTurn(conversation, text, Date.now());
    void scrollIfFollowing(true);
    try {
      if (!claudeConfig) {
        const config = await api.startClaude(token, yolo);
        if (connection !== token) return;
        claudeConfig = config;
      }
      await api.sendPrompt(token, text);
    }
    catch (e) { if (connection === token) { fail(e); prompt = text; endProgress(conversation, 'Interrupted'); } }
    finally { if (connection === token) sending = false; }
  }

  onMount(() => {
    let disposed = false;
    let off = () => {};
    let offCommands = () => {};
    const timer = setInterval(() => { now = Date.now(); }, 1000);
    void (async () => {
      try {
        const info = await initializeWorkspace();
        if (disposed) return;
        workspace = info;
        needsRecovery = info.connected;
        off = subscribe(receive);
        offCommands = subscribeCommands(desktopCommand);
        if (!needsRecovery) await refresh();
      } catch (e) { if (!disposed) fail(e); }
      finally { if (!disposed) initializing = false; }
    })();
    return () => { disposed = true; off(); offCommands(); clearInterval(timer); };
  });
</script>

<svelte:window onkeydowncapture={keyboard} oncopycapture={clipboardEvent} oncutcapture={clipboardEvent} onpastecapture={clipboardEvent} />

<svelte:head><title>{windowTitle}</title></svelte:head>

<div class="app-shell">
  <header class="app-header">
    <div class="brand"><svg aria-hidden="true" viewBox="0 0 28 28"><path d="M4 6h20v16H4zM9 11l4 3-4 3m7 0h4" /></svg><strong>Vibepit</strong><span class="lab-label">Lab</span></div>
    <span class="prototype-label">Throwaway desktop prototype</span>
    <span class="connection-state"><span class:online={connection}></span>{current?.demo ? 'Simulated sandbox' : connection ? 'Sandbox connected' : 'Disconnected'}</span>
  </header>

  <DesktopControls {workspace} />

  <div class="workspace-bar">
    <label for="sandbox">Sandbox</label>
    <select id="sandbox" bind:value={selected} disabled={!workspace || needsRecovery || !!connection || working}>
      {#if !sandboxes.length}<option value="">No running sandboxes</option>{/if}
      {#each sandboxes as sandbox (sandbox.id)}<option value={sandbox.id}>{sandbox.project} · {sandbox.id}</option>{/each}
    </select>
    {#if connection}<button onclick={disconnect} disabled={working}>Disconnect</button>
    {:else}<button class="primary" onclick={connect} disabled={!workspace || needsRecovery || !selected || working}>{working ? 'Working…' : 'Connect'}</button><button onclick={refresh} disabled={!workspace || needsRecovery || working}>Refresh</button>{/if}
  </div>

  {#if needsRecovery}<div class="error-banner" role="status"><span>This window was reloaded while {workspace?.project || 'its sandbox'} was connected. Its agent may still be running. This prototype cannot restore the conversation after a reload. Disconnect to reset this workspace, or leave it running.</span><button onclick={recoverWorkspace} disabled={working}>Disconnect and reset</button></div>{/if}
  {#if !workspace && !initializing}<div class="error-banner"><span>The workspace could not be opened.</span><button onclick={() => window.location.reload()}>Retry</button></div>{/if}
  {#if current?.demo}<div class="demo-banner">Demo mode · Terminal, Claude replies, and requests are simulated. No commands or model calls are made.</div>{/if}
  {#if error}<div class="error-banner" role="alert"><span>{error}</span><button onclick={() => error = ''} aria-label="Dismiss error">Dismiss</button></div>{/if}

  <main>
    <section class="workbench" aria-label="Sandbox workspace">
      <nav class="tabs" aria-label="Workspace views">
        <button class:active={tab === 'terminal'} aria-pressed={tab === 'terminal'} onclick={() => changeView('terminal')}>Terminal</button>
        <button class:active={tab === 'agent'} aria-pressed={tab === 'agent'} onclick={openClaude}>Claude {#if tools.length}<span class="count">{tools.length}</span>{/if}</button>
        <button class:active={tab === 'debug'} aria-pressed={tab === 'debug'} onclick={() => changeView('debug')}>Claude debug</button>
        <span class="view-status">{tab === 'terminal' ? (connection ? 'Connected' : 'Not connected') : agentStatus}</span>
      </nav>

      {#if tab === 'debug'}<ClaudeDebug messages={debugMessages} dropped={debugDropped} recording={streamLog} onclear={clearDebug} />{/if}
      {#if !connection}
        {#if tab !== 'debug'}
        <div class="empty-workspace"><h1>A workspace inside your sandbox.</h1><p>Connect to a running Vibepit session to open a terminal or work with Claude. Network requests appear alongside your work.</p><p class="hint">Start a session from your project:</p><code>vibepit up</code><button onclick={refresh} disabled={!workspace || needsRecovery || working}>Refresh sandboxes</button></div>
        {/if}
      {:else}
        <div class="terminal-panel" class:concealed={tab !== 'terminal'}>
          {#key connection}<Terminal {connection} active={tab === 'terminal'} onselection={useTerminalSelection} oneditor={editor => terminalEditor = editor} onerror={fail} onexit={message => terminalExit = message} />{/key}
          {#if terminalExit}<div class="terminal-exit" role="status">{terminalExit}</div>{/if}
        </div>
        <div class="agent-panel" class:concealed={tab !== 'agent'}>
          <div class="agent-controls">
          <ClaudeSettings config={claudeConfig} busy={busy || agentStatus === 'Exited'} loading={configuring} onchange={changeClaudeOption} onstart={startClaude} bind:yolo />
          <ClaudeUsage {conversation} {now} />
          </div>
          <div class="transcript" bind:this={transcript}>
            {#if !conversation.messages.length}<div class="agent-empty"><h1>Claude, inside Vibepit.</h1><p>Ask about the project or give Claude a task. Replies and tool activity stream here; approval requests stay within reach.</p><p class="hint">Uses the Claude installation and login in your sandbox.</p></div>{/if}
            {#each conversation.messages as message (message.id)}
              <article class="message" class:user-message={message.role === 'user'}>
                {#if message.tool}<ToolActivity tool={message.tool} {now} />
                {:else if message.summary}<TurnDetails summary={message.summary} />
                {:else}<h2>{message.role === 'user' ? 'You' : 'Claude'}</h2>{#if message.role === 'assistant'}<Markdown text={message.text} />{:else}<div class="message-text">{message.text}</div>{/if}{/if}
              </article>
            {/each}
            {#if busy}<p class="working-status" role="status">{configuring ? 'Configuring Claude…' : tools.length ? 'Waiting for your tool decision' : conversation.progress?.label || 'Starting Claude…'}{#if conversation.progress && !configuring} · {Math.max(0, Math.floor((now - conversation.progress.since) / 1000))}s{#if conversation.progress.thinkingTokens != null} · ≈{conversation.progress.thinkingTokens} thinking tokens{/if}{/if}</p>{/if}
          </div>
          {#if diagnostics.length}<details class="diagnostics"><summary>Agent diagnostics ({diagnostics.length})</summary><pre>{diagnostics.join('\n')}</pre></details>{/if}
          {#if tools.length}<div class="tool-requests" aria-label="Claude tool permissions">
            {#each tools as tool (tool.id)}<div class="tool-request"><h2>Claude wants to use {tool.name}</h2><pre>{format(tool.input)}</pre><div class="actions"><button class="primary" disabled={toolDecisions.includes(tool.id)} onclick={() => decideTool(tool.id, true)}>Allow tool</button><button disabled={toolDecisions.includes(tool.id)} onclick={() => decideTool(tool.id, false)}>Deny tool</button></div></div>{/each}
          </div>{/if}
          <ClaudeComposer bind:prompt commands={conversation.commands} {busy} exited={agentStatus === 'Exited'} onsend={send} oninterrupt={() => api.interrupt(connection).catch(fail)} />
        </div>
      {/if}
    </section>

    <aside class="network-panel" aria-label="Network permissions">
      <div class="network-heading"><h2>Network</h2>{#if activeHolds.length}<span class="count">{activeHolds.length}</span>{/if}</div>
      <p class="network-status" title={networkStatus}>{networkStatus}</p>
      <div class="network-scroll">
        {#if activeHolds.length}
          <p class="section-description">These connections are waiting for you.</p>
          {#each activeHolds as hold (hold.id)}
            <section class="network-request">
              <h3>{hold.domain}<span>:{hold.port}</span></h3>
              <p>HTTPS / HTTP · {Math.max(0, Math.ceil((new Date(hold.deadline!).getTime() - now) / 1000))}s remaining</p>
              <div class="actions"><button class="primary" disabled={decisions.includes(hold.id) || networkStatus !== 'Watching'} onclick={() => decideNetwork(hold.id, true)}>Allow for session</button><button disabled={decisions.includes(hold.id) || networkStatus !== 'Watching'} onclick={() => decideNetwork(hold.id, false)}>Reject</button></div>
            </section>
          {/each}
        {:else}<div class="network-empty"><svg aria-hidden="true" viewBox="0 0 36 36"><path d="M18 3l12 5v9c0 8-12 16-12 16S6 25 6 17V8zM12 17l4 4 8-9" /></svg><h3>{connection ? 'No requests waiting' : 'Permissions live here'}</h3><p>{connection ? 'New HTTP connections that need approval will appear here. Your terminal keeps running.' : 'Connect a sandbox to watch its network requests and decide what gets through.'}</p></div>{/if}
        {#if logs.length}<div class="activity"><h3>Recent activity</h3><ul>{#each logs as entry, index (`${index}-${entry}`)}<li>{entry}</li>{/each}</ul></div>{/if}
      </div>
      <p class="network-note">Network decisions apply to the whole sandbox. Session allows are not saved to project config. DNS blocks appear in activity; this spike prompts only for held HTTP(S) requests.</p>
    </aside>
  </main>
</div>
