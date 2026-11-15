<script lang="ts">
  import type { ToolActivity } from './claude-stream';
  import { formatValue } from './claude-stream';
  let { tool, now }: { tool: ToolActivity; now: number } = $props();
  const elapsed = $derived(Math.max(0, Math.floor(((tool.endedAt ?? now) - tool.startedAt) / 1000)));
</script>

<details class="tool-activity">
  <summary><span class="tool-name">{tool.name}</span><span class="tool-title">{tool.title}</span><span class="tool-state" class:reported-error={tool.state === 'Reported error'}>{tool.state} · {elapsed}s</span></summary>
  <div class="tool-body">
    <h3>Input</h3><pre>{formatValue(tool.input) || tool.partial || 'Receiving input…'}</pre>
    {#if tool.filePath}<h3>{tool.filePath}</h3>{/if}
    {#if tool.stdout !== undefined}<h3>Output</h3><pre>{tool.stdout || '(empty)'}</pre>{/if}
    {#if tool.stderr}<h3>Standard error</h3><pre>{tool.stderr}</pre>{/if}
    {#if tool.output !== undefined}<pre>{tool.output || '(empty)'}</pre>{/if}
    {#if tool.state === 'Completed'}<p>Tool returned. Check its output for task results.</p>{/if}
  </div>
</details>

<style>
  .tool-activity { border: 1px solid var(--line); border-radius: 5px; }
  summary { padding: 10px 13px; line-height: 1.6; }
  .tool-name { color: var(--accent); margin-right: 10px; font-size: 11px; }
  .tool-title { color: var(--text); overflow-wrap: anywhere; }
  .tool-state { display: inline-block; color: var(--muted); font-size: 11px; margin-left: 12px; }
  .reported-error { color: #edbcad; }
  .tool-body { padding: 0 14px 12px; }
  h3, p { font-size: 11px; font-weight: 400; color: var(--muted); margin: 10px 0 6px; overflow-wrap: anywhere; }
  pre { max-height: 280px; overflow: auto; padding: 10px; border: 1px solid var(--line); white-space: pre-wrap; overflow-wrap: anywhere; font-size: 11px; line-height: 1.5; }
</style>
