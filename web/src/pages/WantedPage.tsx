import { useEffect, useRef, useState, useCallback } from 'react'
import { Link } from 'react-router'
import { useTranslation } from 'react-i18next'
import { api, Book, SearchResult } from '../api/client'
import { useConfirmDialog } from '../components/useConfirmDialog'
import { grabWithForceConfirm } from '../util/forceGrab'
import BulkActionBar from '../components/BulkActionBar'
import ImportHints from '../components/ImportHints'
import Pagination from '../components/Pagination'
import { useServerPagination } from '../components/usePagination'
import { useListParams, useUrlSearchInput } from '../components/useListParams'
import { usePolling } from '../components/usePolling'
import { safeHref } from '../util/safeHref'
import { formatBytes } from '../util/format'
import { isAutoGrabRefusal } from '../util/autoGrabRefusal'
import CoverImage from '../components/CoverImage'

// Shared grid template so the header row and every list row line up exactly.
// columns: checkbox · cover · title+author · format · actions
// Below sm the fixed format and action columns left the title about 13px at
// 375px, so a phone gets three columns and the format and actions move to a
// second line under the title (see ROW_CONTROLS). The format column fits the
// longest format name in every locale ("📖 Libro electrónico") plus the room
// index.css keeps for the select's arrow (#3148). Selects keep their own size
// on touch screens too (#3081), so one width serves both.
const ROW_GRID = 'grid grid-cols-[1.5rem_2rem_minmax(0,1fr)] sm:grid-cols-[1.5rem_2rem_1fr_9rem_8.5rem] items-center gap-x-3 gap-y-1.5 sm:gap-3'
// Holds the format select and the actions. A flex line spanning the cover and
// title columns on a phone; display: contents from sm, so its two children
// drop back into their own grid columns.
const ROW_CONTROLS = 'col-start-2 col-span-2 flex flex-wrap items-center gap-2 sm:contents'

// Query-string keys and their defaults; defaults stay out of the URL.
const LIST_DEFAULTS = { q: '', excluded: '' }

