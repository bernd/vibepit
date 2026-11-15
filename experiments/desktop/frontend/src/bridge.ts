import { Call, Events } from '@wailsio/runtime';
import type { DesktopCommand } from './editing';

export interface ClaudeModel {
  value: string; displayName: string; resolvedModel?: string;
  supportsEffort?: boolean; supportedEffortLevels?: string[];
}
export interface ClaudeConfig {
  yolo: boolean; permissionMode?: string;
  models: ClaudeModel[] | null; model: string; reportedModel: string; effort: string; effortReset: boolean;
}
export interface Sandbox { id: string; project: string; demo: boolean }
export interface WindowInfo {
  id: string; url: string; native: boolean; title: string; project: string;
  notifyWhenFocused: boolean; connection: string; agentStatus: string; connected: boolean; pending: number;
}
export interface NetworkEntry {
  id: number; domain: string; port?: string; source: string;
  action: string; deadline?: string; outcome?: string; reason?: string;
}
export interface ToolRequest { id: string; name: string; input: unknown; toolID?: string }
export interface AgentDebugMessage {
  sequence: number; time: string; stream: 'stdin' | 'stdout' | 'stderr' | 'transport'; raw: string;
}
export interface StreamLogStatus { path: string; error?: string; closed: boolean }
import type { StreamFrame as AgentFrame } from './claude-stream';
export type { StreamFrame as AgentFrame } from './claude-stream';
export type BackendEvent = { workspace: string; connection: string } & (
  | { kind: 'terminal' | 'terminal-exit' | 'agent-status' | 'agent-error' | 'network-status'; data: string }
  | { kind: 'network-reset'; data: null }
  | { kind: 'network-hold' | 'network-log'; data: NetworkEntry }
  | { kind: 'network-end'; data: { id: number; outcome: string } }
  | { kind: 'agent-config'; data: ClaudeConfig }
  | { kind: 'agent-event'; data: AgentFrame }
  | { kind: 'agent-debug'; data: AgentDebugMessage }
  | { kind: 'agent-log-status'; data: StreamLogStatus }
);

const prefix = 'github.com/bernd/vibepit/experiments/desktop/backend.Workspaces.';
let workspace = '';
let native = false;
function desktopCall<T>(method: string, ...args: unknown[]): Promise<T> { return Call.ByName(prefix + method, ...args); }
function call<T>(method: string, ...args: unknown[]): Promise<T> {
  if (!workspace) return Promise.reject(new Error('The workspace is not ready.'));
  return desktopCall<T>(method, workspace, ...args);
}

export async function initializeWorkspace(): Promise<WindowInfo> {
  const id = new URL(window.location.href).searchParams.get('workspace');
  const info = id ? await desktopCall<WindowInfo>('GetWindow', id) : await desktopCall<WindowInfo>('NewWindow');
  if (!info.id) throw new Error('The desktop returned an invalid workspace.');
  workspace = info.id;
  native = info.native;
  if (!id) window.history.replaceState(null, '', info.url);
  return info;
}

export const api = {
  clipboard: {
    readText: () => native ? call<string>('ReadClipboard') : navigator.clipboard?.readText() ?? Promise.reject(new Error('Clipboard access is unavailable. Use the browser’s Paste action.')),
    writeText: (text: string) => native ? call<void>('WriteClipboard', text) : navigator.clipboard?.writeText(text) ?? Promise.reject(new Error('Clipboard access is unavailable. Use the browser’s Copy action.')),
  },
  windowInfo: () => call<WindowInfo>('GetWindow'),
  newWindow: () => desktopCall<WindowInfo>('NewWindow'),
  hideWindow: () => call<void>('HideWindow'),
  enableNotifications: () => desktopCall<string>('EnableNotifications'),
  setNotifyWhenFocused: (enabled: boolean) => call<boolean>('SetNotifyWhenFocused', enabled),
  sessions: () => call<Sandbox[]>('Sessions'),
  connect: (id: string) => call<string>('Connect', id),
  disconnect: () => call<void>('Disconnect'),
  openTerminal: (token: string, cols: number, rows: number) => call<void>('OpenTerminal', token, cols, rows),
  writeTerminal: (token: string, data: string) => call<void>('WriteTerminal', token, data),
  writeTerminalBytes: (token: string, data: string) => call<void>('WriteTerminalBytes', token, data),
  resizeTerminal: (token: string, cols: number, rows: number) => call<void>('ResizeTerminal', token, cols, rows),
  watchNetwork: (token: string) => call<void>('WatchNetwork', token),
  decideNetwork: (token: string, id: number, allow: boolean) => call<void>('DecideNetwork', token, id, allow),
  startClaude: (token: string, yolo: boolean) => call<ClaudeConfig>('StartClaude', token, yolo),
  setClaudeOption: (token: string, option: 'model' | 'effort', value: string) => call<ClaudeConfig>('SetClaudeOption', token, option, value),
  sendPrompt: (token: string, prompt: string) => call<void>('SendPrompt', token, prompt),
  decideTool: (token: string, id: string, allow: boolean) => call<void>('DecideTool', token, id, allow),
  interrupt: (token: string) => call<void>('InterruptAgent', token),
};

export function subscribe(callback: (event: BackendEvent) => void): () => void {
  return Events.On('vibepit', event => {
    const data = event.data as BackendEvent;
    if (workspace && data.workspace === workspace) callback(data);
  });
}

export function subscribeCommands(callback: (command: DesktopCommand) => void): () => void {
  return Events.On('vibepit:command', event => {
    const data = event.data as { workspace: string; command: DesktopCommand };
    if (workspace && data.workspace === workspace) callback(data.command);
  });
}
