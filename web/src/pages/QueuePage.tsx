import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, PendingRelease, QueueItem } from '../api/client'
import BookAuthorLink from '../components/BookAuthorLink'
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

  useEffect(() => {
    load()
    const interval = setInterval(load, 5000)
    return () => clearInterval(interval)
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

  const statusColors: Record<string, string> = {
    grabbed: 'text-slate-600 dark:text-zinc-400',
    downloading: 'text-blue-400',
    completed: 'text-sky-400',
    importPending: 'text-yellow-400',
    importing: 'text-blue-400',
    imported: 'text-emerald-400',
    failed: 'text-red-400',
    importFailed: 'text-orange-400',
    importBlocked: 'text-red-500',
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
        </div>
      ) : (
        <div className="space-y-6">
          {queue.length > 0 && (
            <div className="space-y-2">
              {queuePage.map(item => (
                <div key={item.id} className="flex items-center justify-between p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900">
                  <div className="min-w-0 flex-1">
                    <h3 className="font-medium text-sm truncate">{item.title}</h3>
                    <BookAuthorLink book={item.book} />
                    <div className="flex flex-wrap items-center gap-x-3 gap-y-1 mt-1 text-xs">
                      <span className={statusColors[item.status] || 'text-slate-600 dark:text-zinc-400'}>
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
                      <div className="mt-1 text-xs text-red-500 bg-red-500/10 rounded px-2 py-1">
                        Import blocked — manual intervention required (check library path permissions)
                      </div>
                    )}
                    {item.errorMessage && (
                      <div className="mt-1 text-xs text-red-400 bg-red-400/10 rounded px-2 py-1 break-words">
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
                            <summary className="cursor-pointer text-red-300 hover:text-red-200">
                              {t('queue.errorDetails', 'Show full error')}
                            </summary>
                            <pre className="mt-1 max-h-48 overflow-auto whitespace-pre-wrap text-[10px] text-red-300">
                              {item.errorMessage.slice(0, 4000)}
                            </pre>
                          </details>
                        )}
                      </div>
                    )}
                    {item.status === 'importFailed' && (
                      <div className="mt-1 text-xs text-slate-600 dark:text-zinc-400 bg-slate-200/70 dark:bg-zinc-800/70 rounded px-2 py-1 break-words">
                        {t('queue.retryImportHint')}
                      </div>
                    )}
                    {retryImportErrors[item.id] && (
                      <div className="mt-1 text-xs text-red-600 dark:text-red-400 bg-red-400/10 rounded px-2 py-1 break-words">
                        {t('queue.retryImportError', { error: retryImportErrors[item.id] })}
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
                    {item.status === 'importFailed' && (
                      <button
                        onClick={() => handleRetryImport(item.id)}
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
