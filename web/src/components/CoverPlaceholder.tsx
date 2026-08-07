import { coverColorFor } from './coverPalette'

// Stand-in for a book with no cover art. The old version was a flat grey box
// with small centred muted text, which read as a failed image load; roughly
// half a typical library looks like that. This paints a deliberate surface
// instead — per-book ground colour, title set large and ranged left along the
// bottom — so a coverless book reads as a book rather than as breakage.
//
// The ground is hashed from the book id, so a given book keeps the same colour
// across reloads and across every place its cover is drawn. See coverPalette.ts
// for the colour choices and the contrast properties they guarantee.

export type CoverSize = 'xs' | 'sm' | 'md'

// At `xs` (the 24x36px table thumbnail) there is no room for legible text, so
// the tile carries colour only — still useful, since the colour is the book's
// identity in the grid and the table alike.
const TITLE_CLS: Record<CoverSize, string> = {
  xs: '',
  sm: 'text-xs font-semibold leading-tight line-clamp-3 p-1.5',
  md: 'text-lg font-semibold leading-tight line-clamp-5 p-3',
}

export default function CoverPlaceholder({
  id,
  title,
  size = 'md',
  className = '',
}: {
  id: number | string
  title: string
  size?: CoverSize
  className?: string
}) {
  const color = coverColorFor(id)
  return (
    <div
      className={`flex flex-col justify-end overflow-hidden text-white ${color.className} ${className}`}
      // The title is already rendered as text alongside every use of this
      // component (card heading, detail <h2>, table cell), so announcing it
      // again here would just double it up for a screen reader.
      aria-hidden
    >
      {size !== 'xs' && <span className={`block ${TITLE_CLS[size]}`}>{title}</span>}
    </div>
  )
}
