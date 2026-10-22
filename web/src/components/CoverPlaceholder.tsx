import { coverColorFor, titleInitials } from './coverPalette'

// Stand-in for a book with no cover art. The old version was a flat grey box
// with small centred muted text, which read as a failed image load; roughly
// half a typical library looks like that. This paints a deliberate surface
// instead — a per-book muted ground, with the title or its initials set along
// the bottom — so a coverless book reads as a book rather than as breakage.
//
// The ground is hashed from the book id, so a given book keeps the same colour
// across reloads and across every place its cover is drawn. A book that is not
// in the library has no id yet; its caller passes the provider id with a
// prefix instead (Discover `rec-<id>`, a Series gap `hc-<foreignBookId>`), so
// the seed is still stable and never collides with a library id. See
// coverPalette.ts for the colour choices and the contrast properties they
// guarantee.
//
// Every list draws coverless books through this one component. Each page used
// to have its own fallback (an icon on Books, a grey box with "no cover" on
// Wanted, a bare grey tile in tables), so the same book looked different on
// every page.

export type CoverSize = 'xs' | 'sm' | 'md'

// At `xs` (the 24x36px table thumbnail) there is no room for legible text, so
// the tile carries colour only — still useful, since the colour is the book's
// identity in the grid and the table alike.
const TITLE_CLS: Record<CoverSize, string> = {
  xs: '',
  sm: 'text-xs font-semibold leading-tight line-clamp-3 p-1.5',
  md: 'text-lg font-semibold leading-tight line-clamp-5 p-3',
}

const INITIALS_CLS: Record<CoverSize, string> = {
  xs: '',
  sm: 'text-lg font-semibold tracking-wide p-2',
  md: 'text-3xl font-semibold tracking-wide p-3',
}

export default function CoverPlaceholder({
  id,
  title,
  size = 'md',
  show = 'title',
  className = '',
}: {
  id: number | string
  title: string
  size?: CoverSize
  /**
   * What the tile prints. 'title' sets the title on the tile; 'initials' sets two letters, for a
   * card that already prints the title under the cover, where the title
   * twice in one card was the most repeated text on the page.
   */
  show?: 'title' | 'initials'
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
      {size !== 'xs' && (
        show === 'initials'
          ? <span className={`block ${INITIALS_CLS[size]}`}>{titleInitials(title)}</span>
          : <span className={`block ${TITLE_CLS[size]}`}>{title}</span>
      )}
    </div>
  )
}
