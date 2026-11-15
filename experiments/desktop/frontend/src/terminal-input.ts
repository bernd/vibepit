// Match wterm's clipboard handling for paste commands from the native menu.
export function terminalPaste(text: string, bracketed: boolean): string {
  if (!text || !bracketed) return text;
  return `\x1b[200~${text.replace(/\x1b/g, '')}\x1b[201~`;
}

export function terminalBytes(data: Uint8Array): string {
  return btoa(Array.from(data, byte => String.fromCharCode(byte)).join(''));
}
