import { useTranslation } from 'react-i18next'
import type { View } from './useView'

interface Props {
  view: View
  onChange: (v: View) => void
}

export default function ViewToggle({ view, onChange }: Props) {
  const { t } = useTranslation()
  const btn = (v: View) =>
    `px-2 py-1 rounded text-xs font-medium transition-colors ${
      view === v
        ? 'bg-slate-300 dark:bg-zinc-700 text-slate-900 dark:text-white'
        : 'text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white hover:bg-slate-200/50 dark:hover:bg-zinc-800/50'
    }`
  return (
    <div className="inline-flex gap-1 border border-slate-200 dark:border-zinc-800 rounded p-0.5">
      <button
        onClick={() => onChange('grid')}
        className={btn('grid')}
        title={t('common.gridView')}
        aria-label={t('common.gridView')}
        aria-pressed={view === 'grid'}
      >
        ▦
      </button>
      <button
        onClick={() => onChange('table')}
        className={btn('table')}
        title={t('common.tableView')}
        aria-label={t('common.tableView')}
        aria-pressed={view === 'table'}
      >
        ☰
      </button>
    </div>
  )
}
