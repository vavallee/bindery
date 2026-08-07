// Palette + hash for CoverPlaceholder, kept separate from the component so
// contrast.test.ts can import the colours without pulling in JSX.
//
// Roughly half a typical library has no cover art. A flat grey box reads as a
// broken image, so the placeholder paints its own deliberate surface instead:
// a saturated ground with the title set large over it, the way an actual cover
// would be. Because the tile supplies its own background, the text contrast is
// self-contained and identical in light and dark themes — there is no
// theme-dependent pairing that can drift.
//
// Every ground satisfies three properties, all enforced by contrast.test.ts:
//   1. >= 4.5:1 against white text — AA for normal text, not merely the 3:1
//      large-text allowance, so the title stays legible at any tile size.
//   2. >= 3:1 against the light page surface (slate-50).
//   3. >= 3:1 against the dark page surface (zinc-950).
// (2) and (3) are WCAG 1.4.11 non-text contrast: without them the tile edge
// dissolves into the page and the card stops reading as an object.
//
// That third constraint is why the palette has no deep blue, indigo or violet:
// at the saturation needed for white text they land within 3:1 of zinc-950.
// Tailwind 900-level grounds fail it across the board (all ~2:1), which is what
// the previous flat `bg-zinc-800` box was doing wrong in the first place.

export interface CoverColor {
  /** Tailwind class applied to the tile. */
  className: string
  /** Ground colour as hex — the value contrast.test.ts asserts against. */
  bg: string
}

// Hues are spread around the wheel so adjacent books in a grid rarely collide.
export const COVER_COLORS: readonly CoverColor[] = [
  { className: 'bg-red-700', bg: '#b91c1c' },
  { className: 'bg-orange-700', bg: '#c2410c' },
  { className: 'bg-amber-700', bg: '#b45309' },
  { className: 'bg-yellow-700', bg: '#a16207' },
  { className: 'bg-lime-700', bg: '#4d7c0f' },
  { className: 'bg-green-700', bg: '#15803d' },
  { className: 'bg-emerald-700', bg: '#047857' },
  { className: 'bg-teal-700', bg: '#0f766e' },
  { className: 'bg-cyan-700', bg: '#0e7490' },
  { className: 'bg-sky-700', bg: '#0369a1' },
  { className: 'bg-fuchsia-700', bg: '#a21caf' },
  { className: 'bg-rose-700', bg: '#be123c' },
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
