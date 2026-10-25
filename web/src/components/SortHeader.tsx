import { useTranslation } from 'react-i18next'

interface Props<K extends string> {
  label: string
  // The sort key this column sorts by.
  sortKey: K
  // The active sort key and direction.
  activeKey: K
  direction: 'asc' | 'desc'
  // Clicking the active column flips direction; another column starts at its
  // natural direction.
  onSort: (key: K) => void
  className?: string
}

// Chevrons drawn as SVG in the same outline style as the header icons, so the
// arrow never depends on a font having the glyph.
function SortIcon({ state }: { state: 'asc' | 'desc' | 'none' }) {
  return (
    <svg aria-hidden="true" viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth={1.75}
      className={`h-3.5 w-3.5 ${state === 'none' ? 'opacity-40' : ''}`}>
      {state !== 'desc' && <path strokeLinecap="round" strokeLinejoin="round" d="M6.5 8.5 10 5l3.5 3.5" />}
      {state !== 'asc' && <path strokeLinecap="round" strokeLinejoin="round" d="M6.5 11.5 10 15l3.5-3.5" />}
    </svg>
  )
}

// SortHeader is a clickable column header showing the active sort. Every list
// table sorts through it (via DataTable); the per-page copies it replaced drew
// ▲/▼ glyphs that depended on the font and disagreed on the idle state.
//
// Tailwind v4's Preflight gives <button> no pointer cursor, so the header sets
// cursor-pointer itself; without it a sortable column reads as plain text.
export default function SortHeader<K extends string>({ label, sortKey, activeKey, direction, onSort, className = '' }: Props<K>) {
  const { t } = useTranslation()
  const active = sortKey === activeKey
  return (
    <th
      scope="col"
      aria-sort={active ? (direction === 'asc' ? 'ascending' : 'descending') : 'none'}
      className={`text-left px-3 py-2 text-xs font-medium uppercase ${className}`}
    >
      <button
        type="button"
        onClick={() => onSort(sortKey)}
        title={t('books.sortByColumn', { column: label, defaultValue: 'Sort by {{column}}' })}
        className={`inline-flex items-center gap-1 uppercase tracking-wide transition-colors cursor-pointer select-none ${active ? 'text-slate-900 dark:text-white' : 'text-fg-muted hover:text-slate-900 dark:hover:text-white'}`}
      >
        {label}
        <SortIcon state={active ? direction : 'none'} />
      </button>
    </th>
  )
}
