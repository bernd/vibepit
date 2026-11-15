import type { ShellIntegrationState } from '@wterm/dom';

export function workingDirectory(uri: string): string {
  try {
    if (!/^file:\/\//i.test(uri)) return '';
    const url = new URL(uri);
    if (url.username || url.password || url.port || url.search || url.hash) return '';
    const path = decodeURIComponent(url.pathname);
    if (/[\x00-\x1f\x7f-\x9f\u202a-\u202e\u2066-\u2069]/.test(path)) return '';
    return `${url.host ? `${url.host}:` : ''}${path}`;
  } catch { return ''; }
}

export function shellStatus(state: ShellIntegrationState): string {
  if (state.phase === 'unknown') return 'Shell status unavailable';
  if (state.phase === 'running') return 'Running';
  if (state.phase === 'complete') return state.exitCode === null ? 'Finished' : state.exitCode === 0 ? 'Done' : `Exit ${state.exitCode}`;
  return state.exitCode ? `Ready · last exit ${state.exitCode}` : 'Ready';
}

export function appendTerminalSelection(draft: string, selection: string): string {
  if (!selection) return draft;
  let longest = 2;
  for (const match of selection.matchAll(/`+/g)) longest = Math.max(longest, match[0].length);
  const fence = '`'.repeat(longest + 1);
  return `${draft ? `${draft}\n\n` : ''}Terminal selection:\n${fence}text\n${selection}\n${fence}`;
}
