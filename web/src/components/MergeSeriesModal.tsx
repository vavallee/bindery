import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useConfirmDialog } from './useConfirmDialog'
import { api, Series, SeriesMergePlan } from '../api/client'
import { useModal } from './useModal'

interface Props {
  target: Series
  series: Series[]
  onClose: () => void
  onMerged: () => void
}

// MergeSeriesModal folds other series into the one it was opened on (#2554):
// one series that a provider named two ways, or renamed, ends up as one row.
// The user picks the series to merge in and optionally a new name, previews
// the plan the server computes (a dry run), and only then applies it. Any
// change to the selection or the name drops the preview, so what is applied is
// always what was shown.
export default function MergeSeriesModal({ target, series, onClose, onMerged }: Props) {
  const { t } = useTranslation()
  const { confirm, confirmDialog } = useConfirmDialog()
  const [filter, setFilter] = useState('')
  const [selected, setSelected] = useState<number[]>([])
  const [title, setTitle] = useState('')
  const [plan, setPlan] = useState<SeriesMergePlan | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const candidates = useMemo(() => {
    const q = filter.trim().toLowerCase()
    return series
      .filter(s => s.id !== target.id && (q === '' || s.title.toLowerCase().includes(q)))
      .sort((a, b) => a.title.localeCompare(b.title))
  }, [series, target.id, filter])
  const titleOf = (id: number) => series.find(s => s.id === id)?.title ?? String(id)

  const toggle = (id: number) => {
    setPlan(null)
    setSelected(prev => (prev.includes(id) ? prev.filter(x => x !== id) : [...prev, id]))
  }

  const run = async (dryRun: boolean) => {
    setBusy(true)
    setError(null)
    try {
      const result = await api.mergeSeries(target.id, { sourceIds: selected, title: title.trim() || undefined, dryRun })
      if (dryRun) {
        setPlan(result)
      } else {
        onMerged()
        onClose()
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : t('series.merge.failed'))
    } finally {
      setBusy(false)
    }
  }

  const apply = async (shown: SeriesMergePlan) => {
    if (!await confirm({
      title: t('series.merge.title', { title: target.title }),
      body: t('series.merge.confirmBody', { count: shown.sources.length, title: shown.title }),
      confirmLabel: t('series.merge.apply'),
    })) return
    await run(false)
  }

  const { titleId, panelProps } = useModal({ onClose, canClose: !busy })

  return (
    <div className="modal-overlay fixed inset-0 bg-black/60 flex items-center justify-center p-4 z-50" onClick={onClose}>
      {confirmDialog}
      <div
        {...panelProps}
        className="bg-slate-100 dark:bg-zinc-900 border border-slate-300 dark:border-zinc-700 rounded-lg w-full max-w-xl shadow-2xl modal-max-h flex flex-col"
        onClick={e => e.stopPropagation()}
      >
        <div className="p-4 border-b border-slate-200 dark:border-zinc-800">
          <h3 id={titleId} className="text-lg font-semibold">{t('series.merge.title', { title: target.title })}</h3>
          <p className="text-xs text-slate-600 dark:text-zinc-500 mt-1">{t('series.merge.description')}</p>
        </div>

        <div className="p-4 space-y-3 overflow-y-auto">
          <input
            value={filter}
            onChange={e => setFilter(e.target.value)}
            placeholder={t('series.merge.filterPlaceholder')}
            aria-label={t('series.merge.filterPlaceholder')}
            className="w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded-md px-3 py-2 text-sm focus:outline-none focus:border-emerald-500"
          />
          <ul className="max-h-48 overflow-y-auto space-y-1" aria-label={t('series.merge.sourcesLabel')}>
            {candidates.map(s => (
              <li key={s.id}>
                <label className="flex items-center gap-2 text-sm cursor-pointer">
                  <input type="checkbox" checked={selected.includes(s.id)} onChange={() => toggle(s.id)} />
                  <span>{s.title}</span>
                  <span className="text-xs text-slate-500 dark:text-zinc-500">{t('series.merge.bookCount', { count: s.books?.length ?? 0 })}</span>
                </label>
              </li>
            ))}
            {candidates.length === 0 && (
              <li className="text-xs text-slate-500 dark:text-zinc-500">{t('series.merge.noCandidates')}</li>
            )}
          </ul>

          <div>
            <label htmlFor="merge-series-title" className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">{t('series.merge.renameLabel')}</label>
            <input
              id="merge-series-title"
              value={title}
              onChange={e => { setTitle(e.target.value); setPlan(null) }}
              placeholder={target.title}
              className="w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded-md px-3 py-2 text-sm focus:outline-none focus:border-emerald-500"
            />
          </div>

          {plan && (
            <div className="text-xs text-slate-700 dark:text-zinc-400 bg-slate-200/50 dark:bg-zinc-800/50 rounded-md p-3 space-y-2" data-testid="merge-preview">
              {plan.sources.map(src => (
                <div key={src.id}>
                  <p className="font-medium text-slate-800 dark:text-zinc-200">
                    {t('series.merge.sourceSummary', { title: src.title, moved: src.moved.length, kept: src.kept.length })}
                  </p>
                  {src.conflicts.map(c => (
                    <p key={c.bookId} className="text-amber-700 dark:text-amber-300">
                      {t('series.merge.conflict', { book: c.title, kept: c.targetPosition, dropped: c.sourcePosition })}
                    </p>
                  ))}
                </div>
              ))}
              {plan.title !== target.title && <p>{t('series.merge.renamed', { title: plan.title })}</p>}
              {plan.hardcoverLinkFrom !== 0 && <p>{t('series.merge.takesHardcoverLink', { title: titleOf(plan.hardcoverLinkFrom) })}</p>}
              {plan.genreOverrideFrom !== 0 && <p>{t('series.merge.takesGenres', { title: titleOf(plan.genreOverrideFrom) })}</p>}
              {plan.monitored && !target.monitored && <p>{t('series.merge.becomesMonitored')}</p>}
              <p>{t('series.merge.aliases', { count: plan.aliases.length })}</p>
            </div>
          )}

          {error && <p className="text-xs text-red-600 dark:text-red-400" role="alert">{error}</p>}
        </div>

        <div className="p-4 border-t border-slate-200 dark:border-zinc-800 flex justify-end gap-2">
          <button onClick={onClose} className="text-sm px-3 py-1.5 rounded bg-slate-200 dark:bg-zinc-800 hover:bg-slate-300 dark:hover:bg-zinc-700">
            {t('common.cancel')}
          </button>
          {plan ? (
            <button
              onClick={() => apply(plan)}
              disabled={busy}
              className="text-sm px-3 py-1.5 rounded bg-emerald-600 hover:bg-emerald-500 text-white disabled:opacity-50"
            >
              {t('series.merge.apply')}
            </button>
          ) : (
            <button
              onClick={() => run(true)}
              disabled={busy || selected.length === 0}
              className="text-sm px-3 py-1.5 rounded bg-emerald-600 hover:bg-emerald-500 text-white disabled:opacity-50"
            >
              {t('series.merge.preview')}
            </button>
          )}
        </div>
      </div>
    </div>
  )
}
