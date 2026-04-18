import { useEffect, useRef, useState, useMemo } from 'react'
import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { api, Book, SearchResult } from '../api/client'
import BulkActionBar from '../components/BulkActionBar'
import Pagination from '../components/Pagination'
import { usePagination } from '../components/usePagination'

export default function WantedPage() {
  const { t } = useTranslation()

  const [books, setBooks] = useState<Book[]>([])
  const [loading, setLoading] = useState(true)
  const [searchingId, setSearchingId] = useState<number | null>(null)
  const [results, setResults] = useState<SearchResult[]>([])
  const [showResults, setShowResults] = useState<number | null>(null)
  const [search, setSearch] = useState('')
  const [grabbingGuid, setGrabbingGuid] = useState<string | null>(null)
  const [grabbedGuid, setGrabbedGuid] = useState<string | null>(null)
  const [unmonitoringId, setUnmonitoringId] = useState<number | null>(null)
  const [selectedIds, setSelectedIds] = useState<Set<number>>(new Set())
  const [bulkBusy, setBulkBusy] = useState(false)
  const [showExcluded, setShowExcluded] = useState(false)
  const selectAllRef = useRef<HTMLInputElement>(null)

  const load = () => {
    api.listWanted({ includeExcluded: showExcluded }).then(setBooks).catch(console.error).finally(() => setLoading(false))
  }

  useEffect(() => {
    load()
  }, [showExcluded])

  const filtered = useMemo(() => {
    if (!search.trim()) return books
    const q = search.trim().toLowerCase()
    return books.filter(b =>
      b.title.toLowerCase().includes(q) ||
      (b.author?.authorName && b.author.authorName.toLowerCase().includes(q))
    )
  }, [books, search])

  const searchBook = async (book: Book) => {
    setSearchingId(book.id)
    try {
      const res = await api.searchBook(book.id)
      setResults(res.results)
      setShowResults(book.id)
    } catch (err) {
      console.error(err)
    } finally {
      setSearchingId(null)
    }
  }

  const changeMediaType = async (book: Book, mediaType: 'ebook' | 'audiobook' | 'both') => {
    try {
      const updated = await api.updateBook(book.id, { mediaType })
      setBooks(books.map(b => b.id === book.id ? updated : b))
    } catch (err) {
      alert(err instanceof Error ? err.message : 'Failed to update')
    }
  }

  const unmonitor = async (book: Book) => {
    setUnmonitoringId(book.id)
    try {
      await api.updateBook(book.id, { monitored: false })
      setBooks(books.filter(b => b.id !== book.id))
    } catch (err) {
      alert(err instanceof Error ? err.message : 'Failed to unmonitor')
    } finally {
      setUnmonitoringId(null)
    }
  }

  const grab = async (result: SearchResult, book: Book) => {
    setGrabbingGuid(result.guid)
    try {
      await api.grab({
        guid: result.guid,
        title: result.title,
        nzbUrl: result.nzbUrl,
        size: result.size,
        bookId: book.id,
        protocol: result.protocol,
        mediaType: book.mediaType,
      })
      setGrabbedGuid(result.guid)
      setTimeout(() => {
        setShowResults(null)
        setGrabbedGuid(null)
        api.listWanted().then(setBooks).catch(console.error)
      }, 1200)
    } catch (err: unknown) {
      alert(err instanceof Error ? err.message : 'Grab failed')
    } finally {
      setGrabbingGuid(null)
    }
  }

  const { pageItems, paginationProps, reset } = usePagination(filtered, 50, 'wanted')

  useEffect(() => { reset() }, [search, reset])

  // Keep the select-all checkbox indeterminate state in sync.
  const allPageSelected = pageItems.length > 0 && pageItems.every(b => selectedIds.has(b.id))
  const somePageSelected = pageItems.some(b => selectedIds.has(b.id)) && !allPageSelected
  useEffect(() => {
    if (selectAllRef.current) selectAllRef.current.indeterminate = somePageSelected
  }, [somePageSelected])

  useEffect(() => {
    document.title = 'Wanted · Bindery'
    return () => { document.title = 'Bindery' }
  }, [])

  const toggleSelect = (id: number) => {
    setSelectedIds(prev => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  const selectAllOnPage = () => setSelectedIds(new Set(pageItems.map(b => b.id)))
  const clearSelection = () => setSelectedIds(new Set())

  const runBulk = async (action: Parameters<typeof api.bulkActionWanted>[1]) => {
    if (selectedIds.size === 0) return
    setBulkBusy(true)
    try {
      await api.bulkActionWanted([...selectedIds], action)
      clearSelection()
      load()
    } catch (err) {
      alert(err instanceof Error ? err.message : 'Bulk action failed')
    } finally {
      setBulkBusy(false)
    }
  }

  const formatSize = (bytes: number) => {
    if (bytes > 1073741824) return (bytes / 1073741824).toFixed(1) + ' GB'
    if (bytes > 1048576) return (bytes / 1048576).toFixed(1) + ' MB'
    return (bytes / 1024).toFixed(0) + ' KB'
  }

  return (
    <div className={selectedIds.size > 0 ? 'pb-16' : ''}>
      <div className="flex items-center justify-between mb-4">
        <h2 className="text-2xl font-bold">{t('wanted.title')}</h2>
        <div className="flex items-center gap-4">
          <label className="flex items-center gap-1.5 text-xs text-slate-600 dark:text-zinc-400 cursor-pointer select-none">
            <input
              type="checkbox"
              checked={showExcluded}
              onChange={e => setShowExcluded(e.target.checked)}
              className="rounded border-slate-400 dark:border-zinc-600 text-emerald-500 focus:ring-emerald-500 focus:ring-offset-0"
            />
            {t('wanted.showExcluded')}
          </label>
          <span className="text-sm text-slate-600 dark:text-zinc-500">{t('wanted.countLabel', { filtered: filtered.length, total: books.length })}</span>
        </div>
      </div>

      <input
        type="search"
        value={search}
        onChange={e => setSearch(e.target.value)}
        placeholder={t('wanted.searchPlaceholder')}
        className="w-full mb-4 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600 placeholder-slate-400 dark:placeholder-zinc-600"
      />

      {loading ? (
        <div className="text-slate-600 dark:text-zinc-500">{t('common.loading')}</div>
      ) : books.length === 0 ? (
        <div className="text-center py-16 text-slate-600 dark:text-zinc-500">
          <p>{t('wanted.empty')}</p>
        </div>
      ) : filtered.length === 0 ? (
        <div className="text-center py-16 text-slate-600 dark:text-zinc-500">
          <p>{t('wanted.noMatch')}</p>
        </div>
      ) : (
        <>
          {/* Select-all row */}
          <div className="flex items-center gap-2 mb-2 px-1">
            <input
              ref={selectAllRef}
              type="checkbox"
              checked={allPageSelected}
              onChange={e => e.target.checked ? selectAllOnPage() : clearSelection()}
              className="rounded-full border-slate-400 dark:border-zinc-600 text-emerald-500 focus:ring-emerald-500 focus:ring-offset-0"
              title="Select all on this page"
            />
            <span className="text-xs text-slate-500 dark:text-zinc-500">{t('common.selectAllPage')}</span>
          </div>

          <div className="space-y-2">
            {pageItems.map(book => (
              <div key={book.id}>
                <div className={`flex items-center justify-between p-3 border rounded-lg transition-colors ${selectedIds.has(book.id) ? 'border-emerald-500 bg-emerald-500/5 dark:bg-emerald-500/5' : 'border-slate-200 dark:border-zinc-800 bg-slate-100 dark:bg-zinc-900'}`}>
                  <div className="flex items-center gap-3 min-w-0">
                    <input
                      type="checkbox"
                      checked={selectedIds.has(book.id)}
                      onChange={() => toggleSelect(book.id)}
                      className="rounded-full border-slate-400 dark:border-zinc-600 text-emerald-500 focus:ring-emerald-500 focus:ring-offset-0 flex-shrink-0"
                      title={t('wanted.selectBook', { title: book.title })}
                    />
                    {book.imageUrl && (
                      <img src={book.imageUrl} alt="" className="w-10 h-14 object-cover rounded flex-shrink-0" />
                    )}
                    <div className="min-w-0">
                      <Link to={`/book/${book.id}`} className="font-medium text-sm truncate block hover:text-emerald-500 dark:hover:text-emerald-400 transition-colors">
                        {book.title}
                      </Link>
                      {book.author && (
                        <Link
                          to={`/author/${book.authorId}`}
                          className="text-[11px] text-slate-500 dark:text-zinc-500 hover:text-emerald-500 dark:hover:text-emerald-400 transition-colors truncate block"
                        >
                          {book.author.authorName}
                        </Link>
                      )}
                      <div className="flex items-center gap-2 mt-0.5 flex-wrap">
                        <select
                          value={book.mediaType || 'ebook'}
                          onChange={e => changeMediaType(book, e.target.value as 'ebook' | 'audiobook' | 'both')}
                          className="bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded text-[11px] px-1.5 py-0.5 focus:outline-none"
                          title="Change media type"
                        >
                          <option value="ebook">{t('books.mediaEbook')}</option>
                          <option value="audiobook">{t('books.mediaAudiobook')}</option>
                          <option value="both">{t('books.mediaBoth')}</option>
                        </select>
                        {book.mediaType === 'both' && (
                          <span className="text-[10px] text-slate-500 dark:text-zinc-500">
                            {book.ebookFilePath ? t('wanted.ebookDone') : t('wanted.ebookNeeded')}
                            {' · '}
                            {book.audiobookFilePath ? t('wanted.audiobookDone') : t('wanted.audiobookNeeded')}
                          </span>
                        )}
                      </div>
                      {book.releaseDate && (
                        <p className="text-xs text-slate-600 dark:text-zinc-500">{new Date(book.releaseDate).getFullYear()}</p>
                      )}
                    </div>
                  </div>
                  <div className="flex items-center gap-2 flex-shrink-0">
                    {book.excluded && (
                      <span className="px-2 py-0.5 rounded text-[10px] font-medium bg-amber-500/20 text-amber-700 dark:text-amber-400">
                        {t('wanted.excluded')}
                      </span>
                    )}
                    <button
                      onClick={() => unmonitor(book)}
                      disabled={unmonitoringId === book.id}
                      className="px-2 py-1.5 bg-slate-200 dark:bg-zinc-800 hover:bg-amber-100 dark:hover:bg-amber-900/30 hover:text-amber-700 dark:hover:text-amber-400 rounded text-xs font-medium disabled:opacity-50 transition-colors"
                      title={t('wanted.unmonitorHint')}
                    >
                      {unmonitoringId === book.id ? '…' : t('common.unmonitor')}
                    </button>
                    <button
                      onClick={() => searchBook(book)}
                      disabled={searchingId === book.id}
                      className="px-3 py-1.5 bg-slate-200 dark:bg-zinc-800 hover:bg-slate-300 dark:hover:bg-zinc-700 rounded text-xs font-medium disabled:opacity-50"
                    >
                      {searchingId === book.id ? t('wanted.searching') : t('common.search')}
                    </button>
                  </div>
                </div>

                {showResults === book.id && results.length === 0 && (
                  <div className="mt-1 mb-3 px-3 py-2 bg-slate-200/50 dark:bg-zinc-800/50 rounded text-xs text-slate-600 dark:text-zinc-500">
                    {t('wanted.noIndexerResults')}
                  </div>
                )}

                {showResults === book.id && results.length > 0 && (
                  <div className="mt-1 mb-3 space-y-1">
                    {results.slice(0, 10).map(r => (
                      <div key={r.guid} className="flex items-center justify-between p-2 bg-slate-200/50 dark:bg-zinc-800/50 rounded text-xs">
                        <div className="min-w-0 mr-3">
                          <span className="truncate block">{r.title}</span>
                          <span className="text-slate-600 dark:text-zinc-500 truncate block">
                            {r.indexerName} &middot; {formatSize(r.size)} &middot; {r.grabs} grabs
                            {r.language && (
                              <span className="ml-1.5 inline-block px-1 py-0 rounded bg-slate-300 dark:bg-zinc-700 text-[9px] font-medium uppercase tracking-wide">{r.language}</span>
                            )}
                          </span>
                        </div>
                        <button
                          onClick={() => grab(r, book)}
                          disabled={grabbingGuid === r.guid || grabbedGuid === r.guid}
                          className={`px-2 py-2 rounded text-[10px] font-medium flex-shrink-0 touch-manipulation transition-colors disabled:cursor-default ${
                            grabbedGuid === r.guid
                              ? 'bg-emerald-700 text-emerald-200'
                              : grabbingGuid === r.guid
                              ? 'bg-emerald-600/60 text-white/70'
                              : 'bg-emerald-600 hover:bg-emerald-500 text-white'
                          }`}
                        >
                          {grabbedGuid === r.guid ? t('wanted.grabbed') : grabbingGuid === r.guid ? t('wanted.grabbing') : t('wanted.grab')}
                        </button>
                      </div>
                    ))}
                  </div>
                )}
              </div>
            ))}
          </div>
        </>
      )}
      <Pagination {...paginationProps} />

      <BulkActionBar
        count={selectedIds.size}
        onClear={clearSelection}
        busy={bulkBusy}
        actions={[
          { label: t('common.search'), onClick: () => runBulk('search') },
          { label: t('common.unmonitor'), onClick: () => runBulk('unmonitor') },
          { label: t('common.blocklist'), onClick: () => runBulk('blocklist'), variant: 'danger' },
        ]}
      />
    </div>
  )
}
