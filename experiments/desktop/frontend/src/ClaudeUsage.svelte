<script lang="ts">
  import type { Conversation } from './claude-stream';
  let { conversation: c, now }: { conversation: Conversation; now: number } = $props();
  const windows = $derived(Object.entries(c.rate?.unifiedWindows ?? {}));
  const accountLabel = $derived(c.apiKeySource && c.apiKeySource !== 'none' ? `API credentials · ${c.apiKeySource}` : c.account.subscriptionType || 'Account not reported');
  const percent = (n: number) => Math.round(n * 100);
  function resetAt(seconds?: number) {
    if (!seconds) return 'Reset time not reported';
    if (seconds * 1000 <= now) return 'Reset time passed · awaiting update';
    return `Resets ${new Date(seconds * 1000).toLocaleString([], { weekday:'short', hour:'2-digit', minute:'2-digit' })}`;
  }
  function label(key: string) { return ({five_hour:'5-hour',seven_day:'Weekly'} as Record<string,string>)[key] || key.replaceAll('_',' '); }
</script>

<details class="usage">
  <summary>
    <span>{accountLabel}</span>
    {#each windows as [key, window] (key)}
      <span class:near-limit={(window.utilization ?? 0) >= .9}>{label(key)} {window.utilization == null ? window.status || 'Unknown' : `${percent(window.utilization)}% used`}</span>
    {/each}
    {#if c.context?.capacity}<span>Context ≈{percent(c.context.tokens / c.context.capacity)}%</span>{/if}
  </summary>
  <div class="usage-details">
    {#if !c.rate}<p>Usage limits have not been reported yet.</p>{/if}
    {#each windows as [key, window] (key)}
      <div class="usage-window"><label for={`usage-${key}`}>{label(key)} · {window.utilization == null ? 'Usage not reported' : `${percent(window.utilization)}% used`}</label>
        {#if window.utilization != null}<meter id={`usage-${key}`} min="0" max="1" value={window.utilization}>{percent(window.utilization)}%</meter>{/if}
        <span>{resetAt(window.resetsAt)}{window.status ? ` · ${window.status}` : ''}</span>
      </div>
    {/each}
    {#if c.rate && !windows.length}<p>{c.rate.rateLimitType?.replaceAll('_',' ') || 'Rate limit'}: {c.rate.status || 'Not reported'} · {resetAt(c.rate.resetsAt)}</p>{/if}
    {#if c.rate?.isUsingOverage === true}<p class="near-limit">Using paid overage</p>
    {:else if c.rate?.overageDisabledReason}<p>Paid overage disabled · {c.rate.overageDisabledReason.replaceAll('_',' ')}</p>
    {:else if c.rate?.isUsingOverage === false}<p>Not currently using paid overage</p>{/if}
    {#if c.context}<p>Latest request context: ≈{c.context.tokens.toLocaleString()} tokens{c.context.capacity ? ` of ${c.context.capacity.toLocaleString()}` : ' · capacity not reported'}. Includes input, cache and generated tokens.</p>{/if}
    {#if c.rateAt}<p class="muted">Last usage report: {new Date(c.rateAt).toLocaleTimeString()}. Limits update when Claude reports them.</p>{/if}
  </div>
</details>

<style>
  .usage { padding: 9px 24px; border-bottom: 1px solid var(--line); flex-shrink: 0; font-size: 11px; color: var(--muted); }
  summary { font-size: 11px; }
  summary span { display: inline-block; margin: 0 16px 3px 0; }
  summary span:first-child { color: var(--text); }
  .usage-details { padding-top: 10px; max-height: 220px; overflow: auto; }
  .usage-details p { margin: 8px 0; line-height: 1.5; }
  .usage-window { display: flex; flex-wrap: wrap; gap: 8px 14px; align-items: center; margin-bottom: 10px; }
  meter { width: 100px; height: 8px; accent-color: var(--accent); }
  .near-limit { color: #e9cf98; }
</style>
