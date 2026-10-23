import { KeyboardEvent, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, ApiError, Series, SeriesHardcoverSearchResult } from '../api/client'
import { hardcoverSeriesUrl } from '../util/metadataSource'

interface Props {
  // The series the pick created and linked.
  onCreated: (series: Series) => Promise<void> | void
  // The pick was already in the library (409): its id, so the page can open it.
  onExisting: (seriesId: number) => Promise<void> | void
}

// Search Hardcover for a series and add it in one step (#1706). The search runs
// when the user submits, not on every keystroke, and picking a result creates
// the local series already linked. No books are added; the series card's
// Hardcover diff offers those one at a time afterwards.
export default function HardcoverSeriesAddSearch({ onCreated, onExisting }: Props) {
  const { t } = useTranslation()
  const [query, setQuery] = useState('')
  const [results, setResults] = useState<SeriesHardcoverSearchResult[] | null>(null)
  const [searching, setSearching] = useState(false)
  const [addingId, setAddingId] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  // Only the latest submitted search may fill the list.
  const searchSeq = useRef(0)

  const search = async () => {
    const term = query.trim()
    if (!term) return
    const seq = ++searchSeq.current
    setSearching(true)
    setError(null)
    try {
      const found = await api.searchHardcoverSeries(term, 10)
      if (seq === searchSeq.current) setResults(found)
    } catch (err) {
      if (seq !== searchSeq.current) return
      setResults(null)
      setError(err instanceof Error ? err.message : t('series.addFromHardcover.searchFailed'))
    } finally {
      if (seq === searchSeq.current) setSearching(false)
    }
  }

  // This sits inside the modal's own form, so it cannot be a form itself:
  // Enter runs the search here and must not submit the manual name below.
  const onKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key !== 'Enter') return
    event.preventDefault()
    void search()
  }

  const add = async (result: SeriesHardcoverSearchResult) => {
    setAddingId(result.foreignId)
    setError(null)
    try {
      const series = await api.createSeries({
        hardcover: {
          foreignId: result.foreignId,
          providerId: result.providerId,
          slug: result.slug,
          title: result.title,
          authorName: result.authorName,
          bookCount: result.bookCount,
          confidence: result.confidence,
        },
      })
      await onCreated(series)
    } catch (err) {
      const existingId = err instanceof ApiError && err.status === 409 ? err.body.seriesId : undefined
      if (typeof existingId === 'number') {
        try {
          await onExisting(existingId)
          return
        } catch (openErr) {
          setError(openErr instanceof Error ? openErr.message : t('series.addFromHardcover.addFailed'))
        }
      } else {
        setError(err instanceof Error ? err.message : t('series.addFromHardcover.addFailed'))
      }
    } finally {
      setAddingId(null)
    }
  }

  return (
    <div className="space-y-3">
      <div role="search" className="flex gap-2">
        <input
          type="search"
          enterKeyHint="search"
          value={query}
          onChange={e => setQuery(e.target.value)}
          onKeyDown={onKeyDown}
          aria-label={t('series.addFromHardcover.searchLabel')}
          placeholder={t('series.addFromHardcover.searchPlaceholder')}
          className="flex-1 min-w-0 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded-md px-3 py-2 text-sm"
          autoFocus
        />
        <button
          type="button"
          onClick={() => void search()}
          disabled={searching || !query.trim()}
          className="px-3 py-2 text-sm rounded-md border border-slate-300 dark:border-zinc-700 hover:bg-slate-200 dark:hover:bg-zinc-800 disabled:opacity-50"
        >
          {searching ? t('series.addFromHardcover.searching') : t('series.addFromHardcover.search')}
        </button>
      </div>

      {error && <p role="alert" className="text-sm text-red-700 dark:text-red-400">{error}</p>}

      {results && results.length === 0 && !error && (
        <p className="text-sm text-fg-muted">{t('series.addFromHardcover.noResults')}</p>
      )}

      {results && results.length > 0 && (
        <ul className="space-y-2">
          {results.map(result => {
            const preview = (result.books ?? []).slice(0, 4)
            const sourceUrl = hardcoverSeriesUrl(result.slug)
            return (
              <li
                key={result.foreignId}
                className="rounded-md border border-slate-300 dark:border-zinc-700 bg-slate-200/50 dark:bg-zinc-800/50 p-3"
              >
                <div className="flex items-start gap-3">
                  <div className="min-w-0 flex-1">
                    <div className="font-semibold truncate">{result.title}</div>
                    <div className="mt-1 flex flex-wrap gap-x-4 gap-y-1 text-xs text-fg-muted">
                      {result.authorName && <span>{result.authorName}</span>}
                      <span>{t('series.addFromHardcover.bookCount', { count: result.bookCount })}</span>
                    </div>
                    {preview.length > 0 && (
                      <p className="mt-2 text-xs text-fg-muted line-clamp-2 italic">
                        {preview.join(', ')}
                      </p>
                    )}
                    {sourceUrl && (
                      <a
                        href={sourceUrl}
                        target="_blank"
                        rel="noopener noreferrer"
                        className="text-xs text-sky-700 dark:text-sky-300 hover:underline mt-1 inline-block"
                      >
                        {t('series.addFromHardcover.viewOnHardcover')}
                      </a>
                    )}
                  </div>
                  <button
                    type="button"
                    onClick={() => void add(result)}
                    disabled={addingId !== null}
                    aria-label={t('series.addFromHardcover.addResult', { title: result.title })}
                    className="px-3 py-1.5 text-sm rounded-md border border-emerald-600 text-accent-text hover:bg-emerald-500/10 disabled:opacity-50 flex-shrink-0"
                  >
                    {addingId === result.foreignId ? t('series.addFromHardcover.adding') : t('series.addFromHardcover.add')}
                  </button>
                </div>
              </li>
            )
          })}
        </ul>
      )}
    </div>
  )
}
