import { useCallback, useEffect, useState } from 'react'
import { Link } from 'react-router'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'
import type { LibraryRequest } from '../../api/client'
import { mediaTypeLabel, requestStatusClass, requestStatusLabel } from './requestLabels'

const PAGE_SIZE = 50

// A requester's own requests, newest first, with what became of each one.
// A pending request can be withdrawn.
export default function MyRequestsPage() {
  const { t } = useTranslation()
  const [items, setItems] = useState<LibraryRequest[]>([])
  const [total, setTotal] = useState(0)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const load = useCallback(async (offset: number) => {
    setLoading(true)
    setError(null)
    try {
      const page = await api.listMyRequests({ limit: PAGE_SIZE, offset })
      setItems(prev => (offset === 0 ? page.items : [...prev, ...page.items]))
      setTotal(page.total)
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : t('requests.mine.loadFailed'))
    } finally {
      setLoading(false)
    }
  }, [t])

  useEffect(() => { void load(0) }, [load])

  const withdraw = async (r: LibraryRequest) => {
    try {
      await api.withdrawRequest(r.id)
      setItems(prev => prev.filter(x => x.id !== r.id))
      setTotal(n => Math.max(0, n - 1))
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : t('requests.mine.withdrawFailed'))
    }
  }

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between gap-4 flex-wrap">
        <h2 className="text-2xl font-bold">{t('requests.mine.title')}</h2>
        <Link to="/request" className="px-4 py-2 bg-emerald-600 hover:bg-emerald-500 rounded-md text-sm font-medium text-white">
          {t('requests.mine.newRequest')}
        </Link>
      </div>

      {error && <p role="alert" className="text-sm text-red-700 dark:text-red-300">{error}</p>}

      {!loading && items.length === 0 && !error && (
        <p className="text-sm text-fg-muted">{t('requests.mine.empty')}</p>
      )}

      {items.length > 0 && (
        <ul className="divide-y divide-slate-200 dark:divide-zinc-800 border border-slate-200 dark:border-zinc-800 rounded-lg" aria-label={t('requests.mine.title')}>
          {items.map(r => (
            <li key={r.id} className="p-4 flex items-start justify-between gap-4">
              <div className="min-w-0">
                <div className="font-medium [overflow-wrap:anywhere]">{r.title}</div>
                <div className="text-xs text-fg-muted mt-0.5">
                  {r.kind === 'author' ? t('requests.kindAuthor') : r.authorName || t('requests.kindBook')}
                  {' · '}
                  {mediaTypeLabel(t, r.mediaType)}
                  {' · '}
                  {new Date(r.createdAt).toLocaleDateString()}
                </div>
                {r.status === 'declined' && r.declineReason && (
                  <p className="text-xs mt-1 text-slate-700 dark:text-zinc-300">{t('requests.mine.reason', { reason: r.declineReason })}</p>
                )}
              </div>
              <div className="flex items-center gap-2 flex-shrink-0">
                <span className={`px-2 py-0.5 rounded-full text-xs font-medium ${requestStatusClass(r)}`}>{requestStatusLabel(t, r)}</span>
                {r.status === 'pending' && (
                  <button
                    type="button"
                    onClick={() => void withdraw(r)}
                    aria-label={t('requests.mine.withdrawLabel', { title: r.title })}
                    className="text-xs text-fg-muted hover:text-slate-900 dark:hover:text-white"
                  >
                    {t('requests.mine.withdraw')}
                  </button>
                )}
              </div>
            </li>
          ))}
        </ul>
      )}

      {loading && <p className="text-sm text-fg-muted">{t('common.loading')}</p>}
      {!loading && items.length < total && (
        <button type="button" onClick={() => void load(items.length)} className="px-4 py-2 text-sm rounded-md border border-slate-300 dark:border-zinc-700">
          {t('requests.loadMore')}
        </button>
      )}
    </div>
  )
}
