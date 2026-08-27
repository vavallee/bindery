import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, CatalogueReconciliation, CatalogueReconciliationReason } from '../api/client'
import { btn, btnSize } from './buttons'

interface Props {
  authorId: number
  authorName: string
  onClose: () => void
  onApplied?: () => void
}

const reasonDefaults: Record<CatalogueReconciliationReason, string> = {
  provider_changed: 'From the previous metadata provider',
  not_in_current_catalogue: 'No longer in the current provider catalogue',
  language_not_allowed: 'Rejected by the language filter',
  part_book: 'Rejected as a box set or part-book',
  missing_release_date: 'Rejected because the release date is missing',
  below_minimum_popularity: 'Below the minimum popularity',
  below_minimum_pages: 'Below the minimum page count',
  missing_isbn: 'No edition has an ISBN',
  catalogue_filter: 'Rejected by the catalogue filter',
}

export default function CatalogueReconciliationModal({ authorId, authorName, onClose, onApplied }: Props) {
  const { t } = useTranslation()
  const [result, setResult] = useState<CatalogueReconciliation | null>(null)
  const [selectedBookIds, setSelectedBookIds] = useState<Set<number>>(new Set())
  const [loading, setLoading] = useState(true)
  const [applying, setApplying] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    api.previewAuthorCatalogueReconciliation(authorId)
      .then(preview => {
        if (!cancelled) {
          setResult(preview)
          setSelectedBookIds(new Set(preview.candidates.map(candidate => candidate.bookId)))
        }
      })
      .catch(err => {
        if (!cancelled) setError(err instanceof Error ? err.message : t('catalogueReconciliation.previewFailed', 'Preview failed'))
      })
      .finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, [authorId, t])

  const apply = async () => {
    if (!result) return
    const selectedIds = result.candidates
      .filter(candidate => selectedBookIds.has(candidate.bookId))
      .map(candidate => candidate.bookId)
    if (selectedIds.length === 0) return
    setApplying(true)
    setError(null)
    try {
      const applied = await api.applyAuthorCatalogueReconciliation(
        authorId,
        selectedIds,
      )
      setResult(applied)
      if ((applied.applied?.deleted ?? 0) > 0) onApplied?.()
    } catch (err) {
      setError(err instanceof Error ? err.message : t('catalogueReconciliation.applyFailed', 'Reconciliation failed'))
    } finally {
      setApplying(false)
    }
  }

  const candidates = result?.candidates ?? []
  const applied = result?.applied
  const selectedCount = candidates.reduce(
    (count, candidate) => count + (selectedBookIds.has(candidate.bookId) ? 1 : 0),
    0,
  )

  const toggleCandidate = (bookId: number) => {
    setSelectedBookIds(current => {
      const next = new Set(current)
      if (next.has(bookId)) next.delete(bookId)
      else next.add(bookId)
      return next
    })
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50" onClick={onClose}>
      <div
        className="bg-white dark:bg-zinc-900 border border-slate-200 dark:border-zinc-700 rounded-lg shadow-xl p-6 w-full max-w-2xl mx-4 max-h-[85vh] flex flex-col"
        onClick={event => event.stopPropagation()}
        role="dialog"
        aria-modal="true"
        aria-labelledby="catalogue-reconciliation-title"
      >
        <h2 id="catalogue-reconciliation-title" className="text-base font-semibold text-slate-900 dark:text-white">
          {t('catalogueReconciliation.title', 'Reconcile catalogue')}
        </h2>
        <p className="mt-1 text-xs text-slate-500 dark:text-zinc-400">
          {t('catalogueReconciliation.description', {
            author: authorName,
            defaultValue: 'Compare {{author}}’s metadata-only Wanted rows with the current provider and metadata profile.',
          })}
        </p>

        {loading ? (
          <p className="mt-4 text-sm text-slate-500 dark:text-zinc-400">
            {t('catalogueReconciliation.loading', 'Checking the current provider catalogue…')}
          </p>
        ) : error && !result ? (
          <p className="mt-4 text-sm text-red-600 dark:text-red-400">{error}</p>
        ) : result ? (
          <>
            <div className="mt-4 grid grid-cols-3 gap-px overflow-hidden rounded-lg border border-slate-200 bg-slate-200 dark:border-zinc-800 dark:bg-zinc-800">
              <SummaryCell label={t('catalogueReconciliation.remove', 'Safe to remove')} value={result.summary.candidates} tone="danger" />
              <SummaryCell label={t('catalogueReconciliation.protected', 'Protected')} value={result.summary.protected} />
              <SummaryCell label={t('catalogueReconciliation.kept', 'Still accepted')} value={result.summary.kept} />
            </div>

            <p className="mt-3 text-xs text-slate-600 dark:text-zinc-400">
              {t('catalogueReconciliation.context', {
                provider: result.provider,
                profile: result.profileName,
                defaultValue: 'Provider: {{provider}} · Profile: {{profile}}',
              })}
            </p>
            <p className="mt-3 text-xs text-slate-600 dark:text-zinc-400">
              {t('catalogueReconciliation.safeguards', {
                files: result.summary.protectedFiles,
                imported: result.summary.protectedImported,
                status: result.summary.protectedStatus,
                excluded: result.summary.protectedExcluded,
                defaultValue: 'Files are never deleted. Protected: {{files}} file-bearing, {{imported}} imported, {{status}} other-status, and {{excluded}} excluded row(s). Apply rechecks every safeguard.',
              })}
            </p>

            {result.summary.indeterminate > 0 && (
              <p className="mt-2 text-xs text-slate-600 dark:text-zinc-400">
                {t('catalogueReconciliation.indeterminate', {
                  count: result.summary.indeterminate,
                  defaultValue: '{{count}} row(s) were kept because provider or profile evidence was incomplete.',
                })}
              </p>
            )}

            {result.warning && (
              <div className="mt-3 rounded border border-amber-300 bg-amber-50 px-3 py-2 text-xs text-amber-800 dark:border-amber-900 dark:bg-amber-950/30 dark:text-amber-300">
                {t('catalogueReconciliation.partialWarning', result.warning)}
              </div>
            )}

            {applied ? (
              <div className="mt-4 rounded border border-emerald-300 bg-emerald-50 px-3 py-2 text-sm text-emerald-800 dark:border-emerald-900 dark:bg-emerald-950/30 dark:text-emerald-300">
                {t('catalogueReconciliation.applied', {
                  deleted: applied.deleted,
                  skipped: applied.skipped,
                  defaultValue: 'Removed {{deleted}} row(s). {{skipped}} were skipped because they were no longer eligible.',
                })}
              </div>
            ) : candidates.length === 0 ? (
              <p className="mt-4 text-sm text-slate-600 dark:text-zinc-400">
                {t('catalogueReconciliation.clean', 'No metadata-only Wanted rows need reconciliation.')}
              </p>
            ) : (
              <div className="mt-4 min-h-0 flex-1 overflow-y-auto rounded border border-slate-200 dark:border-zinc-800">
                <ul className="divide-y divide-slate-200 dark:divide-zinc-800">
                  {candidates.map(candidate => (
                    <li key={candidate.bookId} className="px-3 py-2 text-xs">
                      <label className="flex cursor-pointer items-start gap-3">
                        <input
                          type="checkbox"
                          checked={selectedBookIds.has(candidate.bookId)}
                          onChange={() => toggleCandidate(candidate.bookId)}
                          disabled={applying}
                          aria-label={t('catalogueReconciliation.selectCandidate', {
                            title: candidate.title,
                            defaultValue: 'Select {{title}}',
                          })}
                          className="mt-0.5 h-4 w-4 rounded border-slate-300 text-red-600 focus:ring-red-500 dark:border-zinc-600 dark:bg-zinc-800"
                        />
                        <span>
                          <span className="block font-medium text-slate-800 dark:text-zinc-200">{candidate.title}</span>
                          <span className="mt-0.5 flex flex-wrap gap-x-2 text-slate-500 dark:text-zinc-500">
                            <span>{t(`catalogueReconciliation.reasons.${candidate.reason}`, reasonDefaults[candidate.reason])}</span>
                            <span aria-hidden="true">·</span>
                            <span>{candidate.metadataProvider}</span>
                          </span>
                        </span>
                      </label>
                    </li>
                  ))}
                </ul>
              </div>
            )}
          </>
        ) : null}

        {error && result && <p className="mt-3 text-sm text-red-600 dark:text-red-400">{error}</p>}

        <div className="mt-4 flex justify-end gap-2 border-t border-slate-200 pt-4 dark:border-zinc-800">
          <button type="button" onClick={onClose} className={`${btn.secondary} ${btnSize.md}`}>
            {applied ? t('common.close', 'Close') : t('common.cancel', 'Cancel')}
          </button>
          {!applied && result && (
            <button
              type="button"
              onClick={apply}
              disabled={applying || selectedCount === 0}
              className={`${btn.dangerSolid} ${btnSize.md}`}
            >
              {applying
                ? t('catalogueReconciliation.applying', 'Rechecking and removing…')
                : t('catalogueReconciliation.apply', {
                    count: selectedCount,
                    defaultValue: 'Remove {{count}} stale row(s)',
                  })}
            </button>
          )}
        </div>
      </div>
    </div>
  )
}

function SummaryCell({ label, value, tone }: { label: string; value: number; tone?: 'danger' }) {
  return (
    <div className="bg-white px-3 py-2 dark:bg-zinc-900">
      <div className="text-[11px] text-slate-500 dark:text-zinc-500">{label}</div>
      <div className={`text-lg font-semibold tabular-nums ${tone === 'danger' ? 'text-red-700 dark:text-red-300' : 'text-slate-900 dark:text-white'}`}>
        {value}
      </div>
    </div>
  )
}
