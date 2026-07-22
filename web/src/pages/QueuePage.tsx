import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, Book, PendingRelease, QueueItem } from '../api/client'
import BookAuthorLink from '../components/BookAuthorLink'
import ImportHints from '../components/ImportHints'
import Pagination from '../components/Pagination'
import { usePagination } from '../components/usePagination'
import { summarizeError, ERROR_SUMMARY_LEN } from './queueError'
import { btn, btnSize } from '../components/buttons'

export default function QueuePage() {
  const { t } = useTranslation()
  const [queue, setQueue] = useState<QueueItem[]>([])
  const [pending, setPending] = useState<PendingRelease[]>([])
  const [loading, setLoading] = useState(true)
  const [grabbingPending, setGrabbingPending] = useState<number | null>(null)
  const [retryingImportIds, setRetryingImportIds] = useState<Set<number>>(() => new Set())
  const [retryImportErrors, setRetryImportErrors] = useState<Record<number, string>>({})
  const [matchMessages, setMatchMessages] = useState<Record<number, string>>({})
  const [deleteTarget, setDeleteTarget] = useState<QueueItem | null>(null)
  const [deleteFiles, setDeleteFiles] = useState(false)
  const [deleting, setDeleting] = useState(false)

  const load = () => {
    Promise.all([
      api.listQueue(),
      api.listPending(),
    ]).then(([q, p]) => {
      setQueue(q)
      setPending(p)
    }).catch(console.error).finally(() => setLoading(false))
  }

  // Guard against stale responses and set-state-after-unmount: a `cancelled`
  // flag captured in the effect is checked before every setState and flipped in
  // cleanup, so a poll that resolves after a newer tick or after unmount is
  // ignored. Mirrors AuthorsPage's poll guard.
  useEffect(() => {
    let cancelled = false
    const run = () => {
      Promise.all([
        api.listQueue(),
        api.listPending(),
      ]).then(([q, p]) => {
        if (cancelled) return
        setQueue(q)
        setPending(p)
      }).catch(console.error).finally(() => { if (!cancelled) setLoading(false) })
    }
    run()
    const interval = setInterval(run, 5000)
    return () => { cancelled = true; clearInterval(interval) }
  }, [])

  useEffect(() => {
    document.title = 'Queue · Bindery'
    return () => { document.title = 'Bindery' }
  }, [])

  // Paginate the queue client-side so a large queue (hundreds/thousands of
  // items) doesn't render every row at once and blow up the DOM.
  const { pageItems: queuePage, paginationProps: queuePaginationProps } = usePagination(queue, 50, 'queue')

  // Open the remove dialog. The file-deletion choice always starts unchecked
  // so the default action keeps downloaded data (and torrent seeds) on disk.
  const openDeleteDialog = (item: QueueItem) => {
    setDeleteTarget(item)
    setDeleteFiles(false)
  }

  const closeDeleteDialog = () => {
    if (deleting) return
    setDeleteTarget(null)
    setDeleteFiles(false)
  }

  const confirmDelete = async () => {
    if (!deleteTarget) return
    setDeleting(true)
    try {
      await api.deleteFromQueue(deleteTarget.id, deleteFiles)
      setDeleteTarget(null)
      setDeleteFiles(false)
      load()
    } catch (e) {
      console.error(e)
    } finally {
      setDeleting(false)
    }
  }

  const handleDismissPending = async (id: number) => {
    await api.dismissPending(id).catch(console.error)
    load()
  }

  const handleGrabPending = async (id: number) => {
    setGrabbingPending(id)
    try {
      await api.grabPending(id)
      load()
    } catch (e) {
      console.error(e)
    } finally {
      setGrabbingPending(null)
    }
  }

  const clearRetryImportError = (id: number) => {
    setRetryImportErrors(prev => {
      const next = { ...prev }
      delete next[id]
      return next
    })
  }

  const handleRetryImport = async (id: number) => {
    setRetryingImportIds(prev => new Set(prev).add(id))
    clearRetryImportError(id)
    try {
      await api.retryImport(id)
      load()
    } catch (e) {
      setRetryImportErrors(prev => ({
        ...prev,
        [id]: e instanceof Error ? e.message : 'Retry failed',
      }))
    } finally {
      setRetryingImportIds(prev => {
        const next = new Set(prev)
        next.delete(id)
        return next
      })
    }
  }

  // handleMatch attaches an unmatched download to the picked book and imports its
  // files against it (#1589), then surfaces the outcome inline so the user gets
  // feedback rather than a silent no-op.
  const handleMatch = async (id: number, bookId: number) => {
    setRetryingImportIds(prev => new Set(prev).add(id))
    clearRetryImportError(id)
    setMatchMessages(prev => { const n = { ...prev }; delete n[id]; return n })
    try {
      const res = await api.matchDownload(id, bookId)
      const message = res.imported
        ? t('queue.matchImporting', 'Matched — importing now; the status will update shortly.')
        : res.retryQueued
          ? t('queue.matchQueued', 'Matched — the import will run on the next download-client check.')
          : t('queue.matchNoFiles', 'Matched to the book, but the downloaded files could not be located to import automatically. Re-import them with Manual file import, or re-download.')
      setMatchMessages(prev => ({ ...prev, [id]: message }))
      load()
    } catch (e) {
      setRetryImportErrors(prev => ({
        ...prev,
        [id]: e instanceof Error ? e.message : 'Match failed',
      }))
    } finally {
      setRetryingImportIds(prev => {
        const next = new Set(prev)
        next.delete(id)
        return next
      })
    }
  }


  const statusLabels: Record<string, string> = {
    grabbed: 'Grabbed',
    downloading: 'Downloading',
    completed: 'Completed',
    importPending: 'Import Pending',
    importing: 'Importing',
    imported: 'Imported',
    failed: 'Failed',
    importFailed: 'Import Failed',
    importBlocked: 'Import Blocked',
  }

  // Status pill (chip) styles. Saturated red is reserved for these small chips
  // — error detail below is rendered muted, not as a full red row.
  const statusChip: Record<string, string> = {
    grabbed: 'bg-slate-200 dark:bg-zinc-800 text-slate-700 dark:text-zinc-300',
    downloading: 'bg-blue-100 text-blue-800 dark:bg-blue-950 dark:text-blue-300',
    completed: 'bg-sky-100 text-sky-800 dark:bg-sky-950 dark:text-sky-300',
    importPending: 'bg-amber-100 text-amber-900 dark:bg-amber-950 dark:text-amber-300',
    importing: 'bg-blue-100 text-blue-800 dark:bg-blue-950 dark:text-blue-300',
    imported: 'bg-emerald-100 text-emerald-800 dark:bg-emerald-950 dark:text-emerald-300',
    failed: 'bg-red-100 text-red-800 dark:bg-red-950 dark:text-red-300',
    importFailed: 'bg-orange-100 text-orange-900 dark:bg-orange-950 dark:text-orange-300',
    importBlocked: 'bg-red-100 text-red-800 dark:bg-red-950 dark:text-red-300',
  }
  const FAILED_STATUSES = new Set(['failed', 'importFailed', 'importBlocked'])
  const failedItems = queue.filter(q => FAILED_STATUSES.has(q.status))
  // A download can be manually matched/retried when its files are waiting: an
  // unmatched import failure, or one blocked after exhausting its retry budget
  // ("stuck after three attempts", #1589). A plain 'failed' download never got
  // files, so it isn't matchable.
  const isMatchable = (status: string) => status === 'importFailed' || status === 'importBlocked'

  // Bulk actions over failed/blocked items, done client-side over the existing
  // per-item endpoints (no new API). Retry only applies to importFailed.
  const [bulkBusy, setBulkBusy] = useState(false)
  const clearAllFailed = async () => {
    if (failedItems.length === 0 || !confirm(t('queue.clearAllConfirm', { count: failedItems.length, defaultValue: 'Remove {{count}} failed item(s) from the queue?' }))) return
    setBulkBusy(true)
    try {
      await Promise.all(failedItems.map(it => api.deleteFromQueue(it.id, false).catch(() => {})))
      load()
    } finally {
      setBulkBusy(false)
    }
  }
  const retryAllFailed = async () => {
    const retryable = queue.filter(q => q.status === 'importFailed')
    if (retryable.length === 0) return
    setBulkBusy(true)
    try {
      await Promise.all(retryable.map(it => api.retryImport(it.id).catch(() => {})))
      load()
    } finally {
      setBulkBusy(false)
    }
  }

  const formatSize = (bytes: number) => {
    if (bytes > 1073741824) return (bytes / 1073741824).toFixed(1) + ' GB'
    if (bytes > 1048576) return (bytes / 1048576).toFixed(1) + ' MB'
    return (bytes / 1024).toFixed(0) + ' KB'
  }

  const formatRelativeTime = (timestamp: string): string => {
    const now = Date.now()
    const then = new Date(timestamp).getTime()
    const diffMs = now - then
    const diffSec = Math.floor(diffMs / 1000)
    const diffMin = Math.floor(diffSec / 60)
    const diffHr = Math.floor(diffMin / 60)
    const diffDay = Math.floor(diffHr / 24)

    if (diffSec < 60) return `${diffSec}s ago`
    if (diffMin < 60) return `${diffMin}m ago`
    if (diffHr < 24) return `${diffHr}h ago`
    return `${diffDay}d ago`
  }

  const getContextualTimestamp = (item: QueueItem): { label: string; absolute: string } | null => {
    if (item.importedAt) {
      return { label: formatRelativeTime(item.importedAt), absolute: new Date(item.importedAt).toUTCString() }
    }
    if (item.completedAt) {
      return { label: formatRelativeTime(item.completedAt), absolute: new Date(item.completedAt).toUTCString() }
    }
    if (item.grabbedAt) {
      return { label: formatRelativeTime(item.grabbedAt), absolute: new Date(item.grabbedAt).toUTCString() }
    }
    if (item.addedAt) {
      return { label: formatRelativeTime(item.addedAt), absolute: new Date(item.addedAt).toUTCString() }
    }
    return null
  }

  return (
    <div>
      <h2 className="text-2xl font-bold mb-6">{t('queue.title')}</h2>

      {loading ? (
        <div className="text-slate-600 dark:text-zinc-500">{t('common.loading')}</div>
      ) : queue.length === 0 && pending.length === 0 ? (
        <div className="text-center py-16 text-slate-600 dark:text-zinc-500">
          <p>{t('queue.empty')}</p>
          <ImportHints />
        </div>
      ) : (
        <div className="space-y-6">
          {queue.length > 0 && (
            <div className="space-y-2">
              {failedItems.length > 0 && (
                <div className="flex items-center justify-between gap-2 px-1">
                  <span className="text-xs text-slate-600 dark:text-zinc-400">
                    {t('queue.failedCount', { count: failedItems.length, defaultValue: '{{count}} failed' })}
                  </span>
                  <div className="flex gap-2">
                    {queue.some(q => q.status === 'importFailed') && (
                      <button
                        onClick={retryAllFailed}
                        disabled={bulkBusy}
                        className="px-2.5 py-1 text-xs rounded bg-sky-600 hover:bg-sky-500 disabled:opacity-50 text-white font-medium"
                      >
                        {t('queue.retryAllFailed', 'Retry all failed')}
                      </button>
                    )}
                    <button
                      onClick={clearAllFailed}
                      disabled={bulkBusy}
                      className="px-2.5 py-1 text-xs rounded border border-red-300 dark:border-red-900 text-red-700 dark:text-red-300 hover:bg-red-50 dark:hover:bg-red-950/40 disabled:opacity-50 font-medium"
                    >
                      {t('queue.clearAllFailed', 'Clear all failed')}
                    </button>
                  </div>
                </div>
              )}
              {queuePage.map(item => (
                <div key={item.id} className="flex items-center justify-between p-3 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900">
                  <div className="min-w-0 flex-1">
                    <h3 className="font-medium text-sm truncate">{item.title}</h3>
                    <BookAuthorLink book={item.book} />
                    <div className="flex flex-wrap items-center gap-x-3 gap-y-1 mt-1 text-xs">
                      <span className={`inline-block px-2 py-0.5 rounded text-[10px] font-medium ${statusChip[item.status] ?? 'bg-slate-200 dark:bg-zinc-800 text-slate-700 dark:text-zinc-300'}`}>
                        {statusLabels[item.status] ?? item.status}
                      </span>
                      <span className="text-slate-600 dark:text-zinc-500">{formatSize(item.size)}</span>
                      {item.percentage && (
                        <span className="text-blue-400">{item.percentage}%</span>
                      )}
                      {item.timeLeft && (
                        <span className="text-slate-600 dark:text-zinc-500">{t('queue.remaining', { time: item.timeLeft })}</span>
                      )}
                      {item.protocol && (
                        <span className="text-slate-500 dark:text-zinc-600">{item.protocol}</span>
                      )}
                      {(() => {
                        const ts = getContextualTimestamp(item)
                        return ts ? (
                          <span className="text-slate-500 dark:text-zinc-600" title={ts.absolute}>
                            {ts.label}
                          </span>
                        ) : null
                      })()}
                    </div>
                    {item.status === 'importBlocked' && !item.errorMessage && (
                      <div className="mt-1 text-xs text-slate-600 dark:text-zinc-400 bg-slate-200/60 dark:bg-zinc-800/60 rounded px-2 py-1">
                        Import blocked — manual intervention required (check library path permissions)
                      </div>
                    )}
                    {item.errorMessage && (
                      <div className="mt-1 text-xs text-slate-600 dark:text-zinc-400 bg-slate-200/60 dark:bg-zinc-800/60 rounded px-2 py-1 break-words">
                        <span className="font-medium">
                          {item.status === 'importFailed'
                            ? 'Import failed: '
                            : item.status === 'importBlocked'
                            ? 'Import blocked: '
                            : 'Error: '}
                        </span>
                        {summarizeError(item.errorMessage)}
                        {item.errorMessage.length > ERROR_SUMMARY_LEN && (
                          <details className="mt-1">
                            <summary className="cursor-pointer text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white">
                              {t('queue.errorDetails', 'Show full error')}
                            </summary>
                            <pre className="mt-1 max-h-48 overflow-auto whitespace-pre-wrap text-[10px] text-slate-600 dark:text-zinc-400">
                              {item.errorMessage.slice(0, 4000)}
                            </pre>
                          </details>
                        )}
                      </div>
                    )}
                    {isMatchable(item.status) && !item.book && (
                      <div className="mt-1 text-xs text-slate-600 dark:text-zinc-400 bg-slate-200/70 dark:bg-zinc-800/70 rounded px-2 py-1 break-words">
                        {t('queue.retryImportHint')}
                      </div>
                    )}
                    {/* Persistent match indicator: an import-failed download that
                        already has a book was matched — survives reload so the
                        user isn't left wondering whether the match took (#1589). */}
                    {isMatchable(item.status) && item.book && (
                      <div className="mt-1 text-xs text-emerald-700 dark:text-emerald-400 bg-emerald-400/10 rounded px-2 py-1 break-words">
                        {t('queue.matchedTo', { title: item.book.title, defaultValue: `Matched to ${item.book.title} — use Retry import to import it.` })}
                      </div>
                    )}
                    {retryImportErrors[item.id] && (
                      <div className="mt-1 text-xs text-red-600 dark:text-red-400 bg-red-400/10 rounded px-2 py-1 break-words">
                        {t('queue.retryImportError', { error: retryImportErrors[item.id] })}
                      </div>
                    )}
                    {matchMessages[item.id] && (
                      <div className="mt-1 text-xs text-emerald-700 dark:text-emerald-400 bg-emerald-400/10 rounded px-2 py-1 break-words">
                        {matchMessages[item.id]}
                      </div>
                    )}
                    {item.percentage && (
                      <div className="mt-2 h-1 bg-slate-200 dark:bg-zinc-800 rounded-full overflow-hidden">
                        <div
                          className="h-full bg-blue-500 transition-all"
                          style={{ width: `${item.percentage}%` }}
                        />
                      </div>
                    )}
                  </div>
                  <div className="ml-4 flex flex-col sm:flex-row items-end sm:items-center gap-2 flex-shrink-0">
                    {isMatchable(item.status) && (
                      <MatchBookControl
                        disabled={retryingImportIds.has(item.id)}
                        alreadyMatched={!!item.book}
                        onMatch={bookId => handleMatch(item.id, bookId)}
                      />
                    )}
                    {isMatchable(item.status) && (
                      <button
                        // A matched item re-imports its recorded files against the
                        // assigned book (works with no download client); an
                        // unmatched item resets the client retry counter (#1589).
                        onClick={() => item.book ? handleMatch(item.id, item.book.id) : handleRetryImport(item.id)}
                        disabled={retryingImportIds.has(item.id)}
                        title={t('queue.retryImportHint')}
                        className="px-3 py-2 text-xs bg-sky-600 hover:bg-sky-500 disabled:opacity-50 rounded font-medium touch-manipulation"
                      >
                        {retryingImportIds.has(item.id) ? t('queue.retryingImport') : t('queue.retryImport')}
                      </button>
                    )}
                    <button
                      onClick={() => openDeleteDialog(item)}
                      className={`${btn.danger} ${btnSize.lg} touch-manipulation`}
                    >
                      {t('queue.remove')}
                    </button>
                  </div>
                </div>
              ))}
              <Pagination {...queuePaginationProps} />
            </div>
          )}

          {pending.length > 0 && (
            <div>
              <h3 className="text-sm font-semibold text-slate-600 dark:text-zinc-400 mb-2 flex items-center gap-2">
                <span className="w-2 h-2 rounded-full bg-amber-400 inline-block" />
                Pending Releases ({pending.length})
              </h3>
              <div className="space-y-2">
                {pending.map(item => (
                  <div key={item.id} className="flex items-center justify-between p-4 border border-amber-200 dark:border-amber-900/40 rounded-lg bg-amber-50 dark:bg-amber-950/20">
                    <div className="min-w-0 flex-1">
                      <h3 className="font-medium text-sm truncate">{item.title}</h3>
                      <BookAuthorLink book={item.book} />
                      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 mt-1 text-xs">
                        <span className="text-amber-600 dark:text-amber-400">{item.reason}</span>
                        {item.size > 0 && (
                          <span className="text-slate-500 dark:text-zinc-500">{formatSize(item.size)}</span>
                        )}
                        {item.quality && (
                          <span className="text-slate-500 dark:text-zinc-500">{item.quality}</span>
                        )}
                        <span className="text-slate-500 dark:text-zinc-500">{item.protocol}</span>
                      </div>
                    </div>
                    <div className="ml-4 flex items-center gap-2 flex-shrink-0">
                      <button
                        onClick={() => handleGrabPending(item.id)}
                        disabled={grabbingPending !== null}
                        className="px-3 py-1.5 text-xs bg-emerald-600 hover:bg-emerald-500 disabled:opacity-50 rounded font-medium"
                      >
                        {grabbingPending === item.id ? 'Grabbing…' : 'Grab Now'}
                      </button>
                      <button
                        onClick={() => handleDismissPending(item.id)}
                        className="px-3 py-1.5 text-xs text-slate-500 dark:text-zinc-500 hover:text-red-400 rounded"
                      >
                        Dismiss
                      </button>
                    </div>
                  </div>
                ))}
              </div>
            </div>
          )}
        </div>
      )}

      {deleteTarget && (
        <div
          className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4"
          onClick={closeDeleteDialog}
        >
          <div
            className="w-full max-w-md rounded-lg border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 p-5 shadow-xl"
            onClick={e => e.stopPropagation()}
          >
            <h3 className="text-lg font-semibold mb-2">{t('queue.removeTitle')}</h3>
            <p className="text-sm text-slate-600 dark:text-zinc-400 break-words">
              {t('queue.removeBody', { title: deleteTarget.title })}
            </p>
            <label className="mt-4 flex items-start gap-2 cursor-pointer">
              <input
                type="checkbox"
                checked={deleteFiles}
                onChange={e => setDeleteFiles(e.target.checked)}
                className="mt-0.5"
              />
              <span className="text-sm">
                <span className="font-medium">{t('queue.removeDeleteFiles')}</span>
                <span className="block text-xs text-slate-500 dark:text-zinc-500">
                  {t('queue.removeDeleteFilesHint')}
                </span>
              </span>
            </label>
            <div className="mt-5 flex justify-end gap-2">
              <button
                onClick={closeDeleteDialog}
                disabled={deleting}
                className="px-3 py-2 text-xs rounded font-medium bg-slate-200 dark:bg-zinc-800 hover:bg-slate-300 dark:hover:bg-zinc-700 disabled:opacity-50"
              >
                {t('queue.removeCancel')}
              </button>
              <button
                onClick={confirmDelete}
                disabled={deleting}
                className="px-3 py-2 text-xs rounded font-medium bg-red-600 hover:bg-red-500 text-white disabled:opacity-50"
              >
                {t('queue.removeConfirm')}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}

// MatchBookControl lets the user attach an unmatched (importFailed) download to
// an existing library book, then retry the import against it (#1589). Search is
// against books already in the library — the download's files exist on disk, we
// only need to tell Bindery which book they belong to. Exported for tests.
export function MatchBookControl({ disabled, onMatch, alreadyMatched = false }: { disabled: boolean; onMatch: (bookId: number) => void; alreadyMatched?: boolean }) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState('')
  const [results, setResults] = useState<Book[]>([])
  const [searching, setSearching] = useState(false)

  const search = async () => {
    if (!query.trim()) return
    setSearching(true)
    try {
      const books = await api.listAllBooks({ search: query.trim() })
      setResults(books ?? [])
    } catch {
      setResults([])
    } finally {
      setSearching(false)
    }
  }

  if (!open) {
    return (
      <button
        onClick={() => setOpen(true)}
        disabled={disabled}
        title={t('queue.matchBookHint', 'Attach this download to an existing book and import it')}
        className="px-3 py-2 text-xs bg-slate-200 dark:bg-zinc-700 hover:bg-slate-300 dark:hover:bg-zinc-600 disabled:opacity-50 rounded font-medium touch-manipulation"
      >
        {alreadyMatched ? t('queue.matchBookChange', 'Match to a different book') : t('queue.matchBook', 'Match to book')}
      </button>
    )
  }

  return (
    <div className="w-64 p-2 bg-slate-50 dark:bg-zinc-950 border border-slate-200 dark:border-zinc-800 rounded space-y-1">
      <div className="flex gap-1">
        <input
          autoFocus
          className="flex-1 px-2 py-1 text-xs rounded border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900"
          placeholder={t('queue.matchBookPlaceholder', 'Search your library')}
          value={query}
          onChange={e => setQuery(e.target.value)}
          onKeyDown={e => { if (e.key === 'Enter') search() }}
          aria-label={t('queue.matchBookSearch', 'Search for a book')}
        />
        <button
          onClick={search}
          disabled={searching || !query.trim()}
          className="px-2 py-1 text-xs bg-slate-200 dark:bg-zinc-700 hover:bg-slate-300 dark:hover:bg-zinc-600 disabled:opacity-50 rounded"
        >
          {searching ? t('queue.matchBookSearching', 'Searching…') : t('queue.matchBookSearchBtn', 'Search')}
        </button>
      </div>
      {results.length > 0 && (
        <div className="max-h-40 overflow-auto">
          {results.map(b => (
            <button
              key={b.id}
              onClick={() => { setOpen(false); onMatch(b.id) }}
              className="block w-full text-left text-xs px-1 py-0.5 rounded text-emerald-700 dark:text-emerald-400 hover:bg-slate-200 dark:hover:bg-zinc-800"
            >
              {b.title}{b.author ? ` (${b.author.authorName})` : ''}
            </button>
          ))}
        </div>
      )}
      <button onClick={() => setOpen(false)} className="text-[10px] text-slate-500 dark:text-zinc-500 hover:underline">
        {t('queue.matchBookCancel', 'Cancel')}
      </button>
    </div>
  )
}
