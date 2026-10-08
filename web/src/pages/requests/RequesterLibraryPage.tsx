import { useCallback, useEffect, useState } from 'react'
import { Link } from 'react-router'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'
import type { RequesterLibraryBook } from '../../api/client'
import { mediaTypeLabel } from './requestLabels'

const PAGE_SIZE = 60

// The library as a requester sees it: read only, from the projection
// endpoint, with no links into book pages and no downloads. Anything missing
// is one click away from a request.
export default function RequesterLibraryPage() {
  const { t } = useTranslation()
  const [query, setQuery] = useState('')
  const [search, setSearch] = useState('')
  const [items, setItems] = useState<RequesterLibraryBook[]>([])
  const [total, setTotal] = useState(0)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const load = useCallback(async (offset: number, term: string) => {
    setLoading(true)
    setError(null)
    try {
      const page = await api.requesterLibrary({ search: term, limit: PAGE_SIZE, offset })
      setItems(prev => (offset === 0 ? page.items : [...prev, ...page.items]))
      setTotal(page.total)
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : t('requests.library.loadFailed'))
    } finally {
      setLoading(false)
    }
  }, [t])

  useEffect(() => { void load(0, search) }, [load, search])

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between gap-4 flex-wrap">
        <h2 className="text-2xl font-bold">{t('requests.library.title')}</h2>
        <Link to="/request" className="px-4 py-2 bg-emerald-600 hover:bg-emerald-500 rounded-md text-sm font-medium text-white">
          {t('requests.library.requestSomething')}
        </Link>
      </div>

      <form
        role="search"
        onSubmit={e => { e.preventDefault(); setSearch(query.trim()) }}
        className="flex gap-2 max-w-md"
      >
        <input
          enterKeyHint="search"
          type="search"
          value={query}
          onChange={e => setQuery(e.target.value)}
          aria-label={t('requests.library.searchLabel')}
          placeholder={t('requests.library.searchPlaceholder')}
          className="flex-1 min-w-0 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded-md px-3 py-2 text-sm focus:outline-none focus:border-emerald-500"
        />
        <button type="submit" className="px-4 py-2 rounded-md text-sm border border-slate-300 dark:border-zinc-700">{t('common.search')}</button>
      </form>

      {error && <p role="alert" className="text-sm text-red-700 dark:text-red-300">{error}</p>}
      {!loading && !error && items.length === 0 && (
        <p className="text-sm text-fg-muted">{search ? t('requests.library.noMatches') : t('requests.library.empty')}</p>
      )}

      <ul className="grid grid-cols-2 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-6 gap-4" aria-label={t('requests.library.title')}>
        {items.map(b => (
          <li key={b.id} className="min-w-0">
            {b.coverUrl ? (
              <img src={b.coverUrl} alt="" loading="lazy" decoding="async" className="w-full aspect-[2/3] object-cover rounded-md bg-slate-200 dark:bg-zinc-800" />
            ) : (
              <div aria-hidden="true" className="w-full aspect-[2/3] rounded-md bg-slate-200 dark:bg-zinc-800" />
            )}
            <div className="mt-2 text-sm font-medium leading-snug [overflow-wrap:anywhere]">{b.title}</div>
            <div className="text-xs text-fg-muted [overflow-wrap:anywhere]">{b.authorName}</div>
            {b.series && (
              <div className="text-xs text-fg-muted [overflow-wrap:anywhere]">
                {b.seriesPosition ? t('requests.library.seriesWithPosition', { series: b.series, position: b.seriesPosition }) : b.series}
              </div>
            )}
            <div className="text-xs mt-1">
              {b.formats.length > 0
                ? b.formats.map(f => mediaTypeLabel(t, f)).join(', ')
                : t('requests.library.notYetAvailable')}
            </div>
          </li>
        ))}
      </ul>

      {loading && <p className="text-sm text-fg-muted">{t('common.loading')}</p>}
      {!loading && items.length < total && (
        <button type="button" onClick={() => void load(items.length, search)} className="px-4 py-2 text-sm rounded-md border border-slate-300 dark:border-zinc-700">
          {t('requests.loadMore')}
        </button>
      )}
    </div>
  )
}
