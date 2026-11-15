<script lang="ts">
  import { api } from './bridge';
  import type { WindowInfo } from './bridge';

  let { workspace }: { workspace: WindowInfo | null } = $props();
  let opening = $state(false);
  let enabling = $state(false);
  let hiding = $state(false);
  let notificationStatus = $state('');
  let error = $state('');
  let unopenedURL = $state('');
  let focusPreference = $state<boolean | null>(null);
  let savingPreference = $state(false);

  function fail(value: unknown) { error = value instanceof Error ? value.message : String(value); }

  async function newWindow() {
    if (!workspace || opening) return;
    // Keep the browser popup within the click gesture; native windows open in Go.
    const popup = workspace.native ? null : window.open('', '_blank');
    if (popup) popup.opener = null;
    opening = true; error = ''; unopenedURL = '';
    try {
      const next = await api.newWindow();
      if (!next.native) {
        if (popup && !popup.closed) popup.location.replace(next.url);
        else unopenedURL = next.url;
      } else popup?.close();
    } catch (e) { popup?.close(); fail(e); }
    finally { opening = false; }
  }

  async function hideWindow() {
    hiding = true; error = '';
    try { await api.hideWindow(); }
    catch (e) { fail(e); }
    finally { hiding = false; }
  }

  async function setNotifyWhenFocused(event: Event) {
    const checkbox = event.currentTarget as HTMLInputElement;
    const enabled = checkbox.checked;
    checkbox.checked = focusPreference ?? workspace?.notifyWhenFocused ?? false;
    savingPreference = true; error = '';
    try { focusPreference = await api.setNotifyWhenFocused(enabled); }
    catch (e) { fail(e); }
    finally { savingPreference = false; }
  }

  async function enableNotifications() {
    enabling = true; error = '';
    try { notificationStatus = await api.enableNotifications(); }
    catch (e) { fail(e); }
    finally { enabling = false; }
  }
</script>

<div class="desktop-controls">
  <div class="window-actions" aria-label="Desktop controls">
    <span class="window-label">{workspace?.title || 'Opening workspace…'}</span>
    <button onclick={newWindow} disabled={!workspace || opening}>{opening ? 'Opening…' : 'New window'}</button>
    {#if workspace?.native}
      <button onclick={hideWindow} disabled={hiding} title="Keep this workspace running and restore it from the tray">Hide window</button>
      <button onclick={enableNotifications} disabled={enabling}>{enabling ? 'Enabling…' : 'Enable notifications'}</button>
      <label class="focus-notifications" title="For this window; OS notification permission is still required">
        <input type="checkbox" checked={focusPreference ?? workspace.notifyWhenFocused} onchange={setNotifyWhenFocused} disabled={savingPreference} />
        Notify even when focused
      </label>
    {:else if workspace}
      <span class="preview-note">Browser preview · Tray and notifications require the desktop app.</span>
    {/if}
  </div>
  {#if notificationStatus}<p role="status">{notificationStatus}</p>{/if}
  {#if unopenedURL}<p role="status">The browser blocked the new window. <a href={unopenedURL} target="_blank" rel="noopener noreferrer">Open the new workspace</a></p>{/if}
  {#if error}<p class="desktop-error" role="alert">{error}</p>{/if}
</div>

<style>
  .desktop-controls { padding: 10px 24px; border-bottom: 1px solid var(--line); }
  .window-actions { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
  .window-label { color: var(--muted); font-size: 12px; margin-right: auto; overflow-wrap: anywhere; }
  button { font-size: 12px; padding: 6px 10px; }
  .focus-notifications { display: inline-flex; align-items: center; gap: 6px; font-size: 12px; color: var(--muted); }
  .focus-notifications input { width: auto; margin: 0; accent-color: var(--accent); }
  .preview-note, p { font-size: 11px; line-height: 1.5; color: var(--muted); }
  p { margin-top: 8px; }
  a { color: var(--accent); }
  .desktop-error { color: #ffd2cb; }
  @media (max-width: 700px) { .desktop-controls { padding: 10px 14px; } .preview-note { width: 100%; } }
</style>
