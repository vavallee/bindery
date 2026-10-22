import { describe, it, expect, beforeAll } from 'vitest'
import { COVER_COLORS, COVER_FG, coverColorFor } from '../components/coverPalette'
import { SELECTED_HOVER_DARK } from '../components/DataTable'
import dataTableSource from '../components/DataTable.tsx?raw'
import postcss from 'postcss'
import tailwindcss from '@tailwindcss/postcss'

// WCAG 2.1 relative-luminance + contrast ratio for sRGB hex colors.
function srgbToLinear(c: number): number {
  const cs = c / 255
  return cs <= 0.03928 ? cs / 12.92 : Math.pow((cs + 0.055) / 1.055, 2.4)
}
function luminance(hex: string): number {
  const h = hex.replace('#', '')
  const r = parseInt(h.slice(0, 2), 16)
  const g = parseInt(h.slice(2, 4), 16)
  const b = parseInt(h.slice(4, 6), 16)
  return 0.2126 * srgbToLinear(r) + 0.7152 * srgbToLinear(g) + 0.0722 * srgbToLinear(b)
}
function contrast(a: string, b: string): number {
  const la = luminance(a)
  const lb = luminance(b)
  return (Math.max(la, lb) + 0.05) / (Math.min(la, lb) + 0.05)
}

// Surfaces the tokens render on. Light: slate-50 page / slate-100 cards.
// Dark: zinc-950 page / zinc-900 cards.
const SURFACES = {
  lightPage: '#f8fafc',
  lightCard: '#f1f5f9',
  darkPage: '#09090b',
  darkCard: '#18181b',
}

// The semantic tokens, read from index.css as the production build compiles
// it rather than copied here, so a change there is what gets measured (#3084).
// Built through PostCSS like indexCss.test.ts: a ?raw import of a CSS file is
// empty under Vitest.
type Token = 'fg-muted' | 'accent-text'
type Theme = 'light' | 'dark'
const TOKEN_NAMES: Token[] = ['fg-muted', 'accent-text']
const TOKENS = new Map<Token, Record<Theme, string>>()
const token = (name: Token, theme: Theme) => (theme === 'dark' ? TOKENS.get(name)?.dark : TOKENS.get(name)?.light) ?? ''

beforeAll(async () => {
  const { css } = await postcss([tailwindcss()]).process('@import "./index.css";', { from: 'src/contrast.test.css' })
  const values = (body = '') => new Map([...body.matchAll(/--([\w-]+):\s*(#[0-9a-fA-F]{6})/g)].map(m => [m[1], m[2]]))
  const light = values(css.match(/(?:^|[}\s]):root\s*\{([^}]*)\}/)?.[1])
  const dark = values(css.match(/(?:^|[}\s])html\.dark\s*\{([^}]*)\}/)?.[1])
  for (const name of TOKEN_NAMES) {
    TOKENS.set(name, { light: light.get(name) ?? '', dark: dark.get(name) ?? '' })
  }
}, 60_000)

const AA_NORMAL = 4.5

