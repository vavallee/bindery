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
  { token: '{Genre}', descKey: 'tokenGenre' },
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
  genre: string
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
  genre: 'Fantasy',
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

// render mirrors Renamer.apply: render each "/"-separated path segment with its
// tokens substituted (sanitized where the Go code sanitizes), dropping empty
// segments. For the audiobook kind, {ext} renders empty, matching
// AudiobookDestDir which passes ext="".
export function renderTemplate(
  template: string,
  kind: TemplateKind,
  sample: SampleBook = SAMPLE_BOOK,
): string {
  const ext = kind === 'audiobook' ? '' : sample.ext
  const values: Record<string, string> = {
    Author: sanitizePath(sample.author),
    SortAuthor: sanitizePath(sample.sortAuthor),
    Title: sanitizePath(sample.title),
    Year: sample.year,
    ASIN: sanitizePath(sample.asin),
    Series: sanitizePath(sample.series),
    SeriesNumber: sanitizePath(sample.seriesNumber),
    Genre: sanitizePath(sample.genre),
    ext,
  }
  return template
    .split(SEP)
    .map(seg => renderSegment(seg, values))
    .filter(seg => seg !== '')
    .join(SEP)
}

// Group 1: token name. Group 2 (optional): default after a colon, used when the
// token renders empty ("{Genre:Unsorted}"). Mirrors templateTokenRe in renamer.go.
const SEGMENT_TOKEN_RE = /\{(\w+)(?::([^}]*))?\}/g

// renderSegment mirrors renamer.go renderSegment: substitute "{Token}"
// placeholders in a single path segment and, when the leading token(s) render
// empty, drop the separator glue that would otherwise dangle before the first
// real value ("{SeriesNumber} - {Title}" with no series number → "Title", not
// " - Title"). Only leading glue is collapsed; interior/trailing glue stays so
// "{Title} ({Year})" → "Title ()" and "{Title}.{ext}" → "Title." are preserved.
// A "{Token:default}" empty token renders its default instead. Unknown tokens
// are kept verbatim.
function renderSegment(seg: string, values: Record<string, string>): string {
  const lits: string[] = []
  const vals: string[] = []
  let prev = 0
  SEGMENT_TOKEN_RE.lastIndex = 0
  let m: RegExpExecArray | null
  while ((m = SEGMENT_TOKEN_RE.exec(seg)) !== null) {
    lits.push(seg.slice(prev, m.index))
    const v = values[m[1]]
    if (v === undefined) {
      vals.push(m[0]) // unknown token: keep "{Token}" (or "{Token:def}") verbatim
    } else if (v === '' && m[2] !== undefined) {
      vals.push(sanitizePath(m[2])) // empty known token with a default
    } else {
      vals.push(v)
    }
    prev = m.index + m[0].length
  }
  if (vals.length === 0) return seg.trim()
  lits.push(seg.slice(prev))

  // Drop the separator following each leading empty token. Stop at the first
  // non-empty value, or at a leading literal that is real text, not just glue.
  for (let i = 0; i < vals.length; i++) {
    if (vals[i] !== '') break
    if (lits[i].trim() !== '') break
    lits[i + 1] = ''
  }

  let out = ''
  for (let i = 0; i < vals.length; i++) out += lits[i] + vals[i]
  out += lits[lits.length - 1]
  return out.trim()
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
    // Strip an optional ":default" ("{Genre:Unsorted}" → "{Genre}") before the
    // supported-token check; report the original token text if still unknown.
    const norm = m.replace(/^(\{\w+):[^}]*\}$/, '$1}')
    if (!SUPPORTED.has(norm) && !unknown.includes(m)) unknown.push(m)
  }
  return {
    unknownTokens: unknown,
    empty: template.trim() === '',
    traversal: TRAVERSAL_RE.test(template),
  }
}
