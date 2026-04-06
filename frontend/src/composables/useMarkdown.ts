import MarkdownIt from 'markdown-it'

const md = new MarkdownIt({
  html: false,     // XSS prevention — no raw HTML
  linkify: true,   // auto-link URLs
  breaks: true,    // newlines become <br>
})

// File path pattern: workspace-relative paths like .spec/..., study/..., scripts/..., etc.
// Matches paths starting with known prefixes and ending with common extensions,
// or paths containing / that end with a file extension.
const FILE_PATH_RE = /(?:^|\s|["'(])((\.spec|study|scripts|docs|lessons|journal|becoming|books|callings|data|teaching|yt|\.github|private-brain|public)\/[\w./_-]+(?:\.(?:md|yaml|yml|json|go|ts|vue|js|txt|sql|css|html))?)/g

// Post-process rendered HTML to detect file paths and make them clickable.
// Uses a data attribute so the parent component can intercept clicks.
function linkifyFilePaths(html: string): string {
  return html.replace(FILE_PATH_RE, (match, path) => {
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
