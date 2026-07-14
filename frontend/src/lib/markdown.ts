import MarkdownIt from 'markdown-it'
// Common-languages build keeps the bundle small while covering the languages
// most likely to appear in chat (go, ts/js, python, bash, json, sql, ...).
import hljs from 'highlight.js/lib/common'

// html:false means raw HTML in model output is escaped, not rendered — this is
// our primary XSS defence, so the rendered result is safe to use with v-html.
const md = new MarkdownIt({
  html: false,
  linkify: true,
  breaks: true,
  typographer: true,
})

// Render fenced code blocks with a header (language label + copy button) and
// highlight.js syntax colouring.
md.renderer.rules.fence = (tokens, idx) => {
  const token = tokens[idx]
  const info = token.info ? token.info.trim() : ''
  const lang = (info.split(/\s+/)[0] || 'text').toLowerCase()

  let body: string
  if (lang && lang !== 'text' && hljs.getLanguage(lang)) {
    body = hljs.highlight(token.content, { language: lang, ignoreIllegals: true }).value
  } else {
    body = md.utils.escapeHtml(token.content)
  }

  const label = md.utils.escapeHtml(lang)
  return (
    `<div class="code-block">` +
    `<div class="code-block__header">` +
    `<span class="code-block__lang">${label}</span>` +
    `<button class="code-block__copy" type="button">Copy</button>` +
    `</div>` +
    `<pre class="hljs"><code>${body}</code></pre>` +
    `</div>`
  )
}

// Make links open safely in a new tab.
const defaultLinkOpen =
  md.renderer.rules.link_open ||
  ((tokens, idx, options, _env, self) => self.renderToken(tokens, idx, options))

md.renderer.rules.link_open = (tokens, idx, options, env, self) => {
  tokens[idx].attrSet('target', '_blank')
  tokens[idx].attrSet('rel', 'noopener noreferrer')
  return defaultLinkOpen(tokens, idx, options, env, self)
}

/** Render a Markdown string to sanitized HTML. */
export function renderMarkdown(src: string): string {
  return md.render(src)
}
