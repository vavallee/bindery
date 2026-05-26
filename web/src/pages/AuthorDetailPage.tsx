import { useEffect, useMemo, useRef, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { api, Author, Book, BookBulkAction } from '../api/client'
import ViewToggle from '../components/ViewToggle'
import MergeAuthorsModal from '../components/MergeAuthorsModal'
import EditAuthorModal from '../components/EditAuthorModal'
import BulkActionBar from '../components/BulkActionBar'
import { useView } from '../components/useView'

const statusColors: Record<string, string> = {
  wanted: 'bg-amber-500/20 text-amber-700 dark:text-amber-400',
  downloading: 'bg-blue-500/20 text-blue-700 dark:text-blue-400',
  downloaded: 'bg-purple-500/20 text-purple-700 dark:text-purple-400',
  imported: 'bg-emerald-500/20 text-emerald-700 dark:text-emerald-400',
  skipped: 'bg-slate-300 dark:bg-zinc-700 text-slate-600 dark:text-zinc-400',
}

const fallbackStatusColor = 'bg-slate-300 dark:bg-zinc-700 text-slate-600 dark:text-zinc-400'

const statusLabel: Record<string, string> = {
  wanted: 'Wanted',
  downloading: 'Downloading',
  downloaded: 'Downloaded',
  imported: 'In Library',
  skipped: 'Skipped',
}

type MediaFilter = '' | 'ebook' | 'audiobook'
type StatusFilter = '' | 'wanted' | 'downloading' | 'downloaded' | 'imported' | 'skipped'
type PublishedFilter = '' | 'released' | 'upcoming'
type DateSort = 'none' | 'asc' | 'desc'

const TODAY = new Date().toISOString().slice(0, 10)

function fmtPublishedYear(d?: string): string {
  if (!d) return '—'
  return d.slice(0, 4)
}

function statusBadgeClass(status: string, base = 'inline-block px-2 py-0.5 rounded text-[10px] font-medium'): string {
  return `${base} ${statusColors[status] || fallbackStatusColor}`
}

function mediaLabel(mediaType?: Book['mediaType']): string {
  if (mediaType === 'audiobook') return '🎧 Audiobook'
  if (mediaType === 'both') return '📖🎧 Both'
  return '📖 Ebook'
}

export default function AuthorDetailPage() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
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
  const [showExcluded, setShowExcluded] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // Bulk multi-select state (#791). Selection is keyed by book.id and
  // intentionally scoped to the filtered view: hidden books can't be
  // accidentally swept up by select-all.
  const [selected, setSelected] = useState<Set<number>>(new Set())
  const [bulkBusy, setBulkBusy] = useState(false)
  const selectAllRef = useRef<HTMLInputElement>(null)

  const [view, setView] = useView('author-detail', 'grid')

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
      if (['wanted', 'downloading', 'downloaded', 'imported', 'skipped'].includes(v ?? '')) return v as StatusFilter
    } catch { /* ignore */ }
    return ''
  })

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
    Promise.all([
      api.getAuthor(authorId),
      api.listBooks({ authorId, includeExcluded: showExcluded }),
    ])
      .then(([a, bs]) => { if (!cancelled) { setAuthor(a); setBooks(bs) } })
      .catch(err => setError(err instanceof Error ? err.message : 'Failed to load'))
      .finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, [authorId, showExcluded])

  const handleRefresh = async () => {
    if (!author) return
    setRefreshing(true)
    try {
      await api.refreshAuthor(author.id)
      const [a, bs] = await Promise.all([api.getAuthor(authorId), api.listBooks({ authorId, includeExcluded: showExcluded })])
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
      const bs = await api.listBooks({ authorId, includeExcluded: showExcluded })
      setBooks(bs)
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
    if (typeFilter) list = list.filter(b => (b.mediaType || 'ebook') === typeFilter)
    if (statusFilter) list = list.filter(b => b.status === statusFilter)
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
  const counts = {
    total: books.length,
    imported: books.filter(b => b.status === 'imported').length,
    wanted: searchableWantedCount,
    audiobook: books.filter(b => b.mediaType === 'audiobook').length,
  }

  const chipCls = (active: boolean) =>
    `px-3 py-1 rounded-md text-xs font-medium transition-colors ${active ? 'bg-slate-300 dark:bg-zinc-700 text-slate-900 dark:text-white' : 'text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white hover:bg-slate-200/50 dark:hover:bg-zinc-800/50'}`

  const toggleDateSort = () =>
    setDateSort(prev => prev === 'none' ? 'asc' : prev === 'asc' ? 'desc' : 'none')

  const dateSortIcon = dateSort === 'asc' ? ' ↑' : dateSort === 'desc' ? ' ↓' : ''

  return (
    <div className={`max-w-5xl${selected.size > 0 ? ' pb-20' : ''}`}>
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
            <span>{counts.total} books · {counts.imported} in library · {counts.wanted} wanted{counts.audiobook ? ` · ${counts.audiobook} audiobooks` : ''}</span>
            {author.averageRating > 0 && (
              <span>★ {author.averageRating.toFixed(2)} ({author.ratingsCount.toLocaleString()} ratings)</span>
            )}
          </div>
          {author.description && (
            <p className="mt-3 text-sm text-slate-700 dark:text-zinc-300 leading-relaxed line-clamp-6">{author.description}</p>
          )}
          <div className="flex flex-wrap gap-2 mt-4">
            <button
              onClick={handleToggleMonitored}
              className={`px-3 py-1.5 rounded text-xs font-medium ${author.monitored ? 'bg-emerald-600 text-white hover:bg-emerald-500' : 'bg-slate-200 dark:bg-zinc-800 text-slate-700 dark:text-zinc-300 hover:bg-slate-300 dark:hover:bg-zinc-700'}`}
            >
              {author.monitored ? 'Monitored' : 'Not monitored'}
            </button>
            <button
              onClick={handleRefresh}
              disabled={refreshing}
              className="px-3 py-1.5 bg-slate-200 dark:bg-zinc-800 hover:bg-slate-300 dark:hover:bg-zinc-700 rounded text-xs font-medium disabled:opacity-50"
            >
              {refreshing ? 'Refreshing…' : 'Refresh metadata'}
            </button>
            <button
              onClick={handleSearchWanted}
              disabled={searchingWanted || searchableWantedCount === 0}
              className="px-3 py-1.5 bg-slate-200 dark:bg-zinc-800 hover:bg-slate-300 dark:hover:bg-zinc-700 rounded text-xs font-medium disabled:opacity-50"
              title={searchableWantedCount === 0 ? 'No wanted books to search' : `Search ${searchableWantedCount} wanted book${searchableWantedCount === 1 ? '' : 's'}`}
            >
              {searchingWanted ? 'Searching…' : 'Search all wanted'}
            </button>
            <button
              onClick={() => setShowEdit(true)}
              className="px-3 py-1.5 bg-slate-200 dark:bg-zinc-800 hover:bg-slate-300 dark:hover:bg-zinc-700 rounded text-xs font-medium"
              title="Edit quality, metadata, and root folder"
            >
              Edit
            </button>
            <button
              onClick={() => {
                if (allAuthors.length === 0) api.listAuthors().then(setAllAuthors).catch(console.error)
                setShowMerge(true)
              }}
              className="px-3 py-1.5 bg-slate-200 dark:bg-zinc-800 hover:bg-slate-300 dark:hover:bg-zinc-700 rounded text-xs font-medium"
              title="Merge another author into this one"
            >
              Merge…
            </button>
            <button
              onClick={handleDelete}
              className="px-3 py-1.5 text-red-600 dark:text-red-400 hover:text-red-500 text-xs font-medium"
            >
              Delete
            </button>
          </div>
          {author.aliases && author.aliases.length > 0 && (
            <div className="mt-4 text-xs">
              <div className="text-slate-600 dark:text-zinc-500 mb-1">Also known as</div>
              <div className="flex flex-wrap gap-1.5">
                {author.aliases.map(a => (
                  <span
                    key={a.id}
                    className="px-2 py-0.5 rounded bg-slate-200 dark:bg-zinc-800 text-slate-700 dark:text-zinc-300"
                    title={a.sourceOlId ? `From ${a.sourceOlId}` : undefined}
                  >
                    {a.name}
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

      {showMerge && allAuthors.length > 0 && (
        <MergeAuthorsModal
          authors={allAuthors}
          initialTargetId={author.id}
          onClose={() => setShowMerge(false)}
          onMerged={() => {
            // Reload current author (aliases may have grown) + its books.
            Promise.all([api.getAuthor(authorId), api.listBooks({ authorId })])
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

      <section>
        {/* Section header */}
        <div className="flex items-center justify-between mb-3">
          <h3 className="text-lg font-semibold">
            Books
            {filteredBooks.length !== books.length && (
              <span className="ml-2 text-sm font-normal text-slate-600 dark:text-zinc-500">
                {filteredBooks.length} of {books.length}
              </span>
            )}
          </h3>
          <div className="flex items-center gap-3">
            <label className="flex items-center gap-1.5 text-xs text-slate-600 dark:text-zinc-400 cursor-pointer select-none">
              <input
                type="checkbox"
                checked={showExcluded}
                onChange={e => setShowExcluded(e.target.checked)}
                className="rounded border-slate-400 dark:border-zinc-600 text-emerald-500 focus:ring-emerald-500 focus:ring-offset-0"
              />
              Show excluded
            </label>
            <ViewToggle view={view} onChange={setView} />
          </div>
        </div>

        {/* Filter chips */}
        {books.length > 0 && (
          <div className="flex gap-1 mb-4 flex-wrap">
            <span className="text-xs text-slate-600 dark:text-zinc-500 mr-1 self-center">Type:</span>
            <button onClick={() => setTypeFilter('')} className={chipCls(typeFilter === '')}>All</button>
            <button onClick={() => setTypeFilter('ebook')} className={chipCls(typeFilter === 'ebook')}>📖 Ebook</button>
            <button onClick={() => setTypeFilter('audiobook')} className={chipCls(typeFilter === 'audiobook')}>🎧 Audiobook</button>

            <span className="text-xs text-slate-600 dark:text-zinc-500 mx-2 self-center">Status:</span>
            <button onClick={() => setStatusFilter('')} className={chipCls(statusFilter === '')}>All</button>
            <button onClick={() => setStatusFilter('wanted')} className={chipCls(statusFilter === 'wanted')}>Wanted</button>
            <button onClick={() => setStatusFilter('downloaded')} className={chipCls(statusFilter === 'downloaded')}>Downloaded</button>
            <button onClick={() => setStatusFilter('imported')} className={chipCls(statusFilter === 'imported')}>Imported</button>

            <span className="text-xs text-slate-600 dark:text-zinc-500 mx-2 self-center">Published:</span>
            <button onClick={() => setPublishedFilter('')} className={chipCls(publishedFilter === '')}>All</button>
            <button onClick={() => setPublishedFilter('released')} className={chipCls(publishedFilter === 'released')}>Released</button>
            <button onClick={() => setPublishedFilter('upcoming')} className={chipCls(publishedFilter === 'upcoming')}>Upcoming</button>
          </div>
        )}

        {books.length === 0 ? (
          <p className="text-sm text-slate-600 dark:text-zinc-500">No books tracked for this author yet.</p>
        ) : filteredBooks.length === 0 ? (
          <p className="text-sm text-slate-600 dark:text-zinc-500">No books match the current filters.</p>
        ) : view === 'table' ? (
          <div className="border border-slate-200 dark:border-zinc-800 rounded-lg overflow-hidden">
            <div className="overflow-x-auto">
              <table className="w-full table-fixed text-sm">
                <thead>
                  <tr className="bg-slate-100 dark:bg-zinc-900 border-b border-slate-200 dark:border-zinc-800">
                    <th className="w-10 px-3 py-2">
                      <input
                        ref={selectAllRef}
                        type="checkbox"
                        checked={allVisibleSelected}
                        onChange={e => e.target.checked ? selectAllVisible() : clearSelection()}
                        className="rounded border-slate-400 dark:border-zinc-600 text-emerald-500 focus:ring-emerald-500 focus:ring-offset-0"
                        title={t('common.selectAllPage')}
                        aria-label={t('common.selectAllPage')}
                      />
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
                  {filteredBooks.map(book => (
                    <tr
                      key={book.id}
                      className={`${selected.has(book.id) ? 'bg-emerald-500/10 dark:bg-emerald-500/10' : 'bg-slate-100/50 dark:bg-zinc-900/50'} hover:bg-slate-200/50 dark:hover:bg-zinc-800/50 cursor-pointer`}
                      onClick={() => (window.location.href = `/book/${book.id}`)}
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
                            <div className="w-6 h-9 bg-slate-200 dark:bg-zinc-800 rounded flex-shrink-0" />
                          )}
                          <span className="min-w-0 flex-1">
                            <span className="block text-slate-800 dark:text-zinc-200 truncate">{book.title}</span>
                            <span className="mt-1 flex flex-wrap items-center gap-1 sm:hidden">
                              <span className={statusBadgeClass(book.status, 'inline-block px-1.5 py-0.5 rounded text-[10px] font-medium')}>
                                {statusLabel[book.status] ?? book.status}
                              </span>
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
                        <span className={statusBadgeClass(book.status)}>
                          {statusLabel[book.status] ?? book.status}
                        </span>
                        {book.excluded && (
                          <span className="inline-block ml-1 px-2 py-0.5 rounded text-[10px] font-medium bg-amber-500/20 text-amber-700 dark:text-amber-400">
                            Excluded
                          </span>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </div>
        ) : (
          <div className="grid grid-cols-2 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-5 gap-4">
            {filteredBooks.map(book => (
              <div
                key={book.id}
                className={`relative border rounded-lg bg-slate-100 dark:bg-zinc-900 overflow-hidden group transition-colors ${selected.has(book.id) ? 'border-emerald-500' : 'border-slate-200 dark:border-zinc-800 hover:border-emerald-500'}`}
              >
                <input
                  type="checkbox"
                  checked={selected.has(book.id)}
                  onChange={() => toggleSelect(book.id)}
                  onClick={e => e.stopPropagation()}
                  className="absolute top-2 left-2 z-10 rounded border-slate-400 dark:border-zinc-600 text-emerald-500 focus:ring-emerald-500 focus:ring-offset-0 bg-white/80 dark:bg-zinc-900/80"
                  aria-label={`Select ${book.title}`}
                />
                <Link to={`/book/${book.id}`} className="block">
                  <div className="aspect-[2/3] bg-slate-200 dark:bg-zinc-800 relative">
                    {book.imageUrl ? (
                      <img src={book.imageUrl} alt={book.title} className="w-full h-full object-cover" />
                    ) : (
                      <div className="w-full h-full flex items-center justify-center p-3 text-center">
                        <span className="text-sm text-slate-500 dark:text-zinc-600">{book.title}</span>
                      </div>
                    )}
                  </div>
                  <div className="p-2">
                    <h4 className="text-xs font-medium truncate" title={book.title}>{book.title}</h4>
                    <div className="flex items-center gap-1 mt-1 flex-wrap">
                      <span className={statusBadgeClass(book.status, 'px-1.5 py-0.5 rounded text-[10px] font-medium')}>
                        {statusLabel[book.status] ?? book.status}
                      </span>
                      {book.mediaType === 'audiobook' && (
                        <span className="px-1.5 py-0.5 rounded text-[10px] font-medium bg-indigo-100 text-indigo-800 dark:bg-indigo-950 dark:text-indigo-300">🎧 Audio</span>
                      )}
                      {book.excluded && (
                        <span className="px-1.5 py-0.5 rounded text-[10px] font-medium bg-amber-500/20 text-amber-700 dark:text-amber-400">Excluded</span>
                      )}
                    </div>
                    {book.releaseDate && (
                      <p className="text-[10px] text-slate-600 dark:text-zinc-500 mt-0.5">{fmtPublishedYear(book.releaseDate)}</p>
                    )}
                  </div>
                </Link>
              </div>
            ))}
          </div>
        )}
      </section>

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
