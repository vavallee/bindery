import { useCallback, useEffect, useRef, useState } from 'react'
import { Link, useNavigate } from 'react-router'
import { useTranslation } from 'react-i18next'
import { useConfirmDialog } from '../components/useConfirmDialog'
import { api, Author, AuthorBulkMonitorMode, MediaType, MonitorNewItems, AuthorRefreshStatus } from '../api/client'
import AddToLibraryModal from '../components/AddToLibraryModal'
import MergeAuthorsModal from '../components/MergeAuthorsModal'
import SeriesNameModal from '../components/SeriesNameModal'
import BulkActionBar from '../components/BulkActionBar'
import { FilterGroup } from '../components/FilterPopover'
import FilterBar from '../components/FilterBar'
import DataTable, { type Column } from '../components/DataTable'
import Checkbox from '../components/Checkbox'
import { nextSort, splitSort } from '../components/tableSort'
import { SkeletonCards, SkeletonRows } from '../components/Skeleton'
import EmptyState from '../components/EmptyState'
import MoreMenu from '../components/MoreMenu'
import Pagination from '../components/Pagination'
import { useServerPagination } from '../components/usePagination'
import { oneOf, useListParams, useUrlSearchInput } from '../components/useListParams'
import ViewToggle from '../components/ViewToggle'
import BulkNotice from '../components/BulkNotice'
import { isAutoGrabRefusal } from '../util/autoGrabRefusal'
import { useView } from '../components/useView'
import SetupChecklist from '../components/SetupChecklist'
import { btn, btnSize, primaryFill } from '../components/buttons'
import { useIsAdmin } from '../auth/AuthContext'
import Switch from '../components/Switch'
import { ModalPanel } from '../components/useModal'
import CoverImage from '../components/CoverImage'
import { errorText } from '../util/errorText'
import { SORT_MODES, type SortMode } from './authorSortModes'

type MonitoredFilter = '' | 'monitored' | 'unmonitored'

// Query-string keys and their defaults; defaults stay out of the URL.
const LIST_DEFAULTS = { q: '', sort: 'az' }
// The table's sortable columns and the whitelisted sort values behind them.
const AUTHOR_COLUMN_SORTS = {
  name: { asc: 'az', desc: 'za' },
  books: { asc: 'books-asc', desc: 'books-desc' },
  rating: { asc: 'rating-asc', desc: 'rating-desc' },
  monitored: { asc: 'monitored-asc', desc: 'monitored-desc' },
}

