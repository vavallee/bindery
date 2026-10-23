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

// Matches the key of any t('a.b.c' …) or t("a.b.c" …) call, whatever follows it.
const KEY_RE = /\bt\(\s*['"]([A-Za-z0-9_.]+)['"]/g

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
        if (typeof value !== 'string') continue // missing keys are the next test's job
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

  // A key missing from en.json still renders, from the inline default, so
  // nothing looks wrong in English. But every other locale is translated from
  // en.json, so a key that never lands there never gets translated: nb was
  // found missing 68 of them this way. Template-literal keys (t(`a.${b}`)) are
  // not checked.
  it('exist in en.json for every static key', () => {
    const missing = new Set<string>()
    let checked = 0

    for (const [file, source] of Object.entries(sources)) {
      if (/\.test\.tsx?$/.test(file)) continue
      for (const [, key] of source.matchAll(KEY_RE)) {
        checked++
        // Plural keys live in en.json as key_one / key_other.
        if (typeof lookup(key) !== 'string' && typeof lookup(`${key}_other`) !== 'string') {
          missing.add(`${file} — ${key}`)
        }
      }
    }

    expect(checked).toBeGreaterThan(1000)
    expect([...missing], 'Add these keys to en.json so they can be translated:').toEqual([])
  })
})
