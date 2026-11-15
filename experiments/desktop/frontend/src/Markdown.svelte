<script lang="ts">
  import { Browser } from '@wailsio/runtime';
  import { renderMarkdown } from './markdown';
  let { text }: { text: string } = $props();
  let linkError = $state('');
  function externalLinks(element: HTMLElement) {
    function click(event: MouseEvent) {
      const anchor = (event.target as Element).closest('a');
      if (!anchor) return;
      event.preventDefault();
      const href = anchor.getAttribute('href');
      if (href && /^https?:\/\//i.test(href)) void Browser.OpenURL(href).catch(() => { linkError = 'Could not open link in your browser.'; });
    }
    element.addEventListener('click', click);
    return () => element.removeEventListener('click', click);
  }
  const html = $derived(renderMarkdown(text));
</script>

<div class="markdown" {@attach externalLinks}>{@html html}</div>
{#if linkError}<p role="alert">{linkError}</p>{/if}

<style>
  .markdown { line-height: 1.7; overflow-wrap: anywhere; }
  .markdown :global(p), .markdown :global(ul), .markdown :global(ol), .markdown :global(pre), .markdown :global(blockquote), .markdown :global(table) { margin: 0 0 14px; }
  .markdown :global(h1), .markdown :global(h2), .markdown :global(h3), .markdown :global(h4) { margin: 22px 0 10px; color: var(--text); font-weight: 600; }
  .markdown :global(h1) { font-size: 21px; } .markdown :global(h2) { font-size: 18px; } .markdown :global(h3) { font-size: 15px; }
  .markdown :global(ul), .markdown :global(ol) { padding-left: 24px; }
  .markdown :global(li) { margin-bottom: 5px; }
  .markdown :global(code) { font-size: .9em; background: var(--panel); padding: 2px 5px; border-radius: 3px; }
  .markdown :global(pre) { padding: 14px; overflow: auto; border: 1px solid var(--line); background: var(--panel); white-space: pre; }
  .markdown :global(pre code) { padding: 0; background: none; }
  .markdown :global(blockquote) { padding-left: 14px; border-left: 2px solid var(--accent); color: var(--muted); }
  .markdown :global(a) { color: var(--accent); text-decoration: underline; }
  .markdown :global(table) { display: block; max-width: 100%; overflow-x: auto; border-collapse: collapse; }
  .markdown :global(th), .markdown :global(td) { padding: 8px 12px; border: 1px solid var(--line); text-align: left; }
  .markdown :global(hr) { border: 0; border-top: 1px solid var(--line); margin: 20px 0; }
</style>