export default function AuthorsPage() {
  const { t } = useTranslation()
  const isAdmin = useIsAdmin()
  const { confirm, confirmDialog } = useConfirmDialog()
  const navigate = useNavigate()
  const [authors, setAuthors] = useState<Author[]>([])
  const [total, setTotal] = useState(0)
  // Full author list fetched lazily for the merge picker, which needs every
  // author, not just the current page.
  const [mergeAuthors, setMergeAuthors] = useState<Author[]>([])
  const [loading, setLoading] = useState(true)
  // Both Add buttons open the same search-first dialog (#1227); the mode
  // only seeds the placeholder.
  const [addMode, setAddMode] = useState<'author' | 'book' | null>(null)
  const [showAddSeries, setShowAddSeries] = useState(false)
  const [showMerge, setShowMerge] = useState(false)
  const [showMonitorModeBulk, setShowMonitorModeBulk] = useState(false)
  // Which of Monitor / Unmonitor the bulk bar is asking about, and whether the
  // answer should reach the authors' existing books (#2742). Null means the
  // dialog is closed. The cascade defaults to off, matching the single author
  // path, so the action a user already knows keeps doing exactly what it did.
  const [monitoringBulkAction, setMonitoringBulkAction] = useState<'monitor' | 'unmonitor' | null>(null)
  const [bulkApplyMonitoringToExisting, setBulkApplyMonitoringToExisting] = useState(false)
  const [bulkMonitorMode, setBulkMonitorMode] = useState<AuthorBulkMonitorMode>('none')
  const [bulkMonitorLatestCount, setBulkMonitorLatestCount] = useState(1)
  const [bulkApplyMonitorModeToExisting, setBulkApplyMonitorModeToExisting] = useState(true)
  // '' = leave each author's existing value alone, which is what this dialog
  // did before it could write the field at all (#2065).
  const [bulkMonitorNewItems, setBulkMonitorNewItems] = useState<MonitorNewItems | ''>('')
  const [bulkMonitorModeError, setBulkMonitorModeError] = useState<string | null>(null)
  // Page, search and sort live in the URL so going back from an author lands
  // on the same page of the same list (#3052). The monitored filter, view and
  // page size stay in localStorage as preferences that already survive it.
  const list = useListParams(LIST_DEFAULTS)
  const sort = oneOf(list.values.sort, SORT_MODES, 'az')
  const debouncedSearch = list.values.q
  const updateList = list.update
  const setSort = (next: SortMode) => updateList({ sort: next, page: null })
  // Keystroke-level changes replace the history entry instead of pushing one.
  const commitSearch = useCallback((q: string) => updateList({ q, page: null }, { replace: true }), [updateList])
  const [search, setSearch] = useUrlSearchInput(debouncedSearch, commitSearch)
  const [monitoredFilter, setMonitoredFilterState] = useState<MonitoredFilter>(() => {
    try {
      const v = localStorage.getItem('bindery.filter.authors.monitored')
      if (v === 'monitored' || v === 'unmonitored') return v
    } catch { /* ignore */ }
    return ''
  })
  const setMonitoredFilter = (next: MonitoredFilter) => {
    setMonitoredFilterState(next)
    updateList({ page: null })
  }
  const [view, setView] = useView('authors', 'grid')
  const [selectedIds, setSelectedIds] = useState<Set<number>>(new Set())
  const [bulkBusy, setBulkBusy] = useState(false)
  // A message from the last bulk action that is not an exception, e.g. the
  // server refusing a search because automatic grabbing is off (#2669).
  const [bulkNotice, setBulkNotice] = useState<string | null>(null)
  // "Refresh all metadata" background job (#863). refreshStatus mirrors the
  // persisted server-side progress; refreshing is the in-flight flag that drives
  // polling and disables the button.
  const [refreshStatus, setRefreshStatus] = useState<AuthorRefreshStatus | null>(null)
  const [refreshing, setRefreshing] = useState(false)

  const monitoredParam = monitoredFilter === 'monitored' ? true : monitoredFilter === 'unmonitored' ? false : undefined
  // The query a total belongs to, minus the page; see BooksPage. The snap
  // back to the last page only trusts a total fetched for this query.
  const queryKey = JSON.stringify([debouncedSearch, sort, monitoredFilter])
  const [totalKey, setTotalKey] = useState<string | null>(null)
  const loadRequestRef = useRef(0)
  const { page, pageSize, paginationProps } = useServerPagination(total, 50, 'authors', {
    page: list.page,
    setPage: list.setPage,
    ready: !loading && totalKey === queryKey,
  })

  // Server-side list: page, page size, search, sort, and the monitored filter
  // are all applied by the API, so libraries with >100 authors are fully
  // reachable (issue #1010). load() refetches the current page; the mutation
  // handlers below call it to refresh.
  const load = useCallback(() => {
    const request = ++loadRequestRef.current
    setLoading(true)
    api.listAuthors({
      limit: pageSize,
      offset: (page - 1) * pageSize,
      search: debouncedSearch || undefined,
      sort,
      monitored: monitoredParam,
    }).then(({ items, total }) => {
      setAuthors(items)
      setTotal(total)
      if (request === loadRequestRef.current) setTotalKey(queryKey)
    })
      .catch(err => {
        if (request === loadRequestRef.current) setTotalKey(null)
        console.error(err)
      })
      .finally(() => setLoading(false))
  }, [page, pageSize, debouncedSearch, sort, monitoredParam, queryKey])

  // A selection only means something on the page it was made on.
  useEffect(() => { setSelectedIds(new Set()) }, [page, queryKey])

  useEffect(() => { load() }, [load])

  // The search box is debounced into the URL (useUrlSearchInput). Every query
  // change drops the page param in the same update, which is the jump back to
  // page 1; an effect would also fire on mount and lose the page from the URL.

  // Restore the last-known refresh status on mount so the banner survives a
  // page reload. If a job is still "running", resume polling.
  useEffect(() => {
    api.refreshAllAuthorsStatus()
      .then((st) => {
        setRefreshStatus(st)
        if (st?.status === 'running') setRefreshing(true)
      })
      .catch(() => {/* never run — ignore */})
  }, [])

  // Poll progress every 2s while a refresh is running. Stops as soon as the
  // server reports a terminal state.
  useEffect(() => {
    if (!refreshing) return
    let cancelled = false
    const poll = async () => {
      try {
        const st = await api.refreshAllAuthorsStatus()
        if (cancelled) return
        setRefreshStatus(st)
        if (st && st.status !== 'running') {
          setRefreshing(false)
          load() // pick up any newly-populated catalogues
        }
      } catch {/* transient — keep polling */}
    }
    const id = setInterval(poll, 2000)
    return () => { cancelled = true; clearInterval(id) }
  }, [refreshing])

  const handleRefreshAll = async () => {
    if (refreshing) return
    if (!await confirm({
      title: t('common.confirmTitle'),
      body: t('authors.refreshAllConfirm'),
      confirmLabel: t('common.refresh'),
    })) return
    setRefreshing(true)
    setRefreshStatus(null)
    try {
      await api.refreshAllAuthors()
    } catch (err) {
      setRefreshing(false)
      alert(err instanceof Error ? err.message : t('authors.refreshAllError'))
    }
  }

  useEffect(() => {
    try { localStorage.setItem('bindery.filter.authors.monitored', monitoredFilter) } catch { /* ignore */ }
  }, [monitoredFilter])

  const handleDelete = async (id: number) => {
    let withFiles = 0
    let total = 0
    try {
      const { items: books } = await api.listBooks({ authorId: id })
      total = books.length
      withFiles = books.filter(b => b.filePath).length
    } catch { /* fall through to no-file-sweep default */ }
    const msg = withFiles > 0
      ? t('authors.deleteWithFilesConfirm', { total, withFiles })
      : t('authors.deleteConfirm')
    if (!await confirm({
      title: t('common.confirmTitle'),
      body: msg,
      confirmLabel: t('common.delete'),
      // Deleting files off disk is irreversible, so that variant is gated.
      acknowledgeLabel: withFiles > 0 ? t('authorDetail.deleteFilesAcknowledge') : undefined,
    })) return
    await api.deleteAuthor(id, withFiles > 0)
    load()
  }

  const handleToggleMonitored = async (author: Author) => {
    await api.updateAuthor(author.id, { monitored: !author.monitored })
    load()
  }

  useEffect(() => {
    document.title = `${t('nav.authors', 'Authors')} · Bindery`
    return () => { document.title = 'Bindery' }
  }, [t])

  const toggleSelect = (id: number) => {
    setSelectedIds(prev => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  const selectAllOnPage = () => setSelectedIds(new Set(authors.map(a => a.id)))

  // Merge needs the full author list, not just the current page; fetch it
  // lazily when the modal opens.
  const openMerge = async () => {
    try {
      setMergeAuthors(await api.listAllAuthors())
    } catch {
      setMergeAuthors(authors)
    }
    setShowMerge(true)
  }
  const clearSelection = () => setSelectedIds(new Set())

  const runBulk = async (action: Parameters<typeof api.bulkActionAuthors>[1], applyToExisting = false) => {
    if (selectedIds.size === 0) return
    if (action === 'delete' && !await confirm({
      title: t('common.confirmTitle'),
      body: t('authors.bulkDeleteConfirm', { count: selectedIds.size }),
      confirmLabel: t('common.delete'),
    })) return
    setBulkBusy(true)
    setBulkNotice(null)
    try {
      const res = await api.bulkActionAuthors([...selectedIds], action, undefined, applyToExisting)
      // Same refusal as every other bulk Search surface (#2669): the selection
      // survives and the list is not reloaded, because nothing happened.
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

  const runBulkSetMediaType = async (mediaType: MediaType) => {
    if (selectedIds.size === 0) return
    const confirmMsg = t('authors.bulkSetMediaTypeConfirm', {
      count: selectedIds.size,
      mediaType: t(`mediaType.${mediaType}`, mediaType),
      defaultValue: `Set the media type for every book of {{count}} selected authors to {{mediaType}}? This rewrites existing book rows and may re-evaluate wanted/missing status.`,
    })
    if (!await confirm({ title: t('common.confirmTitle'), body: confirmMsg })) return
    setBulkBusy(true)
    try {
      await api.bulkActionAuthors([...selectedIds], 'set_media_type', mediaType)
      clearSelection()
      load()
    } catch (err) {
      alert(errorText(err, t('common.bulkActionFailed', 'Bulk action failed')))
    } finally {
      setBulkBusy(false)
    }
  }

  const openBulkMonitorMode = () => {
    setBulkMonitorMode('none')
    setBulkMonitorLatestCount(1)
    setBulkApplyMonitorModeToExisting(true)
    setBulkMonitorNewItems('')
    setBulkMonitorModeError(null)
    setShowMonitorModeBulk(true)
  }

  const closeBulkMonitorMode = () => {
    if (bulkBusy) return
    setShowMonitorModeBulk(false)
    setBulkMonitorModeError(null)
  }

  const runBulkSetMonitorMode = async () => {
    if (selectedIds.size === 0) return
    setBulkBusy(true)
    setBulkMonitorModeError(null)
    try {
      const result = await api.bulkSetAuthorMonitorMode([...selectedIds], bulkMonitorMode, {
        monitorLatestCount: bulkMonitorMode === 'latest' ? bulkMonitorLatestCount : undefined,
        applyMonitorModeToExisting: bulkApplyMonitorModeToExisting,
        monitorNewItems: bulkMonitorNewItems === '' ? undefined : bulkMonitorNewItems,
      })
      const failed = Object.entries(result.results).find(([, item]) => !item.ok)
      if (failed) {
        const [id, item] = failed
        setBulkMonitorModeError(t('authors.bulkSetMonitorModePartial', {
          id,
          error: item.error || t('common.unknownError', 'Unknown error'),
          defaultValue: 'Monitor mode was not updated for author {{id}}: {{error}}',
        }))
        return
      }
      setShowMonitorModeBulk(false)
      clearSelection()
      load()
    } catch (err) {
      setBulkMonitorModeError(err instanceof Error ? err.message : t('authors.bulkSetMonitorModeFailed', 'Bulk monitor mode update failed'))
    } finally {
      setBulkBusy(false)
    }
  }

  const openMonitoringBulk = (action: 'monitor' | 'unmonitor') => {
    if (selectedIds.size === 0) return
    setBulkApplyMonitoringToExisting(false)
    setMonitoringBulkAction(action)
  }

  const runMonitoringBulk = async () => {
    if (!monitoringBulkAction) return
    const action = monitoringBulkAction
    const applyToExisting = bulkApplyMonitoringToExisting
    setMonitoringBulkAction(null)
    await runBulk(action, applyToExisting)
  }

  const handleCreateSeries = async (title: string) => {
    const series = await api.createSeries({ title })
    setShowAddSeries(false)
    navigate('/series', { state: { seriesId: series.id } })
  }

  // Column-header sorting: first click ascending, a second click on the same
  // column flips to descending. Both keys are whitelisted server-side, so an
  // unknown value can only ever fall back to the name sort.
  const columnSort = splitSort(sort, AUTHOR_COLUMN_SORTS)
  const sortByColumn = (key: string) => setSort(nextSort(sort, key, AUTHOR_COLUMN_SORTS) as SortMode)

  // Only OpenLibrary supplies an author-level rating, and only for authors it
  // resolved by search rather than by direct id. Hardcover and DNB supply none
  // at all, so for those libraries the column was a full column of em dashes.
  // Drop it when this page has nothing to put in it, rather than removing it
  // outright and costing OpenLibrary users a real sort key.
  const anyRating = authors.some(a => a.averageRating > 0)

  const authorColumns: Column<Author>[] = [
    {
      key: 'name',
      header: t('authors.colName'),
      sortKey: 'name',
      cell: (author, i) => (
        <Link to={`/author/${author.id}`} state={authorNavState(i)} className="flex items-center gap-2">
          <CoverImage loading="lazy" decoding="async" src={author.imageUrl} alt="" className="w-7 h-7 rounded-full object-cover flex-shrink-0"
            fallback={
              <div className="w-7 h-7 rounded-full bg-slate-200 dark:bg-zinc-800 flex items-center justify-center text-xs font-bold text-fg-muted flex-shrink-0">
                {author.authorName.charAt(0)}
              </div>
            }
          />
          <span className="text-slate-800 dark:text-zinc-200 truncate hover:text-emerald-500">{author.authorName}</span>
        </Link>
      ),
    },
    {
      key: 'books',
      header: t('authors.colBooks'),
      sortKey: 'books',
      className: 'text-fg-muted whitespace-nowrap tabular-nums',
      cell: author => (
        <span
          title={author.statistics && author.statistics.bookCount > 0
            ? `${t('authors.booksInLibrary', '{{have}} of {{total}} in library', { have: author.statistics.availableBookCount, total: author.statistics.bookCount })}. ${t('authors.booksInLibraryHint', 'A book counts once at least one of its files is in your library.')}`
            : undefined}
        >
          {author.statistics && author.statistics.bookCount > 0 ? `${author.statistics.availableBookCount} / ${author.statistics.bookCount}` : '—'}
        </span>
      ),
    },
    ...(anyRating ? [{
      key: 'rating',
      header: t('authors.colRating'),
      sortKey: 'rating',
      className: 'text-fg-muted whitespace-nowrap',
      cell: (author: Author) => (author.averageRating > 0 ? `★ ${author.averageRating.toFixed(2)}` : '—'),
    }] : []),
    {
      key: 'monitored',
      header: t('authors.colMonitored'),
      sortKey: 'monitored',
      className: 'whitespace-nowrap',
      // The table is the management view, so monitoring stays a switch here.
      cell: author => (
        <Switch
          checked={author.monitored}
          onChange={() => handleToggleMonitored(author)}
          label={author.monitored ? t('authors.stopMonitoring', 'Stop monitoring') : t('authors.startMonitoring', 'Monitor')}
        />
      ),
    },
  ]

  // This page's loaded ids, in order — handed to AuthorDetailPage as router
  // state (#2548) for Previous/Next; see AuthorNavState there.
  const authorIds = authors.map(a => a.id)
  const authorNavState = (index: number) => ({ ids: authorIds, index })

  return (
    <div className={selectedIds.size > 0 ? 'pb-16' : ''}>
      {confirmDialog}
      <BulkNotice message={bulkNotice} onDismiss={() => setBulkNotice(null)} />
      {/* Page header: one primary action, everything else behind "More".
          The toolbar used to be five equal buttons (Refresh all metadata,
          Merge, Add Book, Add Series, Add Author), so the daily action sat in
          a row with a rare, heavy one (Refresh all re-fetches every author)
          and the eye had to read every label to find it. Add author stays a
          single click; the rest are one more. The row wraps below `sm` and
          keeps the primary first. */}
      <div className="flex flex-wrap items-center justify-between gap-3 mb-4">
        <h2 className="text-2xl font-bold min-w-0">{t('authors.title')}</h2>
        <div className="flex items-center gap-2 flex-wrap justify-end">
          <ViewToggle view={view} onChange={setView} />
          <MoreMenu
            label={t('common.more', 'More')}
            ariaLabel={t('authors.pageActions', 'More author actions')}
            items={[
              { label: t('addToLibrary.addBook'), onSelect: () => setAddMode('book') },
              // Creating a series is admin only (#468).
              ...(isAdmin ? [{ label: t('series.addSeries'), onSelect: () => setShowAddSeries(true) }] : []),
              { label: t('authors.merge'), onSelect: openMerge, disabled: total < 2, title: t('authors.mergeTip') },
              {
                label: refreshing ? t('authors.refreshAllRunning') : t('authors.refreshAll'),
                onSelect: handleRefreshAll,
                disabled: refreshing || total === 0,
                title: t('authors.refreshAllTip'),
                caution: true,
              },
            ]}
          />
          <button
            onClick={() => setAddMode('author')}
            className={`order-first sm:order-none ${btn.primary} px-4 py-2 text-sm`}
          >
            {t('authors.addAuthor')}
          </button>
        </div>
      </div>

      {/* Refresh-all metadata progress banner (#863). Amber while running,
          success/failure summary on completion. Mirrors the Library Scan banner. */}
      {refreshStatus?.status === 'running' && (
        <div className="mb-4 px-3 py-2 rounded-md text-sm bg-amber-100 dark:bg-amber-900/30 text-amber-800 dark:text-amber-300 border border-amber-300 dark:border-amber-800">
          {t('authors.refreshAllProgress', { done: refreshStatus.done, total: refreshStatus.total })}
        </div>
      )}
      {refreshStatus?.status === 'completed' && (
        <div className="mb-4 px-3 py-2 rounded-md text-sm bg-emerald-100 dark:bg-emerald-900/30 text-emerald-800 dark:text-emerald-300 border border-emerald-300 dark:border-emerald-800">
          {refreshStatus.failed > 0
            ? t('authors.refreshAllDoneWithFailures', { done: refreshStatus.done, failed: refreshStatus.failed })
            : t('authors.refreshAllDone', { done: refreshStatus.done })}
        </div>
      )}
      {refreshStatus?.status === 'failed' && (
        <div className="mb-4 px-3 py-2 rounded-md text-sm bg-red-100 dark:bg-red-900/30 text-red-800 dark:text-red-300 border border-red-300 dark:border-red-800">
          {t('authors.refreshAllFailed', { message: refreshStatus.message || t('authors.refreshAllError') })}
        </div>
      )}

      <FilterBar
        search={{ value: search, onChange: setSearch, placeholder: t('authors.searchPlaceholder') }}
        sort={{
          value: sort,
          onChange: setSort,
          label: t('common.sort', 'Sort'),
          options: [
            { value: 'az' as const, label: t('authors.sortAZ') },
            { value: 'za' as const, label: t('authors.sortZA') },
            { value: 'name-az' as const, label: t('authors.sortFirstNameAZ') },
            { value: 'name-za' as const, label: t('authors.sortFirstNameZA') },
            { value: 'recent' as const, label: t('authors.sortRecent') },
            // Every sort a column header can set is here too, so after a
            // header click (or a link carrying one) the popover still shows
            // which sort is on.
            { value: 'books-desc' as const, label: t('authors.sortMostBooks', 'Most books') },
            { value: 'books-asc' as const, label: t('authors.sortFewestBooks', 'Fewest books') },
            { value: 'rating-desc' as const, label: t('authors.sortRatingHigh', 'Highest rated') },
            { value: 'rating-asc' as const, label: t('authors.sortRatingLow', 'Lowest rated') },
            { value: 'monitored-desc' as const, label: t('authors.sortMonitoredFirst', 'Monitored first') },
            { value: 'monitored-asc' as const, label: t('authors.sortUnmonitoredFirst', 'Not monitored first') },
          ],
        }}
        filters={{
          activeCount: monitoredFilter ? 1 : 0,
          content: (
            <FilterGroup
              label={t('authors.filterMonitored')}
              value={monitoredFilter}
              onChange={setMonitoredFilter}
              options={[
                { value: '' as const, label: t('authors.filterAll') },
                { value: 'monitored' as const, label: t('authors.filterMonitoredOnly') },
                { value: 'unmonitored' as const, label: t('authors.filterUnmonitored') },
              ]}
            />
          ),
        }}
        chips={monitoredFilter ? [{
          key: 'monitored',
          label: monitoredFilter === 'monitored' ? t('authors.filterMonitoredOnly') : t('authors.filterUnmonitored'),
          onClear: () => setMonitoredFilter(''),
        }] : []}
        summary={loading ? '' : t('authors.countLabel', { count: total, defaultValue: '{{count}} authors' })}
      />

      <SetupChecklist />

      {loading ? (
        view === 'table' ? <SkeletonRows /> : <SkeletonCards cards={8} />
      ) : total === 0 && !debouncedSearch && !monitoredFilter ? (
        <EmptyState title={t('authors.empty')} hint={t('authors.emptyHint')} />
      ) : total === 0 ? (
        <EmptyState
          variant="no-match"
          title={t('authors.noMatch', { query: search })}
          action={monitoredFilter ? (
            <button type="button" onClick={() => setMonitoredFilter('')} className={`${btn.secondary} ${btnSize.md}`}>
              {t('common.clearFilter', 'Clear filter')}
            </button>
          ) : undefined}
        />
      ) : view === 'table' ? (
        <DataTable
          rows={authors}
          rowKey={a => a.id}
          rowLabel={a => a.authorName}
          ariaLabel={t('authors.title')}
          sort={{ ...columnSort, onSort: sortByColumn }}
          selection={{
            selected: selectedIds,
            onToggle: toggleSelect,
            onToggleAll: select => (select ? selectAllOnPage() : clearSelection()),
          }}
          columns={authorColumns}
          rowActions={author => (
            <MoreMenu
              label={t('common.moreActions', 'More')}
              ariaLabel={t('authors.moreActionsFor', 'More actions for {{name}}', { name: author.authorName })}
              buttonClassName={`${btn.ghost} ${btnSize.sm}`}
              items={[
                { label: t('common.refresh'), onSelect: () => { api.refreshAuthor(author.id).then(load) } },
                { label: t('common.delete'), onSelect: () => handleDelete(author.id), danger: true },
              ]}
            />
          )}
        />
      ) : (
        <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4 gap-4">
          {authors.map((author, i) => (
            <div
              key={author.id}
              data-testid="author-card"
              className={`border rounded-lg bg-slate-100 dark:bg-zinc-900 overflow-hidden hover:border-emerald-500 transition-colors ${selectedIds.has(author.id) ? 'border-emerald-500' : 'border-slate-200 dark:border-zinc-800'}`}
            >
              <div className="relative">
                <label
                  className="absolute top-0 left-0 z-10 flex p-2 pointer-coarse:p-3.5 cursor-pointer"
                  onClick={e => e.stopPropagation()}
                >
                  <Checkbox
                    checked={selectedIds.has(author.id)}
                    onChange={() => toggleSelect(author.id)}
                    label={t('dataTable.selectRow', 'Select {{name}}', { name: author.authorName })}
                    className={selectedIds.has(author.id) ? '' : 'bg-white/80 dark:bg-zinc-900/80'}
                  />
                </label>
                <Link to={`/author/${author.id}`} state={authorNavState(i)} className="flex gap-3 p-4 pl-9 rounded-t-lg focus-visible:-outline-offset-2 hover:bg-slate-200/40 dark:hover:bg-zinc-800/40 transition-colors">
                  <CoverImage loading="lazy" decoding="async" src={author.imageUrl} alt={author.authorName} className="w-16 h-16 rounded-full object-cover flex-shrink-0"
                    fallback={
                      <div className="w-16 h-16 rounded-full bg-slate-200 dark:bg-zinc-800 flex items-center justify-center flex-shrink-0 text-xl font-bold text-fg-muted">
                        {author.authorName.charAt(0)}
                      </div>
                    }
                  />
                  <div className="min-w-0">
                    <h3 className="font-semibold truncate">{author.authorName}</h3>
                    {/* No placeholder sentence when there is no description:
                        the table shows nothing for the same missing data, and
                        "No description available" on most cards was noise. */}
                    {author.description && (
                      <p className="text-xs text-fg-muted mt-1 line-clamp-2">{author.description}</p>
                    )}
                    {author.statistics && author.statistics.bookCount > 0 && (
                      <p className="text-xs text-fg-muted mt-1 tabular-nums">
                        {t('authors.booksInLibrary', '{{have}} of {{total}} in library', { have: author.statistics.availableBookCount, total: author.statistics.bookCount })}
                      </p>
                    )}
                  </div>
                </Link>
              </div>
              {/* A browse card shows monitored as a badge, not a switch. Sixty
                  live toggles on a page whose job is finding an author read as
                  settings to flip. Monitoring changes from the card's menu, the
                  table's switch, the bulk bar or the author page. */}
              <div className="flex items-center justify-between px-4 py-2 bg-slate-200/50 dark:bg-zinc-800/50 border-t border-slate-200 dark:border-zinc-800">
                <MonitoredBadge monitored={author.monitored} />
                <MoreMenu
                  label={t('common.moreActions', 'More')}
                  ariaLabel={t('authors.moreActionsFor', 'More actions for {{name}}', { name: author.authorName })}
                  buttonClassName={`${btn.ghost} ${btnSize.sm}`}
                  items={[
                    {
                      label: author.monitored ? t('authors.stopMonitoring', 'Stop monitoring') : t('authors.startMonitoring', 'Monitor'),
                      onSelect: () => handleToggleMonitored(author),
                    },
                    { label: t('common.refresh'), onSelect: () => { api.refreshAuthor(author.id).then(load) }, title: t('authors.refreshMetadata', 'Refresh metadata') },
                    { label: t('common.delete'), onSelect: () => handleDelete(author.id), danger: true },
                  ]}
                />
              </div>
            </div>
          ))}
        </div>
      )}
      <Pagination {...paginationProps} />

      <BulkActionBar
        count={selectedIds.size}
        onClear={clearSelection}
        busy={bulkBusy}
        actions={[
          { label: t('common.monitor'), onClick: () => openMonitoringBulk('monitor') },
          { label: t('common.unmonitor'), onClick: () => openMonitoringBulk('unmonitor') },
          { label: t('common.search'), onClick: () => runBulk('search') },
          {
            label: t('bulkActionBar.setFormat', 'Set format'),
            choices: [
              { label: t('common.ebook'), onClick: () => runBulkSetMediaType('ebook') },
              { label: t('common.audiobook'), onClick: () => runBulkSetMediaType('audiobook') },
              { label: t('common.both'), onClick: () => runBulkSetMediaType('both') },
            ],
          },
          { label: t('authors.bulkSetMonitorMode', 'Set monitor mode'), onClick: openBulkMonitorMode },
          { label: t('authors.bulkRefreshMetadata', 'Refresh metadata'), onClick: () => runBulk('refresh') },
          { label: t('common.delete'), onClick: () => runBulk('delete'), variant: 'danger' },
        ]}
      />

      {monitoringBulkAction && (
        <div className="modal-overlay fixed inset-0 bg-black/60 flex items-center justify-center p-4 z-50" onClick={() => setMonitoringBulkAction(null)}>
          <ModalPanel onClose={() => setMonitoringBulkAction(null)} canClose={!bulkBusy} labelledBy="bulk-monitoring-title" className="bg-slate-100 dark:bg-zinc-900 border border-slate-300 dark:border-zinc-700 rounded-lg w-full max-w-md shadow-2xl modal-max-h overflow-y-auto" onClick={e => e.stopPropagation()}>
            <div className="p-4 border-b border-slate-200 dark:border-zinc-800">
              <h3 id="bulk-monitoring-title" className="text-lg font-semibold">
                {monitoringBulkAction === 'monitor' ? t('common.monitor') : t('common.unmonitor')}
              </h3>
              <p className="text-xs text-fg-muted mt-1">
                {t('authors.bulkSetMonitorModeCount', {
                  count: selectedIds.size,
                  defaultValue: 'Selected authors: {{count}}',
                })}
              </p>
            </div>
            <div className="p-4">
              <label className="flex items-start gap-2 text-sm cursor-pointer select-none">
                <input
                  type="checkbox"
                  checked={bulkApplyMonitoringToExisting}
                  onChange={e => setBulkApplyMonitoringToExisting(e.target.checked)}
                  disabled={bulkBusy}
                  className="mt-0.5 flex-shrink-0 disabled:opacity-50"
                />
                <span>
                  <span className="font-medium">{t('authors.bulkApplyMonitoringToExisting', 'Also apply to their existing books')}</span>
                  <span className="block text-xs text-fg-muted mt-0.5">{t('authors.bulkApplyMonitoringToExistingHint', 'Rewrites every book of the selected authors to match. Leave it off to change the authors only.')}</span>
                </span>
              </label>
            </div>
            <div className="p-4 border-t border-slate-200 dark:border-zinc-800 flex justify-end gap-2">
              <button
                onClick={() => setMonitoringBulkAction(null)}
                disabled={bulkBusy}
                className="px-4 py-2 text-sm text-fg-muted hover:text-slate-900 dark:hover:text-white disabled:opacity-50"
              >
                {t('common.cancel', 'Cancel')}
              </button>
              <button
                onClick={runMonitoringBulk}
                disabled={bulkBusy}
                className={`px-4 py-2 ${primaryFill} disabled:opacity-50 disabled:cursor-not-allowed rounded-md text-sm font-medium transition-colors`}
              >
                {t('common.apply', 'Apply')}
              </button>
            </div>
          </ModalPanel>
        </div>
      )}

      {showMonitorModeBulk && (
        <div className="modal-overlay fixed inset-0 bg-black/60 flex items-center justify-center p-4 z-50" onClick={closeBulkMonitorMode}>
          <ModalPanel onClose={closeBulkMonitorMode} canClose={!bulkBusy} labelledBy="bulk-monitor-mode-title" className="bg-slate-100 dark:bg-zinc-900 border border-slate-300 dark:border-zinc-700 rounded-lg w-full max-w-md shadow-2xl modal-max-h overflow-y-auto" onClick={e => e.stopPropagation()}>
            <div className="p-4 border-b border-slate-200 dark:border-zinc-800">
              <h3 id="bulk-monitor-mode-title" className="text-lg font-semibold">{t('authors.bulkSetMonitorModeTitle', 'Set monitor mode')}</h3>
              <p className="text-xs text-fg-muted mt-1">
                {t('authors.bulkSetMonitorModeCount', {
                  count: selectedIds.size,
                  defaultValue: 'Selected authors: {{count}}',
                })}
              </p>
            </div>
            <div className="p-4 space-y-4">
              <div>
                <label className="block text-xs text-fg-muted mb-1" htmlFor="bulk-monitor-mode">
                  {t('editAuthorModal.monitorMode', 'Monitor mode')}
                </label>
                <select
                  id="bulk-monitor-mode"
                  value={bulkMonitorMode}
                  onChange={e => setBulkMonitorMode(e.target.value as AuthorBulkMonitorMode)}
                  disabled={bulkBusy}
                  className="w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded-md px-3 py-2 text-sm disabled:opacity-50"
                >
                  <option value="all">{t('monitorMode.all', 'All books')}</option>
                  <option value="future">{t('monitorMode.future', 'Future books only (grows on refresh)')}</option>
                  <option value="latest">{t('monitorMode.latest', 'Latest only')}</option>
                  <option value="none">{t('monitorMode.none', 'None')}</option>
                </select>
              </div>
              <div>
                <label className="block text-xs text-fg-muted mb-1" htmlFor="bulk-monitor-new-items">
                  {t('editAuthorModal.monitorNewItems', 'Monitor newly discovered books')}
                </label>
                <select
                  id="bulk-monitor-new-items"
                  value={bulkMonitorNewItems}
                  onChange={e => setBulkMonitorNewItems(e.target.value as MonitorNewItems | '')}
                  disabled={bulkBusy}
                  className="w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded-md px-3 py-2 text-sm disabled:opacity-50"
                >
                  <option value="">{t('authors.bulkMonitorNewItemsUnchanged', 'Leave unchanged')}</option>
                  <option value="all">{t('monitorNewItems.all', 'Follow monitor mode')}</option>
                  <option value="none">{t('monitorNewItems.none', 'Don’t add them')}</option>
                </select>
                <p className="text-xs text-fg-muted mt-1">
                  {t('authors.bulkMonitorNewItemsHint', 'Separate from monitor mode: this decides whether a refresh may add books it discovers at all. Monitor mode “None” on its own still lets the back-catalogue in, unmonitored.')}
                </p>
              </div>
              {bulkMonitorMode === 'latest' && (
                <div>
                  <label className="block text-xs text-fg-muted mb-1" htmlFor="bulk-monitor-latest-count">
                    {t('editAuthorModal.monitorLatestCount', 'Latest book count')}
                  </label>
                  <input
                    id="bulk-monitor-latest-count"
                    type="number"
                    min={1}
                    value={bulkMonitorLatestCount}
                    onChange={e => {
                      const value = Number(e.target.value)
                      const safe = Number.isFinite(value) && value > 0 ? value : 1
                      setBulkMonitorLatestCount(safe)
                    }}
                    disabled={bulkBusy}
                    className="w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded-md px-3 py-2 text-sm disabled:opacity-50"
                  />
                </div>
              )}
              <label className="flex items-start gap-2 text-sm cursor-pointer select-none">
                <input
                  type="checkbox"
                  checked={bulkApplyMonitorModeToExisting}
                  onChange={e => setBulkApplyMonitorModeToExisting(e.target.checked)}
                  disabled={bulkBusy}
                  className="mt-0.5 flex-shrink-0 disabled:opacity-50"
                />
                <span>
                  <span className="font-medium">{t('authors.bulkApplyMonitorModeToExisting', 'Apply monitor mode to existing books')}</span>
                  <span className="block text-xs text-fg-muted mt-0.5">{t('authors.bulkApplyMonitorModeToExistingHint', 'Otherwise this only affects books discovered in future refreshes.')}</span>
                </span>
              </label>
              {bulkMonitorModeError && (
                <p role="alert" className="text-sm text-red-700 dark:text-red-300 bg-red-100 dark:bg-red-950/40 border border-red-300 dark:border-red-900 rounded-md px-3 py-2">
                  {bulkMonitorModeError}
                </p>
              )}
            </div>
            <div className="p-4 border-t border-slate-200 dark:border-zinc-800 flex justify-end gap-2">
              <button
                onClick={closeBulkMonitorMode}
                disabled={bulkBusy}
                className="px-4 py-2 text-sm text-fg-muted hover:text-slate-900 dark:hover:text-white disabled:opacity-50"
              >
                {t('common.cancel', 'Cancel')}
              </button>
              <button
                onClick={runBulkSetMonitorMode}
                disabled={bulkBusy}
                className={`px-4 py-2 ${primaryFill} disabled:opacity-50 disabled:cursor-not-allowed rounded-md text-sm font-medium transition-colors`}
              >
                {bulkBusy ? t('common.saving', 'Saving…') : t('authors.bulkSetMonitorModeApply', 'Apply monitor mode')}
              </button>
            </div>
          </ModalPanel>
        </div>
      )}

      {addMode && (
        <AddToLibraryModal
          mode={addMode}
          onClose={() => setAddMode(null)}
          onAdded={() => load()}
        />
      )}
      {showAddSeries && (
        <SeriesNameModal
          title={t('series.addSeries')}
          submitLabel={t('series.addSeries')}
          onClose={() => setShowAddSeries(false)}
          onSubmit={handleCreateSeries}
        />
      )}
      {showMerge && (
        <MergeAuthorsModal
          authors={mergeAuthors}
          onClose={() => setShowMerge(false)}
          onMerged={load}
        />
      )}
    </div>
  )
}

// Read-only monitored state for a browse card. Deliberately not shaped like
// the Switch (see Switch.tsx: a badge and a toggle must not look alike).
function MonitoredBadge({ monitored }: { monitored: boolean }) {
  const { t } = useTranslation()
  return (
    <span className={`inline-flex items-center gap-1 px-1.5 py-0.5 rounded text-[11px] font-medium ${monitored ? 'bg-emerald-500/20 text-emerald-800 dark:text-emerald-400' : 'bg-slate-300 dark:bg-zinc-700 text-slate-600 dark:text-zinc-300'}`}>
      <span aria-hidden="true">{monitored ? '●' : '○'}</span>
      {monitored ? t('authors.monitored') : t('authors.unmonitored')}
    </span>
  )
}
