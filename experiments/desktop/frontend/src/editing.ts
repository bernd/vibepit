export type EditCommand = 'copy' | 'paste' | 'cut' | 'select-all' | 'undo' | 'redo';
export type DesktopCommand = EditCommand | 'view-terminal' | 'view-claude' | 'view-debug';
export interface ClipboardAccess { readText(): Promise<string>; writeText(text: string): Promise<void> }
export interface TerminalEditor {
  focused(): boolean;
  alive(): boolean;
  getSelection(): string;
  selectAll(): void;
  paste(text: string): void;
  focus(): void;
}

export function shortcutCommand(event: Pick<KeyboardEvent, 'key' | 'ctrlKey' | 'metaKey' | 'shiftKey' | 'altKey' | 'isComposing' | 'defaultPrevented'>, mac: boolean): EditCommand | undefined {
  if (event.defaultPrevented || event.isComposing || event.altKey) return;
  if (mac ? !event.metaKey || event.ctrlKey : !event.ctrlKey || !event.shiftKey || event.metaKey) return;
  const key = event.key.toLowerCase();
  if (mac && event.shiftKey && key !== 'z') return;
  return ({ c: 'copy', v: 'paste', x: 'cut', a: 'select-all', z: event.shiftKey && mac ? 'redo' : 'undo', y: 'redo' } as const)[key];
}

type Field = HTMLInputElement | HTMLTextAreaElement;
function textField(element: Element | null): Field | undefined {
  if (!element?.getClientRects().length) return;
  if (element instanceof HTMLTextAreaElement || (element instanceof HTMLInputElement && element.selectionStart !== null)) return element;
}
function replaceSelection(field: Field, text: string, inputType: string) {
  // execCommand keeps the WebKit editing history and dispatches the input event Svelte binds to.
  const before = field.value;
  if (document.execCommand?.(text ? 'insertText' : 'delete', false, text)) return;
  if (field.value !== before) return;
  if (!field.dispatchEvent(new InputEvent('beforeinput', { bubbles: true, cancelable: true, inputType, data: text }))) return;
  field.setRangeText(text, field.selectionStart ?? 0, field.selectionEnd ?? 0, 'end');
  field.dispatchEvent(new InputEvent('input', { bubbles: true, inputType, data: text }));
}

export function createEditor(options: {
  clipboard: ClipboardAccess;
  terminal: () => TerminalEditor | undefined;
  context: () => string;
}) {
  return async (command: EditCommand): Promise<void> => {
    const terminal = options.terminal();
    const term = terminal?.focused() ? terminal : undefined;
    const field = term ? undefined : textField(document.activeElement);
    const context = options.context();
    const start = field?.selectionStart ?? 0;
    const end = field?.selectionEnd ?? 0;
    const value = field?.value;
    const valid = () => options.context() === context && (term ? term.alive() && term.focused() : !!field?.isConnected && document.activeElement === field && field.value === value && field.selectionStart === start && field.selectionEnd === end);
    if (command === 'copy' || command === 'cut') {
      const selection = term ? term.getSelection() : field ? field.value.slice(start, end) : document.getSelection()?.toString() ?? '';
      if (!selection) return;
      await options.clipboard.writeText(selection);
      // Terminal/transcript output is never deleted by Cut.
      if (command === 'cut' && field && !field.readOnly && !field.disabled && valid()) replaceSelection(field, '', 'deleteByCut');
      return;
    }
    if (command === 'paste') {
      if (term && !term.alive()) return;
      if (!term && (!field || field.disabled || field.readOnly)) return;
      const text = await options.clipboard.readText();
      if (!text) return;
      if (!valid()) throw new Error('Paste cancelled because its target changed. Focus the destination and paste again.');
      if (term) term.paste(text);
      else if (field) replaceSelection(field, text, 'insertFromPaste');
      return;
    }
    if (command === 'select-all') {
      if (term) term.selectAll();
      else if (field) field.select();
      else document.execCommand('selectAll');
      return;
    }
    if (field && !field.disabled && !field.readOnly) document.execCommand(command);
  };
}
