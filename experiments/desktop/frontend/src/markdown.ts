import { marked } from 'marked';
import DOMPurify from 'dompurify';

export function renderMarkdown(text: string): string {
  const html = marked.parse(text, { async: false, gfm: true });
  // No images, forms or raw active content inside the privileged desktop webview.
  return DOMPurify.sanitize(html, {
    ALLOWED_TAGS: ['p','br','strong','em','del','blockquote','pre','code','ul','ol','li','h1','h2','h3','h4','h5','h6','hr','table','thead','tbody','tr','th','td','a'],
    ALLOWED_ATTR: ['href','title','start'],
    ALLOWED_URI_REGEXP: /^https?:\/\//i,
  });
}
