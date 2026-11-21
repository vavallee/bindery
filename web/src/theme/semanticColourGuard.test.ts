import { describe, it, expect } from 'vitest'
import { RAW_STATUS_ALLOWLIST } from './rawStatusAllowlist'

// A raw palette colour is a colour utility in any Tailwind hue (text-red-700,
// dark:bg-orange-900/30, hover:border-amber-300, ring-sky-500), an arbitrary
// colour value (bg-[#dc2626], text-[rgb(220,38,38)]), or a class built at run
// time ('text-' + hue, `text-${hue}-700`) that no scan can read. The status
// colours are tokens now (#3994, src/index.css), so a new one means someone
// skipped them and the next accent or theme change has one more place to find.
// This fails on one in any file that is not on the allowlist.
const UTILITY = '(?:text|bg|border(?:-[xysetblr])?|ring(?:-offset)?|inset-ring|inset-shadow|drop-shadow|outline|divide|from|via|to|fill|stroke|placeholder|decoration|accent|caret|shadow)'
const HUES = '(?:red|orange|amber|yellow|lime|green|emerald|teal|cyan|sky|blue|indigo|violet|purple|fuchsia|pink|rose)'
const START = '(?<![\\w-])(?:[a-z-]+:)*'
export const RAW_PALETTE_CLASS = new RegExp(`${START}${UTILITY}-${HUES}-\\d{2,3}(?:/\\d+)?(?![\\w-])`, 'g')
export const RAW_ARBITRARY_COLOUR = new RegExp(`${START}${UTILITY}-\\[(?:#|rgba?\\(|hsla?\\(|oklch\\(|oklab\\(|color\\()[^\\]]*\\]`, 'g')
// text-${hue}-700, and 'text-' + hue or "bg-" + hue + "-700": a class name
// assembled from parts. Only the colour utilities are looked for, and a
// template that merely builds a date (`${year}-${month}`) has none.
export const DYNAMIC_COLOUR_CLASS = new RegExp(
  `${START}${UTILITY}-\\$\\{|['"\`](?<![\\w-])${UTILITY}-['"\`]\\s*\\+|['"\`]${UTILITY}-['"\`]\\s*\\+|\\$\\{[^}]*\\}-(?:50|[1-9]00|950)(?![\\w-])`,
  'g',
)
const PATTERNS = [RAW_PALETTE_CLASS, RAW_ARBITRARY_COLOUR, DYNAMIC_COLOUR_CLASS]

const sources = import.meta.glob('../**/*.{ts,tsx,css}', { query: '?raw', import: 'default', eager: true }) as Record<string, string>

// A comment may quote an old class string, as buttons.ts does for the bug it
// fixed. Only code is checked.
function code(source: string): string {
  return source.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '')
}

function hitsIn(source: string): string[] {
  const text = code(source)
  return PATTERNS.flatMap(p => text.match(p) ?? [])
}

function rawClassesByFile(): Map<string, string[]> {
  const found = new Map<string, string[]>()
  for (const [path, source] of Object.entries(sources)) {
    const file = path.replace(/^\.\.\//, '')
    if (/\.test\.tsx?$/.test(file)) continue
    const hits = hitsIn(source)
    if (hits.length) found.set(file, [...new Set(hits)])
  }
  return found
}

describe('raw palette colours (#3994)', () => {
  // One probe per way of writing a raw colour. Each must be caught, so a gap in
  // a pattern shows up here and not in review.
  it.each([
    ['a status hue', 'text-red-700'],
    ['a variant and an opacity', 'dark:hover:bg-emerald-900/30'],
    ['a border', 'border-amber-300'],
    ['a ring', 'ring-sky-500'],
    ['an outline with a variant', 'focus-visible:outline-blue-600'],
    ['orange', 'bg-orange-100'],
    ['lime', 'text-lime-700'],
    ['teal', 'border-teal-300'],
    ['cyan', 'bg-cyan-500'],
    ['pink', 'text-pink-600'],
    ['fuchsia', 'bg-fuchsia-100'],
    ['indigo', 'bg-indigo-600'],
    ['a caret', 'caret-red-500'],
    ['a side border', 'border-t-red-500'],
    ['a horizontal border', 'dark:border-x-amber-300/40'],
    ['an end border', 'border-e-emerald-500'],
    ['a ring offset', 'ring-offset-sky-200'],
    ['an inset ring', 'inset-ring-red-500'],
    ['an inset shadow', 'inset-shadow-blue-500'],
    ['a decoration', 'decoration-rose-500'],
    ['an accent', 'accent-emerald-600'],
    ['a placeholder', 'placeholder-amber-500'],
    ['an outline', 'outline-orange-500'],
    ['an arbitrary side border', 'border-l-[#dc2626]'],
    ['a bare interpolated hue', 'className={`${c}-700`}'],
    ['a bare interpolated hue with a variant', 'dark:${c}-400'],
    ['an arbitrary hex', 'text-[#dc2626]'],
    ['an arbitrary hex with a variant', 'dark:hover:bg-[#023a2d]'],
    ['an arbitrary rgb', 'bg-[rgb(220,38,38)]'],
    ['an interpolated hue', 'className={`text-${hue}-700`}'],
    ['an interpolated shade', 'const c = `bg-${tone}`'],
    ['a concatenated class', "const c = 'text-' + 'red-700'"],
    ['a concatenated class in double quotes', 'const c = "bg-" + hue'],
  ])('catches %s', (_name, probe) => {
    expect(hitsIn(probe), probe).not.toEqual([])
  })

  it('does not flag the tokens, greys, sizes, dates or an ordinary string', () => {
    for (const ok of [
      'text-danger bg-success-subtle border-warning-border ring-accent-ring bg-caution-chip text-link',
      'text-slate-700 bg-zinc-900 border-gray-300',
      'text-red',
      'text-[11px] bg-[length:10px] w-[1.25rem] [@media(hover:hover)]:opacity-0',
      'const from = `${viewYear}-${pad(viewMonth + 1)}-01`',
      "const label = 'text' + suffix",
    ]) {
      expect(hitsIn(ok), ok).toEqual([])
    }
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
      `Use a status token from src/index.css (text-danger, bg-success-subtle, border-warning-border, bg-info-fill with text-on-fill and so on) instead of a raw palette class, an arbitrary colour or a class built at run time. If the colour is a category or a decoration and not a status, add the file to src/theme/rawStatusAllowlist.ts with the reason.`,
    ).toEqual([])
  })

  it('every allowlisted file still needs its place', () => {
    const found = rawClassesByFile()
    const stale = Object.keys(RAW_STATUS_ALLOWLIST).filter(file => !found.has(file))
    expect(stale, 'These files no longer write a raw colour; remove them from rawStatusAllowlist.ts').toEqual([])
  })

  it('every allowlisted file gives a reason', () => {
    for (const [file, why] of Object.entries(RAW_STATUS_ALLOWLIST)) {
      expect(why.length, file).toBeGreaterThan(20)
    }
  })
})