describe('theme text tokens meet WCAG AA on app surfaces', () => {
  it('reads both tokens for both themes from index.css', () => {
    for (const name of TOKEN_NAMES) {
      expect(token(name, 'light')).toMatch(/^#[0-9a-f]{6}$/i)
      expect(token(name, 'dark')).toMatch(/^#[0-9a-f]{6}$/i)
    }
  })

  const cases: Array<[string, Token, Theme, string]> = [
    ['fg-muted light on slate-50', 'fg-muted', 'light', SURFACES.lightPage],
    ['fg-muted light on slate-100', 'fg-muted', 'light', SURFACES.lightCard],
    ['fg-muted dark on zinc-950', 'fg-muted', 'dark', SURFACES.darkPage],
    ['fg-muted dark on zinc-900', 'fg-muted', 'dark', SURFACES.darkCard],
    ['accent-text light on slate-50', 'accent-text', 'light', SURFACES.lightPage],
    ['accent-text light on slate-100', 'accent-text', 'light', SURFACES.lightCard],
    ['accent-text dark on zinc-950', 'accent-text', 'dark', SURFACES.darkPage],
    ['accent-text dark on zinc-900', 'accent-text', 'dark', SURFACES.darkCard],
  ]
  for (const [name, tokenName, theme, bg] of cases) {
    it(`${name} >= ${AA_NORMAL}:1`, () => {
      expect(contrast(token(tokenName, theme), bg)).toBeGreaterThanOrEqual(AA_NORMAL)
    })
  }

  it('regression: the old bare emerald-400 fails on light (why accent-text exists)', () => {
    expect(contrast('#34d399', SURFACES.lightCard)).toBeLessThan(AA_NORMAL)
  })
})

// The tokens are AA on the page and on cards, but not on every background a
// class string can put them on: fg-muted's zinc-400 is 4.08:1 on zinc-700.
// So every class string in the source that sets a token together with a
// slate, zinc or emerald background is measured, in each theme, against that
// background; a translucent one is mixed over the card of its theme, and one
// with no dark: variant counts for both themes. Emerald is here because
// accent-text sits on green tints: emerald-700 is 4.19:1 on emerald-500/20. A
// string that overrides the token's colour for a theme (dark:text-zinc-300)
// is measured with the override. A background set in a different string, as
// a ternary often does, is not seen: this guards the common badge shape.
// Tailwind v4 palette values, from tailwindcss/theme.css converted to sRGB.
const PALETTE = new Map([
  ['white', '#ffffff'],
  ['slate-50', '#f8fafc'],
  ['slate-100', '#f1f5f9'],
  ['slate-200', '#e2e8f0'],
  ['slate-300', '#cad5e2'],
  ['zinc-700', '#3f3f46'],
  ['zinc-800', '#27272a'],
  ['zinc-900', '#18181b'],
  ['zinc-950', '#09090b'],
  ['emerald-400', '#00d492'],
  ['emerald-500', '#00bc7d'],
])
const TEXT_OVERRIDE = new Map([
  ['zinc-200', '#e4e4e7'],
  ['zinc-300', '#d4d4d8'],
  ['slate-700', '#314158'],
  ['slate-800', '#1d293d'],
])
// A background, and an override of the token's colour, as each theme spells
// them, the colour in group 1. A background's opacity (bg-zinc-800/50) is
// split off before matching.
const CLASSES: Record<Theme, { bg: RegExp; text: RegExp }> = {
  light: { bg: /^bg-((?:slate|zinc|emerald)-\d+|white)$/, text: /^text-((?:slate|zinc)-\d+)$/ },
  dark: { bg: /^dark:bg-((?:slate|zinc|emerald)-\d+|white)$/, text: /^dark:text-((?:slate|zinc)-\d+)$/ },
}

function mix(fg: string, bg: string, alpha: number): string {
  const ch = (h: string, i: number) => parseInt(h.slice(1 + 2 * i, 3 + 2 * i), 16)
  return '#' + [0, 1, 2].map(i => Math.round(ch(fg, i) * alpha + ch(bg, i) * (1 - alpha)).toString(16).padStart(2, '0')).join('')
}

const sources = import.meta.glob('../**/*.{ts,tsx}', { query: '?raw', import: 'default', eager: true }) as Record<string, string>

describe('tokens on the backgrounds the source puts them on', () => {
  it('stay AA in both themes', () => {
    const failures: string[] = []
    let checked = 0
    for (const [file, source] of Object.entries(sources)) {
      if (/\.test\.tsx?$/.test(file)) continue
      for (const [, str] of source.matchAll(/["'`]([^"'`]*\btext-(?:fg-muted|accent-text)\b[^"'`]*)["'`]/g)) {
        const classes = str.split(/\s+/)
        const tokenName = classes.find(c => c === 'text-fg-muted' || c === 'text-accent-text')!.slice(5) as Token
        for (const theme of ['light', 'dark'] as const) {
          const { bg: bgPattern, text: textPattern } = theme === 'dark' ? CLASSES.dark : CLASSES.light
          const ownBg = classes.find(c => bgPattern.test(c.split('/')[0]))
          const sharedBg = theme === 'dark' && !classes.some(c => c.startsWith('dark:bg-'))
            ? classes.find(c => CLASSES.light.bg.test(c.split('/')[0]))
            : undefined
          const bgClass = ownBg ?? sharedBg
          const [bgBase, opacity] = bgClass?.split('/') ?? []
          const ground = bgBase && PALETTE.get(bgBase.match(ownBg ? bgPattern : CLASSES.light.bg)![1])
          if (!bgClass || !ground) continue
          const card = theme === 'dark' ? SURFACES.darkCard : SURFACES.lightCard
          const bg = opacity ? mix(ground, card, Number(opacity) / 100) : ground
          const overrideMatch = classes.map(c => c.match(textPattern)).find(m => m)
          const override = overrideMatch?.[0]
          const fg = overrideMatch ? TEXT_OVERRIDE.get(overrideMatch[1]) : token(tokenName, theme)
          if (!fg) continue
          checked++
          const ratio = contrast(fg, bg)
          if (ratio < AA_NORMAL) failures.push(`${file}: ${theme} ${override ?? 'text-' + tokenName} on ${bgClass} is ${ratio.toFixed(2)}:1 in "${str.trim()}"`)
        }
      }
    }
    expect(checked).toBeGreaterThan(20)
    expect(failures, failures.join('\n')).toEqual([])
  })

  // Why the green tints spell out emerald-800 for light rather than use
  // accent-text: the token is AA on surfaces, not on its own hue (#3084).
  it('regression: accent-text fails on the emerald-500/20 tint, emerald-800 passes', () => {
    const tint = mix(PALETTE.get('emerald-500')!, SURFACES.lightCard, 0.2)
    expect(contrast(token('accent-text', 'light'), tint)).toBeLessThan(AA_NORMAL)
    expect(contrast('#006045', tint)).toBeGreaterThanOrEqual(AA_NORMAL)
  })
})

// CoverPlaceholder paints its own ground, so its text contrast is
// theme-independent — but the tile still has to read as an object against both
// page surfaces, which is what pins the palette to the 700 level.
const AA_NON_TEXT = 3

describe('CoverPlaceholder palette', () => {
  for (const color of COVER_COLORS) {
    it(`${color.className}: white title >= ${AA_NORMAL}:1`, () => {
      expect(contrast(COVER_FG, color.bg)).toBeGreaterThanOrEqual(AA_NORMAL)
    })

    it(`${color.className}: tile edge >= ${AA_NON_TEXT}:1 on both page surfaces`, () => {
      expect(contrast(color.bg, SURFACES.lightPage)).toBeGreaterThanOrEqual(AA_NON_TEXT)
      expect(contrast(color.bg, SURFACES.darkPage)).toBeGreaterThanOrEqual(AA_NON_TEXT)
    })
  }

  it('regression: a 900-level ground dissolves into the dark page', () => {
    // Why the palette sits at 700 and not deeper. zinc-800 — what the old flat
    // placeholder used — is worse still.
    expect(contrast('#7f1d1d', SURFACES.darkPage)).toBeLessThan(AA_NON_TEXT)
    expect(contrast('#27272a', SURFACES.darkPage)).toBeLessThan(AA_NON_TEXT)
  })

  it('assigns a stable colour per id', () => {
    expect(coverColorFor(42)).toBe(coverColorFor(42))
    expect(coverColorFor(42)).toBe(coverColorFor('42'))
  })

  it('spreads ids across the whole palette', () => {
    const seen = new Set(Array.from({ length: 400 }, (_, i) => coverColorFor(i + 1).className))
    expect(seen.size).toBe(COVER_COLORS.length)
  })
})

// A selected table row under the pointer in dark mode. emerald-900 left the
// muted text at 3.8:1.
describe('DataTable selected row hover', () => {
  it('keeps muted text AA in dark mode', () => {
    expect(dataTableSource).toContain(`dark:hover:bg-[${SELECTED_HOVER_DARK}]`)
    expect(contrast(token('fg-muted', 'dark'), SELECTED_HOVER_DARK)).toBeGreaterThanOrEqual(AA_NORMAL)
  })
})
