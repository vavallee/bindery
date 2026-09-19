import { useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'
import type { LibraryRequest, RequestStatus } from '../../api/client'
import ApproveRequestForm from './ApproveRequestForm'
import { mediaTypeLabel, REQUESTS_CHANGED_EVENT, requestStatusClass, requestStatusLabel } from './requestLabels'

type Filter = RequestStatus | 'all'
const FILTERS: Filter[] = ['pending', 'approved', 'declined', 'all']
const PAGE_SIZE = 50

// The admin's request queue. Approve opens the choices inline under the row;
// decline takes an optional reason the requester sees. Every decision tells
// the nav badge to recount.
export default function RequestsPage() {
  const { t } = useTranslation()
  const [filter, setFilter] = useState<Filter>('pending')
  const [items, setItems] = useState<LibraryRequest[]>([])
  const [total, setTotal] = useState(0)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [approving, setApproving] = useState<number | null>(null)
  const [declining, setDeclining] = useState<number | null>(null)
  const [reason, setReason] = useState('')

  const load = useCallback(async (offset: number) => {
    setLoading(true)
    setError(null)
    try {
      const page = await api.listRequestQueue({ status: filter, limit: PAGE_SIZE, offset })
      setItems(prev => (offset === 0 ? page.items : [...prev, ...page.items]))
      setTotal(page.total)
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : t('requests.admin.loadFailed'))
    } finally {
      setLoading(false)
    }
  }, [filter, t])

  useEffect(() => { void load(0) }, [load])

  useEffect(() => {
    document.title = 'Requests · Bindery'
    return () => { document.title = 'Bindery' }
  }, [])

  const decided = (updated: LibraryRequest) => {
    setApproving(null)
    setDeclining(null)
    setReason('')
    if (filter === 'pending') {
      setItems(prev => prev.filter(x => x.id !== updated.id))
      setTotal(n => Math.max(0, n - 1))
    } else {
      setItems(prev => prev.map(x => (x.id === updated.id ? updated : x)))
    }
    window.dispatchEvent(new Event(REQUESTS_CHANGED_EVENT))
  }

  const decline = async (r: LibraryRequest) => {
    setError(null)
    try {
      decided(await api.declineRequest(r.id, reason.trim()))
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : t('requests.admin.declineFailed'))
    }
  }

  return (
    <div className="space-y-6">
      <h2 className="text-2xl font-bold">{t('requests.admin.title')}</h2>

      <div role="tablist" aria-label={t('requests.admin.filterLabel')} className="flex gap-1 flex-wrap">
        {FILTERS.map(f => (
          <button
            key={f}
            type="button"
            role="tab"
            aria-selected={filter === f}
            onClick={() => setFilter(f)}
            className={`px-3 py-1.5 rounded-md text-sm ${filter === f ? 'bg-slate-200 dark:bg-zinc-800 font-medium' : 'text-fg-muted hover:bg-slate-200/50 dark:hover:bg-zinc-800/50'}`}
          >
            {t(`requests.admin.filter.${f}`)}
          </button>
        ))}
      </div>

      {error && <p role="alert" className="text-sm text-red-700 dark:text-red-300">{error}</p>}
      {/* Centered, with a hint line, so this reads like the Queue, Discover and
          Import empty states instead of a stray left aligned sentence. */}
      {!loading && !error && items.length === 0 && (
        <div className="text-center py-16 text-slate-600 dark:text-zinc-500">
          <p>{filter === 'pending' ? t('requests.admin.emptyPending') : t('requests.admin.empty')}</p>
          <p className="mt-1 text-sm">
            {filter === 'pending' ? t('requests.admin.emptyPendingHint') : t('requests.admin.emptyHint')}
          </p>
        </div>
      )}

      {items.length > 0 && (
        <ul className="divide-y divide-slate-200 dark:divide-zinc-800 border border-slate-200 dark:border-zinc-800 rounded-lg" aria-label={t('requests.admin.title')}>
          {items.map(r => (
            <li key={r.id} className="p-4">
              <div className="flex items-start justify-between gap-4 flex-wrap">
                <div className="min-w-0">
                  <div className="font-medium break-words">{r.title}</div>
                  <div className="text-xs text-fg-muted mt-0.5">
                    {r.kind === 'author' ? t('requests.kindAuthor') : r.authorName || t('requests.kindBook')}
                    {' · '}
                    {mediaTypeLabel(t, r.mediaType)}
                    {' · '}
                    {t('requests.admin.requestedBy', { username: r.username ?? '', date: new Date(r.createdAt).toLocaleDateString() })}
                  </div>
                  {r.status === 'declined' && r.declineReason && (
                    <p className="text-xs mt-1">{t('requests.mine.reason', { reason: r.declineReason })}</p>
                  )}
                </div>
                <div className="flex items-center gap-2 flex-shrink-0">
                  <span className={`px-2 py-0.5 rounded-full text-xs font-medium ${requestStatusClass(r)}`}>{requestStatusLabel(t, r)}</span>
                  {r.status === 'pending' && approving !== r.id && declining !== r.id && (
                    <>
                      <button type="button" onClick={() => { setApproving(r.id); setDeclining(null) }} aria-label={t('requests.admin.approveLabel', { title: r.title })} className="px-3 py-1 bg-emerald-600 hover:bg-emerald-500 rounded text-xs font-medium text-white">
                        {t('requests.admin.approve')}
                      </button>
                      <button type="button" onClick={() => { setDeclining(r.id); setApproving(null); setReason('') }} aria-label={t('requests.admin.declineLabel', { title: r.title })} className="px-3 py-1 rounded text-xs font-medium border border-slate-300 dark:border-zinc-700">
                        {t('requests.admin.decline')}
                      </button>
                    </>
                  )}
                </div>
              </div>

              {approving === r.id && (
                <ApproveRequestForm request={r} onApproved={decided} onCancel={() => setApproving(null)} />
              )}

              {declining === r.id && (
                <form
                  aria-label={t('requests.admin.declineFormLabel', { title: r.title })}
                  onSubmit={e => { e.preventDefault(); void decline(r) }}
                  className="mt-3 flex gap-2 flex-wrap items-end"
                >
                  <label className="flex-1 min-w-[12rem] text-xs text-fg-muted">
                    {t('requests.admin.reason')}
                    <input
                      type="text"
                      value={reason}
                      maxLength={500}
                      onChange={e => setReason(e.target.value)}
                      className="mt-1 block w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded-md px-2 py-1.5 text-sm text-slate-900 dark:text-zinc-100"
                    />
                  </label>
                  <button type="button" onClick={() => setDeclining(null)} className="px-3 py-1.5 text-sm text-fg-muted">{t('common.cancel')}</button>
                  <button type="submit" className="px-3 py-1.5 rounded text-sm font-medium bg-red-600 hover:bg-red-500 text-white">{t('requests.admin.confirmDecline')}</button>
                </form>
              )}
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
