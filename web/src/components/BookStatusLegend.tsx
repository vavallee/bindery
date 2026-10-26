import { useEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { bookStatusBadge } from './bookStatus'

// The status legend, as a small "?" beside the status filter rather than a
// disclosure line above the grid. It is still one click from the labels it
// explains, but it no longer spends a row of every visit on a glossary.
//
// Each swatch renders via bookStatusBadge so the legend always matches the
// real badges (same labels, same AA-checked colours) instead of being a
// hand-maintained copy that drifts. A native <details> keeps it keyboard
// operable with no script; outside pointerdown, Escape and focus leaving close it, matching
// MoreMenu and FilterPopover.
const ENTRIES: Array<{ status: string; monitored: boolean }> = [
  { status: 'wanted', monitored: true },
  { status: 'imported', monitored: true },
  { status: 'skipped', monitored: true },
  { status: 'wanted', monitored: false }, // resolves to "Not monitored"
]

export default function BookStatusLegend() {
  const { t } = useTranslation()
  const ref = useRef<HTMLDetailsElement>(null)

  useEffect(() => {
    const onPointerDown = (e: PointerEvent) => {
      if (ref.current?.open && !ref.current.contains(e.target as Node)) ref.current.open = false
    }
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && ref.current?.open) {
        ref.current.open = false
        ref.current.querySelector('summary')?.focus()
      }
    }
    document.addEventListener('pointerdown', onPointerDown)
    document.addEventListener('keydown', onKeyDown)
    return () => {
      document.removeEventListener('pointerdown', onPointerDown)
      document.removeEventListener('keydown', onKeyDown)
    }
  }, [])

  const title = t('books.legendTitle', 'What do the status labels mean?')
  return (
    <details
      ref={ref}
      className="relative text-xs text-fg-muted"
      // Tabbing past the "?" closes the popover, so it does not stay open
      // over the list while focus is somewhere else. Only a move to a known
      // element outside counts: a null relatedTarget is a click on plain text
      // inside the panel or the window losing focus, and outside presses are
      // the pointerdown handler's job.
      onBlur={e => {
        const next = e.relatedTarget as Node | null
        if (next && ref.current?.open && !ref.current.contains(next)) ref.current.open = false
      }}
    >
      <summary
        aria-label={title}
        title={title}
        className="touch-target list-none [&::-webkit-details-marker]:hidden cursor-pointer select-none inline-flex items-center justify-center w-5 h-5 rounded-full border border-slate-400 dark:border-zinc-600 font-semibold hover:text-slate-900 dark:hover:text-white"
      >
        ?
      </summary>
      <div className="absolute right-0 sm:left-0 sm:right-auto top-full mt-1 z-20 w-72 max-w-[calc(100vw-2rem)] rounded-lg border border-slate-200 dark:border-zinc-700 bg-white dark:bg-zinc-800 shadow-lg p-3">
        <p className="font-semibold text-slate-800 dark:text-zinc-200 mb-2">{title}</p>
        <ul className="flex flex-col gap-y-1.5">
          {ENTRIES.map(({ status, monitored }) => {
            const badge = bookStatusBadge(status, monitored, t)
            return (
              <li key={`${status}-${monitored}`} className="flex items-baseline gap-2">
                <span className={`shrink-0 px-2 py-0.5 rounded text-[10px] font-medium ${badge.colorClass}`}>
                  {badge.label}
                </span>
                <span>{badge.description}</span>
              </li>
            )
          })}
        </ul>
      </div>
    </details>
  )
}
