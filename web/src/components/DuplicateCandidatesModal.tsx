import { useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, DuplicateCandidates } from '../api/client'
import { btn, btnSize } from './buttons'
import DuplicateGroupCard from './DuplicateGroupCard'
import { useDuplicateReviewActions } from './useDuplicateReviewActions'
import { useModal } from './useModal'

interface Props {
  authorId: number
  authorName: string
  onClose: () => void
  // Called after a successful exclusion toggle so the page can refresh its
  // book lists (the modal re-fetches its own groups on its own).
  onChanged?: () => void
}

export default function DuplicateCandidatesModal({ authorId, authorName, onClose, onChanged }: Props) {
  const { t } = useTranslation()
  const [result, setResult] = useState<DuplicateCandidates | null>(null)
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState<string | null>(null)

  const load = useCallback(() => {
    setLoading(true)
    setLoadError(null)
    api.listAuthorDuplicateCandidates(authorId)
      .then(setResult)
      .catch(err => {
        setLoadError(err instanceof Error ? err.message : t('duplicateCandidates.loadFailed', 'Loading duplicates failed'))
      })
      .finally(() => { setLoading(false) })
  }, [authorId, t])

  useEffect(() => { load() }, [load])

  const { busyBooks, error: actionError, toggleExclusion, excludeEmptyRows, confirmDialog } =
    useDuplicateReviewActions(load, onChanged)
  const error = actionError ?? loadError

  const { panelProps } = useModal({ onClose, labelledBy: 'duplicate-candidates-title' })

  return (
    <>
      <div className="modal-overlay fixed inset-0 z-50 flex items-center justify-center bg-black/50" onClick={onClose}>
        <div
          className="bg-white dark:bg-zinc-900 border border-slate-200 dark:border-zinc-700 rounded-lg shadow-xl p-6 w-full max-w-3xl mx-4 modal-max-h flex flex-col"
          onClick={event => event.stopPropagation()}
          {...panelProps}
        >
          <h2 id="duplicate-candidates-title" className="text-base font-semibold text-slate-900 dark:text-white">
            {t('duplicateCandidates.title', 'Review duplicate titles')}
          </h2>
          <p className="mt-1 text-xs text-slate-500 dark:text-zinc-400">
            {t('duplicateCandidates.description', {
              author: authorName,
              defaultValue: 'Titles for {{author}} that look like the same book. Nothing changes until you exclude a row.',
            })}
          </p>

          {loading && !result ? (
            <p className="mt-4 text-sm text-slate-500 dark:text-zinc-400">
              {t('duplicateCandidates.loading', 'Scanning the catalogue…')}
            </p>
          ) : error && !result ? (
            <p className="mt-4 text-sm text-red-600 dark:text-red-400">{error}</p>
          ) : result ? (
            result.count === 0 ? (
              <p className="mt-4 text-sm text-slate-600 dark:text-zinc-400">
                {t('duplicateCandidates.empty', 'No duplicate titles found.')}
              </p>
            ) : (
              <div className="mt-4 min-h-0 flex-1 overflow-y-auto rounded border border-slate-200 dark:border-zinc-800">
                {result.groups.map(group => (
                  <DuplicateGroupCard
                    key={group.key}
                    group={group}
                    busyBooks={busyBooks}
                    onToggle={toggleExclusion}
                    onExcludeEmpty={excludeEmptyRows}
                  />
                ))}
              </div>
            )
          ) : null}

          {error && result && <p className="mt-3 text-sm text-red-600 dark:text-red-400">{error}</p>}

          <div className="mt-4 flex justify-end gap-2 border-t border-slate-200 pt-4 dark:border-zinc-800">
            <button type="button" onClick={load} disabled={loading} className={`${btn.secondary} ${btnSize.sm}`}>
              {t('duplicateCandidates.refresh', 'Rescan')}
            </button>
            <button type="button" onClick={onClose} className={`${btn.secondary} ${btnSize.sm}`}>
              {t('common.close', 'Close')}
            </button>
          </div>
        </div>
      </div>
      {confirmDialog}
    </>
  )
}
