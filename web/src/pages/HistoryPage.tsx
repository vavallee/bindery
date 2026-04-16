import { useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, HistoryEvent } from '../api/client'
import Pagination from '../components/Pagination'
import { usePagination } from '../components/usePagination'

const EVENT_TYPE_COLORS: Record<string, string> = {
  grabbed: 'bg-blue-500/20 text-blue-400',
  bookImported: 'bg-emerald-500/20 text-emerald-400',
  imported: 'bg-emerald-500/20 text-emerald-400',
  downloadFailed: 'bg-red-500/20 text-red-400',
  importFailed: 'bg-red-500/20 text-red-400',
  deleted: 'bg-red-500/20 text-red-400',
  renamed: 'bg-purple-500/20 text-purple-400',
  ignored: 'bg-slate-300 dark:bg-zinc-700 text-slate-600 dark:text-zinc-400',
  bookFileRenamed: 'bg-purple-500/20 text-purple-400',
}

function formatDate(s: string) {
  return new Date(s).toLocaleString(undefined, {
    year: 'numeric', month: 'short', day: 'numeric',
    hour: '2-digit', minute: '2-digit',
  })
}

function parseEventData(data: string): { message?: string; path?: string; size?: number; [k: string]: unknown } {
  if (!data) return {}
  try { return JSON.parse(data) } catch { return {} }
}

function formatSize(n: number): string {
  if (!n || n <= 0) return ''
  if (n >= 1073741824) return (n / 1073741824).toFixed(1) + ' GB'
  if (n >= 1048576) return (n / 1048576).toFixed(0) + ' MB'
  return (n / 1024).toFixed(0) + ' KB'
}

// Detect media type from the release title — ebook formats vs audiobook
// formats. Falls back to '' when nothing matches (older events or
// non-grab records). Preferred over a server round-trip since the info
// is already in the title.
function detectMediaType(title: string): '' | 'ebook' | 'audiobook' {
  const t = title.toLowerCase()
  if (/\b(m4b|m4a|mp3|flac|ogg)\b/.test(t)) return 'audiobook'
  if (/\b(epub|mobi|azw3?|pdf|djvu|fb2)\b/.test(t)) return 'ebook'
  return ''
}

const BLOCKLISTABLE = new Set(['grabbed', 'downloadFailed', 'importFailed'])

