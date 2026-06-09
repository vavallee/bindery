import { describe, it, expect } from 'vitest'
import { bookStatusBadge } from './bookStatus'

// Stub t: return the key so we can assert which label path was taken without
// depending on the loaded i18n resources.
const t = ((key: string) => key) as unknown as Parameters<typeof bookStatusBadge>[2]

describe('bookStatusBadge', () => {
  it('labels a wanted+monitored book "Wanted" with the amber colour', () => {
    const b = bookStatusBadge('wanted', true, t)
    expect(b.label).toBe('bookStatus.wanted')
    expect(b.colorClass).toContain('amber')
  })

  it('labels a wanted+UNmonitored book "Not monitored" with a muted colour, not Wanted (#977)', () => {
    const b = bookStatusBadge('wanted', false, t)
    expect(b.label).toBe('bookStatus.notMonitored')
    expect(b.label).not.toBe('bookStatus.wanted')
    expect(b.colorClass).toMatch(/slate|zinc/)
    expect(b.colorClass).not.toContain('amber')
  })

  it('leaves non-wanted statuses unaffected by monitored', () => {
    for (const monitored of [true, false]) {
      const imp = bookStatusBadge('imported', monitored, t)
      expect(imp.label).toBe('bookStatus.imported')
      expect(imp.colorClass).toContain('emerald')

      const dl = bookStatusBadge('downloading', monitored, t)
      expect(dl.label).toBe('bookStatus.downloading')
      expect(dl.colorClass).toContain('blue')
    }
  })

  it('falls back to a muted badge for an unknown status', () => {
    const b = bookStatusBadge('weird', true, t)
    expect(b.colorClass).toMatch(/slate|zinc/)
  })
})

// WCAG AA enforcement for the status badges. These pills carry small text
// (text-[10px]/text-xs), so the 4.5:1 normal-text threshold applies — not the
// 3:1 large-text one. The test reads the real colorClass so a future shade
// change re-checks contrast instead of silently regressing.
describe('bookStatusBadge contrast (WCAG AA)', () => {
  // Only the shades actually used by the badges.
  const PALETTE: Record<string, [number, number, number]> = {
    'amber-500': [0xf5, 0x9e, 0x0b], 'amber-800': [0x92, 0x40, 0x0e], 'amber-400': [0xfb, 0xbf, 0x24],
    'blue-500': [0x3b, 0x82, 0xf6], 'blue-700': [0x1d, 0x4e, 0xd8], 'blue-400': [0x60, 0xa5, 0xfa],
    'purple-500': [0xa8, 0x55, 0xf7], 'purple-700': [0x7e, 0x22, 0xce], 'purple-400': [0xc0, 0x84, 0xfc],
    'emerald-500': [0x10, 0xb9, 0x81], 'emerald-800': [0x06, 0x5f, 0x46], 'emerald-400': [0x34, 0xd3, 0x99],
    'slate-300': [0xcb, 0xd5, 0xe1], 'slate-600': [0x47, 0x55, 0x69],
    'zinc-700': [0x3f, 0x3f, 0x46], 'zinc-300': [0xd4, 0xd4, 0xd8],
  }
  // Worst-case card surfaces (darkest light card / lightest dark card).
  const LIGHT_CARD: [number, number, number] = [0xf1, 0xf5, 0xf9]
  const DARK_CARD: [number, number, number] = [0x18, 0x18, 0x1b]

  type RGB = [number, number, number]
  const chan = (c: number) => { const s = c / 255; return s <= 0.04045 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4 }
  const lum = ([r, g, b]: RGB) => 0.2126 * chan(r) + 0.7152 * chan(g) + 0.0722 * chan(b)
  const ratio = (a: RGB, b: RGB) => { const [L1, L2] = [lum(a), lum(b)].sort((x, y) => y - x); return (L1 + 0.05) / (L2 + 0.05) }
  const over = (fg: RGB, alpha: number, bg: RGB): RGB => fg.map((f, i) => alpha * f + (1 - alpha) * bg[i]) as RGB

  // Pull a `bg-<color>/<alpha>` and the theme-appropriate text colour out of a
  // Tailwind class string, then compute the effective foreground/background.
  function resolve(colorClass: string, dark: boolean): { fg: RGB; bg: RGB } {
    const tokens = colorClass.split(/\s+/)
    const pick = (prefix: string) => {
      const want = dark ? `dark:${prefix}` : prefix
      // Prefer the dark: variant in dark mode, else the base utility.
      const t = tokens.find(x => x.startsWith(want)) ?? (dark ? tokens.find(x => x.startsWith(prefix) && !x.startsWith('dark:')) : undefined)
      return t?.slice(want.length) ?? null
    }
    const surface = dark ? DARK_CARD : LIGHT_CARD
    const bgTok = pick('bg-')
    let bg = surface
    if (bgTok) {
      const [name, alpha] = bgTok.split('/')
      const c = PALETTE[name]
      if (c) bg = alpha ? over(c, Number(alpha) / 100, surface) : c
    }
    const fgName = pick('text-')!
    return { fg: PALETTE[fgName], bg }
  }

  const statuses: Array<[string, boolean]> = [
    ['wanted', true], ['downloading', true], ['downloaded', true],
    ['imported', true], ['skipped', true], ['wanted', false], // wanted+unmonitored = muted
  ]

  for (const [status, monitored] of statuses) {
    for (const dark of [false, true]) {
      it(`${status}${monitored ? '' : ' (unmonitored)'} badge passes AA in ${dark ? 'dark' : 'light'}`, () => {
        const { colorClass } = bookStatusBadge(status, monitored, t)
        const { fg, bg } = resolve(colorClass, dark)
        expect(fg, `unknown shade in: ${colorClass}`).toBeDefined()
        expect(ratio(fg, bg)).toBeGreaterThanOrEqual(4.5)
      })
    }
  }
})
