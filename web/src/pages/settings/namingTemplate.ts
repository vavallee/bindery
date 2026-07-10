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

// The tokens supported by Renamer.apply, in display order. {ext} is
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
  { token: '{Lang}', descKey: 'tokenLang' },
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
  lang: string
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
  lang: 'en',
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
    Lang: sanitizePath(sample.lang),
    ext,
  }
  return template
    .split(SEP)
    .map(seg => renderSegment(seg, values))
    .filter(seg => seg !== '')
    .join(SEP)
}

// Mirrors templateGroupRe in renamer.go: one {...} group; renderGroup parses
// the content (simple token, token:default, token:width, or a conditional
// group with literal text alongside the token(s), #1127).
const SEGMENT_GROUP_RE = /\{([^{}]*)\}/g
const SIMPLE_GROUP_RE = /^(\w+)(?::([^{}]*))?$/
const GROUP_WORD_RE = /(\w+)(:\d{1,2})?/g

// sanitizeInline mirrors renamer.go sanitizeInline: neutralise path
// separators in a conditional-group literal without trimming — its
// whitespace is meaningful glue ("{ - Series}").
function sanitizeInline(s: string): string {
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
  return cleaned
}

// parseWidth mirrors renamer.go parseWidth: a ":modifier" of 1–2 digits
// (1–99) is a zero-pad width; longer digit strings keep the historical
// default-text meaning.
function parseWidth(mod: string): number | null {
  if (!/^\d{1,2}$/.test(mod)) return null
  const w = parseInt(mod, 10)
  return w > 0 ? w : null
}

// zeroPad mirrors renamer.go zeroPad: left-pad an all-digit value.
function zeroPad(v: string, width: number): string {
  if (v === '' || v.length >= width || !/^\d+$/.test(v)) return v
  return v.padStart(width, '0')
}

// renderGroup mirrors renamer.go renderGroup. Returns null when the group
// references no known token (caller keeps it verbatim).
function renderGroup(content: string, values: Record<string, string>): string | null {
  const simple = SIMPLE_GROUP_RE.exec(content)
  if (simple) {
    let v = values[simple[1]]
    if (v === undefined) return null
    const mod = simple[2]
    if (mod !== undefined && mod !== '') {
      const w = parseWidth(mod)
      if (w !== null) {
        v = zeroPad(v, w)
      } else if (v === '') {
        v = sanitizePath(mod)
      }
    }
    return v
  }

  let anyKnown = false
  let anyValue = false
  let out = ''
  let prev = 0
  GROUP_WORD_RE.lastIndex = 0
  let m: RegExpExecArray | null
  while ((m = GROUP_WORD_RE.exec(content)) !== null) {
    let v = values[m[1]]
    if (v === undefined) continue // non-keyword word run: stays literal
    anyKnown = true
    out += sanitizeInline(content.slice(prev, m.index))
    if (m[2]) {
      const w = parseWidth(m[2].slice(1))
      if (w !== null) v = zeroPad(v, w)
    }
    if (v !== '') anyValue = true
    out += v
    prev = m.index + m[0].length
  }
  if (!anyKnown) return null
  if (!anyValue) return ''
  return out + sanitizeInline(content.slice(prev))
}

// renderSegment mirrors renamer.go renderSegment: substitute "{...}" groups
// in a single path segment and, when the leading group(s) render empty, drop
// the separator glue that would otherwise dangle before the first real value
// ("{SeriesNumber} - {Title}" with no series number → "Title", not
// " - Title"). Only leading glue is collapsed; interior/trailing glue stays so
// "{Title} ({Year})" → "Title ()" and "{Title}.{ext}" → "Title." are preserved.
// Groups with no known token are kept verbatim.
function renderSegment(seg: string, values: Record<string, string>): string {
  const lits: string[] = []
  const vals: string[] = []
  let prev = 0
  SEGMENT_GROUP_RE.lastIndex = 0
  let m: RegExpExecArray | null
  while ((m = SEGMENT_GROUP_RE.exec(seg)) !== null) {
    lits.push(seg.slice(prev, m.index))
    const rendered = renderGroup(m[1], values)
    vals.push(rendered === null ? m[0] : rendered)
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

// groupIsKnown reports whether one {...} group references at least one
// supported token — a simple "{Token}"/"{Token:mod}" with a known keyword, or
// a conditional group (#1127) whose literal text sits alongside a known
// keyword. Mirrors renderGroup's known/verbatim decision.
function groupIsKnown(content: string): boolean {
  const simple = SIMPLE_GROUP_RE.exec(content)
  if (simple) return SUPPORTED.has(`{${simple[1]}}`)
  GROUP_WORD_RE.lastIndex = 0
  let m: RegExpExecArray | null
  while ((m = GROUP_WORD_RE.exec(content)) !== null) {
    if (SUPPORTED.has(`{${m[1]}}`)) return true
  }
  return false
}

// validateTemplate flags any {...} group that references no supported token,
// an empty template, and explicit path-traversal segments. These mirror the
// failure modes the backend would reject (ensureContained) or that produce
// surprising output (a verbatim "{Titel}" in the folder name).
export function validateTemplate(template: string): ValidationResult {
  const matches = template.match(TOKEN_RE) ?? []
  const unknown: string[] = []
  for (const m of matches) {
    if (!groupIsKnown(m.slice(1, -1)) && !unknown.includes(m)) unknown.push(m)
  }
  return {
    unknownTokens: unknown,
    empty: template.trim() === '',
    traversal: TRAVERSAL_RE.test(template),
  }
}
