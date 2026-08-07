import { describe, it, expect } from 'vitest'
import { COVER_COLORS, COVER_FG, coverColorFor } from '../components/coverPalette'

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

// The semantic tokens defined in index.css (kept in sync by hand — this test
// is the guard that they stay AA).
const TOKENS = {
  fgMuted: { light: '#475569', dark: '#a1a1aa' }, // slate-600 / zinc-400
  accentText: { light: '#047857', dark: '#34d399' }, // emerald-700 / emerald-400
}

const AA_NORMAL = 4.5

describe('theme text tokens meet WCAG AA on app surfaces', () => {
  const cases: Array<[string, string, string]> = [
    ['fg-muted light on slate-50', TOKENS.fgMuted.light, SURFACES.lightPage],
    ['fg-muted light on slate-100', TOKENS.fgMuted.light, SURFACES.lightCard],
    ['fg-muted dark on zinc-950', TOKENS.fgMuted.dark, SURFACES.darkPage],
    ['fg-muted dark on zinc-900', TOKENS.fgMuted.dark, SURFACES.darkCard],
    ['accent-text light on slate-50', TOKENS.accentText.light, SURFACES.lightPage],
    ['accent-text light on slate-100', TOKENS.accentText.light, SURFACES.lightCard],
    ['accent-text dark on zinc-950', TOKENS.accentText.dark, SURFACES.darkPage],
    ['accent-text dark on zinc-900', TOKENS.accentText.dark, SURFACES.darkCard],
  ]
  for (const [name, fg, bg] of cases) {
    it(`${name} >= ${AA_NORMAL}:1`, () => {
      expect(contrast(fg, bg)).toBeGreaterThanOrEqual(AA_NORMAL)
    })
  }

  it('regression: the old bare emerald-400 fails on light (why accent-text exists)', () => {
    expect(contrast('#34d399', SURFACES.lightCard)).toBeLessThan(AA_NORMAL)
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
