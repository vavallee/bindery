import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import Checkbox from './Checkbox'
import SortHeader from './SortHeader'

// The one table for library lists. Books, Authors, Author detail and Wanted
// each hand-rolled theirs, which is how the same job ended up with circles on
// one page and squares on the next, a Status column on one list and not the
// other, and three copies of a sortable header. This is the Author detail
// table promoted: square checkboxes, a Status column where the caller gives
// one, SortHeader for every sortable column, one inline action per row and
// everything else behind the row's More menu.
//
// Narrow screens: the table scrolls inside its own container, and the
// selection box and first column stay pinned on the left, so a phone keeps the
// row's identity on screen while the rest scrolls under it. Pinned cells take
// the row's background (`bg-inherit`), which is why rows use opaque surfaces
// rather than the translucent tints the old tables layered over the page.

export interface Column<T> {
  key: string
  /** Visible header text; empty for an unlabelled column such as actions. */
  header: string
  /** Makes the header a SortHeader that reports this key to `sort.onSort`. */
  sortKey?: string
  /** Applied to both the header and the cells: responsive hiding, widths, alignment. */
  className?: string
  /** Rendered after the header text, e.g. a help popover beside Status. */
  headerExtra?: ReactNode
  cell: (row: T, index: number) => ReactNode
}

export interface DataTableSort {
  key: string
  direction: 'asc' | 'desc'
  onSort: (key: string) => void
}

export interface DataTableSelection<K> {
  selected: Set<K>
  onToggle: (key: K) => void
  /** Header box: true selects every row on this page, false clears. */
  onToggleAll: (select: boolean) => void
}

const ROW = 'bg-slate-100 dark:bg-zinc-900 hover:bg-slate-200 dark:hover:bg-zinc-800'
const ROW_SELECTED = 'bg-emerald-50 dark:bg-emerald-950 hover:bg-emerald-100 dark:hover:bg-emerald-900'
// Pinned columns. The selection column is w-10 (2.5rem), so the first data
// column pins at left-10 when there is one.
const PIN_SELECT = 'sticky left-0 z-[1] bg-inherit'
const PIN_FIRST_AFTER_SELECT = 'sticky left-10 z-[1] bg-inherit'
const PIN_FIRST = 'sticky left-0 z-[1] bg-inherit'

export default function DataTable<T, K extends string | number>({
  rows,
  rowKey,
  rowLabel,
  columns,
  sort,
  selection,
  onRowClick,
  rowActions,
  ariaLabel,
}: {
  rows: T[]
  rowKey: (row: T) => K
  /** Names the row for its checkbox ("Select Dune") and its actions. */
  rowLabel: (row: T) => string
  columns: Column<T>[]
  sort?: DataTableSort
  selection?: DataTableSelection<K>
  /** Whole-row navigation. Cells holding their own controls stop the click. */
  onRowClick?: (row: T, index: number) => void
  /** The row's trailing cell: one inline action and/or a MoreMenu. */
  rowActions?: (row: T, index: number) => ReactNode
  ariaLabel?: string
}) {
  const { t } = useTranslation()
  const keys = rows.map(rowKey)
  const selectedOnPage = selection ? keys.filter(k => selection.selected.has(k)).length : 0
  const allSelected = rows.length > 0 && selectedOnPage === rows.length
  const pinFirst = selection ? PIN_FIRST_AFTER_SELECT : PIN_FIRST

  return (
    <div className="border border-slate-200 dark:border-zinc-800 rounded-lg overflow-hidden">
      <div className="overflow-x-auto">
        <table className="w-full text-sm" aria-label={ariaLabel}>
          <thead>
            <tr className="bg-slate-100 dark:bg-zinc-900 border-b border-slate-200 dark:border-zinc-800">
              {selection && (
                <th scope="col" className={`px-3 py-2 w-10 ${PIN_SELECT}`}>
                  <Checkbox
                    checked={allSelected}
                    indeterminate={selectedOnPage > 0 && !allSelected}
                    onChange={selection.onToggleAll}
                    label={t('dataTable.selectPage', 'Select all on this page')}
                  />
                </th>
              )}
              {columns.map((col, i) => {
                const cls = `${col.className ?? ''} ${i === 0 ? pinFirst : ''}`
                if (col.sortKey && sort) {
                  return (
                    <SortHeader
                      key={col.key}
                      label={col.header}
                      sortKey={col.sortKey}
                      activeKey={sort.key}
                      direction={sort.direction}
                      onSort={sort.onSort}
                      className={cls}
                      extra={col.headerExtra}
                    />
                  )
                }
                return (
                  <th key={col.key} scope="col" className={`text-left px-3 py-2 text-xs font-medium uppercase tracking-wide text-fg-muted ${cls}`}>
                    {col.header}
                    {col.headerExtra}
                  </th>
                )
              })}
              {rowActions && (
                <th scope="col" className="px-3 py-2">
                  <span className="sr-only">{t('dataTable.actions', 'Actions')}</span>
                </th>
              )}
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-200 dark:divide-zinc-800">
            {rows.map((row, i) => {
              const key = keys[i]
              const selected = selection?.selected.has(key) ?? false
              return (
                <tr
                  key={key}
                  className={`${selected ? ROW_SELECTED : ROW} ${onRowClick ? 'cursor-pointer' : ''}`}
                  onClick={onRowClick ? () => onRowClick(row, i) : undefined}
                >
                  {selection && (
                    <td className={`px-3 py-2 w-10 ${PIN_SELECT}`} onClick={e => e.stopPropagation()}>
                      {/* The label fills the cell, so the whole cell toggles
                          the box: the 16px box alone is hard to tap. */}
                      <label className="flex items-center -mx-3 -my-2 px-3 py-2 cursor-pointer pointer-coarse:min-h-11">
                        <Checkbox
                          checked={selected}
                          onChange={() => selection.onToggle(key)}
                          label={t('dataTable.selectRow', 'Select {{name}}', { name: rowLabel(row) })}
                        />
                      </label>
                    </td>
                  )}
                  {columns.map((col, ci) => (
                    <td key={col.key} className={`px-3 py-2 ${col.className ?? ''} ${ci === 0 ? pinFirst : ''}`}>
                      {col.cell(row, i)}
                    </td>
                  ))}
                  {rowActions && (
                    <td className="px-3 py-2 text-right whitespace-nowrap" onClick={e => e.stopPropagation()}>
                      <div className="inline-flex items-center justify-end gap-1">{rowActions(row, i)}</div>
                    </td>
                  )}
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>
    </div>
  )
}
