<script lang="ts">
  import type { AgentDebugMessage, StreamLogStatus } from './bridge';

  let { messages, dropped, recording, onclear }: {
    messages: AgentDebugMessage[]; dropped: number; recording: StreamLogStatus | null; onclear: () => void;
  } = $props();
  let stream = $state('all');
  let pretty = $state(true);
  const labels = { stdin: 'To Claude · stdin', stdout: 'From Claude · stdout', stderr: 'From Claude · stderr', transport: 'Transport / startup' };
  const visible = $derived(messages.filter(message => stream === 'all' || message.stream === stream).reverse());

  function payload(raw: string) {
    if (pretty) { try { return JSON.stringify(JSON.parse(raw), null, 2); } catch { /* Show malformed output verbatim. */ } }
    return raw;
  }
  function title(raw: string) {
    try {
      const frame = JSON.parse(raw);
      return [frame.type, frame.subtype ?? frame.request?.subtype ?? frame.response?.subtype ?? frame.event?.type].filter(Boolean).join(' / ') || 'JSON';
    } catch { return 'Raw text'; }
  }
</script>

<section class="debug-panel" aria-label="Claude connection debug">
  <div class="debug-toolbar">
    <div class="debug-filter"><label for="debug-stream">Stream</label><select id="debug-stream" bind:value={stream}><option value="all">All streams</option><option value="stdin">To Claude (stdin)</option><option value="stdout">From Claude (stdout)</option><option value="stderr">Stderr</option><option value="transport">Transport / startup</option></select></div>
    <label class="pretty-toggle"><input type="checkbox" bind:checked={pretty} /> Pretty JSON</label>
    <button onclick={onclear} disabled={!messages.length && !dropped}>Clear view</button>
  </div>
  <p class="debug-note">{messages.length} captured · Newest first. Input records are write attempts; startup diagnostics and write failures appear under transport. Captures while this tab is closed.</p>
  {#if recording}<p class="debug-note recording-path">Stream file: <code>{recording.path}</code><br />{recording.error || 'Full JSONL recording on disk. Clearing this view does not delete it.'}</p>{/if}
  {#if dropped}<p class="debug-note" role="status">{dropped} older records discarded. Keeps up to 1,000 records and approximately 16 MiB of raw text.</p>{/if}
  <div class="debug-log">
    {#if !visible.length}<div class="debug-empty"><h2>{messages.length ? 'No messages in this stream' : 'No Claude messages yet'}</h2><p>{messages.length ? 'Choose another stream to see the captured messages.' : 'Send a message in the Claude tab. Initialization, prompts, replies, and permission messages will appear here.'}</p></div>{/if}
    {#each visible as message (message.sequence)}
      <details class="debug-record">
        <summary><span class="stream-label" class:input-stream={message.stream === 'stdin'}>{labels[message.stream]}</span><span class="frame-type">{title(message.raw)}</span><time datetime={message.time}>{message.time.slice(11, 23)}</time><span class="sequence">#{message.sequence}</span></summary>
        <pre>{payload(message.raw)}</pre>
      </details>
    {/each}
  </div>
</section>

<style>
  .debug-panel { display: flex; flex-direction: column; flex: 1; min-height: 0; }
  .debug-toolbar { display: flex; align-items: center; flex-wrap: wrap; gap: 16px; padding: 16px 22px 10px; }
  .debug-toolbar label { display: flex; align-items: center; gap: 8px; color: var(--muted); font-size: 12px; }
  .debug-filter { display: flex; align-items: center; gap: 8px; font-size: 12px; }
  .debug-toolbar button { margin-left: auto; }
  .pretty-toggle input { accent-color: var(--accent); }
  .debug-note { padding: 0 22px 12px; color: var(--muted); font-size: 12px; line-height: 1.6; }
  .recording-path code { color: var(--text); overflow-wrap: anywhere; user-select: all; }
  .debug-log { flex: 1; min-height: 0; overflow: auto; padding: 0 22px 20px; }
  .debug-empty { padding: 40px 0; max-width: 60ch; }
  .debug-empty h2 { font-size: 19px; font-weight: 550; margin-bottom: 12px; }
  .debug-empty p { color: var(--muted); line-height: 1.7; }
  .debug-record { border-top: 1px solid var(--line); }
  summary { padding: 12px 0; line-height: 1.8; overflow-wrap: anywhere; }
  .stream-label { display: inline-block; min-width: 155px; color: var(--text); }
  .input-stream { color: var(--accent); }
  .frame-type { margin-right: 14px; }
  time, .sequence { font-variant-numeric: tabular-nums; white-space: nowrap; margin-right: 10px; }
  pre { max-height: 480px; margin: 0 0 16px; padding: 14px; background: var(--base); border: 1px solid var(--line); border-radius: 5px; }
</style>