export default function WantedPage() {
  const { t } = useTranslation()
  const { confirm, confirmDialog } = useConfirmDialog()

  // The current page of wanted books, and how many match the search in all.
  const [books, setBooks] = useState<Book[]>([])
  const [total, setTotal] = useState(0)
  const [loading, setLoading] = useState(true)
  const [searchingId, setSearchingId] = useState<number | null>(null)
  const [results, setResults] = useState<SearchResult[]>([])
  const [showResults, setShowResults] = useState<number | null>(null)
  // Page, search and the excluded toggle live in the URL so going back from a
  // book lands on the same page of the same list (#3052).
  const list = useListParams(LIST_DEFAULTS)
  const updateList = list.update
  // The box follows every keystroke; the URL, and with it the server query,
  // catches up once typing pauses, replacing the entry rather than pushing one
  // per keystroke.
  const commitSearch = useCallback((q: string) => updateList({ q, page: null }, { replace: true }), [updateList])
  const [search, setSearch] = useUrlSearchInput(list.values.q, commitSearch)
  const [grabbingGuid, setGrabbingGuid] = useState<string | null>(null)
  const [grabbedGuid, setGrabbedGuid] = useState<string | null>(null)
  const [unmonitoringId, setUnmonitoringId] = useState<number | null>(null)
  const [selectedIds, setSelectedIds] = useState<Set<number>>(new Set())
  const [bulkBusy, setBulkBusy] = useState(false)
  const showExcluded = list.values.excluded === '1'
  const setShowExcluded = (next: boolean) => updateList({ excluded: next ? '1' : null })
  const [toast, setToast] = useState<string | null>(null)
  const selectAllRef = useRef<HTMLInputElement>(null)

  // Set-state-after-unmount guard for the fetches and the poll, which outlive
  // any one effect.
  const mounted = useRef(true)
  useEffect(() => {
    mounted.current = true
    return () => { mounted.current = false }
  }, [])

  // The query a total belongs to, minus the page. The snap back to the last
  // page may only use a total fetched for the query on screen (see BooksPage).
  const searchQuery = list.values.q
  const queryKey = JSON.stringify([searchQuery, showExcluded])
  const [totalKey, setTotalKey] = useState<string | null>(null)
  const { page, pageSize, paginationProps } = useServerPagination(total, 50, 'wanted', {
    page: list.page,
    setPage: list.setPage,
    ready: totalKey === queryKey,
  })

  // The server pages and searches (limit, offset, search), so a library with
  // thousands of wanted books is not serialised on every 5 second poll. Each
  // request takes a number and only the newest may set state: a response that
  // lands after a page change, a newer poll or an unmount is dropped.
  const loadRequestRef = useRef(0)
  const fetchPage = useCallback(() => {
    const request = ++loadRequestRef.current
    return api.listWantedPage({
      limit: pageSize,
      offset: (page - 1) * pageSize,
      search: searchQuery || undefined,
      includeExcluded: showExcluded,
    }).then(({ items, total }) => {
      if (request !== loadRequestRef.current || !mounted.current) return
      setBooks(items)
      setTotal(total)
      setTotalKey(queryKey)
    })
  }, [page, pageSize, searchQuery, showExcluded, queryKey])

  const load = () => {
    fetchPage().catch(console.error).finally(() => { if (mounted.current) setLoading(false) })
  }

  useEffect(() => {
    fetchPage()
      .catch(console.error)
      .finally(() => { if (mounted.current) setLoading(false) })
  }, [fetchPage])

  // Poll the wanted list so background auto-grabs (and books leaving as they
  // import) show up without a manual reload (#1161). Mirrors QueuePage's 5s
  // poll. Skips a tick while the user is mid-interaction, a results panel open
  // or a grab/search running, so the list doesn't reshuffle under them.
  //
  // That interaction state used to sit in the effect's deps, which tore the
  // timer down and restarted the 5s window on every hover; usePolling holds the
  // callback in a ref, so the timer survives and only the tick is skipped
  // (#2360). It also pauses entirely while the tab is hidden.
  usePolling(() => {
    if (showResults !== null || grabbingGuid !== null || searchingId !== null) return
    fetchPage().catch(() => {})
  }, 5000)

  useEffect(() => {
    if (!toast) return
    const timer = setTimeout(() => setToast(null), 2500)
    return () => clearTimeout(timer)
  }, [toast])

  const showToast = useCallback((msg: string) => { setToast(msg) }, [])

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
    const prevTotal = total
    setBooks(books.filter(b => b.id !== book.id))
    setTotal(Math.max(0, total - 1))
    try {
      await api.updateBook(book.id, { monitored: false })
    } catch {
      setBooks(prev)
      setTotal(prevTotal)
      showToast("Couldn't update wanted list — reverted.")
    } finally {
      setUnmonitoringId(null)
    }
  }

  const grab = async (result: SearchResult, book: Book) => {
    setGrabbingGuid(result.guid)
    try {
      const dl = await grabWithForceConfirm({
        guid: result.guid,
        title: result.title,
        nzbUrl: result.nzbUrl,
        size: result.size,
        bookId: book.id,
        indexerId: result.indexerId,
        protocol: result.protocol,
        // The result's own media type wins over the book's 'both' (#2933).
        mediaType: result.mediaType || book.mediaType,
      }, confirm, t)
      if (!dl) return
      setGrabbedGuid(result.guid)
      setTimeout(() => {
        setShowResults(null)
        setGrabbedGuid(null)
        load()
      }, 1200)
    } catch (err: unknown) {
      alert(err instanceof Error ? err.message : 'Grab failed')
    } finally {
      setGrabbingGuid(null)
    }
  }

  // This page's loaded ids, in order, handed to BookDetailPage as router
  // state (#2548) for Previous/Next; see BookNavState there. Scoped to this
  // page, the slice the server returned, which matches the "only as far as
  // what's currently loaded" rule used by BooksPage/AuthorDetailPage.
  const pageItems = books
  const pageItemIds = pageItems.map(b => b.id)

  // A selection only means something on the page it was made on.
  useEffect(() => { setSelectedIds(new Set()) }, [list.page, list.values.q, showExcluded])

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
      const res = await api.bulkActionWanted([...selectedIds], action)
      // Nothing was searched because automatic grabbing is off. Say so and
      // keep the selection, so pressing Search again after flipping the
      // setting does not mean re-picking every book (#2669).
      if (isAutoGrabRefusal(res)) {
        showToast(t('search.autoGrabDisabled'))
        return
      }
      clearSelection()
      load()
    } catch (err) {
      alert(err instanceof Error ? err.message : 'Bulk action failed')
    } finally {
      setBulkBusy(false)
    }
  }

  const checkboxCls = 'rounded border-slate-400 dark:border-zinc-600'

  return (
    <div className={selectedIds.size > 0 ? 'pb-16' : ''}>
      {confirmDialog}
      {toast && (
        <div className="fixed bottom-safe-6 right-safe-6 z-50 px-4 py-2.5 bg-red-600 text-white rounded-lg shadow-lg text-sm font-medium animate-fade-in">
          {toast}
        </div>
      )}

      {/* Page header: title · count · show-excluded */}
      <div className="flex items-center gap-3 mb-3">
        <h2 className="text-xl font-semibold text-slate-800 dark:text-zinc-200">{t('wanted.title')}</h2>
        <span className="text-xs text-slate-500 dark:text-zinc-500">
          {t('books.countLabel', { count: total, defaultValue: '{{count}} books' })}
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
        enterKeyHint="search"
        type="search"
        value={search}
        onChange={e => setSearch(e.target.value)}
        placeholder={t('wanted.searchPlaceholder')}
        className="w-full mb-3 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-1.5 text-sm placeholder-slate-400 dark:placeholder-zinc-600"
      />

      {loading ? (
        <div className="text-slate-600 dark:text-zinc-500">{t('common.loading')}</div>
      ) : total === 0 && !searchQuery ? (
        <div className="text-center py-16 text-slate-600 dark:text-zinc-500">
          <p>{t('wanted.empty')}</p>
          <ImportHints />
        </div>
      ) : total === 0 ? (
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
            <span className="hidden sm:block">{t('wanted.colFormat')}</span>
            <span className="hidden sm:block text-right">{t('wanted.colActions')}</span>
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
                  <CoverImage loading="lazy" decoding="async" src={book.imageUrl} alt="" className="w-8 h-11 object-cover rounded bg-slate-200 dark:bg-zinc-800"
                    fallback={
                      <div
                        className="w-8 h-11 rounded bg-slate-200 dark:bg-zinc-800 grid place-items-center text-[8px] text-slate-400 dark:text-zinc-600 text-center leading-tight"
                        aria-hidden
                      >
                        {t('wanted.noCover')}
                      </div>
                    }
                  />

                  {/* title + author · year */}
                  <div className="min-w-0">
                    <Link
                      to={`/book/${book.id}`}
                      // hopDepth: 1 — first hop into a book detail page from
                      // this list, not a further Previous/Next chain hop.
                      state={{ ids: pageItemIds, index: i, hopDepth: 1 }}
                      className="block [overflow-wrap:anywhere] sm:truncate text-sm font-medium text-slate-800 dark:text-zinc-200 hover:text-emerald-600 dark:hover:text-emerald-400 transition-colors"
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
                    {book.authorUnmonitored && (
                      <div className="truncate text-[10px] text-amber-700 dark:text-amber-400">
                        {t('wanted.authorNotMonitored', 'Author not monitored, so this is not searched automatically')}
                      </div>
                    )}
                  </div>

                  <div className={ROW_CONTROLS} data-testid="wanted-row-controls">
                    {/* format control: compact select, still changes the value */}
                    <div className="min-w-28 flex-1 sm:min-w-0 sm:flex-none">
                      <select
                        value={book.mediaType || 'ebook'}
                        onChange={e => changeMediaType(book, e.target.value as 'ebook' | 'audiobook' | 'both')}
                        className="w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded text-[11px] px-1.5 py-0.5"
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
                            {r.indexerName} &middot; {formatBytes(r.size)} &middot; {r.grabs} grabs
                            {safeHref(r.infoUrl) && (
                              <>
                                {' '}&middot;{' '}
                                <a
                                  href={safeHref(r.infoUrl)}
                                  target="_blank"
                                  rel="noopener noreferrer"
                                  className="text-sky-600 dark:text-sky-400 hover:underline"
                                >
                                  ↗ indexer
                                </a>
                              </>
                            )}
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
