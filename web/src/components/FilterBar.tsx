import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import FilterPopover, { FilterGroup, type FilterOption } from './FilterPopover'

// The one filter bar for library lists. Books had a search row with status
// pills and then Sort/Filters disclosures; Author detail had six native
// selects in a row; Wanted had a title search and nothing else. Three models
// for one job, so every page taught its own.
//
// Layout and its reasons, carried over from the Books page (#2427):
//   - Search, and status as an inline segment. Status stays out in the open:
//     it is the facet people change constantly, and burying it in a popover
//     would trade tidiness for the thing the page is for.
//   - Sort and the secondary facets behind two disclosures. The Filters
//     trigger carries a count, and every applied facet also renders as a
//     removable chip, so a filter that is on is never only visible inside
//     the popover.
//   - The result count at the end of the row, so a filter's effect is
//     readable without scrolling to the pagination.
//
// Sort is offered in both grid and table views. The table headers sort too,
// but they drive the same value, so the two cannot disagree, and a page whose
// Sort control vanished when you switched view read as a page without sort.

export interface FilterChip {
  key: string
  label: string
  onClear: () => void
}

export interface StatusSegment<T extends string> {
  value: T
  onChange: (next: T) => void
  options: { value: T; label: string }[]
  /** Accessible name for the group. */
  label: string
  /** Rendered after the segment, e.g. the status legend popover. */
  help?: ReactNode
}

// Status chips. 24px tall, so the hit area grows on touch (touch-target), and
// wrapped rows sit 20px apart there so one row's hit area does not take taps
// aimed at the next (pointer-coarse:gap-y-5 on the row).
const statusBtnCls = (active: boolean) =>
  `touch-target px-3 py-1 rounded-md text-xs font-medium transition-colors ${active ? 'bg-slate-300 dark:bg-zinc-700 text-slate-900 dark:text-white' : 'text-fg-muted hover:text-slate-900 dark:hover:text-white'}`

const chipCls = 'touch-target inline-flex items-center gap-1 px-2 py-1 rounded-full bg-slate-200 dark:bg-zinc-800 text-xs text-slate-700 dark:text-zinc-300 hover:bg-slate-300 dark:hover:bg-zinc-700'

export default function FilterBar<S extends string, O extends string>({
  search,
  status,
  sort,
  filters,
  chips = [],
  summary,
  children,
}: {
  search?: { value: string; onChange: (next: string) => void; placeholder: string }
  status?: StatusSegment<S>
  sort?: { value: O; onChange: (next: O) => void; options: FilterOption<O>[]; label: string }
  /** FilterGroup children for the Filters popover, with the number of applied facets. */
  filters?: { activeCount: number; content: ReactNode }
  chips?: FilterChip[]
  /**
   * Result count, e.g. "1,204 books". Pass '' while loading rather than
   * leaving it out: the live region has to be in the page before its text
   * changes, or many screen readers never announce the count.
   */
  summary?: ReactNode
  /** Extra controls at the end of the second row (a toggle, an action). */
  children?: ReactNode
}) {
  const { t } = useTranslation()
  const hasSecondRow = sort || filters || chips.length > 0 || summary !== undefined || children
  return (
    <div className="mb-4 space-y-3" data-testid="filter-bar">
      {(search || status) && (
        <div className="flex flex-col sm:flex-row sm:items-center gap-3">
          {search && (
            <input
              enterKeyHint="search"
              type="search"
              value={search.value}
              onChange={e => search.onChange(e.target.value)}
              placeholder={search.placeholder}
              aria-label={search.placeholder}
              className="flex-1 min-w-0 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm"
            />
          )}
          {status && (
            <div className="flex items-center gap-1">
              <div role="group" aria-label={status.label} className="flex gap-1 pointer-coarse:gap-y-5 flex-wrap">
                {status.options.map(opt => (
                  <button
                    key={opt.value}
                    type="button"
                    aria-pressed={status.value === opt.value}
                    onClick={() => status.onChange(opt.value)}
                    className={statusBtnCls(status.value === opt.value)}
                  >
                    {opt.label}
                  </button>
                ))}
              </div>
              {status.help}
            </div>
          )}
        </div>
      )}
      {hasSecondRow && (
        <div className="flex items-center gap-2 flex-wrap pointer-coarse:gap-y-5">
          {sort && (
            <FilterPopover label={t('common.sort', 'Sort')} ariaLabel={sort.label}>
              <FilterGroup label={sort.label} value={sort.value} onChange={sort.onChange} options={sort.options} />
            </FilterPopover>
          )}
          {filters && (
            <FilterPopover label={t('common.filters', 'Filters')} activeCount={filters.activeCount}>
              {filters.content}
            </FilterPopover>
          )}
          {chips.map(chip => (
            <button key={chip.key} type="button" onClick={chip.onClear} className={chipCls}>
              {chip.label}
              <span aria-hidden="true">✕</span>
              <span className="sr-only">{t('common.clearFilter', 'Clear filter')}</span>
            </button>
          ))}
          {children}
          {/* Polite live region: a filter change is announced as its result
              count, which is the only thing that changes on screen for a
              screen reader user. */}
          {summary !== undefined && <span aria-live="polite" className="ml-auto text-sm text-fg-muted tabular-nums">{summary}</span>}
        </div>
      )}
    </div>
  )
}
