import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, PendingRelease, QueueItem } from '../api/client'

export default function QueuePage() {
  const { t } = useTranslation()
  const [queue, setQueue] = useState<QueueItem[]>([])
  const [pending, setPending] = useState<PendingRelease[]>([])
  const [loading, setLoading] = useState(true)
  const [grabbingPending, setGrabbingPending] = useState<number | null>(null)

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

  const handleDelete = async (id: number) => {
    await api.deleteFromQueue(id)
    load()
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
              {queue.map(item => (
                <div key={item.id} className="flex items-center justify-between p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900">
                  <div className="min-w-0 flex-1">
                    <h3 className="font-medium text-sm truncate">{item.title}</h3>
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
                    </div>
                    {item.status === 'importBlocked' && !item.errorMessage && (
                      <div className="mt-1 text-xs text-red-500 bg-red-500/10 rounded px-2 py-1">
                        Import blocked — manual intervention required (check library path permissions)
                      </div>
                    )}
                    {item.errorMessage && (
                      <div className="mt-1 text-xs text-red-400 bg-red-400/10 rounded px-2 py-1">
                        {item.errorMessage}
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
                  <button
                    onClick={() => handleDelete(item.id)}
                    className="ml-4 px-3 py-2 text-xs text-red-400 hover:text-red-300 flex-shrink-0 touch-manipulation"
                  >
                    {t('queue.remove')}
                  </button>
                </div>
              ))}
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
    </div>
  )
}
