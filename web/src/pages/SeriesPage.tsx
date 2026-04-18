import { useEffect, useState } from 'react'
import { api, Series } from '../api/client'

export default function SeriesPage() {
  const [seriesList, setSeriesList] = useState<Series[]>([])
  const [loading, setLoading] = useState(true)
  const [expanded, setExpanded] = useState<number | null>(null)
  const [filling, setFilling] = useState<number | null>(null)
  const [fillResult, setFillResult] = useState<Record<number, string>>({})

  useEffect(() => {
    api.listSeries().then(setSeriesList).catch(console.error).finally(() => setLoading(false))
  }, [])

  useEffect(() => {
    document.title = 'Series · Bindery'
    return () => { document.title = 'Bindery' }
  }, [])

  const toggleExpanded = (id: number) => {
    setExpanded(prev => (prev === id ? null : id))
  }

  const toggleMonitor = async (series: Series) => {
    const next = !series.monitored
    await api.monitorSeries(series.id, next)
    setSeriesList(prev => prev.map(s => s.id === series.id ? { ...s, monitored: next } : s))
  }

  const fillGaps = async (series: Series) => {
    setFilling(series.id)
    try {
      const r = await api.fillSeries(series.id)
      setFillResult(prev => ({ ...prev, [series.id]: r.queued === 0 ? 'Nothing to fill' : `${r.queued} book${r.queued === 1 ? '' : 's'} queued` }))
    } catch {
      setFillResult(prev => ({ ...prev, [series.id]: 'Failed' }))
    } finally {
      setFilling(null)
    }
  }

  return (
    <div>
      <div className="flex items-center justify-between mb-6">
        <h2 className="text-2xl font-bold">Series</h2>
        <span className="text-sm text-slate-600 dark:text-zinc-500">{seriesList.length} series</span>
      </div>

      {loading ? (
        <div className="text-slate-600 dark:text-zinc-500">Loading...</div>
      ) : seriesList.length === 0 ? (
        <div className="text-center py-16 text-slate-600 dark:text-zinc-500">
          <p className="text-lg mb-2">No series found</p>
          <p className="text-sm">Series are populated automatically from your monitored authors' books</p>
        </div>
      ) : (
        <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4">
          {seriesList.map(series => {
            const books = series.books ?? []
            const bookCount = books.length
            const gapCount = books.filter(b => b.book && b.book.status !== 'imported').length
            const isOpen = expanded === series.id
            const sortedBooks = [...books].sort((a, b) => {
              const posA = parseFloat(a.positionInSeries) || 0
              const posB = parseFloat(b.positionInSeries) || 0
              return posA - posB
            })

            return (
              <div key={series.id} className="border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900 overflow-hidden">
                <div
                  className="p-4 cursor-pointer hover:bg-slate-200/50 dark:hover:bg-zinc-800/50 transition-colors"
                  onClick={() => toggleExpanded(series.id)}
                >
                  <div className="flex items-start justify-between gap-3">
                    <div className="min-w-0">
                      <h3 className="font-semibold truncate">{series.title}</h3>
                      {series.description && (
                        <p className="text-xs text-slate-600 dark:text-zinc-500 mt-1 line-clamp-2">{series.description}</p>
                      )}
                    </div>
                    <div className="flex-shrink-0 flex items-center gap-2">
                      {gapCount > 0 && (
                        <span className="text-xs text-amber-600 dark:text-amber-400 bg-amber-500/10 px-2 py-0.5 rounded-full">
                          {gapCount} missing
                        </span>
                      )}
                      <span className="text-xs text-slate-600 dark:text-zinc-500 bg-slate-200 dark:bg-zinc-800 px-2 py-0.5 rounded-full">
                        {bookCount} {bookCount === 1 ? 'book' : 'books'}
                      </span>
                      <span className="text-slate-500 dark:text-zinc-600 text-xs">{isOpen ? '▲' : '▼'}</span>
                    </div>
                  </div>
                </div>

                {/* Actions row */}
                <div className="px-4 pb-3 flex items-center gap-3" onClick={e => e.stopPropagation()}>
                  <button
                    onClick={() => toggleMonitor(series)}
                    className={`relative w-9 h-5 rounded-full transition-colors flex-shrink-0 ${series.monitored ? 'bg-emerald-600' : 'bg-slate-300 dark:bg-zinc-700'}`}
                    title={series.monitored ? 'Stop monitoring' : 'Monitor series'}
                  >
                    <span className={`absolute top-0.5 left-0.5 w-4 h-4 bg-white rounded-full transition-transform ${series.monitored ? 'translate-x-4' : ''}`} />
                  </button>
                  <span className="text-xs text-slate-600 dark:text-zinc-400">
                    {series.monitored ? 'Monitored' : 'Not monitored'}
                  </span>
                  {gapCount > 0 && (
                    <button
                      onClick={() => fillGaps(series)}
                      disabled={filling === series.id}
                      className="ml-auto text-xs px-2.5 py-1 bg-emerald-600 hover:bg-emerald-500 disabled:opacity-50 rounded font-medium"
                    >
                      {filling === series.id ? 'Queuing…' : 'Fill gaps'}
                    </button>
                  )}
                  {fillResult[series.id] && (
                    <span className="ml-auto text-xs text-emerald-600 dark:text-emerald-400">{fillResult[series.id]}</span>
                  )}
                </div>

                {isOpen && bookCount > 0 && (
                  <div className="border-t border-slate-200 dark:border-zinc-800 divide-y divide-slate-200/50 dark:divide-zinc-800/50">
                    {sortedBooks.map(entry => (
                      <div key={entry.bookId} className="flex items-center gap-3 px-4 py-3 bg-slate-100/80 dark:bg-zinc-900/80">
                        <span className="text-xs text-slate-600 dark:text-zinc-500 w-10 flex-shrink-0 font-mono">
                          #{entry.positionInSeries || '?'}
                        </span>
                        {entry.book?.imageUrl ? (
                          <img
                            src={entry.book.imageUrl}
                            alt={entry.book.title}
                            className="w-8 h-10 object-cover rounded flex-shrink-0"
                          />
                        ) : (
                          <div className="w-8 h-10 bg-slate-200 dark:bg-zinc-800 rounded flex-shrink-0" />
                        )}
                        <div className="min-w-0">
                          <p className="text-sm font-medium truncate">
                            {entry.book?.title ?? `Book ${entry.bookId}`}
                          </p>
                          {entry.book?.releaseDate && (
                            <p className="text-xs text-slate-600 dark:text-zinc-500">
                              {new Date(entry.book.releaseDate).getFullYear()}
                            </p>
                          )}
                        </div>
                        {entry.book?.status && (
                          <span className={`ml-auto text-xs px-2 py-0.5 rounded flex-shrink-0 ${
                            entry.book.status === 'imported'
                              ? 'bg-emerald-500/20 text-emerald-400'
                              : entry.book.status === 'wanted'
                              ? 'bg-amber-500/20 text-amber-400'
                              : 'bg-slate-300 dark:bg-zinc-700 text-slate-600 dark:text-zinc-400'
                          }`}>
                            {entry.book.status}
                          </span>
                        )}
                      </div>
                    ))}
                  </div>
                )}

                {isOpen && bookCount === 0 && (
                  <div className="border-t border-slate-200 dark:border-zinc-800 px-4 py-3 text-sm text-slate-600 dark:text-zinc-500">
                    No books in this series yet
                  </div>
                )}
              </div>
            )
          })}
        </div>
      )}
    </div>
  )
}
