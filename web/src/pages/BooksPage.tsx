import { useCallback, useEffect, useRef, useState } from 'react'
import { Link, useNavigate } from 'react-router'
import { useTranslation } from 'react-i18next'
import { useConfirmDialog } from '../components/useConfirmDialog'
import ViewToggle from '../components/ViewToggle'
import BulkNotice from '../components/BulkNotice'
import { isAutoGrabRefusal } from '../util/autoGrabRefusal'
import { bookStatusBadge } from '../components/bookStatus'
import { FilterGroup } from '../components/FilterPopover'
import FilterBar from '../components/FilterBar'
import DataTable, { type Column } from '../components/DataTable'
import Checkbox from '../components/Checkbox'
import { nextSort, splitSort } from '../components/tableSort'
import { SkeletonCards, SkeletonRows } from '../components/Skeleton'
import EmptyState from '../components/EmptyState'
import BookStatusLegend from '../components/BookStatusLegend'
import { useView } from '../components/useView'
import GettingStartedGuidance from '../components/GettingStartedGuidance'
import { useNeedsSetup } from '../components/useNeedsSetup'
import { api, BINDERY_BASE, Book, MediaType } from '../api/client'
import BulkActionBar from '../components/BulkActionBar'
import Pagination from '../components/Pagination'
import { useServerPagination } from '../components/usePagination'
import { oneOf, useListParams, useUrlSearchInput } from '../components/useListParams'
import AddToLibraryModal from '../components/AddToLibraryModal'
import BookRating from '../components/BookRating'
import CoverImage from '../components/CoverImage'
import { btn } from '../components/buttons'
import { errorText } from '../util/errorText'
import CoverPlaceholder from '../components/CoverPlaceholder'

type SortMode =
  | 'title-az' | 'title-za'
  | 'date-new' | 'date-old'
  | 'author-az' | 'author-za'
  | 'type-az' | 'type-za'
  | 'status-az' | 'status-za'
  | 'rating-asc' | 'rating-desc'
type MonitoredFilter = '' | 'monitored' | 'unmonitored'
type StatusFilter = '' | 'wanted' | 'imported' | 'skipped'
type MediaFilter = '' | 'ebook' | 'audiobook' | 'both'

const SORT_MODES: readonly SortMode[] = [
  'title-az', 'title-za', 'date-new', 'date-old', 'author-az', 'author-za',
  'type-az', 'type-za', 'status-az', 'status-za', 'rating-asc', 'rating-desc',
]
const STATUS_FILTERS: readonly StatusFilter[] = ['', 'wanted', 'imported', 'skipped']
const MEDIA_FILTERS: readonly MediaFilter[] = ['', 'ebook', 'audiobook', 'both']
// Query-string keys and their defaults. A value equal to its default is left
// out of the URL, so an untouched list stays at a clean /books.
const LIST_DEFAULTS = { q: '', status: '', media: '', sort: 'title-az' }
// The table's sortable columns and the whitelisted sort values behind them.
const BOOK_COLUMN_SORTS = {
  title: { asc: 'title-az', desc: 'title-za' },
  author: { asc: 'author-az', desc: 'author-za' },
  year: { asc: 'date-old', desc: 'date-new' },
  rating: { asc: 'rating-asc', desc: 'rating-desc' },
  type: { asc: 'type-az', desc: 'type-za' },
  status: { asc: 'status-az', desc: 'status-za' },
}


// statusLabel is populated at render time from t() — see BooksPage
const statusLabelKeys: Record<string, string> = {
  wanted: 'books.statusWanted',
  imported: 'books.statusImported',
  skipped: 'books.statusSkipped',
}

