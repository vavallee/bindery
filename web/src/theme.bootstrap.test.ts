import { describe, it, expect, vi, afterEach } from 'vitest'
// Vite's ?raw import rather than node:fs, so this needs no @types/node.
// src/i18n/inlineDefaults.test.ts reads sources the same way.
import html from '../index.html?raw'
import bootstrap from '../public/theme-bootstrap.js?raw'
import { THEME_COLORS } from './theme'

// index.html loads public/theme-bootstrap.js, a script that sets the `dark`
// class before the first paint. It exists because useTheme applies the class from an effect,
// which runs after the browser has already painted the light background.
//
// The bootstrap duplicates readInitial()'s rule by necessity: it runs before
// any module has loaded, so it cannot import it. This test is the thing that
// stops the two drifting, which is the whole risk of duplicating a rule.

const START = '/* theme-bootstrap:start */'
const END = '/* theme-bootstrap:end */'

function bootstrapSource(): string {
  // Sliced between the two markers rather than matched with an HTML-ish
  // regexp: the markers are an explicit contract with index.html and there is
  // no tag parsing to get wrong.
  const from = bootstrap.indexOf(START)
  const to = bootstrap.indexOf(END)
  if (from < 0 || to < from) throw new Error('no theme bootstrap markers found in public/theme-bootstrap.js')
  return bootstrap.slice(from + START.length, to)
}

/** Runs the real bootstrap source against a fake document and returns the resulting class state. */
function runBootstrap(opts: { saved: string | null; prefersDark: boolean; storageThrows?: boolean }): boolean {
  return runBootstrapWithMetas(opts).isDark
}

/** As runBootstrap, also returning the content the bootstrap gave each theme-color meta. */
function runBootstrapWithMetas(opts: { saved: string | null; prefersDark: boolean; storageThrows?: boolean }) {
  let isDark = false
  const metas = [{ content: 'light-default' }, { content: 'dark-default' }]
  const documentStub = {
    documentElement: {
      classList: {
        toggle: (_cls: string, on: boolean) => { isDark = on },
      },
    },
    querySelectorAll: (selector: string) =>
      selector === 'meta[name="theme-color"]'
        ? metas.map(meta => ({ setAttribute: (name: string, value: string) => { if (name === 'content') meta.content = value } }))
        : [],
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
  return { isDark, metaContents: metas.map(meta => meta.content) }
}

/** readInitial()'s rule, restated. Kept separate so a change to one side fails loudly. */
function expectedDark(saved: string | null, prefersDark: boolean): boolean {
  if (saved === 'light' || saved === 'dark') return saved === 'dark'
  return prefersDark
}

afterEach(() => vi.restoreAllMocks())

describe('index.html theme bootstrap', () => {
  it('has no inline script, because the CSP (script-src \'self\') blocks one (#2911)', () => {
    // Every <script> in index.html must load from a src. An inline one is
    // refused by the browser and logs a CSP violation on every page load.
    const scripts = html.match(/<script\b[^>]*>/g) ?? []
    expect(scripts.length).toBeGreaterThan(0)
    for (const tag of scripts) expect(tag).toMatch(/\ssrc=/)
  })

  it('is present, and is a plain script so it runs before the first paint', () => {
    expect(() => bootstrapSource()).not.toThrow()
    // A module script is deferred and would run after paint, defeating the point.
    const tag = html.match(/<script\b[^>]*theme-bootstrap\.js[^>]*>/)?.[0]
    expect(tag).toBeDefined()
    expect(tag).not.toMatch(/type="module"|\sdefer|\sasync/)
    // It must come before the app bundle, or the effect wins the race anyway.
    expect(html.indexOf('theme-bootstrap.js')).toBeLessThan(html.indexOf('src/main.tsx'))
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

  it.each([
    { saved: 'dark', prefersDark: false },
    { saved: 'light', prefersDark: true },
    { saved: null, prefersDark: true },
    { saved: null, prefersDark: false },
  ])('sets every theme-color meta to THEME_COLORS for saved=$saved prefersDark=$prefersDark', ({ saved, prefersDark }) => {
    // Before the app renders, the browser bar must already match a stored
    // choice that overrides the OS, in the same colours useTheme applies.
    const want = THEME_COLORS[expectedDark(saved, prefersDark) ? 'dark' : 'light']
    expect(runBootstrapWithMetas({ saved, prefersDark }).metaContents).toEqual([want, want])
  })

  it('runs after the theme-color metas it rewrites', () => {
    const lastMeta = html.lastIndexOf('name="theme-color"')
    expect(lastMeta).toBeGreaterThan(-1)
    expect(lastMeta).toBeLessThan(html.indexOf('theme-bootstrap.js'))
  })

  it('falls back to the OS preference when storage is blocked, instead of throwing', () => {
    expect(runBootstrap({ saved: null, prefersDark: true, storageThrows: true })).toBe(true)
    expect(runBootstrap({ saved: null, prefersDark: false, storageThrows: true })).toBe(false)
  })
})
