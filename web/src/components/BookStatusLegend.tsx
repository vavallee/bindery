import { useTranslation } from 'react-i18next'
import { bookStatusBadge } from './bookStatus'

// Collapsible legend for the library status pills. It renders each swatch via
// bookStatusBadge so the legend always matches the real badges (same labels,
// same AA-checked colours) instead of being a hand-maintained copy that drifts.
const ENTRIES: Array<{ status: string; monitored: boolean }> = [
  { status: 'wanted', monitored: true },
  { status: 'downloading', monitored: true },
  { status: 'downloaded', monitored: true },
  { status: 'imported', monitored: true },
  { status: 'skipped', monitored: true },
  { status: 'wanted', monitored: false }, // resolves to "Not monitored"
]

export default function BookStatusLegend() {
  const { t } = useTranslation()
  return (
    <details className="mb-4 text-xs text-slate-600 dark:text-zinc-400">
      <summary className="cursor-pointer select-none hover:text-slate-900 dark:hover:text-white">
        {t('books.legendTitle', 'What do the status labels mean?')}
      </summary>
      <ul className="mt-2 flex flex-wrap gap-x-4 gap-y-2">
        {ENTRIES.map(({ status, monitored }) => {
          const badge = bookStatusBadge(status, monitored, t)
          return (
            <li key={`${status}-${monitored}`}>
              <span className={`px-2 py-0.5 rounded text-[10px] font-medium ${badge.colorClass}`}>
                {badge.label}
              </span>
            </li>
          )
        })}
      </ul>
    </details>
  )
}
