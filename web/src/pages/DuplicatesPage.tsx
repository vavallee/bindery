import { useCallback, useEffect, useState } from 'react'
import { Link } from 'react-router'
import { useTranslation } from 'react-i18next'
import { api, LibraryDuplicateCandidates } from '../api/client'
import { btn, btnSize } from '../components/buttons'
import DuplicateGroupCard from '../components/DuplicateGroupCard'
import Pagination from '../components/Pagination'
import { useDuplicateReviewActions } from '../components/useDuplicateReviewActions'

// Library-wide duplicate review (#2999): every author's duplicate groups, the
// same detection and evidence as an author's Review duplicates window, one
// page at a time. Nothing changes until a person excludes a row.
export default function DuplicatesPage() {
  const { t } = useTranslation()
  const [page, setPage] = useState(1)
  const [pageSize, setPageSize] = useState(25)
  const [result, setResult] = useState<LibraryDuplicateCandidates | null>(null)
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState<string | null>(null)

  const load = useCallback(() => {
    setLoading(true)
    setLoadError(null)
    api.listLibraryDuplicateCandidates(pageSize, (page - 1) * pageSize)
      .then(res => {
        // Excluding the last group on the last page leaves that page empty;
        // step back rather than show nothing.
        if (res.count === 0 && res.total > 0 && page > 1) {
          setPage(Math.max(1, Math.ceil(res.total / pageSize)))
          return
        }
        setResult(res)
      })
      .catch(err => {
        setLoadError(err instanceof Error ? err.message : t('duplicateCandidates.loadFailed', 'Loading duplicates failed'))
      })
      .finally(() => { setLoading(false) })
  }, [page, pageSize, t])

  useEffect(() => { load() }, [load])
  useEffect(() => {
    document.title = `${t('duplicateReview.pageTitle', 'Duplicates')} · Bindery`
    return () => { document.title = 'Bindery' }
  }, [t])

  const { busyBooks, error: actionError, toggleExclusion, excludeEmptyRows, confirmDialog } = useDuplicateReviewActions(load)
  const error = actionError ?? loadError
  const total = result?.total ?? 0

  return (
    <div>
      {confirmDialog}
      <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
        <div>
          <Link to="/books" className="text-xs text-slate-500 hover:underline dark:text-zinc-400">
            {t('duplicateReview.backToBooks', '← Books')}
          </Link>
          <h2 className="text-2xl font-bold">{t('duplicateReview.pageTitle', 'Duplicates')}</h2>
        </div>
        <div className="ml-auto flex items-center gap-3">
          {result && (
            <span className="text-sm text-fg-muted">
              {t('duplicateReview.groupCount', { count: total, defaultValue: '{{count}} group(s)' })}
            </span>
          )}
          <button type="button" onClick={load} disabled={loading} className={`${btn.secondary} ${btnSize.sm}`}>
            {t('duplicateCandidates.refresh', 'Rescan')}
          </button>
        </div>
      </div>
      <p className="mb-4 max-w-3xl text-sm text-slate-600 dark:text-zinc-400">
        {t('duplicateReview.pageDescription', 'Titles by the same author that look like the same book, across your whole library. The row with files is marked to keep, and the evidence shows whether the rows agree. Nothing changes until you exclude a row.')}
      </p>

      {loading && !result ? (
        <p className="text-sm text-slate-500 dark:text-zinc-400">{t('duplicateCandidates.loading', 'Scanning the catalogue…')}</p>
      ) : error && !result ? (
        <p className="text-sm text-red-600 dark:text-red-400">{error}</p>
      ) : result && result.total === 0 ? (
        <p className="text-sm text-slate-600 dark:text-zinc-400">{t('duplicateCandidates.empty', 'No duplicate titles found.')}</p>
      ) : result ? (
        <>
          {error && <p className="mb-3 text-sm text-red-600 dark:text-red-400">{error}</p>}
          <div className="rounded border border-slate-200 dark:border-zinc-800">
            {result.groups.map(group => (
              <DuplicateGroupCard
                key={`${group.authorId ?? 0}:${group.key}`}
                group={group}
                busyBooks={busyBooks}
                onToggle={toggleExclusion}
                onExcludeEmpty={excludeEmptyRows}
                showAuthor
              />
            ))}
          </div>
          <Pagination
            page={page}
            totalPages={Math.max(1, Math.ceil(total / pageSize))}
            pageSize={pageSize}
            totalItems={total}
            onPageChange={setPage}
            onPageSizeChange={size => { setPageSize(size); setPage(1) }}
            pageSizeOptions={[25, 50, 100]}
          />
        </>
      ) : null}
    </div>
  )
}
