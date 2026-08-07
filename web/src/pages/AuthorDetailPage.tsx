import { useEffect, useMemo, useRef, useState } from 'react'
import { Link, useLocation, useNavigate, useParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { api, Author, AuthorAlias, Book, BookBulkAction, Series } from '../api/client'
import ViewToggle from '../components/ViewToggle'
import { bookStatusBadge } from '../components/bookStatus'
import MergeAuthorsModal from '../components/MergeAuthorsModal'
import EditAuthorModal from '../components/EditAuthorModal'
import AuthorMetadataLinkModal from '../components/AuthorMetadataLinkModal'
import RenameFilesModal from '../components/RenameFilesModal'
import BulkActionBar from '../components/BulkActionBar'
import { useView } from '../components/useView'
import MarkdownDescription from '../components/MarkdownDescription'
import { canLinkAuthorMetadata, hasSparseMetadata } from '../util/authorMetadata'
import { metadataSourceLink } from '../util/metadataSource'
import { btn, btnSize } from '../components/buttons'
import Switch from '../components/Switch'
import CoverPlaceholder from '../components/CoverPlaceholder'
import MoreMenu from '../components/MoreMenu'
import Section from '../components/Section'

type MediaFilter = '' | 'ebook' | 'audiobook'
// 'excluded' folds in what used to be a separate "Show excluded" checkbox. It
// is a status like any other from the user's point of view, and as a checkbox
// sitting outside the chip groups it read as belonging to whichever group it
// happened to wrap next to.
type StatusFilter = '' | 'wanted' | 'downloading' | 'downloaded' | 'imported' | 'skipped' | 'excluded'
type PublishedFilter = '' | 'released' | 'upcoming'
type DateSort = 'none' | 'asc' | 'desc'

const STATUS_FILTERS: readonly StatusFilter[] = [
  '', 'wanted', 'downloading', 'downloaded', 'imported', 'skipped', 'excluded',
] as const

// English fallbacks for the status options, used as t()'s default value so a
// locale missing these keys still renders words rather than key names.
const STATUS_FALLBACK: Record<Exclude<StatusFilter, ''>, string> = {
  wanted: 'Wanted',
  downloading: 'Downloading',
  downloaded: 'Downloaded',
  imported: 'Imported',
  skipped: 'Skipped',
  excluded: 'Excluded',
}

const selectCls =
  'bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded ' +
  'px-2 py-1 text-xs text-slate-800 dark:text-zinc-200 ' +
  'focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600'

const TODAY = new Date().toISOString().slice(0, 10)

function fmtPublishedYear(d?: string): string {
  if (!d) return '—'
  return d.slice(0, 4)
}


function mediaLabel(mediaType?: Book['mediaType']): string {
  if (mediaType === 'audiobook') return '🎧 Audiobook'
  if (mediaType === 'both') return '📖🎧 Both'
  return '📖 Ebook'
}

export default function AuthorDetailPage() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const location = useLocation()
  const { t } = useTranslation()
  const authorId = Number(id)

  const [author, setAuthor] = useState<Author | null>(null)
  const [books, setBooks] = useState<Book[]>([])
  const [allAuthors, setAllAuthors] = useState<Author[]>([])
  const [loading, setLoading] = useState(true)
  const [refreshing, setRefreshing] = useState(false)
  const [searchingWanted, setSearchingWanted] = useState(false)
  const [showMerge, setShowMerge] = useState(false)
  const [showEdit, setShowEdit] = useState(false)
  const [showRename, setShowRename] = useState(false)
  const [showMetadataLink, setShowMetadataLink] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // Bulk multi-select state (#791). Selection is keyed by book.id and
  // intentionally scoped to the filtered view: hidden books can't be
  // accidentally swept up by select-all.
  const [selected, setSelected] = useState<Set<number>>(new Set())
  const [bulkBusy, setBulkBusy] = useState(false)
  const selectAllRef = useRef<HTMLInputElement>(null)

  const [view, setView] = useView('author-detail', 'grid')

  // Opt-in "group by series" view (#1125). Off by default — the flat list
  // stays the default. Persisted per-page in localStorage alongside the
  // grid/table preference. When on, books are grouped under the series they
  // belong to (position-ordered) with a "Standalone" group for the rest.
  const [groupBySeries, setGroupBySeries] = useState<boolean>(() => {
    try {
      return localStorage.getItem('bindery.group.author-detail.series') === 'true'
    } catch { return false }
  })
  const [authorSeries, setAuthorSeries] = useState<Series[]>([])

  useEffect(() => {
    try { localStorage.setItem('bindery.group.author-detail.series', String(groupBySeries)) } catch { /* ignore */ }
  }, [groupBySeries])

  // Lazily load the author's series the first time grouping is switched on, so
  // the default flat view never pays for the extra round trip. Failures fall
  // back to an empty set — every book then lands in the Standalone group.
  useEffect(() => {
    if (!groupBySeries || authorSeries.length > 0) return
    let cancelled = false
    api.listAuthorSeries(authorId)
      .then(s => { if (!cancelled) setAuthorSeries(s) })
      .catch(() => { /* leave empty: books fall into Standalone */ })
    return () => { cancelled = true }
  }, [groupBySeries, authorId, authorSeries.length])

  // Filter / sort state — persisted to localStorage under page-scoped keys
  const [typeFilter, setTypeFilter] = useState<MediaFilter>(() => {
    try {
      const v = localStorage.getItem('bindery.filter.author-detail.type')
      if (v === 'ebook' || v === 'audiobook') return v
    } catch { /* ignore */ }
    return ''
  })

  const [statusFilter, setStatusFilter] = useState<StatusFilter>(() => {
    try {
      const v = localStorage.getItem('bindery.filter.author-detail.status')
      if (v && v !== '' && (STATUS_FILTERS as readonly string[]).includes(v)) return v as StatusFilter
    } catch { /* ignore */ }
    return ''
  })

  // Excluded books are omitted by the API unless asked for, so the status
  // filter has to reach the request, not just the client-side filter.
  const showExcluded = statusFilter === 'excluded'

  const [publishedFilter, setPublishedFilter] = useState<PublishedFilter>(() => {
    try {
      const v = localStorage.getItem('bindery.filter.author-detail.published')
      if (v === 'released' || v === 'upcoming') return v
    } catch { /* ignore */ }
    return ''
  })

  const [dateSort, setDateSort] = useState<DateSort>(() => {
    try {
      const v = localStorage.getItem('bindery.sort.author-detail.date')
      if (v === 'asc' || v === 'desc') return v
    } catch { /* ignore */ }
    return 'none'
  })

  useEffect(() => {
    try { localStorage.setItem('bindery.filter.author-detail.type', typeFilter) } catch { /* ignore */ }
  }, [typeFilter])

  useEffect(() => {
    try { localStorage.setItem('bindery.filter.author-detail.status', statusFilter) } catch { /* ignore */ }
  }, [statusFilter])

  useEffect(() => {
    try { localStorage.setItem('bindery.filter.author-detail.published', publishedFilter) } catch { /* ignore */ }
  }, [publishedFilter])

  useEffect(() => {
    try { localStorage.setItem('bindery.sort.author-detail.date', dateSort) } catch { /* ignore */ }
  }, [dateSort])

  useEffect(() => {
    if (author?.authorName) {
      document.title = `${author.authorName} · Bindery`
      return () => { document.title = 'Bindery' }
    }
  }, [author?.authorName])

  useEffect(() => {
    let cancelled = false
    setLoading(true)
    // listAllBooks pages through the server until the author's complete
    // catalogue is loaded — a plain listBooks call silently capped the list at
    // the server default of 100, corrupting counts/filters/select-all (#1467).
    Promise.all([
      api.getAuthor(authorId),
      api.listAllBooks({ authorId, includeExcluded: showExcluded }),
    ])
      .then(([a, bs]) => { if (!cancelled) { setAuthor(a); setBooks(bs) } })
      .catch(err => setError(err instanceof Error ? err.message : 'Failed to load'))
      .finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, [authorId, showExcluded])

  useEffect(() => {
    const params = new URLSearchParams(location.search)
    if (params.get('linkMetadata') !== '1') {
      return
    }
    setShowMetadataLink(true)
    params.delete('linkMetadata')
    const search = params.toString()
    navigate(
      {
        pathname: location.pathname,
        search: search ? `?${search}` : '',
        hash: location.hash,
      },
      { replace: true },
    )
  }, [location.hash, location.pathname, location.search, navigate])

  const handleRefresh = async () => {
    if (!author) return
    setRefreshing(true)
    try {
      await api.refreshAuthor(author.id)
      const [a, bs] = await Promise.all([api.getAuthor(authorId), api.listAllBooks({ authorId, includeExcluded: showExcluded })])
      setAuthor(a)
      setBooks(bs)
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Refresh failed')
    } finally {
      setRefreshing(false)
    }
  }

  const handleToggleMonitored = async () => {
    if (!author) return
    try {
      await api.updateAuthor(author.id, { monitored: !author.monitored })
      setAuthor({ ...author, monitored: !author.monitored })
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Update failed')
    }
  }

  const handleSearchWanted = async () => {
    if (!author) return
    const searchableWantedCount = books.filter(b => b.status === 'wanted' && b.monitored && !b.excluded).length
    if (searchableWantedCount === 0) return
    setSearchingWanted(true)
    setError(null)
    try {
      const res = await api.searchAuthorWanted(author.id)
      const item = res.results[String(author.id)]
      if (item && !item.ok) {
        throw new Error(item.error || 'Search failed')
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Search failed')
    } finally {
      setSearchingWanted(false)
    }
  }

  const handleDelete = async () => {
    if (!author) return
    const withFiles = books.filter(b => b.filePath)
    const msg = withFiles.length > 0
      ? `Delete ${author.authorName}, all ${books.length} book(s), AND ${withFiles.length} file(s)/folder(s) on disk?\n\nThis cannot be undone.`
      : `Delete ${author.authorName} and all ${books.length} book(s)?`
    if (!confirm(msg)) return
    try {
      await api.deleteAuthor(author.id, withFiles.length > 0)
      navigate('/')
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Delete failed')
    }
  }

  const handleDeleteAlias = async (alias: AuthorAlias) => {
    if (!author) return
    if (!confirm(t('authorDetail.aliases.removeConfirm', { name: alias.name, author: author.authorName }))) return
    setError(null)
    try {
      await api.deleteAuthorAlias(author.id, alias.id)
      setAuthor(current => current ? {
        ...current,
        aliases: (current.aliases ?? []).filter(a => a.id !== alias.id),
      } : current)
    } catch (e) {
      setError(t('authorDetail.aliases.removeFailed', {
        error: e instanceof Error ? e.message : String(e),
      }))
    }
  }

  const toggleSelect = (bookId: number) => {
    setSelected(prev => {
      const next = new Set(prev)
      if (next.has(bookId)) next.delete(bookId)
      else next.add(bookId)
      return next
    })
  }

  const clearSelection = () => setSelected(new Set())

  // reloadBooks refetches the author's books without clobbering loading state —
  // used after a bulk action to reflect changes (e.g. exclude hides rows
  // unless showExcluded is on; delete removes them outright).
  const reloadBooks = async () => {
    try {
      const items = await api.listAllBooks({ authorId, includeExcluded: showExcluded })
      setBooks(items)
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Reload failed')
    }
  }

  // runBulk routes a multi-select action through the existing /book/bulk
  // endpoint. The handler returns 200 with per-id outcomes even when some
  // rows fail (stale IDs, missing books) — surface the first error inline
  // so the user knows partial success happened without burying it.
  const runBulk = async (action: BookBulkAction, actionLabel: string, confirmMsg?: string) => {
    if (selected.size === 0) return
    if (confirmMsg && !confirm(confirmMsg)) return
    setBulkBusy(true)
    setError(null)
    try {
      const ids = Array.from(selected)
      const res = await api.bulkActionBooks(ids, action)
      let okCount = 0
      let firstError = ''
      for (const id of ids) {
        const r = res.results[String(id)]
        if (r?.ok) {
          okCount++
        } else if (!firstError) {
          firstError = r?.error || 'unknown error'
        }
      }
      if (okCount < ids.length) {
        setError(t('authorDetail.bulk.partial', {
          action: actionLabel,
          ok: okCount,
          total: ids.length,
          error: firstError,
        }))
      }
      clearSelection()
      await reloadBooks()
    } catch (e) {
      setError(t('authorDetail.bulk.failed', {
        action: actionLabel,
        error: e instanceof Error ? e.message : String(e),
      }))
    } finally {
      setBulkBusy(false)
    }
  }

  const filteredBooks = useMemo(() => {
    let list = books
    // A 'both' book carries the selected format too, so it matches either
    // type chip — an exact mediaType comparison made dual-format books vanish
    // from both Type: Ebook and Type: Audiobook (#1406).
    if (typeFilter) {
      list = list.filter(b => {
        const mt = b.mediaType || 'ebook'
        return mt === typeFilter || mt === 'both'
      })
    }
    if (statusFilter) {
      // For a 'both' book under an active type filter, judge the selected
      // format rather than the aggregate status: the aggregate stays 'wanted'
      // until BOTH formats are on disk, which hid a book whose ebook was
      // already imported from Type: Ebook + Status: Imported (#1406). The
      // per-format file path is the format-scoped truth for 'imported'; when
      // the selected format has no file yet, the aggregate still supplies
      // 'wanted' and the in-flight states (which have no per-format field).
      const statusOf = (b: Book): string => {
        if (!typeFilter || (b.mediaType || 'ebook') !== 'both') return b.status
        const path = typeFilter === 'ebook' ? b.ebookFilePath : b.audiobookFilePath
        return path ? 'imported' : b.status
      }
      // "Wanted" means genuinely wanted: monitored AND status=wanted. An
      // unmonitored book can carry a stale `wanted` status (#1173) but is not
      // actually wanted, so it must be excluded — mirroring the backend's
      // BookListFilter ("wanted" additionally requires monitored=1) and the
      // monitored-aware status badge.
      list = statusFilter === 'wanted'
        ? list.filter(b => statusOf(b) === 'wanted' && b.monitored)
        // 'excluded' is a flag on the book rather than a value of `status`, so
        // it can't go through statusOf. The request already asked for excluded
        // books; this narrows the view to just them.
        : statusFilter === 'excluded'
          ? list.filter(b => b.excluded)
          : list.filter(b => statusOf(b) === statusFilter)
    }
    if (publishedFilter === 'released') {
      list = list.filter(b => !b.releaseDate || b.releaseDate.slice(0, 10) <= TODAY)
    } else if (publishedFilter === 'upcoming') {
      list = list.filter(b => !!b.releaseDate && b.releaseDate.slice(0, 10) > TODAY)
    }
    if (dateSort !== 'none') {
      list = [...list].sort((a, b) => {
        const da = a.releaseDate ? new Date(a.releaseDate).getTime() : 0
        const db = b.releaseDate ? new Date(b.releaseDate).getTime() : 0
        return dateSort === 'asc' ? da - db : db - da
      })
    }
    return list
  }, [books, typeFilter, statusFilter, publishedFilter, dateSort])

  // Build the grouped-by-series sections from the author's series membership
  // joined against the filtered book set (#1125). Each section lists its books
  // ordered by position-in-series; any filtered book not in a series collects
  // in a trailing "Standalone" group. A book in multiple series appears under
  // each — matching how the series page presents membership.
  const seriesGroups = useMemo(() => {
    const byId = new Map(filteredBooks.map(b => [b.id, b]))
    const grouped: { key: string; title: string; books: Book[] }[] = []
    const placed = new Set<number>()
    for (const series of authorSeries) {
      const entries = [...(series.books ?? [])].sort((a, b) => {
        const posA = parseFloat(a.positionInSeries) || 0
        const posB = parseFloat(b.positionInSeries) || 0
        return posA - posB
      })
      const books: Book[] = []
      for (const entry of entries) {
        const book = byId.get(entry.bookId)
        if (!book) continue
        books.push(book)
        placed.add(book.id)
      }
      if (books.length > 0) grouped.push({ key: `series-${series.id}`, title: series.title, books })
    }
    const standalone = filteredBooks.filter(b => !placed.has(b.id))
    if (standalone.length > 0) grouped.push({ key: 'standalone', title: 'Standalone', books: standalone })
    return grouped
  }, [authorSeries, filteredBooks])

  // Drop any selected IDs that are no longer in the filtered view so the
  // bulk bar count never lies about what's about to be acted on.
  const visibleIds = useMemo(() => new Set(filteredBooks.map(b => b.id)), [filteredBooks])
  useEffect(() => {
    setSelected(prev => {
      let changed = false
      const next = new Set<number>()
      for (const id of prev) {
        if (visibleIds.has(id)) next.add(id)
        else changed = true
      }
      return changed ? next : prev
    })
  }, [visibleIds])

  const allVisibleSelected = filteredBooks.length > 0 && filteredBooks.every(b => selected.has(b.id))
  const someVisibleSelected = filteredBooks.some(b => selected.has(b.id)) && !allVisibleSelected
  useEffect(() => {
    if (selectAllRef.current) selectAllRef.current.indeterminate = someVisibleSelected
  }, [someVisibleSelected])

  const selectAllVisible = () => setSelected(new Set(filteredBooks.map(b => b.id)))

  if (loading) return <div className="text-slate-600 dark:text-zinc-500">Loading…</div>
  if (!author) return <div className="text-slate-600 dark:text-zinc-500">Author not found</div>

  const searchableWantedCount = books.filter(b => b.status === 'wanted' && b.monitored && !b.excluded).length
  const showMetadataLinkAction = canLinkAuthorMetadata(author) || hasSparseMetadata(author)
  const metadataLinkLabel = canLinkAuthorMetadata(author)
    ? t('authorMetadataLink.actionLink', 'Link metadata')
    : t('authorMetadataLink.actionFindBetter', 'Find better metadata')
  const counts = {
    total: books.length,
    imported: books.filter(b => b.status === 'imported').length,
    wanted: searchableWantedCount,
    audiobook: books.filter(b => b.mediaType === 'audiobook').length,
  }

  const toggleDateSort = () =>
    setDateSort(prev => prev === 'none' ? 'asc' : prev === 'asc' ? 'desc' : 'none')

  const dateSortIcon = dateSort === 'asc' ? ' ↑' : dateSort === 'desc' ? ' ↓' : ''

  // Render helpers shared by the flat and grouped-by-series (#1125) layouts so
  // each series section and the standalone group reuse the exact same table
  // rows / grid cards instead of duplicating the markup.
  const renderTableRows = (list: Book[]) =>
    list.map(book => (
      <tr
        key={book.id}
        className={`${selected.has(book.id) ? 'bg-emerald-500/10 dark:bg-emerald-500/10' : 'bg-slate-100/50 dark:bg-zinc-900/50'} hover:bg-slate-200/50 dark:hover:bg-zinc-800/50 cursor-pointer`}
        // Client-side, matching the <Link> in this same row. The row used to do
        // a full page reload while the link inside it routed client-side, so
        // one row had two different navigation behaviours.
        onClick={() => navigate(`/book/${book.id}`)}
      >
        <td className="px-3 py-2 w-10 align-middle" onClick={e => e.stopPropagation()}>
          <input
            type="checkbox"
            checked={selected.has(book.id)}
            onChange={() => toggleSelect(book.id)}
            className="rounded border-slate-400 dark:border-zinc-600 text-emerald-500 focus:ring-emerald-500 focus:ring-offset-0"
            aria-label={`Select ${book.title}`}
          />
        </td>
        <td className="px-3 py-2 align-middle">
          <Link to={`/book/${book.id}`} className="flex items-center gap-2 min-w-0" onClick={e => e.stopPropagation()}>
            {book.imageUrl ? (
              <img src={book.imageUrl} alt="" className="w-6 h-9 object-cover rounded flex-shrink-0" />
            ) : (
              <CoverPlaceholder
                id={book.id}
                title={book.title}
                size="xs"
                className="w-6 h-9 rounded flex-shrink-0"
              />
            )}
            <span className="min-w-0 flex-1">
              <span className="block text-slate-800 dark:text-zinc-200 truncate">{book.title}</span>
              <span className="mt-1 flex flex-wrap items-center gap-1 sm:hidden">
                {(() => {
                  const badge = bookStatusBadge(book.status, book.monitored, t)
                  return (
                    <span className={`inline-block px-1.5 py-0.5 rounded text-[10px] font-medium ${badge.colorClass}`}>
                      {badge.label}
                    </span>
                  )
                })()}
                <span className="inline-block px-1.5 py-0.5 rounded text-[10px] font-medium bg-slate-200 dark:bg-zinc-800 text-slate-600 dark:text-zinc-400">
                  {mediaLabel(book.mediaType)}
                </span>
                <span className="inline-block px-1.5 py-0.5 rounded text-[10px] font-medium bg-slate-200 dark:bg-zinc-800 text-slate-600 dark:text-zinc-400">
                  {fmtPublishedYear(book.releaseDate)}
                </span>
                {book.excluded && (
                  <span className="inline-block px-1.5 py-0.5 rounded text-[10px] font-medium bg-amber-500/20 text-amber-700 dark:text-amber-400">
                    Excluded
                  </span>
                )}
              </span>
            </span>
          </Link>
        </td>
        <td className="hidden sm:table-cell px-3 py-2 text-slate-600 dark:text-zinc-400 whitespace-nowrap align-middle">{fmtPublishedYear(book.releaseDate)}</td>
        <td className="hidden sm:table-cell px-3 py-2 text-xs whitespace-nowrap align-middle">
          {mediaLabel(book.mediaType)}
        </td>
        <td className="hidden sm:table-cell px-3 py-2 whitespace-nowrap align-middle">
          {(() => {
            const badge = bookStatusBadge(book.status, book.monitored, t)
            return (
              <span className={`inline-block px-2 py-0.5 rounded text-[10px] font-medium ${badge.colorClass}`}>
                {badge.label}
              </span>
            )
          })()}
          {book.excluded && (
            <span className="inline-block ml-1 px-2 py-0.5 rounded text-[10px] font-medium bg-amber-500/20 text-amber-700 dark:text-amber-400">
              Excluded
            </span>
          )}
        </td>
      </tr>
    ))

  const renderTable = (list: Book[], withHeaderControls: boolean) => (
    <div className="border border-slate-200 dark:border-zinc-800 rounded-lg overflow-hidden">
      <div className="overflow-x-auto">
        <table className="w-full table-fixed text-sm">
          <thead>
            <tr className="bg-slate-100 dark:bg-zinc-900 border-b border-slate-200 dark:border-zinc-800">
              <th className="w-10 px-3 py-2">
                {withHeaderControls && (
                  <input
                    ref={selectAllRef}
                    type="checkbox"
                    checked={allVisibleSelected}
                    onChange={e => e.target.checked ? selectAllVisible() : clearSelection()}
                    className="rounded border-slate-400 dark:border-zinc-600 text-emerald-500 focus:ring-emerald-500 focus:ring-offset-0"
                    title={t('common.selectAllPage')}
                    aria-label={t('common.selectAllPage')}
                  />
                )}
              </th>
              <th className="w-full sm:w-[46%] text-left px-3 py-2 text-xs font-medium text-slate-600 dark:text-zinc-400 uppercase">Title</th>
              <th
                className="hidden sm:table-cell sm:w-28 text-left px-3 py-2 text-xs font-medium text-slate-600 dark:text-zinc-400 uppercase cursor-pointer select-none hover:text-slate-900 dark:hover:text-white whitespace-nowrap"
                onClick={toggleDateSort}
                title="Sort by publication date"
              >
                Published{dateSortIcon}
              </th>
              <th className="hidden sm:table-cell sm:w-36 text-left px-3 py-2 text-xs font-medium text-slate-600 dark:text-zinc-400 uppercase">Type</th>
              <th className="hidden sm:table-cell sm:w-36 text-left px-3 py-2 text-xs font-medium text-slate-600 dark:text-zinc-400 uppercase">Status</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-200 dark:divide-zinc-800">
            {renderTableRows(list)}
          </tbody>
        </table>
      </div>
    </div>
  )

  const renderGrid = (list: Book[]) => (
    <div className="grid grid-cols-2 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-5 gap-4">
      {list.map(book => (
        <div
          key={book.id}
          className={`relative border rounded-lg bg-slate-100 dark:bg-zinc-900 overflow-hidden group transition-colors ${selected.has(book.id) ? 'border-emerald-500' : 'border-slate-200 dark:border-zinc-800 hover:border-emerald-500'}`}
        >
          <input
            type="checkbox"
            checked={selected.has(book.id)}
            onChange={() => toggleSelect(book.id)}
            onClick={e => e.stopPropagation()}
            className={`absolute top-2 left-2 z-10 rounded border-slate-400 dark:border-zinc-600 text-emerald-500 focus:ring-emerald-500 focus:ring-offset-0 ${selected.has(book.id) ? '' : 'bg-white/80 dark:bg-zinc-900/80'}`}
            aria-label={`Select ${book.title}`}
          />
          <Link to={`/book/${book.id}`} className="block">
            <div className="aspect-[2/3] bg-slate-200 dark:bg-zinc-800 relative">
              {book.imageUrl ? (
                <img src={book.imageUrl} alt={book.title} className="w-full h-full object-cover" />
              ) : (
                <CoverPlaceholder id={book.id} title={book.title} size="sm" className="w-full h-full" />
              )}
            </div>
            {/* Fixed height. The badge row and the year are both variable, so
                cards in the same row used to end at different heights and the
                grid went ragged. */}
            <div className="p-2 h-[68px] flex flex-col">
              <h4 className="text-xs font-medium truncate" title={book.title}>{book.title}</h4>
              <div className="flex items-center gap-1 mt-1 flex-wrap overflow-hidden">
                {(() => {
                  const badge = bookStatusBadge(book.status, book.monitored, t)
                  return (
                    <span className={`px-1.5 py-0.5 rounded text-[10px] font-medium ${badge.colorClass}`}>
                      {badge.label}
                    </span>
                  )
                })()}
                {book.mediaType === 'audiobook' && (
                  <span className="px-1.5 py-0.5 rounded text-[10px] font-medium bg-indigo-100 text-indigo-800 dark:bg-indigo-950 dark:text-indigo-300">🎧 Audio</span>
                )}
                {book.excluded && (
                  <span className="px-1.5 py-0.5 rounded text-[10px] font-medium bg-amber-500/20 text-amber-700 dark:text-amber-400">Excluded</span>
                )}
              </div>
              {/* No `book.releaseDate &&` guard: fmtPublishedYear already
                  returns an em dash for a missing date, so the conditional only
                  made the row appear and disappear between cards. */}
              <p className="text-[10px] text-slate-600 dark:text-zinc-500 mt-auto">{fmtPublishedYear(book.releaseDate)}</p>
            </div>
          </Link>
        </div>
      ))}
    </div>
  )

  const renderBooks = (list: Book[], withHeaderControls = true) =>
    view === 'table' ? renderTable(list, withHeaderControls) : renderGrid(list)

  return (
    // One width shared with BookDetailPage — see the note there.
    <div className={`max-w-7xl ${selected.size > 0 ? 'pb-20' : ''}`}>
      <div className="mb-4 flex items-center gap-3 text-sm">
        <button onClick={() => navigate(-1)} className="text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white">← Back</button>
      </div>

      <div className="flex flex-col sm:flex-row gap-6 mb-8">
        <div className="w-32 flex-shrink-0">
          {author.imageUrl ? (
            <img src={author.imageUrl} alt={author.authorName} className="w-full rounded-full shadow-lg aspect-square object-cover" />
          ) : (
            <div className="aspect-square rounded-full bg-slate-200 dark:bg-zinc-800 flex items-center justify-center text-2xl font-bold text-slate-500 dark:text-zinc-600">
              {author.authorName.charAt(0).toUpperCase()}
            </div>
          )}
        </div>
        <div className="min-w-0 flex-1">
          <h2 className="text-2xl font-bold mb-1">{author.authorName}</h2>
          {author.disambiguation && (
            <p className="text-xs text-slate-600 dark:text-zinc-500">{author.disambiguation}</p>
          )}
          <div className="flex flex-wrap gap-x-4 gap-y-1 mt-2 text-xs text-slate-600 dark:text-zinc-500">
            {author.averageRating > 0 && (
              <span>★ {author.averageRating.toFixed(2)} ({author.ratingsCount.toLocaleString()} ratings)</span>
            )}
            {(() => {
              const src = metadataSourceLink(author.foreignAuthorId, 'author')
              return src ? (
                <a
                  href={src.url}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="text-emerald-600 dark:text-emerald-400 hover:underline"
                >
                  {t('common.viewOnSource', { source: src.label, defaultValue: 'View on {{source}} ↗' })}
                </a>
              ) : null
            })()}
          </div>

          {/* Four fixed cells, replacing a run-on sentence. The audiobook count
              used to vanish at zero, so the row changed shape between authors
              and the numbers never sat in the same place twice. */}
          <dl
            data-testid="author-stats"
            className="grid grid-cols-4 gap-px mt-3 rounded-lg overflow-hidden border border-slate-200 dark:border-zinc-800 bg-slate-200 dark:bg-zinc-800 max-w-lg"
          >
            {[
              { key: 'books', label: t('authorDetail.stats.books', 'Books'), value: counts.total },
              { key: 'inLibrary', label: t('authorDetail.stats.inLibrary', 'In library'), value: counts.imported },
              { key: 'wanted', label: t('authorDetail.stats.wanted', 'Wanted'), value: counts.wanted },
              { key: 'audiobooks', label: t('authorDetail.stats.audiobooks', 'Audiobooks'), value: counts.audiobook },
            ].map(cell => (
              <div key={cell.key} className="bg-slate-100 dark:bg-zinc-900 px-3 py-2">
                <dt className="text-[11px] text-slate-600 dark:text-zinc-500">{cell.label}</dt>
                <dd className="text-lg font-semibold tabular-nums text-slate-900 dark:text-white">
                  {cell.value}
                </dd>
              </div>
            ))}
          </dl>
          {author.description && (
            <MarkdownDescription
              text={author.description}
              showMoreLabel={t('authorDetail.description.showMore', 'Show more')}
              showLessLabel={t('authorDetail.description.showLess', 'Show less')}
              className="mt-3 max-w-prose"
            />
          )}
          <div className="flex flex-wrap gap-2 mt-4">
            <Switch
              checked={author.monitored}
              onChange={handleToggleMonitored}
              label={author.monitored ? t('authors.stopMonitoring', 'Stop monitoring') : t('authors.startMonitoring', 'Monitor')}
              className="px-1"
            >
              {author.monitored ? t('authors.monitored') : t('authors.unmonitored')}
            </Switch>
            {/* One primary action, carrying its own count, so it doesn't need a
                tooltip to say how much work it will do. */}
            <button
              onClick={handleSearchWanted}
              disabled={searchingWanted || searchableWantedCount === 0}
              className={`${btn.primary} ${btnSize.sm}`}
              title={searchableWantedCount === 0 ? t('authorDetail.actions.searchWantedNone', 'No wanted books to search') : undefined}
            >
              {searchingWanted
                ? t('authorDetail.actions.searching', 'Searching…')
                : t('authorDetail.actions.searchWanted', { count: searchableWantedCount, defaultValue: 'Search {{count}} wanted' })}
            </button>
            <button
              onClick={handleRefresh}
              disabled={refreshing}
              className={`${btn.secondary} ${btnSize.sm}`}
            >
              {refreshing ? t('authorDetail.actions.refreshing', 'Refreshing…') : t('authorDetail.actions.refresh', 'Refresh')}
            </button>
            <button
              onClick={() => setShowEdit(true)}
              className={`${btn.secondary} ${btnSize.sm}`}
              title={t('authorDetail.actions.editHint', 'Edit quality, metadata, and root folder')}
            >
              {t('authorDetail.actions.edit', 'Edit')}
            </button>
            {/* The rest were eight controls on one row, which wrapped and left
                Delete orphaned on a line of its own — the most destructive
                action given the most prominence by accident. */}
            <MoreMenu
              label={t('common.more', 'More')}
              buttonClassName={`${btn.secondary} ${btnSize.sm}`}
              items={[
                {
                  label: t('authorDetail.actions.renameFiles', 'Rename files'),
                  title: t('authorDetail.actions.renameFilesHint', 'Move this author’s files to match the current naming template'),
                  onSelect: () => setShowRename(true),
                },
                {
                  label: t('authorDetail.actions.merge', 'Merge…'),
                  title: t('authorDetail.actions.mergeHint', 'Merge another author into this one'),
                  onSelect: () => {
                    if (allAuthors.length === 0) api.listAllAuthors().then(setAllAuthors).catch(console.error)
                    setShowMerge(true)
                  },
                },
                ...(showMetadataLinkAction
                  ? [{ label: metadataLinkLabel, onSelect: () => setShowMetadataLink(true) }]
                  : []),
                {
                  label: t('authorDetail.actions.delete', 'Delete'),
                  danger: true,
                  onSelect: handleDelete,
                },
              ]}
            />
          </div>
          {author.aliases && author.aliases.length > 0 && (
            <div className="mt-4 text-xs">
              <div className="text-slate-600 dark:text-zinc-500 mb-1">Also known as</div>
              <div className="flex flex-wrap gap-1.5">
                {author.aliases.map(a => (
                  <span
                    key={a.id}
                    className="group/alias inline-flex items-center gap-1 px-2 py-0.5 rounded bg-slate-200 dark:bg-zinc-800 text-slate-700 dark:text-zinc-300"
                    title={a.sourceOlId ? `From ${a.sourceOlId}` : undefined}
                  >
                    <span>{a.name}</span>
                    <button
                      type="button"
                      onClick={() => handleDeleteAlias(a)}
                      className="ml-0.5 leading-none text-slate-500 hover:text-red-600 focus:text-red-600 focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-red-500 dark:text-zinc-500 dark:hover:text-red-400 dark:focus:text-red-400 opacity-100 transition-opacity [@media(hover:hover)]:opacity-0 [@media(hover:hover)]:group-hover/alias:opacity-100 [@media(hover:hover)]:focus:opacity-100 [@media(hover:hover)]:focus-visible:opacity-100"
                      aria-label={t('authorDetail.aliases.removeLabel', { name: a.name })}
                      title={t('authorDetail.aliases.removeLabel', { name: a.name })}
                    >
                      ×
                    </button>
                  </span>
                ))}
              </div>
            </div>
          )}
        </div>
      </div>

      {showEdit && (
        <EditAuthorModal
          author={author}
          onClose={() => setShowEdit(false)}
          onSaved={updated => setAuthor(updated)}
        />
      )}

      {showRename && (
        <RenameFilesModal
          scope="author"
          id={author.id}
          label={author.authorName}
          onClose={() => setShowRename(false)}
          onApplied={() => api.listAllBooks({ authorId, includeExcluded: showExcluded }).then(setBooks).catch(() => {})}
        />
      )}

      {showMetadataLink && (
        <AuthorMetadataLinkModal
          author={author}
          onClose={() => setShowMetadataLink(false)}
          onLinked={updated => {
            setAuthor(updated)
            Promise.all([api.getAuthor(authorId), api.listAllBooks({ authorId, includeExcluded: showExcluded })])
              .then(([a, bs]) => { setAuthor(a); setBooks(bs) })
              .catch(console.error)
          }}
        />
      )}

      {showMerge && allAuthors.length > 0 && (
        <MergeAuthorsModal
          authors={allAuthors}
          initialTargetId={author.id}
          onClose={() => setShowMerge(false)}
          onMerged={() => {
            // Reload current author (aliases may have grown) + its books.
            Promise.all([api.getAuthor(authorId), api.listAllBooks({ authorId })])
              .then(([a, bs]) => { setAuthor(a); setBooks(bs) })
              .catch(console.error)
          }}
        />
      )}

      {error && (
        <div className="mb-4 px-3 py-2 bg-red-100 dark:bg-red-950/30 border border-red-300 dark:border-red-900 rounded text-sm text-red-800 dark:text-red-300">
          {error}
        </div>
      )}

      <Section
        bare
        title={
          <>
            {t('authorDetail.booksHeading', 'Books')}
            {filteredBooks.length !== books.length && (
              <span className="ml-2 text-sm font-normal text-slate-600 dark:text-zinc-500">
                {t('authorDetail.filteredCount', {
                  shown: filteredBooks.length,
                  total: books.length,
                  defaultValue: '{{shown}} of {{total}}',
                })}
              </span>
            )}
          </>
        }
        actions={
          <>
            <Switch
              checked={groupBySeries}
              onChange={() => setGroupBySeries(v => !v)}
              label={groupBySeries ? t('authorDetail.showFlatList', 'Show flat list') : t('authorDetail.groupBySeries', 'Group by series')}
            >
              {t('authorDetail.groupBySeries', 'Group by series')}
            </Switch>
            <ViewToggle view={view} onChange={setView} />
          </>
        }
      >
        {/* Three selects on one line, replacing three labelled chip groups
            totalling ten buttons plus an ml-auto "Select all" that wrapped to a
            second row and read as if it belonged to the Published group. The
            selects also expose `downloading` and `skipped`, which have been in
            StatusFilter all along but were never offered. */}
        {books.length > 0 && (
          <div className="flex flex-wrap items-center gap-3 mb-4">
            <label className="flex items-center gap-1.5 text-xs text-slate-600 dark:text-zinc-500">
              {t('authorDetail.filters.type', 'Type')}
              <select
                value={typeFilter}
                onChange={e => setTypeFilter(e.target.value as MediaFilter)}
                className={selectCls}
              >
                <option value="">{t('authorDetail.filters.allTypes', 'All types')}</option>
                <option value="ebook">📖 {t('common.ebook')}</option>
                <option value="audiobook">🎧 {t('common.audiobook')}</option>
              </select>
            </label>

            <label className="flex items-center gap-1.5 text-xs text-slate-600 dark:text-zinc-500">
              {t('authorDetail.filters.status', 'Status')}
              <select
                value={statusFilter}
                onChange={e => setStatusFilter(e.target.value as StatusFilter)}
                className={selectCls}
              >
                {STATUS_FILTERS.map(s => (
                  <option key={s || 'all'} value={s}>
                    {s === ''
                      ? t('authorDetail.filters.allStatuses', 'All statuses')
                      : t(`authorDetail.filters.status_${s}`, STATUS_FALLBACK[s])}
                  </option>
                ))}
              </select>
            </label>

            <label className="flex items-center gap-1.5 text-xs text-slate-600 dark:text-zinc-500">
              {t('authorDetail.filters.published', 'Published')}
              <select
                value={publishedFilter}
                onChange={e => setPublishedFilter(e.target.value as PublishedFilter)}
                className={selectCls}
              >
                <option value="">{t('authorDetail.filters.allPublished', 'Any date')}</option>
                <option value="released">{t('authorDetail.filters.released', 'Released')}</option>
                <option value="upcoming">{t('authorDetail.filters.upcoming', 'Upcoming')}</option>
              </select>
            </label>

            {/* Select/deselect every currently displayed book (#1172). Operates on
                filteredBooks, so it respects the active filters and composes
                with the per-book checkboxes used by the bulk action bar. */}
            {filteredBooks.length > 0 && (
              <button
                onClick={() => allVisibleSelected ? clearSelection() : selectAllVisible()}
                className={`${btn.ghost} ${btnSize.sm} ml-auto`}
                aria-pressed={allVisibleSelected}
              >
                {allVisibleSelected ? t('authorDetail.bulk.deselectAll') : t('authorDetail.bulk.selectAll')}
              </button>
            )}
          </div>
        )}

        {books.length === 0 ? (
          <p className="text-sm text-slate-600 dark:text-zinc-500">No books tracked for this author yet.</p>
        ) : filteredBooks.length === 0 ? (
          <p className="text-sm text-slate-600 dark:text-zinc-500">No books match the current filters.</p>
        ) : groupBySeries ? (
          <div className="space-y-6">
            {seriesGroups.map(group => (
              <div key={group.key}>
                <h4 className="text-sm font-semibold text-slate-700 dark:text-zinc-300 mb-2 flex items-center gap-2">
                  <span>{group.title}</span>
                  <span className="text-xs font-normal text-slate-500 dark:text-zinc-500 bg-slate-200 dark:bg-zinc-800 px-2 py-0.5 rounded-full">
                    {group.books.length} {group.books.length === 1 ? 'book' : 'books'}
                  </span>
                </h4>
                {renderBooks(group.books, false)}
              </div>
            ))}
          </div>
        ) : (
          renderBooks(filteredBooks)
        )}
      </Section>

      <BulkActionBar
        count={selected.size}
        onClear={clearSelection}
        busy={bulkBusy}
        actions={[
          { label: t('authorDetail.bulk.monitor'), onClick: () => runBulk('monitor', t('authorDetail.bulk.monitor')) },
          { label: t('authorDetail.bulk.unmonitor'), onClick: () => runBulk('unmonitor', t('authorDetail.bulk.unmonitor')) },
          {
            label: t('authorDetail.bulk.exclude'),
            variant: 'caution',
            onClick: () => runBulk('exclude', t('authorDetail.bulk.exclude'), t('authorDetail.bulk.excludeConfirm', { count: selected.size })),
          },
          {
            label: t('authorDetail.bulk.delete'),
            variant: 'danger',
            onClick: () => runBulk('delete', t('authorDetail.bulk.delete'), t('authorDetail.bulk.deleteConfirm', { count: selected.size })),
          },
        ]}
      />
    </div>
  )
}