export default function BooksPage() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const { confirm, confirmDialog } = useConfirmDialog()
  const [books, setBooks] = useState<Book[]>([])
  const [total, setTotal] = useState(0)
  const [loading, setLoading] = useState(true)
  // Page, search, status, media type and sort live in the URL so going back
  // from a book lands on the same page of the same list (#3052). The monitored
  // filter, view and page size stay in localStorage: they are preferences that
  // already survive navigation.
  const list = useListParams(LIST_DEFAULTS)
  const statusFilter = oneOf(list.values.status, STATUS_FILTERS, '')
  const mediaFilter = oneOf(list.values.media, MEDIA_FILTERS, '')
  const sort = oneOf(list.values.sort, SORT_MODES, 'title-az')
  const debouncedSearch = list.values.q
  const updateList = list.update
  const setStatusFilter = (status: StatusFilter) => updateList({ status, page: null })
  const setMediaFilter = (media: MediaFilter) => updateList({ media, page: null })
  const setSort = (next: SortMode) => updateList({ sort: next, page: null })
  // Keystroke-level changes replace the history entry instead of pushing one.
  const commitSearch = useCallback((q: string) => updateList({ q, page: null }, { replace: true }), [updateList])
  const [search, setSearch] = useUrlSearchInput(debouncedSearch, commitSearch)
  const [monitoredFilter, setMonitoredFilterState] = useState<MonitoredFilter>(() => {
    try {
      const v = localStorage.getItem('bindery.filter.books.monitored')
      if (v === 'monitored' || v === 'unmonitored') return v
    } catch { /* ignore */ }
    return ''
  })
  const setMonitoredFilter = (next: MonitoredFilter) => {
    setMonitoredFilterState(next)
    updateList({ page: null })
  }
  const [view, setView] = useView('books', 'grid')
  const { needsIndexer, needsClient, needsAny } = useNeedsSetup()
  const [selectedIds, setSelectedIds] = useState<Set<number>>(new Set())
  const [bulkBusy, setBulkBusy] = useState(false)
  // A message from the last bulk action that is not an exception, e.g. the
  // server refusing a search because automatic grabbing is off (#2669).
  const [bulkNotice, setBulkNotice] = useState<string | null>(null)
  const [showAddBook, setShowAddBook] = useState(false)
  const addBookButtonRef = useRef<HTMLButtonElement>(null)
  const addBookWasOpenRef = useRef(false)
  const loadRequestRef = useRef(0)

  const monitoredParam = monitoredFilter === 'monitored' ? true : monitoredFilter === 'unmonitored' ? false : undefined

  // Shown on the Filters trigger so an applied filter is visible without
  // opening it. Status is not counted: it has its own row and is never hidden.
  const activeFilterCount = [mediaFilter, monitoredFilter].filter(Boolean).length
  // The query a total belongs to, minus the page. The snap back to the last
  // page may only use a total fetched for the query on screen: going back
  // across a filter change would otherwise clamp with the other query's total
  // before the refetch starts, and a failed fetch (total 0) would reset it.
  const queryKey = JSON.stringify([debouncedSearch, statusFilter, mediaFilter, monitoredFilter, sort])
  const [totalKey, setTotalKey] = useState<string | null>(null)
  const { page, pageSize, paginationProps } = useServerPagination(total, 50, 'books', {
    page: list.page,
    setPage: list.setPage,
    ready: !loading && totalKey === queryKey,
  })

  // Server-side list: page, page size, search, status, media type, monitored,
  // and sort are all applied by the API so a library with >100 books is fully
  // reachable (issue #1010). load() refetches the current page after mutations.
  const load = useCallback(() => {
    const request = ++loadRequestRef.current
    setLoading(true)
    api.listBooks({
      limit: pageSize,
      offset: (page - 1) * pageSize,
      search: debouncedSearch || undefined,
      status: statusFilter || undefined,
      mediaType: mediaFilter || undefined,
      monitored: monitoredParam,
      sort,
    }).then(({ items, total }) => {
      if (request !== loadRequestRef.current) return
      setBooks(items)
      setTotal(total)
      setTotalKey(queryKey)
    })
      .catch(err => {
        if (request === loadRequestRef.current) setTotalKey(null)
        console.error(err)
      })
      .finally(() => {
        if (request === loadRequestRef.current) setLoading(false)
      })
  }, [page, pageSize, debouncedSearch, statusFilter, mediaFilter, monitoredParam, sort, queryKey])

  // A selection only means something on the page it was made on.
  useEffect(() => { setSelectedIds(new Set()) }, [page, queryKey])

  useEffect(() => { load() }, [load])

  // The search box is debounced into the URL (useUrlSearchInput) so typing
  // does not fire a request per keystroke. Every query change drops the page
  // param in the same update, which is the jump back to page 1; doing it in
  // an effect instead would also fire on mount and lose the page from the URL.

  // Persist the monitored filter so it survives a reload, mirroring AuthorsPage.
  useEffect(() => {
    try { localStorage.setItem('bindery.filter.books.monitored', monitoredFilter) } catch { /* ignore */ }
  }, [monitoredFilter])

  useEffect(() => {
    document.title = `${t('nav.books', 'Books')} · Bindery`
    return () => { document.title = 'Bindery' }
  }, [t])

  useEffect(() => {
    if (addBookWasOpenRef.current && !showAddBook) addBookButtonRef.current?.focus()
    addBookWasOpenRef.current = showAddBook
  }, [showAddBook])

  const toggleSelect = (id: number) => {
    setSelectedIds(prev => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  const selectAllOnPage = () => setSelectedIds(new Set(books.map(b => b.id)))
  const clearSelection = () => setSelectedIds(new Set())

  const runBulk = async (action: Parameters<typeof api.bulkActionBooks>[1], mediaType?: MediaType) => {
    if (selectedIds.size === 0) return
    if (action === 'delete' && !await confirm({
      title: t('common.confirmTitle'),
      body: t('books.deleteConfirm', { count: selectedIds.size }),
      confirmLabel: t('common.delete'),
    })) return
    setBulkBusy(true)
    setBulkNotice(null)
    try {
      const res = await api.bulkActionBooks([...selectedIds], action, mediaType)
      // The server refused rather than queued: say so and keep the selection,
      // so retrying after flipping the switch does not mean re-picking every
      // book (#2669). Reloading here would also be a lie, since nothing
      // changed.
      if (isAutoGrabRefusal(res)) {
        setBulkNotice(t('search.autoGrabDisabled'))
        return
      }
      clearSelection()
      load()
    } catch (err) {
      alert(errorText(err, t('common.bulkActionFailed', 'Bulk action failed')))
    } finally {
      setBulkBusy(false)
    }
  }

  // This page's loaded ids, in order — handed to BookDetailPage as router
  // state (#2548) for Previous/Next; see BookNavState there.
  const bookIds = books.map(b => b.id)
  // hopDepth: 1 — this is the first hop into a book detail page from a list,
  // not a further Previous/Next chain hop; see BookNavState in
  // BookDetailPage.tsx for how Back uses it to skip the whole chain.
  const bookNavState = (index: number) => ({ ids: bookIds, index, hopDepth: 1 })

  const columnSort = splitSort(sort, BOOK_COLUMN_SORTS)
  const sortByColumn = (key: string) => setSort(nextSort(sort, key, BOOK_COLUMN_SORTS) as SortMode)

  const bookColumns: Column<Book>[] = [
    {
      key: 'title',
      header: t('books.colTitle'),
      sortKey: 'title',
      className: 'max-w-[18rem] sm:max-w-none',
      cell: (book, i) => (
        <Link to={`/book/${book.id}`} state={bookNavState(i)} className="flex items-center gap-2 min-w-0" onClick={e => e.stopPropagation()}>
          <CoverImage loading="lazy" decoding="async" src={book.imageUrl} alt="" className="w-6 h-9 object-cover rounded flex-shrink-0"
            fallback={<CoverPlaceholder id={book.id} title={book.title} size="xs" className="w-6 h-9 rounded flex-shrink-0" />}
          />
          <span className="text-slate-800 dark:text-zinc-200 truncate">{book.title}</span>
        </Link>
      ),
    },
    {
      key: 'author',
      header: t('books.colAuthor'),
      sortKey: 'author',
      className: 'whitespace-nowrap hidden md:table-cell',
      cell: book => book.author ? (
        <Link
          to={`/author/${book.authorId}`}
          className="text-fg-muted hover:text-emerald-500 dark:hover:text-emerald-400"
          onClick={e => e.stopPropagation()}
        >
          {book.author.authorName}
        </Link>
      ) : '—',
    },
    {
      key: 'year',
      header: t('books.colYear'),
      sortKey: 'year',
      className: 'text-fg-muted whitespace-nowrap hidden sm:table-cell',
      cell: book => (book.releaseDate ? new Date(book.releaseDate).getFullYear() : '—'),
    },
    {
      key: 'rating',
      header: t('books.colRating'),
      sortKey: 'rating',
      className: 'text-fg-muted whitespace-nowrap hidden lg:table-cell',
      cell: book => (book.averageRating && book.averageRating > 0 ? <BookRating book={book} /> : '—'),
    },
    {
      key: 'type',
      header: t('books.colType'),
      sortKey: 'type',
      className: 'text-xs whitespace-nowrap',
      cell: book => book.mediaType === 'both'
        ? `📖🎧 ${t('common.ebook')} + ${t('common.audiobook')}`
        : book.mediaType === 'audiobook'
          ? `🎧 ${t('common.audiobook')}`
          : `📖 ${t('common.ebook')}`,
    },
    {
      key: 'status',
      header: t('books.colStatus'),
      sortKey: 'status',
      className: 'whitespace-nowrap',
      cell: book => {
        const badge = bookStatusBadge(book.status, book.monitored, t)
        return (
          <span title={badge.description} className={`inline-block px-2 py-0.5 rounded text-[10px] font-medium ${badge.colorClass}`}>
            {badge.label}
          </span>
        )
      },
    },
  ]


  return (
    <div className={selectedIds.size > 0 ? 'pb-16' : ''}>
      {confirmDialog}
      <BulkNotice message={bulkNotice} onDismiss={() => setBulkNotice(null)} />
      <div className="flex flex-wrap items-center justify-between gap-3 mb-4">
        <h2 className="text-2xl font-bold">{t('books.title')}</h2>
        <div className="ml-auto flex items-center gap-3 flex-wrap justify-end">
          <ViewToggle view={view} onChange={setView} />
          <Link
            to="/books/duplicates"
            title={t('books.reviewDuplicatesHint', 'Find titles across the library that look like the same book; nothing changes until you exclude a row')}
            className={`${btn.secondary} px-3 py-2 text-sm`}
          >
            {t('books.reviewDuplicates', 'Review duplicates')}
          </Link>
          <button
            ref={addBookButtonRef}
            type="button"
            onClick={() => setShowAddBook(true)}
            className={`${btn.primary} px-4 py-2 text-sm`}
          >
            {t('addToLibrary.addBook')}
          </button>
        </div>
      </div>

      <FilterBar
        search={{ value: search, onChange: setSearch, placeholder: t('books.searchPlaceholder') }}
        status={{
          value: statusFilter,
          onChange: setStatusFilter,
          label: t('books.colStatus'),
          options: STATUS_FILTERS.map(s => ({ value: s, label: s ? t(statusLabelKeys[s]) : t('common.all') })),
          help: <BookStatusLegend />,
        }}
        sort={{
          value: sort,
          onChange: setSort,
          label: t('books.sortLabel'),
          options: [
            { value: 'title-az' as const, label: t('books.sortTitleAZ') },
            { value: 'title-za' as const, label: t('books.sortTitleZA') },
            { value: 'date-new' as const, label: t('books.sortNewest') },
            { value: 'date-old' as const, label: t('books.sortOldest') },
            { value: 'author-az' as const, label: t('books.sortAuthorAZ') },
            { value: 'author-za' as const, label: t('books.sortAuthorZA') },
            // Every sort a column header can set is here too, so after a
            // header click (or a link carrying one) the popover still shows
            // which sort is on, in grid view as well.
            { value: 'rating-desc' as const, label: t('books.sortRatingHigh', 'Highest rated') },
            { value: 'rating-asc' as const, label: t('books.sortRatingLow', 'Lowest rated') },
            { value: 'type-az' as const, label: t('books.sortTypeAZ', 'Type A–Z') },
            { value: 'type-za' as const, label: t('books.sortTypeZA', 'Type Z–A') },
            { value: 'status-az' as const, label: t('books.sortStatusAZ', 'Status A–Z') },
            { value: 'status-za' as const, label: t('books.sortStatusZA', 'Status Z–A') },
          ],
        }}
        filters={{
          activeCount: activeFilterCount,
          content: (
            <>
              <FilterGroup
                label={t('books.typeLabel')}
                value={mediaFilter}
                onChange={setMediaFilter}
                options={[
                  { value: '' as const, label: t('common.all') },
                  { value: 'ebook' as const, label: `📖 ${t('common.ebook')}`, ariaLabel: t('common.ebook') },
                  { value: 'audiobook' as const, label: `🎧 ${t('common.audiobook')}`, ariaLabel: t('common.audiobook') },
                  // The Ebook/Audiobook filters deliberately include dual-format
                  // books, so neither one isolates them; this does.
                  { value: 'both' as const, label: `📖🎧 ${t('common.both')}`, ariaLabel: t('common.both') },
                ]}
              />
              <FilterGroup
                label={t('books.filterMonitored', 'Monitored:')}
                value={monitoredFilter}
                onChange={setMonitoredFilter}
                options={[
                  { value: '' as const, label: t('books.filterAll', 'All') },
                  { value: 'monitored' as const, label: t('books.filterMonitoredOnly', 'Monitored') },
                  { value: 'unmonitored' as const, label: t('books.filterUnmonitored', 'Unmonitored') },
                ]}
              />
            </>
          ),
        }}
        chips={[
          ...(mediaFilter ? [{ key: 'media', label: t(`common.${mediaFilter}`), onClear: () => setMediaFilter('') }] : []),
          ...(monitoredFilter ? [{
            key: 'monitored',
            label: monitoredFilter === 'monitored' ? t('books.filterMonitoredOnly', 'Monitored') : t('books.filterUnmonitored', 'Unmonitored'),
            onClear: () => setMonitoredFilter(''),
          }] : []),
        ]}
        // At the end of the filter row, as on Authors, so a filter's effect
        // is read where the filter is (and announced, see FilterBar).
        summary={loading ? '' : t('books.countLabel', { count: total, defaultValue: '{{count}} books' })}
      />

      {loading ? (
        view === 'table' ? <SkeletonRows /> : <SkeletonCards />
      ) : total === 0 ? (
        !debouncedSearch && !statusFilter && !mediaFilter && !monitoredFilter ? (
          <EmptyState title={t('books.empty')} hint={t('books.emptyHint')}>
            {needsAny && <GettingStartedGuidance reasonKey="gettingStarted.reasonBooks" needsIndexer={needsIndexer} needsClient={needsClient} />}
          </EmptyState>
        ) : (
          <EmptyState variant="no-match" title={t('books.noMatch')} />
        )
      ) : (
        view === 'table' ? (
        <DataTable
          rows={books}
          rowKey={b => b.id}
          rowLabel={b => b.title}
          ariaLabel={t('books.title')}
          sort={{ ...columnSort, onSort: sortByColumn }}
          selection={{
            selected: selectedIds,
            onToggle: toggleSelect,
            onToggleAll: select => (select ? selectAllOnPage() : clearSelection()),
          }}
          // Client-side, matching the <Link> in the title cell, so the whole
          // row carries the same Previous/Next state as the title text.
          onRowClick={(book, i) => navigate(`/book/${book.id}`, { state: bookNavState(i) })}
          columns={bookColumns}
        />
        ) : (
        <div className="grid grid-cols-2 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-5 xl:grid-cols-6 gap-4">
          {books.map((book, i) => (
            <div
              key={book.id}
              className={`border rounded-lg bg-slate-100 dark:bg-zinc-900 overflow-hidden group text-left transition-colors ${selectedIds.has(book.id) ? 'border-emerald-500' : 'border-slate-200 dark:border-zinc-800 hover:border-emerald-500'}`}
            >
              <div className="aspect-[2/3] bg-slate-200 dark:bg-zinc-800 relative">
                {/* The label is the tap area: 32px round the box, 44px on a
                    touch screen, where the box sits further in to make room. */}
                <label
                  className="absolute top-0 left-0 z-10 flex p-2 pointer-coarse:p-3.5 cursor-pointer"
                  onClick={e => e.stopPropagation()}
                >
                  <Checkbox
                    checked={selectedIds.has(book.id)}
                    onChange={() => toggleSelect(book.id)}
                    label={t('dataTable.selectRow', 'Select {{name}}', { name: book.title })}
                    className={selectedIds.has(book.id) ? '' : 'bg-white/80 dark:bg-zinc-900/80'}
                  />
                </label>
                <Link to={`/book/${book.id}`} state={bookNavState(i)} aria-label={book.title} className="block w-full h-full rounded-t-lg focus-visible:-outline-offset-2">
                  <CoverImage loading="lazy" decoding="async" src={book.imageUrl} alt={book.title} className="w-full h-full object-cover"
                    fallback={<CoverPlaceholder id={book.id} title={book.title} show="initials" className="w-full h-full" />}
                  />
                </Link>
              </div>
              <div className="p-2">
                <h3 className="text-xs font-medium truncate" title={book.title}>{book.title}</h3>
                {book.author && (
                  <p className="text-[10px] text-fg-muted truncate mt-0.5">{book.author.authorName}</p>
                )}
                <div className="flex items-center gap-1 mt-1 flex-wrap">
                  {(() => {
                    const badge = bookStatusBadge(book.status, book.monitored, t)
                    return (
                      <span title={badge.description} className={`px-1.5 py-0.5 rounded text-[10px] font-medium ${badge.colorClass}`}>
                        {badge.label}
                      </span>
                    )
                  })()}
                  {(book.mediaType === 'audiobook' || book.mediaType === 'both') && (
                    <span className="px-1.5 py-0.5 rounded text-[10px] font-medium bg-indigo-100 text-indigo-800 dark:bg-indigo-950 dark:text-indigo-300">{t('books.audioLabel')}</span>
                  )}
                  {book.mediaType === 'both' && (
                    <span className="px-1.5 py-0.5 rounded text-[10px] font-medium bg-amber-100 text-amber-800 dark:bg-amber-950 dark:text-amber-300">{t('common.ebook')}</span>
                  )}
                  {/* Was a bare "?" with no legend entry. It says what it means now. */}
                  {!book.language && (
                    <span
                      className="px-1.5 py-0.5 rounded text-[10px] font-medium bg-amber-200 text-amber-900 dark:bg-amber-900/50 dark:text-amber-300"
                      title={t('books.languageUnknown', 'Metadata source did not report a language for this book.')}
                    >
                      {t('books.languageUnknownBadge', 'No language')}
                    </span>
                  )}
                </div>
                <div className="flex items-center justify-between mt-0.5">
                  {book.releaseDate && (
                    <p className="text-[10px] text-fg-muted">{new Date(book.releaseDate).getFullYear()}</p>
                  )}
                  <BookRating book={book} className="text-[10px] text-fg-muted" />
                  {/* An action that fetches a file, styled as one. As a green
                      text link it read as navigation. */}
                  {book.filePath && (
                    <a
                      href={`${BINDERY_BASE}/api/v1/book/${book.id}/file`}
                      onClick={e => e.stopPropagation()}
                      className="touch-target inline-flex items-center gap-0.5 rounded px-1 pointer-coarse:py-1.5 text-[10px] font-medium text-slate-700 dark:text-zinc-300 hover:bg-slate-200 dark:hover:bg-zinc-800"
                      title={t('books.downloadFile')}
                    >
                      {t('books.download')}
                    </a>
                  )}
                </div>
              </div>
            </div>
          ))}
        </div>
        )
      )}
      <Pagination {...paginationProps} />

      <BulkActionBar
        count={selectedIds.size}
        onClear={clearSelection}
        busy={bulkBusy}
        actions={[
          { label: t('common.monitor'), onClick: () => runBulk('monitor') },
          { label: t('common.unmonitor'), onClick: () => runBulk('unmonitor') },
          { label: t('common.search'), onClick: () => runBulk('search') },
          {
            label: t('bulkActionBar.setFormat', 'Set format'),
            choices: [
              { label: t('common.ebook'), onClick: () => runBulk('set_media_type', 'ebook') },
              { label: t('common.audiobook'), onClick: () => runBulk('set_media_type', 'audiobook') },
              { label: t('common.both'), onClick: () => runBulk('set_media_type', 'both') },
            ],
          },
          { label: t('common.delete'), onClick: () => runBulk('delete'), variant: 'danger' },
        ]}
      />

      {showAddBook && (
        <AddToLibraryModal
          mode="book"
          onClose={() => setShowAddBook(false)}
          onAdded={added => {
            // An author add has nothing on this list until its catalogue
            // syncs, so land on the author instead of refreshing an unchanged
            // page.
            if (added.kind === 'author') navigate(`/author/${added.author.id}`)
            else load()
          }}
        />
      )}
    </div>
  )
}
