import { useEffect, useState, useRef } from 'react'
import { Link, useSearchParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { api, isNoDownloadClientError, SearchResult } from '../api/client'
import { formatBytes } from '../util/format'
import { useConfirmDialog } from '../components/useConfirmDialog'
import { grabWithForceConfirm } from '../util/forceGrab'
import { primaryFill } from '../components/buttons'
import { errorText } from '../util/errorText'

export default function SearchPage() {
  const { t } = useTranslation()
  const { confirm, confirmDialog } = useConfirmDialog()
  // The header search's "Search indexers for …" row lands here with ?q=.
  const [searchParams, setSearchParams] = useSearchParams()
  const initialQuery = searchParams.get('q') ?? ''
  const [query, setQuery] = useState(initialQuery)
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
  const lastRun = useRef('')
  // Two quick header searches: only the latest one's answer is shown, even
  // when the earlier, slower one comes back last.
  const searchSeq = useRef(0)

  useEffect(() => {
    document.title = `${t('search.pageTitle', 'Search')} · Bindery`
    return () => { document.title = 'Bindery' }
  }, [t])

  const search = async (raw: string = query) => {
    const q = raw.trim()
    if (!q) return
    // The URL follows the query, so a reload or Back keeps it, and the effect
    // below knows this one already ran.
    lastRun.current = q
    if (searchParams.get('q') !== q) setSearchParams({ q }, { replace: true })
    setSearching(true)
    setError(null)
    setNeedsClient(false)
    setResults(null)
    const seq = ++searchSeq.current
    try {
      const r = await api.searchIndexers(q)
      if (seq === searchSeq.current) setResults(r)
    } catch (e) {
      if (seq === searchSeq.current) setError(errorText(e, t('search.searchFailed', 'Search failed')))
    } finally {
      if (seq === searchSeq.current) setSearching(false)
    }
  }

  // Arriving from the header with a query runs it, so the row reads as
  // "search for this" rather than "go to a page and type it again". That
  // includes a header search for a new query made while already on /search:
  // the page stays mounted and only ?q= changes, so the effect keys on the
  // query, not on mount. lastRun stops a search typed here from running
  // twice; it also means a header search for the query already shown does
  // not run again (the URL does not change), and Search on the page does.
  useEffect(() => {
    const q = initialQuery.trim()
    if (!q || q === lastRun.current) return
    setQuery(initialQuery)
    void search(initialQuery)
    // search is recreated each render; the query is the only trigger.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [initialQuery])

  const grab = async (r: SearchResult) => {
    setGrabbing(r.guid)
    setError(null)
    setNeedsClient(false)
    try {
      const dl = await grabWithForceConfirm({
        guid: r.guid,
        title: r.title,
        nzbUrl: r.nzbUrl,
        size: r.size,
        indexerId: r.indexerId,
        protocol: r.protocol,
      }, confirm, t)
      if (!dl) return
      setGrabbed(prev => new Set(prev).add(r.guid))
    } catch (e) {
      if (isNoDownloadClientError(e)) {
        setNeedsClient(true)
      } else {
        setError(errorText(e, t('search.grabFailed', 'Grab failed')))
      }
    } finally {
      setGrabbing(null)
    }
  }

  return (
    <div className="max-w-4xl mx-auto sm:px-6 lg:px-8 py-8">
      {confirmDialog}
      <h2 className="text-xl font-bold mb-6">{t('search.heading')}</h2>

      <form
        onSubmit={e => { e.preventDefault(); search() }}
        className="flex gap-2 mb-6"
      >
        <input
          enterKeyHint="search"
          ref={inputRef}
          value={query}
          onChange={e => setQuery(e.target.value)}
          placeholder={t('search.placeholder')}
          // min-w-0 matters: a flex item's automatic minimum size is its
          // min-content width, and an input's is its default 20-character
          // size, which pushed this row past a phone viewport.
          className="flex-1 min-w-0 bg-slate-100 dark:bg-zinc-900 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm"
          autoFocus
        />
        <button
          type="submit"
          disabled={searching || !query.trim()}
          className={`shrink-0 whitespace-nowrap px-4 py-2 ${primaryFill} disabled:opacity-50 rounded text-sm font-medium`}
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
            className={`inline-block px-3 py-1.5 ${primaryFill} rounded-md text-sm font-medium transition-colors`}
          >
            {t('search.noClient.action')}
          </Link>
        </div>
      )}

      {results !== null && results.length === 0 && (
        <div className="text-center py-10 text-sm text-fg-muted border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900">
          {t('search.noResults')}
        </div>
      )}

      {results !== null && results.length > 0 && (
        <div>
          <p className="text-xs text-fg-muted mb-2">{t('search.resultCount', { count: results.length })}</p>
          <div className="space-y-1">
            {results.map(r => (
              <div
                key={r.guid}
                className="flex items-center justify-between p-2 border border-slate-200 dark:border-zinc-800 rounded bg-slate-100 dark:bg-zinc-900 text-xs"
              >
                <div className="min-w-0 mr-3">
                  <span className="truncate block text-slate-800 dark:text-zinc-200">{r.title}</span>
                  <span className="text-fg-muted truncate block">
                    {r.indexerName}{r.size ? ` · ${formatBytes(r.size)}` : ''}{r.grabs ? ` · ${r.grabs} grabs` : ''}
                    {r.language && <span className="ml-2 uppercase">{r.language}</span>}
                  </span>
                </div>
                {grabbed.has(r.guid) ? (
                  <span className="px-3 py-2 text-accent-text text-[11px] font-medium flex-shrink-0">
                    {t('search.grabbed')}
                  </span>
                ) : (
                  <button
                    onClick={() => grab(r)}
                    disabled={grabbing !== null}
                    className={`px-3 py-2 ${primaryFill} disabled:opacity-50 rounded text-[11px] font-medium flex-shrink-0`}
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
