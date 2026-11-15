<script lang="ts">
  import type { ClaudeConfig } from './bridge';
  let { config, busy, loading, yolo = $bindable(false), onchange, onstart }: {
    config: ClaudeConfig | null; busy: boolean; loading: boolean; yolo?: boolean;
    onchange: (option: 'model' | 'effort', value: string) => Promise<void>;
    onstart: () => void;
  } = $props();
  const model = $derived(config?.models?.find(m => config.model ? m.value === config.model : m.resolvedModel === config.reportedModel));
  const levels = $derived(model?.supportsEffort === false ? [] : model?.supportedEffortLevels ?? ['low', 'medium', 'high']);

  async function change(event: Event, option: 'model' | 'effort') {
    const select = event.currentTarget as HTMLSelectElement;
    const value = select.value;
    // Keep the indicator at the acknowledged value while the request is pending.
    select.value = config?.[option] ?? '';
    await onchange(option, value);
    select.value = config?.[option] ?? '';
  }
</script>

<div class="claude-settings" aria-label="Claude model settings">
  <div class="startup-mode">
    <label><input type="checkbox" bind:checked={yolo} disabled={!!config || busy || loading} /> YOLO · skip Claude approvals</label>
    {#if config}<span class:yolo-active={config.permissionMode === 'bypassPermissions'}>
      {config.permissionMode === 'bypassPermissions' ? 'Permissions: YOLO' : config.permissionMode ? `Permissions: ${config.permissionMode}` : `${config.yolo ? 'YOLO' : 'Default'} requested · not yet reported`}
    </span>{/if}
    {#if loading}<span role="status">Starting Claude…</span>
    {:else if !config}<button onclick={onstart} disabled={busy}>{yolo ? 'Start Claude in YOLO mode' : 'Start Claude'}</button>{/if}
  </div>
  <p class="startup-note">{config ? 'Reconnect to change startup mode.' : 'Choose startup mode, then start Claude or send a message.'} Vibepit network approvals still apply.</p>
  <div class="settings-selectors">
    <label for="claude-model">Model</label>
    <select id="claude-model" value={config?.model ?? ''} disabled={!config || busy || loading} onchange={event => change(event, 'model')}>
      <option value="">Claude default</option>
      {#each config?.models ?? [] as option (option.value)}<option value={option.value}>{option.displayName || option.value}</option>{/each}
    </select>
    <label for="claude-effort">Effort</label>
    <select id="claude-effort" value={config?.effort ?? ''} disabled={!config || busy || loading} onchange={event => change(event, 'effort')}>
      <option value="">{config?.effortReset ? 'Model default' : 'Inherited · not reported'}</option>
      {#each levels as level (level)}<option value={level}>{level.charAt(0).toUpperCase() + level.slice(1)}</option>{/each}
      {#if config?.effort && !levels.includes(config.effort)}<option value={config.effort}>{config.effort} · unsupported here</option>{/if}
    </select>

  </div>
  <p class="settings-note" aria-live="polite">
    {#if config?.reportedModel}Last reported model: <code>{config.reportedModel}</code>. {/if}
    {#if model?.supportsEffort === false}This model does not support effort settings.
    {:else}Effort shows the session override; Claude may apply model or policy limits.{/if}
    Changes apply to the next turn.
  </p>
</div>

<style>
  .startup-mode { display: flex; flex-wrap: wrap; align-items: center; gap: 8px 16px; margin-bottom: 6px; }
  .startup-mode label { display: flex; align-items: center; gap: 7px; color: var(--text); }
  .startup-mode input { accent-color: var(--accent); }
  .startup-mode button { padding: 5px 10px; font-size: 12px; }
  .startup-mode span, .startup-note { font-size: 11px; color: var(--muted); }
  .startup-note { margin-bottom: 12px; line-height: 1.5; }
  .startup-mode .yolo-active { color: var(--accent); }
  .claude-settings { flex-shrink: 0; padding: 12px 24px; border-bottom: 1px solid var(--line); background: var(--panel); }
  .settings-selectors { display: flex; flex-wrap: wrap; align-items: center; gap: 8px 12px; }
  label { color: var(--muted); font-size: 12px; }
  select { min-width: 125px; max-width: 250px; font-size: 12px; }
  .settings-note { margin-top: 9px; color: var(--muted); font-size: 11px; line-height: 1.5; overflow-wrap: anywhere; }
  code { font-size: inherit; }
</style>
