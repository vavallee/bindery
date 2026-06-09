import { useEffect, useState } from 'react'
import { Link, useLocation } from 'react-router-dom'
import { api, Series, SeriesFillBookRequest, SeriesHardcoverDiff, SeriesHardcoverDiffBook, SeriesHardcoverLink, SeriesHardcoverSearchResult, SystemStatus } from '../api/client'
import AddSeriesBookModal from '../components/AddSeriesBookModal'
import HardcoverSeriesLinkModal from '../components/HardcoverSeriesLinkModal'
import SeriesNameModal from '../components/SeriesNameModal'
import { btn, btnSize } from '../components/buttons'
import Switch from '../components/Switch'

export default function SeriesPage() {
  const location = useLocation()
  const [seriesList, setSeriesList] = useState<Series[]>([])
  const [loading, setLoading] = useState(true)
  const [expanded, setExpanded] = useState<number | null>(null)
  const [filling, setFilling] = useState<number | null>(null)
  const [fillResult, setFillResult] = useState<Record<number, string>>({})
  const [linking, setLinking] = useState<number | null>(null)
  const [linkResult, setLinkResult] = useState<Record<number, string>>({})
  const [linkModalSeries, setLinkModalSeries] = useState<Series | null>(null)
  const [linkModalResults, setLinkModalResults] = useState<SeriesHardcoverSearchResult[]>([])
  const [diffs, setDiffs] = useState<Record<number, SeriesHardcoverDiff>>({})
  const [diffLoading, setDiffLoading] = useState<Record<number, boolean>>({})
  const [diffErrors, setDiffErrors] = useState<Record<number, string>>({})
  const [systemStatus, setSystemStatus] = useState<SystemStatus | null>(null)
  const [showAddSeries, setShowAddSeries] = useState(false)
  const [editingSeries, setEditingSeries] = useState<Series | null>(null)
  const [bookModalSeries, setBookModalSeries] = useState<Series | null>(null)
  const enhancedHardcoverApi = systemStatus?.enhancedHardcoverApi ?? false

  useEffect(() => {
    const state = location.state as { seriesId?: number } | null
    Promise.all([api.listSeries(), api.status()])
      .then(([list, status]) => {
        setSeriesList(list)
        setSystemStatus(status)
        if (state?.seriesId) {
          setExpanded(state.seriesId)
        }
      })
      .catch(console.error)
      .finally(() => setLoading(false))
  }, [location.state])

  useEffect(() => {
    document.title = 'Series · Bindery'
    return () => { document.title = 'Bindery' }
  }, [])

  const refreshSeriesList = async () => {
    const list = await api.listSeries()
    setSeriesList(list)
    return list
  }

  const handleCreateSeries = async (title: string) => {
    const series = await api.createSeries({ title })
    await refreshSeriesList()
    setExpanded(series.id)
    setShowAddSeries(false)
  }

  const handleRenameSeries = async (title: string) => {
    if (!editingSeries) return
    const updated = await api.updateSeries(editingSeries.id, { title })
    setSeriesList(prev => prev.map(series => series.id === updated.id ? { ...series, ...updated } : series))
    setEditingSeries(null)
  }

  const deleteSeries = async (series: Series) => {
    if (!confirm(`Delete "${series.title}" from Series? Linked books will stay in your library.`)) return
    await api.deleteSeries(series.id)
    setSeriesList(prev => prev.filter(item => item.id !== series.id))
    setDiffs(prev => {
      const next = { ...prev }
      delete next[series.id]
      return next
    })
    if (expanded === series.id) {
      setExpanded(null)
    }
  }

  const handleBookLinked = (updated: Series) => {
    setSeriesList(prev => prev.map(series => series.id === updated.id ? updated : series))
    setExpanded(updated.id)
    if (enhancedHardcoverApi && updated.hardcoverLink) {
      void loadHardcoverDiff(updated, true)
    }
  }

  const loadHardcoverDiff = async (series: Series, force = false) => {
    if (!enhancedHardcoverApi) return
    if (!series.hardcoverLink) return
    if (!force && (diffs[series.id] || diffLoading[series.id])) return
    setDiffLoading(prev => ({ ...prev, [series.id]: true }))
    setDiffErrors(prev => {
      const next = { ...prev }
      delete next[series.id]
      return next
    })
    try {
      const diff = await api.getSeriesHardcoverDiff(series.id)
      setDiffs(prev => ({ ...prev, [series.id]: diff }))
    } catch (err) {
      setDiffErrors(prev => ({ ...prev, [series.id]: err instanceof Error ? err.message : 'Failed to load Hardcover diff' }))
    } finally {
      setDiffLoading(prev => ({ ...prev, [series.id]: false }))
    }
  }

  const toggleExpanded = (series: Series) => {
    const opening = expanded !== series.id
    setExpanded(opening ? series.id : null)
    if (opening) {
      void loadHardcoverDiff(series)
    }
  }

  const toggleMonitor = async (series: Series) => {
    const next = !series.monitored
    await api.monitorSeries(series.id, next)
    setSeriesList(prev => prev.map(s => s.id === series.id ? { ...s, monitored: next } : s))
  }

  const fillGaps = async (series: Series, book?: SeriesHardcoverDiffBook) => {
    setFilling(series.id)
    try {
      const request: SeriesFillBookRequest | undefined = book ? {
        foreignBookId: book.foreignBookId,
        providerId: book.providerId,
        position: book.position,
      } : undefined
      const r = await api.fillSeries(series.id, request)
      setFillResult(prev => ({ ...prev, [series.id]: r.queued === 0 ? 'Nothing to fill' : `${r.queued} book${r.queued === 1 ? '' : 's'} queued` }))
      const list = await refreshSeriesList()
      const updated = list.find(s => s.id === series.id)
      if (enhancedHardcoverApi && updated?.hardcoverLink) {
        await loadHardcoverDiff(updated, true)
      }
    } catch {
      setFillResult(prev => ({ ...prev, [series.id]: 'Failed' }))
    } finally {
      setFilling(null)
    }
  }

  const openHardcoverLink = async (series: Series) => {
    if (!enhancedHardcoverApi) return
    setLinkResult(prev => {
      const next = { ...prev }
      delete next[series.id]
      return next
    })
    if (series.hardcoverLink) {
      setLinkModalResults([])
      setLinkModalSeries(series)
      return
    }

    setLinking(series.id)
    try {
      const response = await api.autoLinkSeriesHardcover(series.id)
      const modalSeries = response.link ? { ...series, hardcoverLink: response.link } : series
      setLinkModalResults(response.candidates ?? [])
      setLinkModalSeries(modalSeries)
      if (response.link) {
        const link = response.link
        setSeriesList(prev => prev.map(s => s.id === series.id ? { ...s, hardcoverLink: link } : s))
        await loadHardcoverDiff(modalSeries, true)
      } else if (response.reason) {
        const reason = response.reason
        setLinkResult(prev => ({ ...prev, [series.id]: reason }))
      }
    } catch (err) {
      setLinkResult(prev => ({ ...prev, [series.id]: err instanceof Error ? err.message : 'Failed to search Hardcover' }))
    } finally {
      setLinking(null)
    }
  }

  const handleHardcoverLinked = (seriesId: number, link?: SeriesHardcoverLink) => {
    setSeriesList(prev => prev.map(series => series.id === seriesId ? { ...series, hardcoverLink: link } : series))
    if (!link) {
      setDiffs(prev => {
        const next = { ...prev }
        delete next[seriesId]
        return next
      })
      return
    }
    const series = seriesList.find(item => item.id === seriesId)
    if (enhancedHardcoverApi && series) {
      void loadHardcoverDiff({ ...series, hardcoverLink: link }, true)
    }
  }

  return (
    <div>
      <div className="flex items-center justify-between gap-3 flex-wrap mb-6">
        <h2 className="text-2xl font-bold">Series</h2>
        <div className="flex items-center gap-3">
          <span className="text-sm text-slate-600 dark:text-zinc-500">{seriesList.length} series</span>
          <button
            onClick={() => setShowAddSeries(true)}
            className="px-4 py-2 bg-emerald-600 hover:bg-emerald-500 rounded-md text-sm font-medium transition-colors"
          >
            Add Series
          </button>
        </div>
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
            const diff = diffs[series.id]
            const hardcoverMissingEstimate = enhancedHardcoverApi ? Math.max(0, (series.hardcoverLink?.hardcoverBookCount ?? 0) - bookCount) : 0
            const hardcoverMissingCount = enhancedHardcoverApi ? (diff?.missingCount ?? hardcoverMissingEstimate) : 0
            const displayMissingCount = Math.max(gapCount, hardcoverMissingCount)
            const fillNeeded = gapCount > 0 || hardcoverMissingCount > 0
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
                  onClick={() => toggleExpanded(series)}
                >
                  <div className="flex items-start justify-between gap-3">
                    <div className="min-w-0">
                      <h3 className="font-semibold truncate">{series.title}</h3>
                      {series.description && (
                        <p className="text-xs text-slate-600 dark:text-zinc-500 mt-1 line-clamp-2">{series.description}</p>
                      )}
                    </div>
                    <div className="flex-shrink-0 flex items-center gap-2">
                      {displayMissingCount > 0 && (
                        <span className="text-xs text-amber-600 dark:text-amber-400 bg-amber-500/10 px-2 py-0.5 rounded-full">
                          {displayMissingCount} missing
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
                <div className="px-4 pb-3 flex items-center gap-3 flex-wrap" onClick={e => e.stopPropagation()}>
                  <Switch
                    checked={series.monitored}
                    onChange={() => toggleMonitor(series)}
                    label={series.monitored ? 'Stop monitoring' : 'Monitor series'}
                  >
                    {series.monitored ? 'Monitored' : 'Not monitored'}
                  </Switch>
                  {enhancedHardcoverApi && (
                    <button
                      onClick={() => openHardcoverLink(series)}
                      disabled={linking === series.id}
                      className={`text-xs px-2.5 py-1 rounded font-medium border disabled:opacity-50 ${
                        series.hardcoverLink
                          ? 'border-sky-500/40 bg-sky-500/10 text-sky-700 dark:text-sky-300'
                          : 'border-amber-500/40 bg-amber-500/10 text-amber-700 dark:text-amber-300'
                      }`}
                      title={series.hardcoverLink ? `Linked to ${series.hardcoverLink.hardcoverTitle}` : 'Search Hardcover series'}
                    >
                      {linking === series.id ? 'Searching...' : series.hardcoverLink ? `${series.hardcoverLink.linkedBy === 'auto' ? 'Auto' : 'Manual'} link` : 'Search'}
                    </button>
                  )}
                  <button
                    onClick={() => setEditingSeries(series)}
                    className="text-xs px-2.5 py-1 rounded font-medium bg-slate-200 dark:bg-zinc-800 hover:bg-slate-300 dark:hover:bg-zinc-700"
                  >
                    Rename
                  </button>
                  {isOpen && (
                    <button
                      onClick={() => setBookModalSeries(series)}
                      className="text-xs px-2.5 py-1 rounded font-medium bg-slate-200 dark:bg-zinc-800 hover:bg-slate-300 dark:hover:bg-zinc-700"
                    >
                      Add Book
                    </button>
                  )}
                  <button
                    onClick={() => deleteSeries(series)}
                    className={`${btn.danger} ${btnSize.sm}`}
                  >
                    Delete
                  </button>
                  {fillNeeded && (
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
                  {!fillResult[series.id] && linkResult[series.id] && (
                    <span className="ml-auto text-xs text-slate-600 dark:text-zinc-400">{linkResult[series.id]}</span>
                  )}
                </div>

                {isOpen && bookCount > 0 && (
                  <div className="border-t border-slate-200 dark:border-zinc-800 divide-y divide-slate-200/50 dark:divide-zinc-800/50">
                    {sortedBooks.map(entry => (
                      <Link
                        key={entry.bookId}
                        to={`/book/${entry.bookId}`}
                        className="flex items-center gap-3 px-4 py-3 bg-slate-100/80 dark:bg-zinc-900/80 hover:bg-slate-200/50 dark:hover:bg-zinc-800/50 transition-colors"
                      >
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
                      </Link>
                    ))}
                  </div>
                )}

                {isOpen && bookCount === 0 && (
                  <div className="border-t border-slate-200 dark:border-zinc-800 px-4 py-3 text-sm text-slate-600 dark:text-zinc-500">
                    No books in this series yet
                  </div>
                )}

                {isOpen && enhancedHardcoverApi && series.hardcoverLink && (
                  <div className="border-t border-slate-200 dark:border-zinc-800 bg-slate-100/80 dark:bg-zinc-900/80">
                    <div className="px-4 py-3 flex items-center justify-between gap-3">
                      <div className="min-w-0">
                        <p className="text-sm font-medium truncate">Hardcover: {series.hardcoverLink.hardcoverTitle}</p>
                        <p className="text-xs text-slate-600 dark:text-zinc-500">
                          {diff ? `${diff.presentCount} matched · ${diff.missingCount} missing` : 'Checking Hardcover catalog...'}
                        </p>
                      </div>
                      {(diff?.missingCount ?? 0) > 0 && (
                        <button
                          onClick={() => fillGaps(series)}
                          disabled={filling === series.id}
                          className="text-xs px-2.5 py-1 bg-emerald-600 hover:bg-emerald-500 disabled:opacity-50 rounded font-medium flex-shrink-0"
                        >
                          {filling === series.id ? 'Queuing...' : 'add all'}
                        </button>
                      )}
                    </div>
                    {diffLoading[series.id] && (
                      <div className="px-4 pb-3 text-sm text-slate-600 dark:text-zinc-500">Loading Hardcover books...</div>
                    )}
                    {diffErrors[series.id] && (
                      <div className="px-4 pb-3 text-sm text-rose-600 dark:text-rose-400">{diffErrors[series.id]}</div>
                    )}
                    {diff && diff.missing.length > 0 && (
                      <div className="px-4 pb-4 space-y-2">
                        {diff.missing.slice(0, 8).map(book => (
                          <div key={`${book.foreignBookId}-${book.position}`} className="flex items-center gap-3 p-3 rounded-md bg-slate-200/50 dark:bg-zinc-800/50">
                            <span className="text-xs text-slate-600 dark:text-zinc-500 w-10 flex-shrink-0 font-mono">
                              #{book.position || '?'}
                            </span>
                            {book.imageUrl ? (
                              <img src={book.imageUrl} alt={book.title} className="w-8 h-10 object-cover rounded flex-shrink-0" />
                            ) : (
                              <div className="w-8 h-10 bg-slate-200 dark:bg-zinc-800 rounded flex-shrink-0" />
                            )}
                            <div className="min-w-0">
                              <p className="text-sm font-medium truncate">{book.title}</p>
                              {book.authorName && <p className="text-xs text-slate-600 dark:text-zinc-500 truncate">{book.authorName}</p>}
                            </div>
                            <button
                              onClick={() => fillGaps(series, book)}
                              disabled={filling === series.id}
                              className="ml-auto text-xs px-2.5 py-1 bg-emerald-600 hover:bg-emerald-500 disabled:opacity-50 rounded font-medium flex-shrink-0"
                              title="Add this missing Hardcover book and search indexers"
                            >
                              {filling === series.id ? '...' : 'add'}
                            </button>
                          </div>
                        ))}
                        {diff.missing.length > 8 && (
                          <p className="text-xs text-slate-600 dark:text-zinc-500 px-1">{diff.missing.length - 8} more missing books</p>
                        )}
                      </div>
                    )}
                  </div>
                )}
              </div>
            )
          })}
        </div>
      )}
      {showAddSeries && (
        <SeriesNameModal
          title="Add Series"
          submitLabel="Add Series"
          onClose={() => setShowAddSeries(false)}
          onSubmit={handleCreateSeries}
        />
      )}
      {editingSeries && (
        <SeriesNameModal
          title="Rename Series"
          initialName={editingSeries.title}
          submitLabel="Save"
          onClose={() => setEditingSeries(null)}
          onSubmit={handleRenameSeries}
        />
      )}
      {bookModalSeries && (
        <AddSeriesBookModal
          series={bookModalSeries}
          onClose={() => setBookModalSeries(null)}
          onLinked={handleBookLinked}
        />
      )}
      {enhancedHardcoverApi && linkModalSeries && (
        <HardcoverSeriesLinkModal
          series={linkModalSeries}
          initialResults={linkModalResults}
          onClose={() => setLinkModalSeries(null)}
          onLinked={handleHardcoverLinked}
        />
      )}
    </div>
  )
}
