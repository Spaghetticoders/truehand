import { Marked } from 'marked'
import DOMPurify from 'dompurify'
import { revealParts } from './invisible'

const marked = new Marked({ gfm: true, breaks: true, async: false })

DOMPurify.addHook('afterSanitizeAttributes', (node) => {
  if (node.tagName === 'A') {
    node.setAttribute('target', '_blank')
    node.setAttribute('rel', 'noreferrer noopener')
  }
  if (node.tagName === 'INPUT') node.setAttribute('disabled', '')
})

/** Markdown to HTML that is always sanitized: task text comes from agents and is untrusted. */
export function markdown(src: string): string {
  // Hidden and bidi characters become visible stand-ins: what is read is what was written.
  const shown = revealParts(src)
    .map((p) => (p.hidden ? `⟦${p.text}⟧` : p.text))
    .join('')
  const html = marked.parse(shown) as string
  return DOMPurify.sanitize(html, {
    USE_PROFILES: { html: true },
    FORBID_TAGS: ['style', 'form', 'iframe', 'object', 'embed', 'svg', 'math'],
    FORBID_ATTR: ['style'],
    ALLOWED_URI_REGEXP: /^(?:https?:|mailto:|#)/i,
  })
}
