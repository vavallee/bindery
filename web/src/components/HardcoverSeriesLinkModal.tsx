import { useEffect, useState } from 'react'
import { api, Series, SeriesHardcoverLink, SeriesHardcoverSearchResult } from '../api/client'
import { hardcoverSeriesUrl } from '../util/metadataSource'
import { useModal } from './useModal'

interface HardcoverSeriesLinkModalProps {
  series: Series
  initialResults?: SeriesHardcoverSearchResult[]
  onClose: () => void
  onLinked: (seriesId: number, link?: SeriesHardcoverLink) => void
}

function formatConfidence(value?: number) {
  if (!value) return ''
  return `${Math.round(value * 100)}%`
}

export default function HardcoverSeriesLinkModal({ series, initialResults, onClose, onLinked }: HardcoverSeriesLinkModalProps) {
  const [query, setQuery] = useState(series.title)
  const [results, setResults] = useState<SeriesHardcoverSearchResult[]>(initialResults ?? [])
  const [selectedId, setSelectedId] = useState(initialResults?.[0]?.foreignId ?? '')
  const [currentLink, setCurrentLink] = useState<SeriesHardcoverLink | null>(series.hardcoverLink ?? null)
  const [searching, setSearching] = useState(false)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const currentLinkId = series.hardcoverLink?.id
  const initialResultsCount = initialResults?.length ?? 0

  useEffect(() => {
    setQuery(series.title)
    setResults(initialResults ?? [])
    setSelectedId(initialResults?.[0]?.foreignId ?? '')
    setCurrentLink(series.hardcoverLink ?? null)
    setError(null)
  }, [series.id, series.title, currentLinkId, initialResultsCount])

  useEffect(() => {
    let cancelled = false
    api.getSeriesHardcoverLink(series.id)
      .then(link => {
        if (!cancelled) setCurrentLink(link)
      })
      .catch(() => {
        if (!cancelled) setCurrentLink(series.hardcoverLink ?? null)
      })
    return () => {
      cancelled = true
    }
  }, [series.id, currentLinkId])

  useEffect(() => {
    const term = query.trim()
    if (!term) {
      setResults([])
      setSelectedId('')
      return
    }
    if (initialResultsCount > 0 && term === series.title) {
      return
    }
    let cancelled = false
    const timer = window.setTimeout(() => {
      setSearching(true)
      setError(null)
      api.searchHardcoverSeries(term, 10)
        .then(found => {
          if (cancelled) return
          setResults(found)
          setSelectedId(prev => prev || found[0]?.foreignId || '')
        })
        .catch(err => {
          if (cancelled) return
          setResults([])
          setSelectedId('')
          setError(err instanceof Error ? err.message : 'Search failed')
        })
        .finally(() => {
          if (!cancelled) setSearching(false)
        })
    }, 300)

    return () => {
      cancelled = true
      window.clearTimeout(timer)
    }
  }, [query, series.id, series.title, initialResultsCount])

  const selected = results.find(result => result.foreignId === selectedId)

  const confirmSelection = async () => {
    if (!selected) return
    setSaving(true)
    setError(null)
    try {
      const link = await api.linkSeriesHardcover(series.id, selected)
      setCurrentLink(link)
      onLinked(series.id, link)
      onClose()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to link series')
    } finally {
      setSaving(false)
    }
  }

  const removeLink = async () => {
    setSaving(true)
    setError(null)
    try {
      await api.unlinkSeriesHardcover(series.id)
      setCurrentLink(null)
      onLinked(series.id, undefined)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to remove link')
    } finally {
      setSaving(false)
    }
  }

  const { titleId, panelProps } = useModal({ onClose, canClose: !saving })

  return (
    <div className="modal-overlay fixed inset-0 bg-black/60 flex items-center justify-center p-4 z-50" onClick={onClose}>
      <div {...panelProps} className="bg-slate-100 dark:bg-zinc-900 border border-slate-300 dark:border-zinc-700 rounded-lg w-full max-w-2xl shadow-2xl modal-max-h flex flex-col" onClick={e => e.stopPropagation()}>
        <div className="p-4 border-b border-slate-200 dark:border-zinc-800 flex items-center justify-between gap-4">
          <h3 id={titleId} className="text-lg font-semibold">Link to Hardcover Series</h3>
          <button
            type="button"
            onClick={onClose}
            className="text-2xl leading-none text-slate-500 dark:text-zinc-500 hover:text-slate-900 dark:hover:text-white"
            aria-label="Close"
          >
            ×
          </button>
        </div>

        <div className="p-4 flex-1 overflow-y-auto space-y-4">
          {currentLink && (
            <div className="rounded-md border border-sky-500/30 bg-sky-500/10 p-4">
              <div className="flex items-start justify-between gap-3">
                <div className="min-w-0">
                  <p className="text-xs uppercase tracking-wide text-slate-600 dark:text-zinc-400">Currently linked</p>
                  <p className="font-semibold truncate">{currentLink.hardcoverTitle}</p>
                  {currentLink.hardcoverAuthorName && (
                    <p className="text-xs text-slate-600 dark:text-zinc-400 mt-0.5">{currentLink.hardcoverAuthorName}</p>
                  )}
                  {hardcoverSeriesUrl(currentLink.hardcoverSlug) && (
                    <a
                      href={hardcoverSeriesUrl(currentLink.hardcoverSlug)!}
                      target="_blank"
                      rel="noopener noreferrer"
                      className="text-xs text-sky-700 dark:text-sky-300 hover:underline mt-1 inline-block"
                    >
                      View on Hardcover ↗
                    </a>
                  )}
                </div>
                <span className="text-xs px-2 py-1 rounded-full border border-sky-500/40 text-sky-700 dark:text-sky-300 flex-shrink-0">
                  {currentLink.linkedBy === 'auto' ? 'Auto' : 'Manual'} {formatConfidence(currentLink.confidence)}
                </span>
              </div>
              <button
                type="button"
                onClick={removeLink}
                disabled={saving}
                className="mt-3 text-sm text-rose-600 dark:text-rose-400 hover:text-rose-500 disabled:opacity-50"
              >
                Remove Link
              </button>
            </div>
          )}

          <input
            enterKeyHint="search"
            type="text"
            value={query}
            onChange={e => setQuery(e.target.value)}
            placeholder="Search Hardcover series"
            className="w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded-md px-3 py-2 text-sm focus:outline-none focus:border-emerald-500"
            autoFocus
          />

          {error && <p className="text-sm text-rose-600 dark:text-rose-400">{error}</p>}

          <div className="space-y-2">
            {results.map(result => {
              const selectedResult = selectedId === result.foreignId
              const previewBooks = result.books ?? []
              const sourceUrl = hardcoverSeriesUrl(result.slug)
              return (
                <div
                  key={result.foreignId}
                  className={`rounded-md border transition-colors ${
                    selectedResult
                      ? 'border-emerald-500 bg-emerald-500/10'
                      : 'border-slate-300 dark:border-zinc-700 bg-slate-200/50 dark:bg-zinc-800/50 hover:bg-slate-200 dark:hover:bg-zinc-800'
                  }`}
                >
                  <button
                    type="button"
                    onClick={() => setSelectedId(result.foreignId)}
                    className="w-full text-left p-4 pb-2"
                  >
                    <div className="flex items-start gap-3">
                      <span className={`mt-1 h-4 w-4 rounded-full border flex-shrink-0 ${selectedResult ? 'border-emerald-500 bg-emerald-500' : 'border-slate-400 dark:border-zinc-500'}`} />
                      <div className="min-w-0 flex-1">
                        <div className="font-semibold truncate">{result.title}</div>
                        <div className="mt-1 flex flex-wrap gap-x-4 gap-y-1 text-xs text-slate-600 dark:text-zinc-400">
                          {result.authorName && <span>{result.authorName}</span>}
                          <span>{result.bookCount} {result.bookCount === 1 ? 'book' : 'books'}</span>
                          {result.readersCount > 0 && <span>{result.readersCount.toLocaleString()} readers</span>}
                          {result.confidence && <span>{formatConfidence(result.confidence)} match</span>}
                        </div>
                        {previewBooks.length > 0 && (
                          <p className="mt-2 text-xs text-slate-600 dark:text-zinc-500 line-clamp-2 italic">
                            {previewBooks.slice(0, 4).join(', ')}
                          </p>
                        )}
                      </div>
                    </div>
                  </button>
                  {/* Outside the selection button: near-identical light novel
                      candidates cannot be told apart without opening them, and
                      an anchor nested in a button is invalid markup. */}
                  <div className="px-4 pb-3 pl-11">
                    {sourceUrl ? (
                      <a
                        href={sourceUrl}
                        target="_blank"
                        rel="noopener noreferrer"
                        className="text-xs text-sky-700 dark:text-sky-300 hover:underline"
                      >
                        View on Hardcover ↗
                      </a>
                    ) : (
                      <span className="text-xs text-slate-500 dark:text-zinc-500">No Hardcover page for this result</span>
                    )}
                  </div>
                </div>
              )
            })}
            {searching && <p className="text-sm text-slate-600 dark:text-zinc-500 text-center py-4">Searching...</p>}
            {!searching && results.length === 0 && !error && (
              <p className="text-sm text-slate-600 dark:text-zinc-500 text-center py-4">No Hardcover series found.</p>
            )}
          </div>
        </div>

        <div className="p-4 border-t border-slate-200 dark:border-zinc-800 flex justify-end gap-3">
          <button type="button" onClick={onClose} className="px-4 py-2 text-sm text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white">
            Cancel
          </button>
          <button
            type="button"
            onClick={confirmSelection}
            disabled={!selected || saving}
            className="px-4 py-2 bg-emerald-600 hover:bg-emerald-500 disabled:opacity-50 rounded-md text-sm font-medium"
          >
            {saving ? 'Saving...' : 'Confirm Selection'}
          </button>
        </div>
      </div>
    </div>
  )
}
