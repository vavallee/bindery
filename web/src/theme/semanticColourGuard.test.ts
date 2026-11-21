import { describe, it, expect } from 'vitest'
import { RAW_STATUS_ALLOWLIST } from './rawStatusAllowlist'

// A raw status palette class is a colour utility in a status hue: text-red-700,
// dark:bg-emerald-900/30, hover:border-amber-300, ring-sky-500 and so on. The
// status colours are tokens now (#3994, src/index.css), so a new raw class means
// someone skipped them and the next accent or theme change has one more place
// to find. This fails on one in any file that is not on the allowlist.
const RAW_STATUS_CLASS = /(?<![\w-])(?:[a-z-]+:)*(?:text|bg|border|ring|outline|divide|from|via|to|fill|stroke|placeholder|decoration|accent|shadow)-(?:amber|rose|sky|emerald|red|green|yellow|blue)-\d{2,3}(?:\/\d+)?(?![\w-])/g

const sources = import.meta.glob('../**/*.{ts,tsx,css}', { query: '?raw', import: 'default', eager: true }) as Record<string, string>

// A comment may quote an old class string, as buttons.ts does for the bug it
// fixed. Only code is checked.
function code(source: string): string {
  return source.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '')
}

function rawClassesByFile(): Map<string, string[]> {
  const found = new Map<string, string[]>()
  for (const [path, source] of Object.entries(sources)) {
    const file = path.replace(/^\.\.\//, '')
    if (/\.test\.tsx?$/.test(file)) continue
    const hits = code(source).match(RAW_STATUS_CLASS)
    if (hits) found.set(file, [...new Set(hits)])
  }
  return found
}

describe('raw status palette classes (#3994)', () => {
  it('recognises the classes it is meant to catch, and not the tokens', () => {
    const hit = (s: string) => (s.match(RAW_STATUS_CLASS) ?? []).length
    expect(hit('text-red-700')).toBe(1)
    expect(hit('dark:hover:bg-emerald-900/30')).toBe(1)
    expect(hit('border-amber-300 ring-sky-500')).toBe(2)
    expect(hit('focus-visible:outline-blue-600')).toBe(1)
    expect(hit('text-danger bg-success-subtle border-warning-border ring-accent-ring')).toBe(0)
    expect(hit('text-slate-700 bg-zinc-900 text-indigo-800 bg-violet-100')).toBe(0)
    expect(hit('text-red')).toBe(0)
  })

  it('scans the source (so an empty result means something)', () => {
    expect(Object.keys(sources).length).toBeGreaterThan(200)
  })

  it('appear only in the allowlisted files', () => {
    const offenders = [...rawClassesByFile()]
      .filter(([file]) => !(file in RAW_STATUS_ALLOWLIST))
      .map(([file, hits]) => `${file}: ${hits.join(' ')}`)
    expect(
      offenders,
      `Use a status token from src/index.css (text-danger, bg-success-subtle, border-warning-border, bg-info-fill with text-on-fill and so on) instead of a raw palette class. If the colour is a category or a brand colour and not a status, add the file to src/theme/rawStatusAllowlist.ts with the reason.`,
    ).toEqual([])
  })

  it('every allowlisted file still needs its place', () => {
    const found = rawClassesByFile()
    const stale = Object.keys(RAW_STATUS_ALLOWLIST).filter(file => !found.has(file))
    expect(stale, 'These files no longer write a raw status class; remove them from rawStatusAllowlist.ts').toEqual([])
  })

  it('every allowlisted file gives a reason', () => {
    for (const [file, why] of Object.entries(RAW_STATUS_ALLOWLIST)) {
      expect(why.length, file).toBeGreaterThan(20)
    }
  })
})
