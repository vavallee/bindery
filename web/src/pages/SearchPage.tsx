import { useEffect, useState, useRef } from 'react'
import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { api, isNoDownloadClientError, SearchResult } from '../api/client'

function formatSize(n: number): string {
  if (!n || n <= 0) return ''
  if (n >= 1073741824) return (n / 1073741824).toFixed(1) + ' GB'
  if (n >= 1048576) return (n / 1048576).toFixed(0) + ' MB'
  return (n / 1024).toFixed(0) + ' KB'
}

export default function SearchPage() {
  const { t } = useTranslation()
  const [query, setQuery] = useState('')
  const [results, setResults] = useState<SearchResult[] | null>(null)
  const [searching, setSearching] = useState(false)
  const [grabbing, setGrabbing] = useState<string | null>(null)
  const [grabbed, setGrabbed] = useState<Set<string>>(new Set())
  const [error, setError] = useState<string | null>(null)
  // When a grab fails because no download client of the needed protocol is
  // enabled (#959 backend error), we swap the raw error for a contextual nudge
  // linking to the client settings — shown regardless of whether the library is
  // empty, which the first-run guidance (#960) didn't cover (#968).
  const [needsClient, setNeedsClient] = useState(false)
  const inputRef = useRef<HTMLInputElement>(null)

  useEffect(() => {
    document.title = 'Search · Bindery'
    return () => { document.title = 'Bindery' }
  }, [])

  const search = async () => {
    const q = query.trim()
    if (!q) return
    setSearching(true)
    setError(null)
    setNeedsClient(false)
    setResults(null)
    try {
      const r = await api.searchIndexers(q)
      setResults(r)
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Search failed')
    } finally {
      setSearching(false)
    }
  }

  const grab = async (r: SearchResult) => {
    setGrabbing(r.guid)
    setError(null)
    setNeedsClient(false)
    try {
      await api.grab({
        guid: r.guid,
        title: r.title,
        nzbUrl: r.nzbUrl,
        size: r.size,
        protocol: r.protocol,
      })
      setGrabbed(prev => new Set(prev).add(r.guid))
    } catch (e) {
      if (isNoDownloadClientError(e)) {
        setNeedsClient(true)
      } else {
        setError(e instanceof Error ? e.message : 'Grab failed')
      }
    } finally {
      setGrabbing(null)
    }
  }

  return (
    <div className="max-w-4xl mx-auto px-4 sm:px-6 lg:px-8 py-8">
      <h2 className="text-xl font-bold mb-6">{t('search.heading')}</h2>

      <form
        onSubmit={e => { e.preventDefault(); search() }}
        className="flex gap-2 mb-6"
      >
        <input
          ref={inputRef}
          value={query}
          onChange={e => setQuery(e.target.value)}
          placeholder={t('search.placeholder')}
          className="flex-1 bg-slate-100 dark:bg-zinc-900 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-500"
          autoFocus
        />
        <button
          type="submit"
          disabled={searching || !query.trim()}
          className="px-4 py-2 bg-emerald-600 hover:bg-emerald-500 disabled:opacity-50 rounded text-sm font-medium"
        >
          {searching ? t('search.searching') : t('search.submit')}
        </button>
      </form>

      {error && (
        <div className="mb-4 px-3 py-2 bg-red-100 text-red-800 dark:bg-red-900/30 dark:text-red-300 rounded text-sm">
          {error}
        </div>
      )}

      {needsClient && (
        <div className="mb-4 px-4 py-3 bg-amber-100 text-amber-900 dark:bg-amber-900/30 dark:text-amber-200 border border-amber-200 dark:border-amber-900/50 rounded-lg text-sm">
          <p className="mb-2">{t('search.noClient.body')}</p>
          <Link
            to="/settings?tab=clients"
            className="inline-block px-3 py-1.5 bg-emerald-600 hover:bg-emerald-500 text-white rounded-md text-sm font-medium transition-colors"
          >
            {t('search.noClient.action')}
          </Link>
        </div>
      )}

      {results !== null && results.length === 0 && (
        <div className="text-center py-10 text-sm text-slate-500 dark:text-zinc-500 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900">
          {t('search.noResults')}
        </div>
      )}

      {results !== null && results.length > 0 && (
        <div>
          <p className="text-xs text-slate-500 dark:text-zinc-500 mb-2">{t('search.resultCount', { count: results.length })}</p>
          <div className="space-y-1">
            {results.map(r => (
              <div
                key={r.guid}
                className="flex items-center justify-between p-2 border border-slate-200 dark:border-zinc-800 rounded bg-slate-100 dark:bg-zinc-900 text-xs"
              >
                <div className="min-w-0 mr-3">
                  <span className="truncate block text-slate-800 dark:text-zinc-200">{r.title}</span>
                  <span className="text-slate-500 dark:text-zinc-500 truncate block">
                    {r.indexerName}{r.size ? ` · ${formatSize(r.size)}` : ''}{r.grabs ? ` · ${r.grabs} grabs` : ''}
                    {r.language && <span className="ml-2 uppercase">{r.language}</span>}
                  </span>
                </div>
                {grabbed.has(r.guid) ? (
                  <span className="px-3 py-2 text-emerald-600 dark:text-emerald-400 text-[11px] font-medium flex-shrink-0">
                    {t('search.grabbed')}
                  </span>
                ) : (
                  <button
                    onClick={() => grab(r)}
                    disabled={grabbing !== null}
                    className="px-3 py-2 bg-emerald-600 hover:bg-emerald-500 disabled:opacity-50 rounded text-[11px] font-medium flex-shrink-0"
                  >
                    {grabbing === r.guid ? t('search.grabbing') : t('search.grab')}
                  </button>
                )}
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  )
}
