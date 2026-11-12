import { useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, DuplicateMergeGroup, DuplicateSeriesGroup, Series } from '../api/client'
import { useConfirmDialog } from './useConfirmDialog'
import MergeSeriesModal from './MergeSeriesModal'
import { btn, btnSize } from './buttons'

interface Props {
  // The page's series list: the merge dialog lists and names series from it,
  // and a change in its length (a merge or delete elsewhere on the page)
  // reloads the duplicates.
  series: Series[]
  onMerged: () => void | Promise<unknown>
}

// SeriesDuplicates finds series that exist more than once under one title, as
// the Audiobookshelf import compares titles (#2173). Each such group is a
// series the import cannot link a Hardcover series to, so it is worth
// clearing. A banner says how many there are and opens the list. Each group
// merges through the usual merge dialog and its preview; groups whose series
// share an author can also be merged together, after a dry run and a
// confirmation. Groups with different authors are probably different series
// with one name and are only ever merged one at a time.
export default function SeriesDuplicates({ series, onMerged }: Props) {
  const { t } = useTranslation()
  const { confirm, confirmDialog } = useConfirmDialog()
  const [groups, setGroups] = useState<DuplicateSeriesGroup[] | null>(null)
  const [open, setOpen] = useState(false)
  const [keep, setKeep] = useState<Record<string, number>>({})
  const [merging, setMerging] = useState<{ target: Series; sourceIds: number[] } | null>(null)
  const [busy, setBusy] = useState(false)
  const [result, setResult] = useState<string | null>(null)
  const [failures, setFailures] = useState<string[]>([])

  const load = useCallback(async () => {
    try {
      const { groups } = await api.listSeriesDuplicates()
      setGroups(groups)
    } catch (err) {
      // The page works without it; the banner just does not show.
      console.error(err)
      setGroups([])
    }
  }, [])

  const seriesCount = series.length
  useEffect(() => { void load() }, [load, seriesCount])

  const keepOf = (g: DuplicateSeriesGroup) => keep[g.key] ?? g.suggestedKeepId
  const asGroup = (g: DuplicateSeriesGroup): DuplicateMergeGroup => {
    const keepId = keepOf(g)
    return { keepId, sourceIds: g.series.filter(s => s.id !== keepId).map(s => s.id) }
  }

  const afterMerge = async () => {
    await load()
    await onMerged()
  }

  const mergeOne = (g: DuplicateSeriesGroup) => {
    const { keepId, sourceIds } = asGroup(g)
    const dup = g.series.find(s => s.id === keepId)
    const target = series.find(s => s.id === keepId) ?? (dup && {
      id: dup.id, title: dup.title, foreignSeriesId: dup.foreignSeriesId, description: '', monitored: dup.monitored,
    })
    if (!target) return
    setResult(null)
    setFailures([])
    setMerging({ target, sourceIds })
  }

  const matching = (groups ?? []).filter(g => g.sameAuthors)

  const mergeAllMatching = async () => {
    if (matching.length === 0) return
    setBusy(true)
    setResult(null)
    setFailures([])
    try {
      const preview = await api.mergeSeriesDuplicates({ groups: matching.map(asGroup), dryRun: true })
      const ready = preview.results.filter(r => r.ok)
      const removed = ready.reduce((n, r) => n + r.sourceIds.length, 0)
      const moved = ready.reduce((n, r) => n + (r.plan?.sources ?? []).reduce((m, s) => m + s.moved.length, 0), 0)
      if (ready.length === 0) {
        setFailures(preview.results.filter(r => !r.ok).map(r => r.error ?? ''))
        setResult(t('series.duplicates.nothingToMerge'))
        return
      }
      const body = [t('series.duplicates.bulkSummary', { count: ready.length, removed, moved })]
      if (preview.failed > 0) body.push(t('series.duplicates.bulkSkipped', { count: preview.failed }))
      body.push(t('series.duplicates.bulkIrreversible'))
      if (!await confirm({
        title: t('series.duplicates.bulkTitle'),
        body: body.join('\n\n'),
        confirmLabel: t('series.duplicates.bulkConfirm', { count: ready.length }),
      })) return
      const done = await api.mergeSeriesDuplicates({ groups: ready.map(r => ({ keepId: r.keepId, sourceIds: r.sourceIds })) })
      setResult(t('series.duplicates.bulkDone', { count: done.merged }))
      setFailures(done.results.filter(r => !r.ok).map(r => r.error ?? ''))
      await afterMerge()
    } catch (err) {
      setResult(err instanceof Error ? err.message : t('series.merge.failed'))
    } finally {
      setBusy(false)
    }
  }

  if (!groups || (groups.length === 0 && !result)) return null

  return (
    <div className="mb-4 border border-amber-300 dark:border-amber-800 bg-amber-50 dark:bg-amber-950/30 rounded-lg p-3" data-testid="series-duplicates">
      {confirmDialog}
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="min-w-0">
          <p className="text-sm font-medium">
            {groups.length > 0 ? t('series.duplicates.banner', { count: groups.length }) : t('series.duplicates.none')}
          </p>
          {groups.length > 0 && <p className="text-xs text-fg-muted">{t('series.duplicates.bannerHint')}</p>}
        </div>
        {groups.length > 0 && (
          <button type="button" onClick={() => setOpen(o => !o)} aria-expanded={open} className={`${btn.secondary} ${btnSize.sm}`}>
            {open ? t('series.duplicates.hide') : t('series.duplicates.review')}
          </button>
        )}
      </div>

      {result && (
        <div className="mt-2 text-xs" role="status">
          <p>{result}</p>
          {failures.map((f, i) => <p key={i} className="text-red-700 dark:text-red-400">{f}</p>)}
        </div>
      )}

      {open && groups.length > 0 && (
        <div className="mt-3 space-y-3">
          <div className="flex flex-wrap items-center gap-2">
            <button
              type="button"
              onClick={() => { void mergeAllMatching() }}
              disabled={busy || matching.length === 0}
              className={`${btn.primary} ${btnSize.sm} disabled:opacity-50`}
            >
              {t('series.duplicates.mergeAllMatching', { count: matching.length })}
            </button>
            <span className="text-xs text-fg-muted">{t('series.duplicates.mergeAllHint')}</span>
          </div>
          <ul className="space-y-2" aria-label={t('series.duplicates.listLabel')}>
            {groups.map(g => {
              const keepId = keepOf(g)
              return (
                <li key={g.key} data-testid="duplicate-group" className="bg-slate-100 dark:bg-zinc-900 border border-slate-200 dark:border-zinc-800 rounded-md p-3">
                  <div className="flex flex-wrap items-start justify-between gap-2">
                    <div className="min-w-0 space-y-1">
                      <fieldset>
                        <legend className="text-xs text-fg-muted mb-1">{t('series.duplicates.keepLabel')}</legend>
                        {g.series.map(s => (
                          <label key={s.id} className="flex flex-wrap items-center gap-x-2 text-sm cursor-pointer">
                            <input
                              type="radio"
                              name={`keep-${g.key}`}
                              checked={keepId === s.id}
                              onChange={() => setKeep(prev => ({ ...prev, [g.key]: s.id }))}
                            />
                            <span className="font-medium [overflow-wrap:anywhere]">{s.title}</span>
                            <span className="text-xs text-fg-muted">{t('series.merge.bookCount', { count: s.bookCount })}</span>
                            {s.authors.length > 0 && <span className="text-xs text-fg-muted">{s.authors.join(', ')}</span>}
                            {s.hardcoverLinked && <span className="text-xs px-1.5 rounded bg-sky-100 dark:bg-sky-900/40">{t('series.duplicates.hardcoverLinked')}</span>}
                            {s.id === g.suggestedKeepId && <span className="text-xs px-1.5 rounded bg-emerald-100 dark:bg-emerald-900/40">{t('series.duplicates.suggested')}</span>}
                          </label>
                        ))}
                      </fieldset>
                      {!g.sameAuthors && (
                        <p className="text-xs text-amber-700 dark:text-amber-300">{t('series.duplicates.differentAuthors')}</p>
                      )}
                    </div>
                    <button type="button" onClick={() => mergeOne(g)} disabled={busy} className={`${btn.secondary} ${btnSize.sm}`}>
                      {t('series.duplicates.merge')}
                    </button>
                  </div>
                </li>
              )
            })}
          </ul>
        </div>
      )}

      {merging && (
        <MergeSeriesModal
          target={merging.target}
          series={series}
          initialSourceIds={merging.sourceIds}
          onClose={() => setMerging(null)}
          onMerged={() => { void afterMerge() }}
        />
      )}
    </div>
  )
}
