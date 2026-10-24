import { describe, it, expect } from 'vitest'
import ts from 'typescript'

// Visible UI text must go through t() so every locale can translate it
// (#3411). A sweep found several hundred strings that never had, because
// nothing failed when a component rendered English straight from JSX.
//
// This flags the two shapes that reach the screen directly: JSX text between
// tags, and string literals in the attributes a user sees or hears (title,
// aria-label, placeholder, alt, label). Strings built in code, such as error
// fallbacks, are not covered; the parser cannot tell them from identifiers.

const sources = import.meta.glob('../**/*.tsx', { query: '?raw', import: 'default', eager: true }) as Record<string, string>

const ATTRIBUTES = new Set(['title', 'aria-label', 'placeholder', 'alt', 'label'])

// Names and sample input that read the same in every language. Paths, URLs,
// environment variables and acronyms pass without a listing; see codeLike.
const ALLOWED = new Set([
  'Bindery', 'Audiobookshelf', 'Calibre', 'Calibre-Web-Automated (CWA)', 'Grimmory', 'Hardcover',
  'OpenLibrary', 'Prowlarr', 'SABnzbd', 'NZBGet', 'qBittorrent', 'Transmission', 'Deluge',
  'rTorrent / ruTorrent', 'Google', 'google', 'en', 'my-topic', 'Fantasy, Epic',
])

// A naming template, or a single token holding a path, URL, key or digit, or
// written in capitals, is input syntax or an acronym rather than prose.
function codeLike(text: string): boolean {
  if (/\{\w+\}/.test(text) || /\{\w+:\d+\}/.test(text)) return true
  return !/\s/.test(text) && (/[/:_{.@\d]/.test(text) || text === text.toUpperCase())
}

// Files left out on purpose, each with its reason.
const SKIPPED_FILES = new Map([
  // Diagnostic output meant to be pasted into issues; whether to translate it
  // is an open question on #3411.
  ['../components/SearchDebugPanel.tsx', 'pending a decision on #3411'],
])

// Text with at least two letters in a row, so arrows, bullets, digits and
// punctuation pass.
const WORDY = /\p{L}{2,}/u

function findings(file: string, source: string): string[] {
  const sf = ts.createSourceFile(file, source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX)
  const out: string[] = []
  const report = (node: ts.Node, text: string) => {
    if (!WORDY.test(text) || ALLOWED.has(text) || codeLike(text)) return
    const { line } = sf.getLineAndCharacterOfPosition(node.getStart(sf))
    out.push(`${file}:${line + 1}: ${text}`)
  }
  const visit = (node: ts.Node) => {
    if (ts.isJsxText(node)) {
      const text = node.text.replace(/&\w+;/g, ' ').replace(/\s+/g, ' ').trim()
      if (text) report(node, text)
    } else if (ts.isJsxAttribute(node) && node.initializer && ts.isStringLiteral(node.initializer)) {
      if (ATTRIBUTES.has(node.name.getText(sf))) report(node, node.initializer.text)
    }
    ts.forEachChild(node, visit)
  }
  visit(sf)
  return out
}

describe('hard-coded UI strings', () => {
  it('are not rendered from JSX text or user-facing attributes', () => {
    const found: string[] = []
    let scanned = 0
    for (const [file, source] of Object.entries(sources)) {
      if (/\.test\.tsx$/.test(file) || SKIPPED_FILES.has(file)) continue
      scanned++
      found.push(...findings(file, source))
    }
    // Guards the scanner itself: an empty glob would pass silently.
    expect(scanned).toBeGreaterThan(100)
    expect(found).toEqual([])
  })

  it('catches the shapes it is meant to catch', () => {
    const sample = [
      'const a = <p>Save changes</p>',
      'const b = <input placeholder="Search books" />',
      'const c = <p>{t("x.y", "Fine")} → SABnzbd</p>',
      'const d = <option>Transmission</option>',
      'const e = <input placeholder="/data/books" title="ASIN" />',
    ].join('\n')
    expect(findings('sample.tsx', sample)).toEqual([
      'sample.tsx:1: Save changes',
      'sample.tsx:2: Search books',
      'sample.tsx:3: → SABnzbd',
    ])
  })
})
