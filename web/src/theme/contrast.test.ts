import { describe, it, expect, beforeAll } from 'vitest'
import { COVER_COLORS, COVER_FG, coverColorFor } from '../components/coverPalette'
import { SELECTED_HOVER_DARK } from '../components/DataTable'
import { TOAST_FILL } from '../pages/settings/toastBus'
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
// Every `--name: #rrggbb` in each theme's block, for the status tokens (#3994).
const ALL_TOKENS: Record<Theme, Map<string, string>> = { light: new Map(), dark: new Map() }
const token = (name: Token, theme: Theme) => (theme === 'dark' ? TOKENS.get(name)?.dark : TOKENS.get(name)?.light) ?? ''

beforeAll(async () => {
  const { css } = await postcss([tailwindcss()]).process('@import "./index.css";', { from: 'src/contrast.test.css' })
  const values = (body = '') => new Map([...body.matchAll(/--([\w-]+):\s*(#[0-9a-fA-F]{6})/g)].map(m => [m[1], m[2]]))
  const light = values(css.match(/(?:^|[}\s]):root\s*\{([^}]*)\}/)?.[1])
  const dark = values(css.match(/(?:^|[}\s])html\.dark\s*\{([^}]*)\}/)?.[1])
  ALL_TOKENS.light = light
  ALL_TOKENS.dark = dark
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

// The tokens are not the only text colours: badges and chips set an explicit
// hue on a tint (bg-red-500/20 text-red-800), chosen per theme by hand. Every
// class string with both a text colour and a background is measured too, in
// each theme, with the dark: class where there is one and the shared class
// where there is not; a translucent background is mixed over the card. The
// palette is read from Tailwind's own theme, so any hue and shade can be
// measured (#3084).
const TAILWIND = new Map<string, string>([['white', '#ffffff'], ['black', '#000000']])

// OKLCH to sRGB hex, as the browser converts Tailwind v4's colours, clamped to
// the sRGB gamut.
function oklchToHex(l: number, c: number, h: number): string {
  const a = c * Math.cos((h * Math.PI) / 180)
  const b = c * Math.sin((h * Math.PI) / 180)
  const lp = (l + 0.3963377774 * a + 0.2158037573 * b) ** 3
  const mp = (l - 0.1055613458 * a - 0.0638541728 * b) ** 3
  const sp = (l - 0.0894841775 * a - 1.291485548 * b) ** 3
  const linear = [
    4.0767416621 * lp - 3.3077115913 * mp + 0.2309699292 * sp,
    -1.2684380046 * lp + 2.6097574011 * mp - 0.3413193965 * sp,
    -0.0041960863 * lp - 0.7034186147 * mp + 1.707614701 * sp,
  ]
  const encode = (x: number) => {
    const v = Math.min(Math.max(x, 0), 1)
    return v <= 0.0031308 ? 12.92 * v : 1.055 * v ** (1 / 2.4) - 0.055
  }
  return '#' + linear.map(x => Math.round(encode(x) * 255).toString(16).padStart(2, '0')).join('')
}

beforeAll(async () => {
  const { css } = await postcss([tailwindcss()]).process('@import "tailwindcss/theme.css" theme(static);', { from: 'src/contrast.test.css' })
  for (const [, name, l, c, h] of css.matchAll(/--color-([a-z]+-\d+):\s*oklch\(([\d.]+)% ([\d.]+) ([\d.]+)\)/g)) {
    TAILWIND.set(name, oklchToHex(Number(l) / 100, Number(c), Number(h)))
  }
}, 60_000)

// A text class also sets a checkbox's tick colour; that is not text.
const NOT_TEXT = new Set([
  '../components/CatalogueReconciliationModal.tsx text-red-600',
  // The recommendation card's stars are decoration beside the rating, which
  // is stated as text (#3396).
  '../components/RecommendationCard.tsx text-amber-500',
  // The upgrade checkboxes' tick (#3661).
  '../pages/settings/QualityTab.tsx text-emerald-600',
])

const GREYS = /^(?:slate|zinc|gray|neutral|stone)-/

const HUE_TEXT = /^(dark:)?text-([a-z]+-\d+|white|black)$/
const HUE_BG = /^(dark:)?bg-([a-z]+-\d+|white|black)$/

describe('explicit text colours on the background in the same string', () => {
  it('reads the Tailwind palette', () => {
    expect(TAILWIND.get('red-800')).toMatch(/^#[0-9a-f]{6}$/)
  })

  it('stay AA in both themes', () => {
    const failures: string[] = []
    let checked = 0
    for (const [file, source] of Object.entries(sources)) {
      if (/\.test\.tsx?$/.test(file)) continue
      for (const [, str] of source.matchAll(/["'`]([^"'`]*\btext-[a-z]+-\d+[^"'`]*)["'`]/g)) {
        const classes = str.split(/\s+/)
        for (const theme of ['light', 'dark'] as const) {
          // The dark: class when the string has one, else the shared class.
          // A background's opacity (bg-red-500/20) is split off before matching.
          const pick = (pattern: RegExp) => {
            const matches = classes.map(c => c.split('/')[0].match(pattern)).filter((m): m is RegExpMatchArray => m !== null)
            const own = matches.find(m => Boolean(m[1]) === (theme === 'dark'))
            return own ?? (theme === 'dark' ? matches.find(m => !m[1]) : undefined)
          }
          const text = pick(HUE_TEXT)
          const ground = pick(HUE_BG)
          if (!text || !ground || NOT_TEXT.has(`${file} ${text[0]}`)) continue
          const opacity = classes.find(c => c.split('/')[0] === ground[0])?.split('/')[1]
          const fg = TAILWIND.get(text[2])
          const base = TAILWIND.get(ground[2])
          if (!fg || !base) continue
          const card = theme === 'dark' ? SURFACES.darkCard : SURFACES.lightCard
          const bg = opacity ? mix(base, card, Number(opacity) / 100) : base
          checked++
          const ratio = contrast(fg, bg)
          const shown = opacity ? `${ground[0]}/${opacity}` : ground[0]
          if (ratio < AA_NORMAL) failures.push(`${file}: ${theme} ${text[0]} on ${shown} is ${ratio.toFixed(2)}:1`)
        }
      }
    }
    expect(checked).toBeGreaterThan(100)
    expect(failures, failures.join('\n')).toEqual([])
  })

  // The case above finds a string by its hued text class, so white text on a
  // fill (bg-amber-500 text-white, a banner or a pill) was never seen: white
  // is 2.1:1 on amber-500 and 3.7:1 on emerald-600. A hover fill is the
  // ground while the pointer is on it, so it is measured too, with the hover
  // text colour when the string sets one (hover:bg-emerald-600
  // hover:text-white). Picked per theme as above, the dark: class when the
  // string has one, else the shared class; text and fill opacity are mixed in.
  it('white and black text stay AA on the fill in the same string, resting and hovered', () => {
    const HOVER_TEXT = /^(dark:)?hover:text-([a-z]+-\d+|white|black)$/
    const HOVER_BG = /^(dark:)?hover:bg-([a-z]+-\d+|white|black)$/
    const failures: string[] = []
    let checked = 0
    for (const [file, source] of Object.entries(sources)) {
      if (/\.test\.tsx?$/.test(file)) continue
      const code = source.replace(/^\s*\/\/.*$/gm, '')
      for (const [, str] of code.matchAll(/["'`]([^"'`]*\btext-(?:white|black)\b[^"'`]*)["'`]/g)) {
        const classes = str.split(/\s+/)
        const opacityOf = (m: RegExpMatchArray) => classes.find(c => c.split('/')[0] === m[0])?.split('/')[1]
        for (const theme of ['light', 'dark'] as const) {
          const pick = (pattern: RegExp) => {
            const matches = classes.map(c => c.split('/')[0].match(pattern)).filter((m): m is RegExpMatchArray => m !== null)
            const own = matches.find(m => Boolean(m[1]) === (theme === 'dark'))
            return own ?? (theme === 'dark' ? matches.find(m => !m[1]) : undefined)
          }
          const restText = pick(HUE_TEXT)
          const restBg = pick(HUE_BG)
          const hoverText = pick(HOVER_TEXT) ?? restText
          const hoverBg = pick(HOVER_BG) ?? restBg
          const pairs: Array<[RegExpMatchArray | undefined, RegExpMatchArray | undefined]> = [[restText, restBg]]
          if (hoverText !== restText || hoverBg !== restBg) pairs.push([hoverText, hoverBg])
          const card = theme === 'dark' ? SURFACES.darkCard : SURFACES.lightCard
          for (const [text, ground] of pairs) {
            // Only white and black: a hued text colour is the case above.
            if (!text || !ground || (text[2] !== 'white' && text[2] !== 'black')) continue
            const fgBase = TAILWIND.get(text[2])
            const base = TAILWIND.get(ground[2])
            if (!fgBase || !base) continue
            const bgOpacity = opacityOf(ground)
            const bg = bgOpacity ? mix(base, card, Number(bgOpacity) / 100) : base
            const fgOpacity = opacityOf(text)
            const fg = fgOpacity ? mix(fgBase, bg, Number(fgOpacity) / 100) : fgBase
            checked++
            const ratio = contrast(fg, bg)
            const shown = (m: RegExpMatchArray, o?: string) => (o ? `${m[0]}/${o}` : m[0])
            if (ratio < AA_NORMAL) failures.push(`${file}: ${theme} ${shown(text, fgOpacity)} on ${shown(ground, bgOpacity)} is ${ratio.toFixed(2)}:1`)
          }
        }
      }
    }
    expect(checked).toBeGreaterThan(50)
    expect(failures, failures.join('\n')).toEqual([])
  })

  // Coloured text with no background of its own sits on whatever its parent
  // paints, which a string cannot show. It is measured on the page and on the
  // card of each theme, the two surfaces nearly all of it lands on: the dark:
  // class on dark when the string has one, else the one class it has, which
  // shows in both themes. Text opacity (dark:text-amber-500/90) is mixed in.
  // Greys are left to the tokens above.
  it('stay AA on the page and on cards when the string has no background', () => {
    const failures: string[] = []
    let checked = 0
    for (const [file, source] of Object.entries(sources)) {
      if (/\.test\.tsx?$/.test(file)) continue
      // Comments quote old class strings (buttons.ts does); skip them.
      const code = source.replace(/^\s*\/\/.*$/gm, '')
      for (const [, str] of code.matchAll(/["'`]([^"'`]*\btext-[a-z]+-\d+[^"'`]*)["'`]/g)) {
        const classes = str.split(/\s+/)
        if (classes.some(c => HUE_BG.test(c.split('/')[0]))) continue
        const texts = classes
          .map(c => ({ m: c.split('/')[0].match(HUE_TEXT), opacity: c.split('/')[1] }))
          .filter((t): t is { m: RegExpMatchArray, opacity: string } => t.m !== null)
        const light = texts.find(t => !t.m[1])
        const dark = texts.find(t => t.m[1]) ?? light
        for (const [theme, t] of [['light', light], ['dark', dark]] as const) {
          if (!t) continue
          const text = t.m
          if (GREYS.test(text[2]) || NOT_TEXT.has(`${file} ${text[0]}`)) continue
          const colour = TAILWIND.get(text[2])
          if (!colour) continue
          const grounds = theme === 'dark'
            ? [['the page', SURFACES.darkPage], ['a card', SURFACES.darkCard]]
            : [['the page', SURFACES.lightPage], ['a card', SURFACES.lightCard]]
          for (const [name, bg] of grounds) {
            checked++
            const fg = t.opacity ? mix(colour, bg, Number(t.opacity) / 100) : colour
            const ratio = contrast(fg, bg)
            if (ratio < AA_NORMAL) failures.push(`${file}: ${theme} ${text[0]} on ${name} is ${ratio.toFixed(2)}:1`)
          }
        }
      }
    }
    expect(checked).toBeGreaterThan(100)
    expect(failures, failures.join('\n')).toEqual([])
  })

  // A hover or focus colour is the text's colour while it is pointed at or
  // focused, so it has to read as well as the resting one. The cases above
  // only see resting colours, which let hover:text-emerald-500 (2.5:1) and
  // hover:text-rose-500 through. Measured like the case above, with the
  // dark:hover: class on dark when the string has one, else the shared class.
  it('stay AA on hover and focus, on the page and on cards', () => {
    const STATE_TEXT = /^(dark:)?(?:hover|focus):text-([a-z]+-\d+)$/
    const failures: string[] = []
    let checked = 0
    for (const [file, source] of Object.entries(sources)) {
      if (/\.test\.tsx?$/.test(file)) continue
      // Comments quote old class strings (buttons.ts does); skip them.
      const code = source.replace(/^\s*\/\/.*$/gm, '')
      for (const [, str] of code.matchAll(/["'`]([^"'`]*\b(?:hover|focus):text-[a-z]+-\d+[^"'`]*)["'`]/g)) {
        const classes = str.split(/\s+/)
        if (classes.some(c => HUE_BG.test(c.split('/')[0]))) continue
        const states = classes.map(c => c.match(STATE_TEXT)).filter((m): m is RegExpMatchArray => m !== null)
        const light = states.filter(m => !m[1])
        const dark = states.some(m => m[1]) ? states.filter(m => m[1]) : light
        for (const [theme, texts] of [['light', light], ['dark', dark]] as const) {
          for (const text of texts) {
            if (GREYS.test(text[2])) continue
            const fg = TAILWIND.get(text[2])
            if (!fg) continue
            const grounds = theme === 'dark'
              ? [['the page', SURFACES.darkPage], ['a card', SURFACES.darkCard]]
              : [['the page', SURFACES.lightPage], ['a card', SURFACES.lightCard]]
            for (const [name, bg] of grounds) {
              checked++
              const ratio = contrast(fg, bg)
              if (ratio < AA_NORMAL) failures.push(`${file}: ${theme} ${text[0]} on ${name} is ${ratio.toFixed(2)}:1`)
            }
          }
        }
      }
    }
    expect(checked).toBeGreaterThan(10)
    expect(failures, failures.join('\n')).toEqual([])
  })
})

// A grey text colour with no background in its string sits on the page or on a
// card, and the scans above cannot measure it because they need a background
// to compare against. This is the old secondary text, text-slate-400 or
// dark:text-zinc-500 (2.5:1 and 3.7:1), which slipped through for that reason.
// Every such string is measured against the page and the card of each theme,
// with the dark: class where there is one and the shared class where not
// (#3084).
const GREY_TEXT = /^(dark:)?text-((?:slate|zinc|gray|neutral|stone)-\d+)$/
// Strings whose surface is set somewhere else, or that are exempt from the
// contrast rule, each with the reason.
const GREY_EXEMPT: Array<[string, string]> = [
  // The clear button sits on the bar's own bg-slate-800 / dark:bg-zinc-950,
  // which is set on the wrapper, and slate-400 is 5.7:1 on it.
  ['../components/BulkActionBar.tsx', 'text-slate-400'],
  // A greyed-out token chip is an inactive control (WCAG 1.4.3 exempts it).
  ['../pages/settings/NamingTemplateField.tsx', 'text-slate-400'],
  // Empty stars and the empty-state illustration are decoration, not text.
  ['../components/RecommendationCard.tsx', 'text-slate-300'],
  ['../pages/DiscoverPage.tsx', 'text-slate-300'],
]

describe('grey text colours that have no background in their string', () => {
  it('stay AA on the page and the card in both themes', () => {
    const failures: string[] = []
    let checked = 0
    for (const [file, source] of Object.entries(sources)) {
      if (/\.test\.tsx?$/.test(file)) continue
      for (const [, str] of source.matchAll(/["'`]([^"'`]*\btext-(?:slate|zinc|gray|neutral|stone)-\d+[^"'`]*)["'`]/g)) {
        const classes = str.split(/\s+/)
        if (classes.some(c => /^(dark:)?bg-/.test(c))) continue
        if (GREY_EXEMPT.some(([f, c]) => f === file && str.includes(c))) continue
        for (const theme of ['light', 'dark'] as const) {
          const matches = classes.map(c => c.match(GREY_TEXT)).filter((m): m is RegExpMatchArray => m !== null)
          // A dark: text colour that is not a grey (dark:text-white) replaces
          // the shared class in dark mode, so there is nothing grey to measure.
          const darkOther = theme === 'dark' && classes.some(c => c.startsWith('dark:text-') && !GREY_TEXT.test(c))
          const text = matches.find(m => Boolean(m[1]) === (theme === 'dark')) ?? (theme === 'dark' && !darkOther ? matches.find(m => !m[1]) : undefined)
          const fg = text && TAILWIND.get(text[2])
          if (!text || !fg) continue
          for (const [surface, bg] of theme === 'dark' ? [['page', SURFACES.darkPage], ['card', SURFACES.darkCard]] : [['page', SURFACES.lightPage], ['card', SURFACES.lightCard]]) {
            checked++
            const ratio = contrast(fg, bg)
            if (ratio < AA_NORMAL) failures.push(`${file}: ${theme} ${text[0]} on the ${surface} is ${ratio.toFixed(2)}:1 in "${str.trim().slice(0, 90)}"`)
          }
        }
      }
    }
    expect(checked).toBeGreaterThan(100)
    expect(failures, failures.join('\n')).toEqual([])
  })

  // The BulkActionBar exemption above rests on the bar's own fill, so measure
  // it: if the bar moves to a lighter fill, the clear button has to follow.
  it('the bulk action bar clear button stays AA on the bar in both themes', () => {
    const bar = sources['../components/BulkActionBar.tsx']
    expect(bar).toContain('bg-slate-800 dark:bg-zinc-950')
    expect(bar).toMatch(/clearClass = '[^']*\btext-slate-400\b/)
    expect(contrast(TAILWIND.get('slate-400')!, TAILWIND.get('slate-800')!)).toBeGreaterThanOrEqual(AA_NORMAL)
    expect(contrast(TAILWIND.get('slate-400')!, TAILWIND.get('zinc-950')!)).toBeGreaterThanOrEqual(AA_NORMAL)
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

// The Settings save toast (#3991) sets white text on a red or emerald fill. The
// scanners above read a text colour and a fill out of one quoted string, and
// the toast's classes used to be split across a ternary where they saw
// neither, so the fills are named whole in toastBus.ts and measured here.
describe('Settings save toast', () => {
  // The toast now names token classes (#3994), measured in both themes.
  for (const [kind, status] of [['success', 'success'], ['error', 'danger']] as const) {
    for (const theme of ['light', 'dark'] as const) {
      it(`${kind}: on-fill text on its fill is at least ${AA_NORMAL}:1 in ${theme}`, () => {
        expect(TOAST_FILL[kind].split(/\s+/).sort()).toEqual([`bg-${status}-fill`, 'text-on-fill'])
        expect(contrast(tok('on-fill', theme), tok(`${status}-fill`, theme))).toBeGreaterThanOrEqual(AA_NORMAL)
      })
    }
  }

  // The fills keep the shades the toast had before the tokens: emerald-700
  // and red-600, 5.4:1 and 4.8:1 against white (#4070).
  it('keeps the measured fills', () => {
    expect(tok('success-fill', 'light')).toBe(TAILWIND.get('emerald-700'))
    expect(tok('danger-fill', 'light')).toBe(TAILWIND.get('red-600'))
    expect(contrast('#ffffff', TAILWIND.get('emerald-700')!)).toBeGreaterThanOrEqual(5.3)
    expect(contrast('#ffffff', TAILWIND.get('red-600')!)).toBeGreaterThanOrEqual(4.7)
  })
})

// Status colour tokens (#3994): success, warning, danger and info, each with
// text on a surface, text on its own tint, the tint and its border, a solid
// fill and its hover, and a marker for dots and bars. They replace the raw
// emerald, amber, red and sky classes, so each is pinned to the palette shade
// it replaced (no visual change) and measured here, in both themes, from the
// CSS as the build compiles it.
const STATUSES = { danger: 'red', warning: 'amber', success: 'emerald', info: 'sky', progress: 'blue' } as const
type Status = keyof typeof STATUSES
const STATUS_NAMES = Object.keys(STATUSES) as Status[]
const tok = (name: string, theme: Theme) => ALL_TOKENS[theme].get(name) ?? ''
const SURFACE_LIST = (theme: Theme): Array<[string, string]> =>
  theme === 'dark' ? [['page', SURFACES.darkPage], ['card', SURFACES.darkCard]] : [['page', SURFACES.lightPage], ['card', SURFACES.lightCard]]
const FILL_SHADE: Record<Status, [number, number]> = { danger: [600, 700], warning: [700, 800], success: [700, 800], info: [700, 800], progress: [700, 800] }
// What each token's value was before it became a token: the palette shade of
// the raw class it replaced, per theme. The dark tint is the old
// dark:bg-red-900/30 mixed over the dark card, as an opaque colour.
function expectedShade(status: Status, kind: string, theme: Theme): string {
  const h = STATUSES[status]
  const light: Record<string, string> = { '': '700', strong: '800', subtle: '100', border: '300', fill: String(FILL_SHADE[status][0]), 'fill-hover': String(FILL_SHADE[status][1]), marker: '500' }
  const dark: Record<string, string> = { '': '400', strong: '300', border: '900', fill: String(FILL_SHADE[status][0]), 'fill-hover': String(FILL_SHADE[status][1]), marker: '500' }
  if (theme === 'dark' && kind === 'subtle') return mix(TAILWIND.get(`${h}-900`)!, SURFACES.darkCard, 0.3)
  return TAILWIND.get(`${h}-${(theme === 'dark' ? dark : light)[kind]}`)!
}
const KINDS = ['', 'strong', 'subtle', 'border', 'fill', 'fill-hover', 'marker']
const nameOf = (status: Status, kind: string) => (kind ? `${status}-${kind}` : status)

describe('status colour tokens (#3994)', () => {
  it('reads every token for both themes from index.css', () => {
    for (const theme of ['light', 'dark'] as const) {
      for (const status of STATUS_NAMES) {
        for (const kind of KINDS) expect(tok(nameOf(status, kind), theme), `${nameOf(status, kind)} ${theme}`).toMatch(/^#[0-9a-f]{6}$/i)
      }
      for (const name of ['on-fill', 'accent-fill', 'accent-fill-hover', 'accent-ring']) expect(tok(name, theme), `${name} ${theme}`).toMatch(/^#[0-9a-f]{6}$/i)
    }
  })

  it('each equals the palette shade it replaced, so nothing changes on screen', () => {
    for (const theme of ['light', 'dark'] as const) {
      for (const status of STATUS_NAMES) {
        for (const kind of KINDS) {
          expect(tok(nameOf(status, kind), theme), `${nameOf(status, kind)} ${theme}`).toBe(expectedShade(status, kind, theme))
        }
      }
    }
    expect(tok('accent-fill', 'light')).toBe(TAILWIND.get('emerald-700'))
    expect(tok('accent-fill-hover', 'dark')).toBe(TAILWIND.get('emerald-800'))
    expect(tok('accent-ring', 'light')).toBe(TAILWIND.get('emerald-600'))
    expect(tok('accent-ring', 'dark')).toBe(TAILWIND.get('emerald-500'))
  })

  for (const theme of ['light', 'dark'] as const) {
    for (const status of STATUS_NAMES) {
      for (const [surface, bg] of SURFACE_LIST(theme)) {
        for (const kind of ['', 'strong']) {
          it(`text-${nameOf(status, kind)} ${theme} on the ${surface} >= ${AA_NORMAL}:1`, () => {
            expect(contrast(tok(nameOf(status, kind), theme), bg)).toBeGreaterThanOrEqual(AA_NORMAL)
          })
        }
        // The tint is also drawn translucent (bg-danger-subtle/40) over the
        // surface, so the strong text is measured on each mix.
        for (const alpha of [1, 0.5, 0.4]) {
          it(`text-${status}-strong ${theme} on ${status}-subtle at ${alpha * 100}% over the ${surface} >= ${AA_NORMAL}:1`, () => {
            const ground = mix(tok(`${status}-subtle`, theme), bg, alpha)
            expect(contrast(tok(`${status}-strong`, theme), ground)).toBeGreaterThanOrEqual(AA_NORMAL)
          })
        }
      }
      for (const fill of ['fill', 'fill-hover']) {
        it(`on-fill ${theme} on ${status}-${fill} >= ${AA_NORMAL}:1`, () => {
          expect(contrast(tok('on-fill', theme), tok(`${status}-${fill}`, theme))).toBeGreaterThanOrEqual(AA_NORMAL)
        })
      }
    }
    for (const fill of ['accent-fill', 'accent-fill-hover']) {
      it(`on-fill ${theme} on ${fill} >= ${AA_NORMAL}:1`, () => {
        expect(contrast(tok('on-fill', theme), tok(fill, theme))).toBeGreaterThanOrEqual(AA_NORMAL)
      })
    }
    for (const [surface, bg] of SURFACE_LIST(theme)) {
      it(`accent-ring ${theme} on the ${surface} is a visible focus ring (>= ${AA_NON_TEXT}:1)`, () => {
        expect(contrast(tok('accent-ring', theme), bg)).toBeGreaterThanOrEqual(AA_NON_TEXT)
      })
    }
  }

  // Dots, bars and rings carry no text, and the colour is repeated in a
  // label beside them. They keep the contrast of the 500 shades they were, so
  // this pins them at that floor rather than at 3:1, which amber misses.
  it('markers keep at least the contrast of the 500 shades they replaced', () => {
    for (const theme of ['light', 'dark'] as const) {
      for (const status of STATUS_NAMES) {
        for (const [, bg] of SURFACE_LIST(theme)) {
          const old = contrast(TAILWIND.get(`${STATUSES[status]}-500`)!, bg)
          expect(contrast(tok(`${status}-marker`, theme), bg)).toBeGreaterThanOrEqual(old)
        }
      }
    }
  })

  // The class strings in the source. A token text colour is measured against
  // the token or palette background in the same string (a translucent one
  // mixed over the card), resting and hovered, in each theme: the pairing the
  // checks above cannot see, such as text-danger on bg-danger-subtle.
  it('token text stays AA on the background in the same string', () => {
    const TEXT = /^(?:hover:)?text-(?:(?:danger|warning|success|info)(?:-strong)?|on-fill)$/
    const failures: string[] = []
    let checked = 0
    const resolveBg = (cls: string, theme: Theme, card: string): string | undefined => {
      const [base, alpha] = cls.split('/')
      const m = base.match(/^bg-((?:danger|warning|success|info)-(?:subtle|border|fill|fill-hover|marker)|accent-fill|accent-fill-hover)$/)
      const hex = m ? tok(m[1], theme) : TAILWIND.get(base.replace(/^bg-/, ''))
      return hex ? (alpha ? mix(hex, card, Number(alpha) / 100) : hex) : undefined
    }
    for (const [file, source] of Object.entries(sources)) {
      if (/\.test\.tsx?$/.test(file)) continue
      const code = source.replace(/^\s*\/\/.*$/gm, '')
      for (const [, str] of code.matchAll(/["'`]([^"'`]*\b(?:hover:)?text-(?:danger|warning|success|info|on-fill)[^"'`]*)["'`]/g)) {
        const classes = str.split(/\s+/)
        for (const theme of ['light', 'dark'] as const) {
          const card = theme === 'dark' ? SURFACES.darkCard : SURFACES.lightCard
          // The dark: background wins in dark mode; else the shared one.
          const bgFor = (prefix: string) => {
            const own = theme === 'dark' ? classes.find(c => c.startsWith(`dark:${prefix}bg-`)) : undefined
            return own ? own.slice(5 + prefix.length) : classes.find(c => c.startsWith(`${prefix}bg-`))?.slice(prefix.length)
          }
          const rest = bgFor('')
          const hover = bgFor('hover:') ?? rest
          const restText = classes.find(c => TEXT.test(c) && !c.startsWith('hover:'))
          const hoverText = classes.find(c => TEXT.test(c) && c.startsWith('hover:')) ?? restText
          for (const [state, bgClass, textClass] of [['rest', rest, restText], ['hover', hover, hoverText]] as const) {
            if (!bgClass || !textClass) continue
            const bg = resolveBg(bgClass, theme, card)
            const fg = tok(textClass.replace(/^hover:text-/, '').replace(/^text-/, ''), theme)
            if (!bg || !fg) continue
            checked++
            const ratio = contrast(fg, bg)
            if (ratio < AA_NORMAL) failures.push(`${file}: ${theme} ${state} ${textClass} on ${bgClass} is ${ratio.toFixed(2)}:1 in "${str.trim().slice(0, 90)}"`)
          }
        }
      }
    }
    expect(checked).toBeGreaterThanOrEqual(0)
    expect(failures, failures.join('\n')).toEqual([])
  })
})
