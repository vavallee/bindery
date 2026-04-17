import { useEffect, useRef, useState, useMemo } from 'react'
import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { api, Author } from '../api/client'
import AddAuthorModal from '../components/AddAuthorModal'
import AddBookModal from '../components/AddBookModal'
import MergeAuthorsModal from '../components/MergeAuthorsModal'
import BulkActionBar from '../components/BulkActionBar'
import Pagination from '../components/Pagination'
import { usePagination } from '../components/usePagination'
import ViewToggle from '../components/ViewToggle'
import { useView } from '../components/useView'

type SortMode = 'az' | 'za' | 'recent'
type MonitoredFilter = '' | 'monitored' | 'unmonitored'

export default function AuthorsPage() {
  const { t } = useTranslation()
  const [authors, setAuthors] = useState<Author[]>([])
  const [loading, setLoading] = useState(true)
  const [showAdd, setShowAdd] = useState(false)
  const [showAddBook, setShowAddBook] = useState(false)
  const [showMerge, setShowMerge] = useState(false)
  const [search, setSearch] = useState('')
  const [sort, setSort] = useState<SortMode>('az')
  const [monitoredFilter, setMonitoredFilter] = useState<MonitoredFilter>(() => {
    try {
      const v = localStorage.getItem('bindery.filter.authors.monitored')
      if (v === 'monitored' || v === 'unmonitored') return v
    } catch { /* ignore */ }
    return ''
  })
  const [view, setView] = useView('authors', 'grid')
  const [selectedIds, setSelectedIds] = useState<Set<number>>(new Set())
  const [bulkBusy, setBulkBusy] = useState(false)
  const selectAllRef = useRef<HTMLInputElement>(null)

  const load = () => {
    setLoading(true)
    api.listAuthors().then(setAuthors).catch(console.error).finally(() => setLoading(false))
  }

  useEffect(() => { load() }, [])

  useEffect(() => {
    try { localStorage.setItem('bindery.filter.authors.monitored', monitoredFilter) } catch { /* ignore */ }
  }, [monitoredFilter])

  const handleDelete = async (id: number) => {
    let withFiles = 0
    let total = 0
    try {
      const books = await api.listBooks({ authorId: id })
      total = books.length
      withFiles = books.filter(b => b.filePath).length
    } catch { /* fall through to no-file-sweep default */ }
    const msg = withFiles > 0
      ? t('authors.deleteWithFilesConfirm', { total, withFiles })
      : t('authors.deleteConfirm')
    if (!confirm(msg)) return
    await api.deleteAuthor(id, withFiles > 0)
    load()
  }

  const handleToggleMonitored = async (author: Author) => {
    await api.updateAuthor(author.id, { monitored: !author.monitored } as Partial<Author>)
    load()
  }

  const filtered = useMemo(() => {
    let list = authors
    if (monitoredFilter === 'monitored') list = list.filter(a => a.monitored)
    else if (monitoredFilter === 'unmonitored') list = list.filter(a => !a.monitored)
    if (search.trim()) {
      const q = search.trim().toLowerCase()
      list = list.filter(a =>
        a.authorName.toLowerCase().includes(q) ||
        (a.description && a.description.toLowerCase().includes(q))
      )
    }
    if (sort === 'az') list = [...list].sort((a, b) => a.authorName.localeCompare(b.authorName))
    else if (sort === 'za') list = [...list].sort((a, b) => b.authorName.localeCompare(a.authorName))
    return list
  }, [authors, monitoredFilter, search, sort])

  const { pageItems, paginationProps, reset } = usePagination(filtered, 50, 'authors')

  useEffect(() => { reset() }, [search, sort, monitoredFilter, reset])

  // Keep the select-all checkbox indeterminate state in sync.
  const allPageSelected = pageItems.length > 0 && pageItems.every(a => selectedIds.has(a.id))
  const somePageSelected = pageItems.some(a => selectedIds.has(a.id)) && !allPageSelected
  useEffect(() => {
    if (selectAllRef.current) selectAllRef.current.indeterminate = somePageSelected
  }, [somePageSelected])

  useEffect(() => {
    document.title = 'Authors · Bindery'
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

  const selectAllOnPage = () => setSelectedIds(new Set(pageItems.map(a => a.id)))
  const clearSelection = () => setSelectedIds(new Set())

  const runBulk = async (action: Parameters<typeof api.bulkActionAuthors>[1]) => {
    if (selectedIds.size === 0) return
    if (action === 'delete' && !confirm(t('authors.bulkDeleteConfirm', { count: selectedIds.size }))) return
    setBulkBusy(true)
    try {
      await api.bulkActionAuthors([...selectedIds], action)
      clearSelection()
      load()
    } catch (err) {
      alert(err instanceof Error ? err.message : 'Bulk action failed')
    } finally {
      setBulkBusy(false)
    }
  }

  const sortBtnCls = (active: boolean) =>
    `px-3 py-1 rounded-md text-xs font-medium transition-colors ${active ? 'bg-slate-300 dark:bg-zinc-700 text-slate-900 dark:text-white' : 'text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white hover:bg-slate-200/50 dark:hover:bg-zinc-800/50'}`

  return (
    <div className={selectedIds.size > 0 ? 'pb-16' : ''}>
      <div className="flex items-center justify-between mb-4">
        <h2 className="text-2xl font-bold">{t('authors.title')}</h2>
        <div className="flex items-center gap-3">
          <ViewToggle view={view} onChange={setView} />
          <button
            onClick={() => setShowMerge(true)}
            disabled={authors.length < 2}
            className="px-3 py-2 bg-slate-200 dark:bg-zinc-800 hover:bg-slate-300 dark:hover:bg-zinc-700 disabled:opacity-50 disabled:cursor-not-allowed rounded-md text-sm font-medium transition-colors"
            title={t('authors.mergeTip')}
          >
            {t('authors.merge')}
          </button>
          <button
            onClick={() => setShowAddBook(true)}
            className="px-4 py-2 bg-slate-200 dark:bg-zinc-800 hover:bg-slate-300 dark:hover:bg-zinc-700 rounded-md text-sm font-medium transition-colors"
          >
            Add Book
          </button>
          <button
            onClick={() => setShowAdd(true)}
            className="px-4 py-2 bg-emerald-600 hover:bg-emerald-500 rounded-md text-sm font-medium transition-colors"
          >
            {t('authors.addAuthor')}
          </button>
        </div>
      </div>

      {/* Search & Sort controls */}
      <div className="flex flex-col sm:flex-row gap-3 mb-4">
        <input
          type="search"
          value={search}
          onChange={e => setSearch(e.target.value)}
          placeholder={t('authors.searchPlaceholder')}
          className="flex-1 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600 placeholder-slate-400 dark:placeholder-zinc-600"
        />
        <div className="flex gap-1">
          <button onClick={() => setSort('az')} className={sortBtnCls(sort === 'az')}>{t('authors.sortAZ')}</button>
          <button onClick={() => setSort('za')} className={sortBtnCls(sort === 'za')}>{t('authors.sortZA')}</button>
          <button onClick={() => setSort('recent')} className={sortBtnCls(sort === 'recent')}>{t('authors.sortRecent')}</button>
        </div>
      </div>

      {/* Monitored filter chips */}
      <div className="flex gap-1 mb-6 flex-wrap">
        <span className="text-xs text-slate-600 dark:text-zinc-500 mr-1 self-center">{t('authors.filterMonitored')}</span>
        <button onClick={() => setMonitoredFilter('')} className={sortBtnCls(monitoredFilter === '')}>{t('authors.filterAll')}</button>
        <button onClick={() => setMonitoredFilter('monitored')} className={sortBtnCls(monitoredFilter === 'monitored')}>{t('authors.filterMonitoredOnly')}</button>
        <button onClick={() => setMonitoredFilter('unmonitored')} className={sortBtnCls(monitoredFilter === 'unmonitored')}>{t('authors.filterUnmonitored')}</button>
      </div>

      {loading ? (
        <div className="text-slate-600 dark:text-zinc-500">{t('common.loading')}</div>
      ) : filtered.length === 0 && authors.length === 0 ? (
        <div className="text-center py-16 text-slate-600 dark:text-zinc-500">
          <p className="text-lg mb-2">{t('authors.empty')}</p>
          <p className="text-sm">{t('authors.emptyHint')}</p>
        </div>
      ) : filtered.length === 0 ? (
        <div className="text-center py-16 text-slate-600 dark:text-zinc-500">
          <p>{t('authors.noMatch', { query: search })}</p>
        </div>
      ) : view === 'table' ? (
        <div className="border border-slate-200 dark:border-zinc-800 rounded-lg overflow-hidden">
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="bg-slate-100 dark:bg-zinc-900 border-b border-slate-200 dark:border-zinc-800">
                  <th className="px-3 py-2 w-8">
                    <input
                      ref={selectAllRef}
                      type="checkbox"
                      checked={allPageSelected}
                      onChange={e => e.target.checked ? selectAllOnPage() : clearSelection()}
                      className="rounded-full border-slate-400 dark:border-zinc-600 text-emerald-500 focus:ring-emerald-500 focus:ring-offset-0"
                      title="Select all on this page"
                    />
                  </th>
                  <th className="text-left px-3 py-2 text-xs font-medium text-slate-600 dark:text-zinc-400 uppercase">{t('authors.colName')}</th>
                  <th className="text-left px-3 py-2 text-xs font-medium text-slate-600 dark:text-zinc-400 uppercase">{t('authors.colBooks')}</th>
                  <th className="text-left px-3 py-2 text-xs font-medium text-slate-600 dark:text-zinc-400 uppercase">{t('authors.colRating')}</th>
                  <th className="text-left px-3 py-2 text-xs font-medium text-slate-600 dark:text-zinc-400 uppercase">{t('authors.colMonitored')}</th>
                  <th className="px-3 py-2" />
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-200 dark:divide-zinc-800">
                {pageItems.map(author => (
                  <tr
                    key={author.id}
                    className={`hover:bg-slate-200/50 dark:hover:bg-zinc-800/50 ${selectedIds.has(author.id) ? 'bg-emerald-500/10 dark:bg-emerald-500/10' : 'bg-slate-100/50 dark:bg-zinc-900/50'}`}
                  >
                    <td className="px-3 py-2 w-8">
                      <input
                        type="checkbox"
                        checked={selectedIds.has(author.id)}
                        onChange={() => toggleSelect(author.id)}
                        className="rounded-full border-slate-400 dark:border-zinc-600 text-emerald-500 focus:ring-emerald-500 focus:ring-offset-0"
                        onClick={e => e.stopPropagation()}
                      />
                    </td>
                    <td className="px-3 py-2">
                      <Link to={`/author/${author.id}`} className="flex items-center gap-2">
                        {author.imageUrl ? (
                          <img src={author.imageUrl} alt="" className="w-7 h-7 rounded-full object-cover flex-shrink-0" />
                        ) : (
                          <div className="w-7 h-7 rounded-full bg-slate-200 dark:bg-zinc-800 flex items-center justify-center text-xs font-bold text-slate-500 dark:text-zinc-600 flex-shrink-0">
                            {author.authorName.charAt(0)}
                          </div>
                        )}
                        <span className="text-slate-800 dark:text-zinc-200 truncate hover:text-emerald-500">{author.authorName}</span>
                      </Link>
                    </td>
                    <td className="px-3 py-2 text-slate-600 dark:text-zinc-400 whitespace-nowrap">{author.statistics?.bookCount ?? '—'}</td>
                    <td className="px-3 py-2 text-slate-600 dark:text-zinc-400 whitespace-nowrap">
                      {author.averageRating > 0 ? `★ ${author.averageRating.toFixed(2)}` : '—'}
                    </td>
                    <td className="px-3 py-2 whitespace-nowrap">
                      <button
                        onClick={() => handleToggleMonitored(author)}
                        className={`text-xs px-2 py-0.5 rounded ${author.monitored ? 'bg-emerald-500/20 text-emerald-400' : 'bg-slate-300 dark:bg-zinc-700 text-slate-600 dark:text-zinc-400'}`}
                      >
                        {author.monitored ? t('common.yes') : t('common.no')}
                      </button>
                    </td>
                    <td className="px-3 py-2 text-right whitespace-nowrap">
                      <button
                        onClick={() => api.refreshAuthor(author.id).then(load)}
                        className="text-xs text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white mr-3"
                      >
                        {t('common.refresh')}
                      </button>
                      <button
                        onClick={() => handleDelete(author.id)}
                        className="text-xs text-red-400 hover:text-red-300"
                      >
                        {t('common.delete')}
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      ) : (
        <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4 gap-4">
          {pageItems.map(author => (
            <div
              key={author.id}
              className={`border rounded-lg bg-slate-100 dark:bg-zinc-900 overflow-hidden hover:border-emerald-500 transition-colors ${selectedIds.has(author.id) ? 'border-emerald-500' : 'border-slate-200 dark:border-zinc-800'}`}
            >
              <div className="relative">
                <input
                  type="checkbox"
                  checked={selectedIds.has(author.id)}
                  onChange={() => toggleSelect(author.id)}
                  className="absolute top-2 left-2 z-10 rounded-full border-slate-400 dark:border-zinc-600 text-emerald-500 focus:ring-emerald-500 focus:ring-offset-0 bg-white/80 dark:bg-zinc-900/80"
                  title={`Select ${author.authorName}`}
                />
                <Link to={`/author/${author.id}`} className="flex gap-3 p-4 hover:bg-slate-200/40 dark:hover:bg-zinc-800/40 transition-colors">
                  {author.imageUrl ? (
                    <img src={author.imageUrl} alt={author.authorName} className="w-16 h-16 rounded-full object-cover flex-shrink-0" />
                  ) : (
                    <div className="w-16 h-16 rounded-full bg-slate-200 dark:bg-zinc-800 flex items-center justify-center flex-shrink-0 text-xl font-bold text-slate-500 dark:text-zinc-600">
                      {author.authorName.charAt(0)}
                    </div>
                  )}
                  <div className="min-w-0">
                    <h3 className="font-semibold truncate">{author.authorName}</h3>
                    <p className="text-xs text-slate-600 dark:text-zinc-500 mt-1 line-clamp-2">
                      {author.description || t('authors.noDescription')}
                    </p>
                  </div>
                </Link>
              </div>
              <div className="flex items-center justify-between px-4 py-2 bg-slate-200/50 dark:bg-zinc-800/50 border-t border-slate-200 dark:border-zinc-800">
                <button
                  onClick={() => handleToggleMonitored(author)}
                  className={`text-xs px-2 py-1 rounded ${author.monitored ? 'bg-emerald-500/20 text-emerald-400' : 'bg-slate-300 dark:bg-zinc-700 text-slate-600 dark:text-zinc-400'}`}
                >
                  {author.monitored ? t('authors.monitored') : t('authors.unmonitored')}
                </button>
                <div className="flex gap-2">
                  <button
                    onClick={() => api.refreshAuthor(author.id).then(load)}
                    className="text-xs text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white"
                    title="Refresh metadata"
                  >
                    {t('common.refresh')}
                  </button>
                  <button
                    onClick={() => handleDelete(author.id)}
                    className="text-xs text-red-400 hover:text-red-300"
                  >
                    {t('common.delete')}
                  </button>
                </div>
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
          { label: t('common.monitor'), onClick: () => runBulk('monitor') },
          { label: t('common.unmonitor'), onClick: () => runBulk('unmonitor') },
          { label: t('common.search'), onClick: () => runBulk('search') },
          { label: t('common.delete'), onClick: () => runBulk('delete'), variant: 'danger' },
        ]}
      />

      {showAdd && <AddAuthorModal onClose={() => setShowAdd(false)} onAdded={load} />}
      {showAddBook && <AddBookModal onClose={() => setShowAddBook(false)} onAdded={() => setShowAddBook(false)} />}
      {showMerge && (
        <MergeAuthorsModal
          authors={authors}
          onClose={() => setShowMerge(false)}
          onMerged={load}
        />
      )}
    </div>
  )
}
