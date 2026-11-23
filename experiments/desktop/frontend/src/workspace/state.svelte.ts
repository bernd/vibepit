import { tick } from 'svelte';
import { LayoutError, close, equalize, find, group, isGroup, joinTitle, leaves, setDivider, setFocus, split, transform } from '../core/workspace/layout.ts';
import type { Destination, Dir, Layout, Node, Pane, PaneType, Size, Tab } from '../core/workspace/layout.ts';
import { restore } from '../core/workspace/persist.ts';

const storageKey = 'vp.workspace.v1';
const newId = () => (globalThis.crypto?.randomUUID?.() ?? Math.random().toString(36).slice(2)).replace(/-/g, '').slice(0, 10);

export interface Session { name: string }
export type MenuEntry = { label: string; run: () => void; danger?: boolean } | { heading: string } | 'separator';
export interface MenuState { anchor: HTMLElement; items: MenuEntry[] }
export interface DragState {
  src: string;
  dest: Destination | null;
  // The committed result of dropping here, or why the drop is unavailable.
  result: Layout | null;
  error: string;
}

const typeNames: Record<PaneType, string> = { session: 'Session', monitor: 'Monitor', logs: 'Logs' };

export class Workspace {
  // Layout operations return a new layout, so the tree is replaced whole.
  layout = $state.raw<Layout>({ tabs: [], active: null });
  sessions = $state<Record<string, Session>>({});
  // View-local state lives here, keyed by pane, so it survives every move.
  drafts = $state<Record<string, string>>({});
  // Zoom is temporary window state and is never saved.
  zoomed = $state<string | null>(null);
  space = $state.raw<Size>({ w: 0, h: 0 });
  message = $state('');
  menu = $state.raw<MenuState | null>(null);
  moving = $state<string | null>(null);
  drag = $state.raw<DragState | null>(null);

  constructor() {
    if (!this.load()) this.reset();
  }

  get active(): Tab | undefined { return this.layout.tabs.find((t) => t.id === this.layout.active); }
  get focused(): Pane | undefined { const t = this.active; return t && (find(t.root, t.focus) as Pane | undefined); }

  paneTitle(p: Pane): string { return p.type === 'session' ? this.sessions[p.sid ?? '']?.name ?? 'Unavailable' : typeNames[p.type]; }
  title(n: Node): string { return joinTitle(leaves(n).map((p) => this.paneTitle(p))); }
  fullTitle(n: Node): string { return leaves(n).map((p) => this.paneTitle(p)).join(' + '); }

  private options() { return { newId, space: this.space.w > 0 ? this.space : undefined }; }

  // What a move would produce, without changing anything. The docking
  // preview and the commit both use this result.
  preview(src: string, dest: Destination): { layout: Layout | null; error: string } {
    try {
      return { layout: transform(this.layout, src, dest, this.options()), error: '' };
    } catch (e) {
      if (e instanceof LayoutError) return { layout: null, error: e.message };
      throw e;
    }
  }

  commit(next: Layout) {
    this.layout = next;
    this.zoomed = null;
    this.save();
    void this.focusPane();
  }

  move(src: string, dest: Destination): boolean {
    const { layout, error } = this.preview(src, dest);
    if (layout) this.commit(layout); else this.say(error);
    return !!layout;
  }

  private shellName(): string {
    const used = new Set(Object.values(this.sessions).map((s) => s.name));
    let n = 1;
    while (used.has(`Shell ${n}`)) n++;
    return `Shell ${n}`;
  }

  private newPane(type: PaneType, name?: string): Pane {
    if (type !== 'session') return { id: newId(), type };
    const sid = newId();
    this.sessions[sid] = { name: name ?? this.shellName() };
    return { id: newId(), type, sid };
  }

  addTab(type: PaneType) {
    const pane = this.newPane(type);
    const tab: Tab = { id: newId(), root: pane, focus: pane.id };
    this.commit({ tabs: [...this.layout.tabs, tab], active: tab.id });
  }

  // If the split cannot fit, the layout stays intact and no session is created.
  splitPane(paneId: string, dir: Dir) {
    const fresh: Pane = { id: newId(), type: 'session', sid: newId() };
    try {
      const next = split(this.layout, paneId, dir, fresh, this.options());
      this.sessions[fresh.sid as string] = { name: this.shellName() };
      this.commit(next);
    } catch (e) {
      if (!(e instanceof LayoutError)) throw e;
      this.say(e.message);
    }
  }

  closeNode(id: string) {
    const tab = this.layout.tabs.find((t) => find(t.root, id));
    const node = tab && find(tab.root, id);
    if (!node) return;
    for (const p of leaves(node)) {
      delete this.drafts[p.id];
      if (p.sid) delete this.sessions[p.sid];
    }
    this.commit(close(this.layout, id));
  }

  selectTab(id: string) {
    if (id !== this.layout.active) this.zoomed = null;
    this.layout = { ...this.layout, active: id };
    this.save();
    void this.focusPane();
  }

  // Records which pane has focus. It does not move DOM focus.
  focus(paneId: string) {
    if (this.drag || this.active?.focus === paneId) return;
    this.layout = setFocus(this.layout, paneId);
    this.save();
  }

  toggleZoom(paneId: string) {
    const tab = this.active;
    if (!tab || !isGroup(tab.root) || !find(tab.root, paneId) || this.drag) return;
    this.zoomed = this.zoomed === paneId ? null : paneId;
    this.layout = setFocus(this.layout, paneId);
    void this.focusPane(paneId);
  }

  equalize(groupId: string) {
    this.layout = equalize(this.layout, groupId);
    this.save();
  }

  divider(groupId: string, index: number, share: number, save: boolean) {
    this.layout = setDivider(this.layout, groupId, index, share);
    if (save) this.save();
  }

  // A refusal stays visible until the next action.
  say(text: string) { this.message = text; }

  async focusPane(id = this.active?.focus) {
    await tick();
    if (!id) return;
    document.getElementById(`pane-${id}`)?.querySelector<HTMLElement>('[data-primary]')?.focus({ preventScroll: true });
  }

  reset() {
    this.sessions = {};
    this.drafts = {};
    this.zoomed = null;
    const pair = group('cols', [this.newPane('session', 'Codex'), this.newPane('session', 'Tests')], newId);
    const first: Tab = { id: newId(), root: pair, focus: pair.children[0].id };
    const logs = this.newPane('logs');
    const monitor = this.newPane('monitor');
    this.layout = { tabs: [first, { id: newId(), root: logs, focus: logs.id }, { id: newId(), root: monitor, focus: monitor.id }], active: first.id };
    this.save();
  }

  // Only committed state is saved. A hover preview and zoom never are.
  private save() {
    try { localStorage.setItem(storageKey, JSON.stringify({ layout: this.layout, sessions: this.sessions })); } catch { /* storage may be unavailable */ }
  }

  private load(): boolean {
    try {
      const saved = JSON.parse(localStorage.getItem(storageKey) ?? 'null');
      const layout = restore(saved?.layout);
      if (!layout) return false;
      this.layout = layout;
      const sessions: Record<string, Session> = {};
      for (const t of layout.tabs) for (const p of leaves(t.root)) {
        const name = p.sid && saved.sessions?.[p.sid]?.name;
        if (p.sid && typeof name === 'string') sessions[p.sid] = { name };
      }
      this.sessions = sessions;
      return true;
    } catch {
      return false;
    }
  }
}