export default function HistoryPage() {
  const { t } = useTranslation()
  const [events, setEvents] = useState<HistoryEvent[]>([])
  const [loading, setLoading] = useState(true)
  const [typeFilter, setTypeFilter] = useState('')

  const load = useCallback((filter?: string) => {
    api.listHistory(filter ? { eventType: filter } : undefined)
      .then(setEvents)
      .catch(console.error)
      .finally(() => setLoading(false))
  }, [])

  useEffect(() => { load() }, [load])

  const handleFilterChange = (val: string) => {
    setTypeFilter(val)
    setLoading(true)
    load(val || undefined)
  }

  const handleDelete = async (id: number) => {
    await api.deleteHistory(id).catch(console.error)
    setEvents(prev => prev.filter(e => e.id !== id))
  }

  const handleBlocklist = async (id: number) => {
    await api.blocklistFromHistory(id).catch(console.error)
    setEvents(prev => prev.filter(e => e.id !== id))
  }

  const eventTypes = Array.from(new Set(events.map(e => e.eventType))).sort()

  const { pageItems, paginationProps, reset } = usePagination(events, 100, 'history')

  useEffect(() => { reset() }, [typeFilter, reset])

  return (
    <div>
      <div className="flex flex-wrap items-center justify-between gap-3 mb-6">
        <h2 className="text-2xl font-bold">{t('history.title')}</h2>
        <select
          value={typeFilter}
          onChange={e => handleFilterChange(e.target.value)}
          className="bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-1.5 text-sm text-slate-800 dark:text-zinc-200 focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600"
        >
          <option value="">{t('history.allEventTypes')}</option>
          {eventTypes.map(t => (
            <option key={t} value={t}>{t}</option>
          ))}
        </select>
      </div>

      {loading ? (
        <div className="text-slate-600 dark:text-zinc-500">{t('common.loading')}</div>
      ) : events.length === 0 ? (
        <div className="text-center py-16 text-slate-600 dark:text-zinc-500">
          <p>{t('history.empty')}</p>
        </div>
      ) : (
        <>
          {/* Desktop table */}
          <div className="hidden sm:block border border-slate-200 dark:border-zinc-800 rounded-lg overflow-hidden">
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead>
                  <tr className="bg-slate-100 dark:bg-zinc-900 border-b border-slate-200 dark:border-zinc-800">
                    <th className="text-left px-4 py-3 text-xs font-medium text-slate-600 dark:text-zinc-400 uppercase tracking-wider">{t('history.colEvent')}</th>
                    <th className="text-left px-4 py-3 text-xs font-medium text-slate-600 dark:text-zinc-400 uppercase tracking-wider">{t('history.colSourceTitle')}</th>
                    <th className="text-left px-4 py-3 text-xs font-medium text-slate-600 dark:text-zinc-400 uppercase tracking-wider">{t('history.colType')}</th>
                    <th className="text-left px-4 py-3 text-xs font-medium text-slate-600 dark:text-zinc-400 uppercase tracking-wider">{t('history.colSize')}</th>
                    <th className="text-left px-4 py-3 text-xs font-medium text-slate-600 dark:text-zinc-400 uppercase tracking-wider">{t('history.colDate')}</th>
                    <th className="px-4 py-3" />
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-200 dark:divide-zinc-800">
                  {pageItems.map(event => {
                    const parsed = parseEventData(event.data)
                    const detail = parsed.message || parsed.path || ''
                    const isError = event.eventType === 'downloadFailed' || event.eventType === 'importFailed'
                    const size = typeof parsed.size === 'number' ? parsed.size : 0
                    const mt = detectMediaType(event.sourceTitle)
                    return (
                      <tr key={event.id} className="bg-slate-100/50 dark:bg-zinc-900/50 hover:bg-slate-200/50 dark:hover:bg-zinc-800/50 transition-colors">
                        <td className="px-4 py-3 align-top">
                          <span className={`inline-block px-2 py-0.5 rounded text-xs font-medium ${EVENT_TYPE_COLORS[event.eventType] ?? 'bg-slate-300 dark:bg-zinc-700 text-slate-600 dark:text-zinc-400'}`}>
                            {event.eventType}
                          </span>
                        </td>
                        <td className="px-4 py-3 text-slate-800 dark:text-zinc-200 max-w-md">
                          <div className="truncate" title={event.sourceTitle}>
                            {event.sourceTitle || <span className="text-slate-500 dark:text-zinc-600">—</span>}
                          </div>
                          {detail && (
                            <div className={`mt-1 text-xs break-words ${isError ? 'text-red-400' : 'text-slate-600 dark:text-zinc-500'}`}>
                              {detail}
                            </div>
                          )}
                        </td>
                        <td className="px-4 py-3 align-top text-xs whitespace-nowrap">
                          {mt === 'audiobook' ? (
                            <span className="inline-flex items-center gap-1 px-1.5 py-0.5 rounded bg-indigo-100 text-indigo-800 dark:bg-indigo-950 dark:text-indigo-300 text-[10px] font-medium">🎧 Audiobook</span>
                          ) : mt === 'ebook' ? (
                            <span className="inline-flex items-center gap-1 px-1.5 py-0.5 rounded bg-emerald-100 text-emerald-800 dark:bg-emerald-950 dark:text-emerald-300 text-[10px] font-medium">📖 Ebook</span>
                          ) : (
                            <span className="text-slate-500 dark:text-zinc-600">—</span>
                          )}
                        </td>
                        <td className="px-4 py-3 text-slate-600 dark:text-zinc-400 whitespace-nowrap align-top text-xs font-mono">
                          {formatSize(size) || <span className="text-slate-500 dark:text-zinc-600">—</span>}
                        </td>
                        <td className="px-4 py-3 text-slate-600 dark:text-zinc-400 whitespace-nowrap align-top text-xs">
                          {formatDate(event.createdAt)}
                        </td>
                        <td className="px-4 py-3 text-right align-top whitespace-nowrap">
                          {BLOCKLISTABLE.has(event.eventType) && (
                            <button
                              onClick={() => handleBlocklist(event.id)}
                              className="text-xs text-amber-400 hover:text-amber-300 transition-colors mr-3"
                              title="Add to blocklist — prevents this release from being grabbed again"
                            >
                              {t('history.blocklist')}
                            </button>
                          )}
                          <button
                            onClick={() => handleDelete(event.id)}
                            className="text-xs text-red-400 hover:text-red-300 transition-colors"
                          >
                            {t('history.delete')}
                          </button>
                        </td>
                      </tr>
                    )
                  })}
                </tbody>
              </table>
            </div>
          </div>

          {/* Mobile card list */}
          <div className="sm:hidden space-y-2">
            {pageItems.map(event => {
              const parsed = parseEventData(event.data)
              const detail = parsed.message || parsed.path || ''
              const isError = event.eventType === 'downloadFailed' || event.eventType === 'importFailed'
              const size = typeof parsed.size === 'number' ? parsed.size : 0
              const mt = detectMediaType(event.sourceTitle)
              return (
                <div key={event.id} className="border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100/50 dark:bg-zinc-900/50 p-3">
                  <div className="flex items-start justify-between gap-2 mb-2">
                    <div className="flex flex-wrap items-center gap-1.5">
                      <span className={`inline-block px-2 py-0.5 rounded text-xs font-medium flex-shrink-0 ${EVENT_TYPE_COLORS[event.eventType] ?? 'bg-slate-300 dark:bg-zinc-700 text-slate-600 dark:text-zinc-400'}`}>
                        {event.eventType}
                      </span>
                      {mt === 'audiobook' && (
                        <span className="inline-flex items-center gap-1 px-1.5 py-0.5 rounded bg-indigo-100 text-indigo-800 dark:bg-indigo-950 dark:text-indigo-300 text-[10px] font-medium">🎧</span>
                      )}
                      {mt === 'ebook' && (
                        <span className="inline-flex items-center gap-1 px-1.5 py-0.5 rounded bg-emerald-100 text-emerald-800 dark:bg-emerald-950 dark:text-emerald-300 text-[10px] font-medium">📖</span>
                      )}
                      {size > 0 && (
                        <span className="text-[10px] text-slate-600 dark:text-zinc-500 font-mono">{formatSize(size)}</span>
                      )}
                    </div>
                    <span className="text-[10px] text-slate-600 dark:text-zinc-500">{formatDate(event.createdAt)}</span>
                  </div>
                  <p className="text-sm text-slate-800 dark:text-zinc-200 break-words mb-1">
                    {event.sourceTitle || <span className="text-slate-500 dark:text-zinc-600">—</span>}
                  </p>
                  {detail && (
                    <p className={`text-xs break-words mb-2 ${isError ? 'text-red-400' : 'text-slate-600 dark:text-zinc-500'}`}>
                      {detail}
                    </p>
                  )}
                  <div className="flex gap-3 mt-2">
                    {BLOCKLISTABLE.has(event.eventType) && (
                      <button
                        onClick={() => handleBlocklist(event.id)}
                        className="text-xs text-amber-400 hover:text-amber-300 transition-colors py-1"
                      >
                        {t('history.blocklist')}
                      </button>
                    )}
                    <button
                      onClick={() => handleDelete(event.id)}
                      className="text-xs text-red-400 hover:text-red-300 transition-colors py-1"
                    >
                      {t('history.delete')}
                    </button>
                  </div>
                </div>
              )
            })}
          </div>
        </>
      )}
      <Pagination {...paginationProps} />
    </div>
  )
}
