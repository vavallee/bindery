import { useEffect, useRef, useState, useMemo, useCallback } from 'react'
import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { api, Book, SearchResult } from '../api/client'
import BulkActionBar from '../components/BulkActionBar'
import ImportHints from '../components/ImportHints'
import Pagination from '../components/Pagination'
import { usePagination } from '../components/usePagination'

// Shared grid template so the header row and every list row line up exactly.
// columns: checkbox · cover · title+author · format · actions
const ROW_GRID = 'grid grid-cols-[1.5rem_2rem_1fr_6rem_8.5rem] items-center gap-3'

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
  const [toast, setToast] = useState<string | null>(null)
  const selectAllRef = useRef<HTMLInputElement>(null)

  const load = () => {
    api.listWanted({ includeExcluded: showExcluded }).then(setBooks).catch(console.error).finally(() => setLoading(false))
  }

  useEffect(() => {
    load()
  }, [showExcluded])

  useEffect(() => {
    if (!toast) return
    const timer = setTimeout(() => setToast(null), 2500)
    return () => clearTimeout(timer)
  }, [toast])

  const showToast = useCallback((msg: string) => { setToast(msg) }, [])

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
    const prev = books
    setBooks(books.filter(b => b.id !== book.id))
    try {
      await api.updateBook(book.id, { monitored: false })
    } catch {
      setBooks(prev)
      showToast("Couldn't update wanted list — reverted.")
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

  const checkboxCls = 'rounded border-slate-400 dark:border-zinc-600 text-emerald-600 focus:ring-emerald-500 focus:ring-offset-0'

  return (
    <div className={selectedIds.size > 0 ? 'pb-16' : ''}>
      {toast && (
        <div className="fixed bottom-6 right-6 z-50 px-4 py-2.5 bg-red-600 text-white rounded-lg shadow-lg text-sm font-medium animate-fade-in">
          {toast}
        </div>
      )}

      {/* Page header: title · count · show-excluded */}
      <div className="flex items-center gap-3 mb-3">
        <h2 className="text-xl font-semibold text-slate-800 dark:text-zinc-200">{t('wanted.title')}</h2>
        <span className="text-xs text-slate-500 dark:text-zinc-500">
          {t('wanted.countLabel', { filtered: filtered.length, total: books.length })}
        </span>
        <label className="ml-auto flex items-center gap-1.5 text-xs text-slate-600 dark:text-zinc-400 cursor-pointer select-none">
          <input
            type="checkbox"
            checked={showExcluded}
            onChange={e => setShowExcluded(e.target.checked)}
            className={checkboxCls}
          />
          {t('wanted.showExcluded')}
        </label>
      </div>

      <input
        type="search"
        value={search}
        onChange={e => setSearch(e.target.value)}
        placeholder={t('wanted.searchPlaceholder')}
        className="w-full mb-3 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-1.5 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600 placeholder-slate-400 dark:placeholder-zinc-600"
      />

      {loading ? (
        <div className="text-slate-600 dark:text-zinc-500">{t('common.loading')}</div>
      ) : books.length === 0 ? (
        <div className="text-center py-16 text-slate-600 dark:text-zinc-500">
          <p>{t('wanted.empty')}</p>
          <ImportHints />
        </div>
      ) : filtered.length === 0 ? (
        <div className="text-center py-16 text-slate-600 dark:text-zinc-500">
          <p>{t('wanted.noMatch')}</p>
        </div>
      ) : (
        <div className="border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900 overflow-hidden">
          {/* Column-header row with select-all checkbox */}
          <div className={`${ROW_GRID} px-3 py-2 border-b border-slate-200 dark:border-zinc-800 bg-slate-200/60 dark:bg-zinc-800/60 text-[11px] font-medium uppercase tracking-wide text-slate-500 dark:text-zinc-500`}>
            <input
              ref={selectAllRef}
              type="checkbox"
              checked={allPageSelected}
              onChange={e => e.target.checked ? selectAllOnPage() : clearSelection()}
              className={checkboxCls}
              aria-label={t('common.selectAllPage')}
              title={t('common.selectAllPage')}
            />
            <span aria-hidden />
            <span>{t('wanted.colTitleAuthor')}</span>
            <span>{t('wanted.colFormat')}</span>
            <span className="text-right">{t('wanted.colActions')}</span>
          </div>

          {pageItems.map((book, i) => {
            const isSelected = selectedIds.has(book.id)
            const year = book.releaseDate ? new Date(book.releaseDate).getFullYear() : null
            const authorName = book.author?.authorName
            return (
              <div key={book.id} className={i < pageItems.length - 1 ? 'border-b border-slate-200 dark:border-zinc-800' : ''}>
                <div className={`${ROW_GRID} px-3 py-1.5 transition-colors ${
                  isSelected
                    ? 'bg-emerald-500/10 dark:bg-emerald-500/10'
                    : 'hover:bg-slate-200/50 dark:hover:bg-zinc-800/50'
                }`}>
                  {/* selection */}
                  <input
                    type="checkbox"
                    checked={isSelected}
                    onChange={() => toggleSelect(book.id)}
                    className={checkboxCls}
                    aria-label={t('wanted.selectBook', { title: book.title })}
                    title={t('wanted.selectBook', { title: book.title })}
                  />

                  {/* uniform cover slot */}
                  {book.imageUrl ? (
                    <img
                      src={book.imageUrl}
                      alt=""
                      className="w-8 h-11 object-cover rounded bg-slate-200 dark:bg-zinc-800"
                    />
                  ) : (
                    <div
                      className="w-8 h-11 rounded bg-slate-200 dark:bg-zinc-800 grid place-items-center text-[8px] text-slate-400 dark:text-zinc-600 text-center leading-tight"
                      aria-hidden
                    >
                      {t('wanted.noCover')}
                    </div>
                  )}

                  {/* title + author · year */}
                  <div className="min-w-0">
                    <Link
                      to={`/book/${book.id}`}
                      className="block truncate text-sm font-medium text-slate-800 dark:text-zinc-200 hover:text-emerald-600 dark:hover:text-emerald-400 transition-colors"
                    >
                      {book.title}
                    </Link>
                    <div className="truncate text-xs text-slate-500 dark:text-zinc-500">
                      {authorName ? (
                        <Link
                          to={`/author/${book.authorId}`}
                          className="hover:text-emerald-600 dark:hover:text-emerald-400 transition-colors"
                        >
                          {authorName}
                        </Link>
                      ) : (
                        <span>{t('wanted.authorUnknown')}</span>
                      )}
                      {year != null && <span> · {year}</span>}
                    </div>
                  </div>

                  {/* format control — compact select, still changes the value */}
                  <div className="min-w-0">
                    <select
                      value={book.mediaType || 'ebook'}
                      onChange={e => changeMediaType(book, e.target.value as 'ebook' | 'audiobook' | 'both')}
                      className="w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded text-[11px] px-1.5 py-0.5 focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600"
                      aria-label={t('wanted.changeFormat', { title: book.title })}
                      title={t('wanted.changeFormat', { title: book.title })}
                    >
                      <option value="ebook">{t('books.mediaEbook')}</option>
                      <option value="audiobook">{t('books.mediaAudiobook')}</option>
                      <option value="both">{t('books.mediaBoth')}</option>
                    </select>
                    {book.mediaType === 'both' && (
                      <div className="mt-0.5 text-[10px] text-slate-500 dark:text-zinc-500 truncate">
                        {book.ebookFilePath ? t('wanted.ebookDone') : t('wanted.ebookNeeded')}
                        {' · '}
                        {book.audiobookFilePath ? t('wanted.audiobookDone') : t('wanted.audiobookNeeded')}
                      </div>
                    )}
                  </div>

                  {/* actions */}
                  <div className="flex items-center justify-end gap-1.5">
                    {book.excluded && (
                      <span className="px-1.5 py-0.5 rounded text-[10px] font-medium bg-amber-500/20 text-amber-700 dark:text-amber-400">
                        {t('wanted.excluded')}
                      </span>
                    )}
                    <button
                      type="button"
                      onClick={() => unmonitor(book)}
                      disabled={unmonitoringId === book.id}
                      className="px-2 py-1 rounded text-xs font-medium bg-slate-200 dark:bg-zinc-800 hover:bg-amber-100 dark:hover:bg-amber-900/30 hover:text-amber-700 dark:hover:text-amber-400 text-slate-700 dark:text-zinc-300 disabled:opacity-50 transition-colors"
                      title={t('wanted.unmonitorHint')}
                    >
                      {unmonitoringId === book.id ? '…' : t('common.unmonitor')}
                    </button>
                    <button
                      type="button"
                      onClick={() => searchBook(book)}
                      disabled={searchingId === book.id}
                      className="px-2 py-1 rounded text-xs font-medium bg-emerald-600 hover:bg-emerald-500 text-white disabled:opacity-50 transition-colors"
                    >
                      {searchingId === book.id ? t('wanted.searching') : t('common.search')}
                    </button>
                  </div>
                </div>

                {showResults === book.id && results.length === 0 && (
                  <div className="px-3 pb-2 text-xs text-slate-600 dark:text-zinc-500">
                    {t('wanted.noIndexerResults')}
                  </div>
                )}

                {showResults === book.id && results.length > 0 && (
                  <div className="px-3 pb-2 space-y-1">
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
                          type="button"
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
            )
          })}
        </div>
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
