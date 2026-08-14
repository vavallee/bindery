import { describe, it, expect } from 'vitest'
import en from './locales/en.json'

// Every `t('some.key', 'inline default')` call whose key also exists in
// en.json must carry the *same* text.
//
// i18next only falls back to the inline default when the key is missing, so
// once a key is in en.json the inline string is dead code that still reads as
// authoritative. Updating it and not the JSON changes nothing a user ever
// sees. That has now bitten three separate times while enumerating download
// clients — most recently the Torznab protocol-mismatch banner, which kept
// telling users rTorrent was not a valid torrent client long after the
// component was "fixed" to say it was.
//
// Reviewing for it does not work: the two strings live in different files, and
// the component tests mock react-i18next to return the fallback, so they pass
// either way. This asserts the invariant directly instead.

const sources = import.meta.glob('../**/*.{ts,tsx}', { query: '?raw', import: 'default', eager: true }) as Record<string, string>

// Matches t('a.b.c', 'default') with single-quoted arguments, allowing escaped
// quotes inside the default. Defaults built from template literals, JSX or
// concatenation are skipped — this is a guard, not a parser.
const CALL_RE = /\bt\(\s*'([A-Za-z0-9_.]+)'\s*,\s*'((?:[^'\\]|\\.)*)'\s*\)/g

function unescape(raw: string): string {
  return raw.replace(/\\'/g, "'").replace(/\\n/g, '\n').replace(/\\\\/g, '\\')
}

function lookup(key: string): unknown {
  return key.split('.').reduce<unknown>(
    (node, part) => (node && typeof node === 'object' ? (node as Record<string, unknown>)[part] : undefined),
    en,
  )
}

describe('inline i18next defaults', () => {
  it('match the en.json value for keys that exist', () => {
    const mismatches: string[] = []
    let checked = 0

    for (const [file, source] of Object.entries(sources)) {
      if (/\.test\.tsx?$/.test(file)) continue
      for (const [, key, raw] of source.matchAll(CALL_RE)) {
        const value = lookup(key)
        if (typeof value !== 'string') continue // key not in en.json: the default is live
        checked++
        const inline = unescape(raw)
        if (value !== inline) {
          mismatches.push(`${file} — ${key}\n  en.json renders: ${value}\n  inline default:  ${inline}`)
        }
      }
    }

    // Sanity check on the scanner itself: if a refactor moves every call out of
    // this shape, an empty scan would pass silently and the guard would be gone.
    expect(checked).toBeGreaterThan(100)
    expect(
      mismatches,
      `Update en.json (it is what renders), not just the inline default:\n\n${mismatches.join('\n\n')}`,
    ).toEqual([])
  })
})
