import { describe, it, expect, vi, afterEach } from 'vitest'
// Vite's ?raw import rather than node:fs, so this needs no @types/node.
// src/i18n/inlineDefaults.test.ts reads sources the same way.
import html from '../index.html?raw'

// index.html carries an inline script that sets the `dark` class before the
// first paint. It exists because useTheme applies the class from an effect,
// which runs after the browser has already painted the light background.
//
// The bootstrap duplicates readInitial()'s rule by necessity: it runs before
// any module has loaded, so it cannot import it. This test is the thing that
// stops the two drifting, which is the whole risk of duplicating a rule.

function bootstrapSource(): string {
  const scripts = [...html.matchAll(/<script>([\s\S]*?)<\/script>/g)].map(m => m[1])
  const found = scripts.find(s => s.includes('bindery.theme'))
  if (!found) throw new Error('no inline theme bootstrap found in index.html')
  return found
}

/** Runs the real bootstrap source against a fake document and returns the resulting class state. */
function runBootstrap(opts: { saved: string | null; prefersDark: boolean; storageThrows?: boolean }): boolean {
  let isDark = false
  const documentStub = {
    documentElement: {
      classList: {
        toggle: (_cls: string, on: boolean) => { isDark = on },
      },
    },
  }
  const windowStub = {
    matchMedia: (q: string) => ({ matches: q.includes('dark') ? opts.prefersDark : false }),
  }
  const localStorageStub = {
    getItem: (k: string) => {
      if (opts.storageThrows) throw new DOMException('blocked', 'SecurityError')
      return k === 'bindery.theme' ? opts.saved : null
    },
  }
  new Function('document', 'window', 'localStorage', bootstrapSource())(documentStub, windowStub, localStorageStub)
  return isDark
}

/** readInitial()'s rule, restated. Kept separate so a change to one side fails loudly. */
function expectedDark(saved: string | null, prefersDark: boolean): boolean {
  if (saved === 'light' || saved === 'dark') return saved === 'dark'
  return prefersDark
}

afterEach(() => vi.restoreAllMocks())

describe('index.html theme bootstrap', () => {
  it('is present, and is a plain script so it runs before the first paint', () => {
    expect(() => bootstrapSource()).not.toThrow()
    // A module script is deferred and would run after paint, defeating the point.
    expect(html).not.toMatch(/<script type="module">[\s\S]*bindery\.theme/)
    // It must come before the app bundle, or the effect wins the race anyway.
    expect(html.indexOf('bindery.theme')).toBeLessThan(html.indexOf('src/main.tsx'))
  })

  it.each([
    { saved: 'dark', prefersDark: false },
    { saved: 'dark', prefersDark: true },
    { saved: 'light', prefersDark: true },
    { saved: 'light', prefersDark: false },
    { saved: null, prefersDark: true },
    { saved: null, prefersDark: false },
    { saved: 'garbage', prefersDark: true },
    { saved: 'garbage', prefersDark: false },
  ])('agrees with readInitial for saved=$saved prefersDark=$prefersDark', ({ saved, prefersDark }) => {
    expect(runBootstrap({ saved, prefersDark })).toBe(expectedDark(saved, prefersDark))
  })

  it('falls back to the OS preference when storage is blocked, instead of throwing', () => {
    expect(runBootstrap({ saved: null, prefersDark: true, storageThrows: true })).toBe(true)
    expect(runBootstrap({ saved: null, prefersDark: false, storageThrows: true })).toBe(false)
  })
})
