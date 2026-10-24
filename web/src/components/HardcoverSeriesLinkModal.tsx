import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, Series, SeriesHardcoverLink, SeriesHardcoverSearchResult } from '../api/client'
import { hardcoverSeriesUrl } from '../util/metadataSource'
import { useModal } from './useModal'
import { primaryFill } from './buttons'
import { errorText } from '../util/errorText'

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
  const { t } = useTranslation()
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
          setError(errorText(err, t('series.hardcoverLink.searchFailed', 'Search failed')))
        })
        .finally(() => {
          if (!cancelled) setSearching(false)
        })
    }, 300)

    return () => {
      cancelled = true
      window.clearTimeout(timer)
    }
  }, [query, series.id, series.title, initialResultsCount]) // eslint-disable-line react-hooks/exhaustive-deps

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
      setError(errorText(err, t('series.hardcoverLink.linkFailed', 'Failed to link series')))
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
      setError(errorText(err, t('series.hardcoverLink.unlinkFailed', 'Failed to remove link')))
    } finally {
      setSaving(false)
    }
  }

  const { titleId, panelProps } = useModal({ onClose, canClose: !saving })

  return (
    <div className="modal-overlay fixed inset-0 bg-black/60 flex items-center justify-center p-4 z-50" onClick={onClose}>
      <div {...panelProps} className="bg-slate-100 dark:bg-zinc-900 border border-slate-300 dark:border-zinc-700 rounded-lg w-full max-w-2xl shadow-2xl modal-max-h flex flex-col" onClick={e => e.stopPropagation()}>
        <div className="p-4 border-b border-slate-200 dark:border-zinc-800 flex items-center justify-between gap-4">
          <h3 id={titleId} className="text-lg font-semibold">{t('series.hardcoverLink.title', 'Link to Hardcover Series')}</h3>
          <button
            type="button"
            onClick={onClose}
            className="text-2xl leading-none text-fg-muted hover:text-slate-900 dark:hover:text-white"
            aria-label={t('common.close', 'Close')}
          >
            ×
          </button>
        </div>

        <div className="p-4 flex-1 overflow-y-auto space-y-4">
          {currentLink && (
            <div className="rounded-md border border-sky-500/30 bg-sky-500/10 p-4">
              <div className="flex items-start justify-between gap-3">
                <div className="min-w-0">
                  <p className="text-xs uppercase tracking-wide text-fg-muted">{t('series.hardcoverLink.current', 'Currently linked')}</p>
                  <p className="font-semibold truncate">{currentLink.hardcoverTitle}</p>
                  {currentLink.hardcoverAuthorName && (
                    <p className="text-xs text-fg-muted mt-0.5">{currentLink.hardcoverAuthorName}</p>
                  )}
                  {hardcoverSeriesUrl(currentLink.hardcoverSlug) && (
                    <a
                      href={hardcoverSeriesUrl(currentLink.hardcoverSlug)!}
                      target="_blank"
                      rel="noopener noreferrer"
                      className="text-xs text-sky-700 dark:text-sky-300 hover:underline mt-1 inline-block"
                    >
                      {t('series.hardcoverLink.view', 'View on Hardcover ↗')}
                    </a>
                  )}
                </div>
                <span className="text-xs px-2 py-1 rounded-full border border-sky-500/40 text-sky-700 dark:text-sky-300 flex-shrink-0">
                  {currentLink.linkedBy === 'auto' ? t('series.hardcoverLink.auto', 'Auto') : t('series.hardcoverLink.manual', 'Manual')} {formatConfidence(currentLink.confidence)}
                </span>
              </div>
              <button
                type="button"
                onClick={removeLink}
                disabled={saving}
                className="mt-3 text-sm text-rose-600 dark:text-rose-400 hover:text-rose-500 disabled:opacity-50"
              >
                {t('series.hardcoverLink.remove', 'Remove Link')}
              </button>
            </div>
          )}

          <input
            enterKeyHint="search"
            type="text"
            value={query}
            onChange={e => setQuery(e.target.value)}
            placeholder={t('series.hardcoverLink.searchPlaceholder', 'Search Hardcover series')}
            className="w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded-md px-3 py-2 text-sm"
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
                        <div className="mt-1 flex flex-wrap gap-x-4 gap-y-1 text-xs text-fg-muted">
                          {result.authorName && <span>{result.authorName}</span>}
                          <span>{t('series.hardcoverLink.books', { count: result.bookCount })}</span>
                          {result.readersCount > 0 && <span>{t('series.hardcoverLink.readers', { n: result.readersCount.toLocaleString(), defaultValue: '{{n}} readers' })}</span>}
                          {result.confidence && <span>{t('series.hardcoverLink.match', { confidence: formatConfidence(result.confidence), defaultValue: '{{confidence}} match' })}</span>}
                        </div>
                        {previewBooks.length > 0 && (
                          <p className="mt-2 text-xs text-fg-muted line-clamp-2 italic">
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
                        {t('series.hardcoverLink.view', 'View on Hardcover ↗')}
                      </a>
                    ) : (
                      <span className="text-xs text-fg-muted">{t('series.hardcoverLink.noPage', 'No Hardcover page for this result')}</span>
                    )}
                  </div>
                </div>
              )
            })}
            {searching && <p className="text-sm text-fg-muted text-center py-4">{t('series.hardcoverLink.searching', 'Searching…')}</p>}
            {!searching && results.length === 0 && !error && (
              <p className="text-sm text-fg-muted text-center py-4">{t('series.hardcoverLink.noResults', 'No Hardcover series found.')}</p>
            )}
          </div>
        </div>

        <div className="p-4 border-t border-slate-200 dark:border-zinc-800 flex justify-end gap-3">
          <button type="button" onClick={onClose} className="px-4 py-2 text-sm text-fg-muted hover:text-slate-900 dark:hover:text-white">
            {t('common.cancel', 'Cancel')}
          </button>
          <button
            type="button"
            onClick={confirmSelection}
            disabled={!selected || saving}
            className={`px-4 py-2 ${primaryFill} disabled:opacity-50 rounded-md text-sm font-medium`}
          >
            {saving ? t('series.hardcoverLink.saving', 'Saving…') : t('series.hardcoverLink.confirm', 'Confirm Selection')}
          </button>
        </div>
      </div>
    </div>
  )
}
