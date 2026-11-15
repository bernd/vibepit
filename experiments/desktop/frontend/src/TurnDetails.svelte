<script lang="ts">
  import type { TurnSummary } from './claude-stream';
  let { summary }: { summary: TurnSummary } = $props();
  const counts = $derived([
    ['Input', summary.usage?.input_tokens], ['Cache read', summary.usage?.cache_read_input_tokens],
    ['Cache created', summary.usage?.cache_creation_input_tokens], ['Output', summary.usage?.output_tokens],
    ['Thinking (included in output)', summary.usage?.output_tokens_details?.thinking_tokens],
  ] as [string, number | undefined][]);
</script>
<details class="turn-details">
  <summary>{summary.outcome}{summary.durationMs == null ? '' : ` in ${(summary.durationMs / 1000).toFixed(1)}s`} · {summary.toolCount} {summary.toolCount === 1 ? 'tool' : 'tools'}</summary>
  <dl>
    {#if summary.reason}<dt>Completion reason</dt><dd>{summary.reason.replaceAll('_',' ')}</dd>{/if}
    {#if summary.apiMs != null}<dt>Model API time</dt><dd>{(summary.apiMs / 1000).toFixed(1)}s</dd>{/if}
    {#each counts as [label, count] (label)}{#if count != null}<dt>{label}</dt><dd>{count.toLocaleString()} tokens</dd>{/if}{/each}
    {#if summary.models}<dt>Models</dt><dd>{Object.keys(summary.models).join(', ')}</dd>{/if}
  </dl>
  <p>Token totals cover this turn. Tool completion does not establish that checks passed.</p>
</details>
<style>
  .turn-details { border-top: 1px solid var(--line); padding-top: 10px; color: var(--muted); }
  summary { font-size: 11px; }
  dl { display: grid; grid-template-columns: minmax(100px, auto) 1fr; gap: 7px 16px; font-size: 11px; margin: 14px 0; }
  dd { margin: 0; overflow-wrap: anywhere; } p { font-size: 11px; }
</style>
