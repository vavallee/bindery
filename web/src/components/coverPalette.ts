// Palette + hash for CoverPlaceholder, kept separate from the component so
// contrast.test.ts can import the colours without pulling in JSX.
//
// Roughly half a typical library has no cover art. A flat grey box reads as a
// broken image, so the placeholder paints its own deliberate surface instead:
// a ground with the title (or initials) set over it, the way a plain cloth
// binding would look. Because the tile supplies its own background, the text
// contrast is self-contained and identical in light and dark themes.
//
// The grounds are dusty mid-tones rather than saturated 700-level fills. On an
// Author page where most books lack covers, the saturated set turned the grid
// into a wall of colour swatches that outshouted the real covers beside them.
// Low saturation keeps each book distinguishable without competing.
//
// Every ground satisfies three properties, all enforced by contrast.test.ts:
//   1. >= 4.5:1 against white text — AA for normal text, not merely the 3:1
//      large-text allowance, so the title stays legible at any tile size.
//   2. >= 3:1 against the light page surface (slate-50).
//   3. >= 3:1 against the dark page surface (zinc-950).
// (2) and (3) are WCAG 1.4.11 non-text contrast: without them the tile edge
// dissolves into the page and the card stops reading as an object. Meeting all
// three pins each ground to a narrow luminance band (about 0.11 to 0.18), so
// the hues below are HSL at 22% saturation, lightness tuned per hue into it.

export interface CoverColor {
  /** Tailwind class applied to the tile. */
  className: string
  /** Ground colour as hex — the value contrast.test.ts asserts against. */
  bg: string
}

// Hues are spread around the wheel so adjacent books in a grid rarely collide.
export const COVER_COLORS: readonly CoverColor[] = [
  { className: 'bg-[#9c6363]', bg: '#9c6363' }, // dusty red
  { className: 'bg-[#8c6f5a]', bg: '#8c6f5a' }, // clay
  { className: 'bg-[#837354]', bg: '#837354' }, // sand
  { className: 'bg-[#627c50]', bg: '#627c50' }, // moss
  { className: 'bg-[#507c66]', bg: '#507c66' }, // sage
  { className: 'bg-[#507c79]', bg: '#507c79' }, // teal
  { className: 'bg-[#587889]', bg: '#587889' }, // slate blue
  { className: 'bg-[#617498]', bg: '#617498' }, // denim
  { className: 'bg-[#7c6aa0]', bg: '#7c6aa0' }, // lavender
  { className: 'bg-[#986198]', bg: '#986198' }, // plum
  { className: 'bg-[#9c637b]', bg: '#9c637b' }, // rose
  { className: 'bg-[#64748b]', bg: '#64748b' }, // slate-500
] as const

/** Foreground used on every ground above. */
export const COVER_FG = '#ffffff'

// FNV-1a over the id's decimal string. Any stable, well-mixed hash works; the
// only real requirement is that a given book keeps its colour across reloads
// and across the several places its cover is drawn (detail header, grid card,
// table thumbnail), so the same book stays recognisably the same object.
export function coverColorFor(id: number | string): CoverColor {
  const key = String(id)
  let h = 0x811c9dc5
  for (let i = 0; i < key.length; i++) {
    h ^= key.charCodeAt(i)
    // FNV prime 16777619. Math.imul keeps the multiply in 32-bit space.
    h = Math.imul(h, 0x01000193) >>> 0
  }
  return COVER_COLORS[h % COVER_COLORS.length]
}

// Up to two initials from the title's first words, skipping a leading article
// so "The Left Hand of Darkness" reads LH rather than TL. Digits count, so
// "1984" stays "1". Initials are whole characters (graphemes), not UTF-16
// units, so an emoji or an astral CJK character is not cut in half. A title
// in a script written without spaces (Chinese, Japanese) is one "word", so it
// takes its first two characters instead: 三体 stays 三体, not 三.
export function titleInitials(title: string): string {
  const words = title
    .replace(/["'“”‘’()[\]]/g, '')
    .split(/[\s\-–—:;,.]+/)
    .filter(Boolean)
  const body = words.length > 1 && /^(the|a|an)$/i.test(words[0]) ? words.slice(1) : words
  if (body.length === 1 && UNSPACED_SCRIPT.test(body[0])) return graphemes(body[0]).slice(0, 2).join('')
  return body.slice(0, 2).map(w => (graphemes(w)[0] ?? '').toUpperCase()).join('')
}

const UNSPACED_SCRIPT = /^[\p{Script=Han}\p{Script=Hiragana}\p{Script=Katakana}]/u

function graphemes(s: string): string[] {
  if (typeof Intl !== 'undefined' && 'Segmenter' in Intl) {
    return Array.from(new Intl.Segmenter(undefined, { granularity: 'grapheme' }).segment(s), x => x.segment)
  }
  return Array.from(s)
}
