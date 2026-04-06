import MarkdownIt from 'markdown-it'

const md = new MarkdownIt({
  html: false,     // XSS prevention — no raw HTML
  linkify: true,   // auto-link URLs
  breaks: true,    // newlines become <br>
})

// External links (http/https) open in a new tab
const defaultLinkOpen = md.renderer.rules.link_open || function(tokens: any, idx: any, options: any, _env: any, self: any) {
  return self.renderToken(tokens, idx, options)
}
md.renderer.rules.link_open = function(tokens: any, idx: any, options: any, env: any, self: any) {
  const href = tokens[idx].attrGet('href')
  if (href && /^https?:\/\//.test(href)) {
    tokens[idx].attrSet('target', '_blank')
    tokens[idx].attrSet('rel', 'noopener noreferrer')
  }
  return defaultLinkOpen(tokens, idx, options, env, self)
}

// File path pattern: workspace-relative paths like .spec/..., study/..., scripts/..., etc.
// Matches paths with forward slashes (we normalize backslashes before matching).
// The lookbehind includes > (for paths inside <code> tags from markdown-it) and backtick.
const FILE_PATH_RE = /(?:^|\s|["'(>\x60])((\.spec|study|scripts|docs|lessons|journal|becoming|books|callings|data|teaching|yt|\.github|private-brain|public)\/[\w./_-]+(?:\.(?:md|yaml|yml|json|go|ts|vue|js|txt|sql|css|html))?)/g

// Post-process rendered HTML to detect file paths and make them clickable.
// Normalizes backslashes to forward slashes first (Windows agent messages use \).
function linkifyFilePaths(html: string): string {
  // Normalize Windows backslashes in known path prefixes to forward slashes
  // so the regex can match them. We do this on the HTML, targeting only
  // path-like sequences (not all backslashes, which could be escape chars).
  const normalized = html.replace(
    /(\.(spec|github)|study|scripts|docs|lessons|journal|becoming|books|callings|data|teaching|yt|private-brain|public)(\\[\w._-]+)+/g,
    (m) => m.replace(/\\/g, '/')
  )

  return normalized.replace(FILE_PATH_RE, (match, path) => {
    const prefix = match.slice(0, match.length - path.length)
    return `${prefix}<a href="#" class="file-link text-sky-400 hover:text-sky-300 underline decoration-dotted" data-file-path="${escapeAttr(path)}">${escapeHtml(path)}</a>`
  })
}

function escapeHtml(s: string): string {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
}

function escapeAttr(s: string): string {
  return s.replace(/&/g, '&amp;').replace(/"/g, '&quot;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
}

export function renderMarkdown(text: string): string {
  const html = md.render(text)
  return linkifyFilePaths(html)
}
