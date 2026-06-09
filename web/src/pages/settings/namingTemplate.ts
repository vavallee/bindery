// Client-side mirror of the Go naming-template engine in
// internal/importer/renamer.go (Renamer.apply + sanitizePath). Used purely
// for the live preview + validation in the File Naming settings section.
//
// IMPORTANT: This must stay byte-for-byte faithful to renamer.go. A Go test
// (internal/importer/renamer_preview_test.go) pins the renderer output for the
// SAMPLE_BOOK fixture below, so any drift in apply/sanitizePath that this file
// does not mirror will fail CI. Keep the two in lockstep.

export interface NamingToken {
  /** The literal token text including braces, e.g. "{Author}". */
  token: string
  /** i18n key suffix for the token's one-line description. */
  descKey: string
  /** True when the token is ignored for the audiobook (folder) template. */
  ebookOnly?: boolean
}

// The 8 tokens supported by Renamer.apply, in display order. {ext} is
// ebook-only: the audiobook template names a directory, so the renderer
// substitutes it with the empty string there.
export const NAMING_TOKENS: readonly NamingToken[] = [
  { token: '{Author}', descKey: 'tokenAuthor' },
  { token: '{SortAuthor}', descKey: 'tokenSortAuthor' },
  { token: '{Title}', descKey: 'tokenTitle' },
  { token: '{Year}', descKey: 'tokenYear' },
  { token: '{ASIN}', descKey: 'tokenAsin' },
  { token: '{Series}', descKey: 'tokenSeries' },
  { token: '{SeriesNumber}', descKey: 'tokenSeriesNumber' },
  { token: '{ext}', descKey: 'tokenExt', ebookOnly: true },
] as const

const SUPPORTED = new Set(NAMING_TOKENS.map(t => t.token))

export type TemplateKind = 'book' | 'audiobook'

// SampleBook mirrors the canned fixture used by the preview. Field names match
// the substitution tokens. Keep in sync with renamer_preview_test.go.
export interface SampleBook {
  author: string
  sortAuthor: string
  title: string
  year: string
  asin: string
  series: string
  seriesNumber: string
  ext: string
}

export const SAMPLE_BOOK: SampleBook = {
  author: 'Jane Doe',
  // authorSortName("Jane Doe") => "Doe, Jane"
  sortAuthor: 'Doe, Jane',
  title: 'Sample Book',
  year: '2024',
  asin: 'B01ABCDEFG',
  series: 'Demo Series',
  seriesNumber: '2',
  ext: 'epub',
}

// sanitizePath mirrors renamer.go sanitizePath: strip the problematic chars,
// trim, then drop empty / "." / ".." path segments. The path separator is "/"
// (preview always renders POSIX-style paths regardless of host OS).
const SEP = '/'

export function sanitizePath(s: string): string {
  // strings.NewReplacer: "/"->"-", "\\"->"-", ":"->"-", and *?"<>| -> "".
  let cleaned = ''
  for (const ch of s) {
    switch (ch) {
      case '/':
      case '\\':
      case ':':
        cleaned += '-'
        break
      case '*':
      case '?':
      case '"':
      case '<':
      case '>':
      case '|':
        break
      default:
        cleaned += ch
    }
  }
  cleaned = cleaned.trim()
  const parts = cleaned.split(SEP)
  const kept: string[] = []
  for (let p of parts) {
    p = p.trim()
    if (p === '' || p === '.' || p === '..') continue
    kept.push(p)
  }
  if (kept.length === 0) return ''
  return kept.join(SEP)
}

// render mirrors Renamer.apply: substitute each token (sanitized where the Go
// code sanitizes) for the given sample. For the audiobook kind, {ext} renders
// empty, matching AudiobookDestDir which passes ext="".
export function renderTemplate(
  template: string,
  kind: TemplateKind,
  sample: SampleBook = SAMPLE_BOOK,
): string {
  const ext = kind === 'audiobook' ? '' : sample.ext
  let result = template
  result = replaceAll(result, '{Author}', sanitizePath(sample.author))
  result = replaceAll(result, '{SortAuthor}', sanitizePath(sample.sortAuthor))
  result = replaceAll(result, '{Title}', sanitizePath(sample.title))
  result = replaceAll(result, '{Year}', sample.year)
  result = replaceAll(result, '{ASIN}', sanitizePath(sample.asin))
  result = replaceAll(result, '{Series}', sanitizePath(sample.series))
  result = replaceAll(result, '{SeriesNumber}', sanitizePath(sample.seriesNumber))
  result = replaceAll(result, '{ext}', ext)
  return result
}

function replaceAll(s: string, from: string, to: string): string {
  return s.split(from).join(to)
}

export interface ValidationResult {
  /** Tokens of the form {Foo} present in the template but not supported. */
  unknownTokens: string[]
  /** True when the template is empty after trimming. */
  empty: boolean
  /** True when the template contains a "." or ".." traversal segment. */
  traversal: boolean
}

const TOKEN_RE = /\{[^{}]*\}/g
const TRAVERSAL_RE = /(^|\/)\.\.?($|\/)/

// validateTemplate flags any {Foo} not in the supported set, an empty template,
// and explicit path-traversal segments. These mirror the failure modes the
// backend would reject (ensureContained) or that produce surprising output.
export function validateTemplate(template: string): ValidationResult {
  const matches = template.match(TOKEN_RE) ?? []
  const unknown: string[] = []
  for (const m of matches) {
    if (!SUPPORTED.has(m) && !unknown.includes(m)) unknown.push(m)
  }
  return {
    unknownTokens: unknown,
    empty: template.trim() === '',
    traversal: TRAVERSAL_RE.test(template),
  }
}
