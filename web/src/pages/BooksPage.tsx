import { useEffect, useMemo, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import ViewToggle from '../components/ViewToggle'
import { useView } from '../components/useView'
import { api, Book } from '../api/client'
import BulkActionBar from '../components/BulkActionBar'
import Pagination from '../components/Pagination'
import { usePagination } from '../components/usePagination'

type SortMode = 'title-az' | 'title-za' | 'date-new' | 'date-old'

const statusColors: Record<string, string> = {
  wanted: 'bg-amber-500/20 text-amber-400',
  downloading: 'bg-blue-500/20 text-blue-400',
  downloaded: 'bg-cyan-500/20 text-cyan-400',
  imported: 'bg-emerald-500/20 text-emerald-400',
  skipped: 'bg-slate-300 dark:bg-zinc-700 text-slate-600 dark:text-zinc-400',
}

// statusLabel is populated at render time from t() — see BooksPage
const statusLabelKeys: Record<string, string> = {
  wanted: 'books.statusWanted',
  downloading: 'books.statusDownloading',
  downloaded: 'books.statusDownloaded',
  imported: 'books.statusImported',
  skipped: 'books.statusSkipped',
}

export default function BooksPage() {
  const { t } = useTranslation()
  const [books, setBooks] = useState<Book[]>([])
  const [loading, setLoading] = useState(true)
  const [statusFilter, setStatusFilter] = useState('')
  const [mediaFilter, setMediaFilter] = useState<'' | 'ebook' | 'audiobook'>('')
  const [search, setSearch] = useState('')
  const [sort, setSort] = useState<SortMode>('title-az')
  const [view, setView] = useView('books', 'grid')
  const [selectedIds, setSelectedIds] = useState<Set<number>>(new Set())
  const [bulkBusy, setBulkBusy] = useState(false)
  const selectAllRef = useRef<HTMLInputElement>(null)

  const load = () => {
    api.listBooks().then(setBooks).catch(console.error).finally(() => setLoading(false))
  }

  useEffect(() => {
    load()
  }, [])

  const filtered = useMemo(() => {
    let list = books
    if (statusFilter) list = list.filter(b => b.status === statusFilter)
    if (mediaFilter) {
      list = list.filter(b => {
        const mt = b.mediaType || 'ebook'
        // 'both' books count as both ebook and audiobook so they show up
        // under either filter.
        return mt === mediaFilter || mt === 'both'
      })
    }
    if (search.trim()) {
      const q = search.trim().toLowerCase()
      list = list.filter(b =>
        b.title.toLowerCase().includes(q) ||
        (b.author?.authorName && b.author.authorName.toLowerCase().includes(q))
      )
    }
    if (sort === 'title-az') list = [...list].sort((a, b) => a.title.localeCompare(b.title))
    else if (sort === 'title-za') list = [...list].sort((a, b) => b.title.localeCompare(a.title))
    else if (sort === 'date-new') list = [...list].sort((a, b) => {
      if (!a.releaseDate && !b.releaseDate) return 0
      if (!a.releaseDate) return 1
      if (!b.releaseDate) return -1
      return new Date(b.releaseDate).getTime() - new Date(a.releaseDate).getTime()
    })
    else if (sort === 'date-old') list = [...list].sort((a, b) => {
      if (!a.releaseDate && !b.releaseDate) return 0
      if (!a.releaseDate) return 1
      if (!b.releaseDate) return -1
      return new Date(a.releaseDate).getTime() - new Date(b.releaseDate).getTime()
    })
    return list
  }, [books, statusFilter, mediaFilter, search, sort])

  const { pageItems, paginationProps, reset } = usePagination(filtered, 50, 'books')

  useEffect(() => { reset() }, [statusFilter, mediaFilter, search, sort, reset])

  // Keep the select-all checkbox indeterminate state in sync.
  const allPageSelected = pageItems.length > 0 && pageItems.every(b => selectedIds.has(b.id))
  const somePageSelected = pageItems.some(b => selectedIds.has(b.id)) && !allPageSelected
  useEffect(() => {
    if (selectAllRef.current) selectAllRef.current.indeterminate = somePageSelected
  }, [somePageSelected])

  useEffect(() => {
    document.title = 'Books · Bindery'
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

  const runBulk = async (action: Parameters<typeof api.bulkActionBooks>[1], mediaType?: 'ebook' | 'audiobook') => {
    if (selectedIds.size === 0) return
    if (action === 'delete' && !confirm(t('books.deleteConfirm', { count: selectedIds.size }))) return
    setBulkBusy(true)
    try {
      await api.bulkActionBooks([...selectedIds], action, mediaType)
      clearSelection()
      load()
    } catch (err) {
      alert(err instanceof Error ? err.message : 'Bulk action failed')
    } finally {
      setBulkBusy(false)
    }
  }

  const statusBtnCls = (active: boolean) =>
    `px-3 py-1 rounded-md text-xs font-medium transition-colors ${active ? 'bg-slate-300 dark:bg-zinc-700 text-slate-900 dark:text-white' : 'text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white'}`

  const sortBtnCls = (active: boolean) =>
    `px-3 py-1 rounded-md text-xs font-medium transition-colors ${active ? 'bg-slate-300 dark:bg-zinc-700 text-slate-900 dark:text-white' : 'text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white hover:bg-slate-200/50 dark:hover:bg-zinc-800/50'}`

  return (
    <div className={selectedIds.size > 0 ? 'pb-16' : ''}>
      <div className="flex items-center justify-between mb-4">
        <h2 className="text-2xl font-bold">{t('books.title')}</h2>
        <div className="flex items-center gap-3">
          <span className="text-sm text-slate-600 dark:text-zinc-500">{filtered.length} of {books.length}</span>
          <ViewToggle view={view} onChange={setView} />
        </div>
      </div>

      {/* Controls */}
      <div className="flex flex-col sm:flex-row gap-3 mb-6">
        <input
          type="search"
          value={search}
          onChange={e => setSearch(e.target.value)}
          placeholder={t('books.searchPlaceholder')}
          className="flex-1 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600 placeholder-slate-400 dark:placeholder-zinc-600"
        />
        <div className="flex gap-1 flex-wrap">
          {(['', 'wanted', 'downloading', 'imported', 'skipped'] as const).map(s => (
            <button
              key={s}
              onClick={() => setStatusFilter(s)}
              className={statusBtnCls(statusFilter === s)}
            >
              {s ? (t(statusLabelKeys[s]) ?? s) : t('common.all')}
            </button>
          ))}
        </div>
      </div>

      <div className="flex gap-1 mb-4 flex-wrap">
        <span className="text-xs text-slate-600 dark:text-zinc-500 mr-1 self-center">{t('books.sortLabel')}</span>
        <button onClick={() => setSort('title-az')} className={sortBtnCls(sort === 'title-az')}>{t('books.sortTitleAZ')}</button>
        <button onClick={() => setSort('title-za')} className={sortBtnCls(sort === 'title-za')}>{t('books.sortTitleZA')}</button>
        <button onClick={() => setSort('date-new')} className={sortBtnCls(sort === 'date-new')}>{t('books.sortNewest')}</button>
        <button onClick={() => setSort('date-old')} className={sortBtnCls(sort === 'date-old')}>{t('books.sortOldest')}</button>

        <span className="text-xs text-slate-600 dark:text-zinc-500 mx-2 self-center">{t('books.typeLabel')}</span>
        <button onClick={() => setMediaFilter('')} className={sortBtnCls(mediaFilter === '')}>{t('common.all')}</button>
        <button onClick={() => setMediaFilter('ebook')} className={sortBtnCls(mediaFilter === 'ebook')}>📖 {t('common.ebook')}</button>
        <button onClick={() => setMediaFilter('audiobook')} className={sortBtnCls(mediaFilter === 'audiobook')}>🎧 {t('common.audiobook')}</button>
      </div>

      {loading ? (
        <div className="text-slate-600 dark:text-zinc-500">{t('common.loading')}</div>
      ) : filtered.length === 0 ? (
        <div className="text-center py-16 text-slate-600 dark:text-zinc-500">
          {books.length === 0 ? (
            <>
              <p className="font-medium">{t('books.empty')}</p>
              <p className="text-sm mt-1">{t('books.emptyHint')}</p>
            </>
          ) : (
            <p>{t('books.noMatch')}</p>
          )}
        </div>
      ) : (
        view === 'table' ? (
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
                  <th className="text-left px-3 py-2 text-xs font-medium text-slate-600 dark:text-zinc-400 uppercase">{t('books.colTitle')}</th>
                  <th className="text-left px-3 py-2 text-xs font-medium text-slate-600 dark:text-zinc-400 uppercase hidden md:table-cell">{t('books.colAuthor')}</th>
                  <th className="text-left px-3 py-2 text-xs font-medium text-slate-600 dark:text-zinc-400 uppercase hidden sm:table-cell">{t('books.colYear')}</th>
                  <th className="text-left px-3 py-2 text-xs font-medium text-slate-600 dark:text-zinc-400 uppercase">{t('books.colType')}</th>
                  <th className="text-left px-3 py-2 text-xs font-medium text-slate-600 dark:text-zinc-400 uppercase">{t('books.colStatus')}</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-200 dark:divide-zinc-800">
                {pageItems.map(book => (
                  <tr
                    key={book.id}
                    className={`hover:bg-slate-200/50 dark:hover:bg-zinc-800/50 cursor-pointer ${selectedIds.has(book.id) ? 'bg-emerald-500/10 dark:bg-emerald-500/10' : 'bg-slate-100/50 dark:bg-zinc-900/50'}`}
                    onClick={() => (window.location.href = `/book/${book.id}`)}
                  >
                    <td className="px-3 py-2 w-8" onClick={e => e.stopPropagation()}>
                      <input
                        type="checkbox"
                        checked={selectedIds.has(book.id)}
                        onChange={() => toggleSelect(book.id)}
                        className="rounded-full border-slate-400 dark:border-zinc-600 text-emerald-500 focus:ring-emerald-500 focus:ring-offset-0"
                      />
                    </td>
                    <td className="px-3 py-2">
                      <Link to={`/book/${book.id}`} className="flex items-center gap-2" onClick={e => e.stopPropagation()}>
                        {book.imageUrl ? (
                          <img src={book.imageUrl} alt="" className="w-6 h-9 object-cover rounded flex-shrink-0" />
                        ) : (
                          <div className="w-6 h-9 bg-slate-200 dark:bg-zinc-800 rounded flex-shrink-0" />
                        )}
                        <span className="text-slate-800 dark:text-zinc-200 truncate">{book.title}</span>
                      </Link>
                    </td>
                    <td className="px-3 py-2 whitespace-nowrap hidden md:table-cell">
                      {book.author ? (
                        <Link
                          to={`/author/${book.authorId}`}
                          className="text-slate-600 dark:text-zinc-400 hover:text-emerald-500 dark:hover:text-emerald-400"
                          onClick={e => e.stopPropagation()}
                        >
                          {book.author.authorName}
                        </Link>
                      ) : '—'}
                    </td>
                    <td className="px-3 py-2 text-slate-600 dark:text-zinc-400 whitespace-nowrap hidden sm:table-cell">{book.releaseDate ? new Date(book.releaseDate).getFullYear() : '—'}</td>
                    <td className="px-3 py-2 text-xs whitespace-nowrap">
                      {book.mediaType === 'both'
                        ? `📖🎧 ${t('common.ebook')} + ${t('common.audiobook')}`
                        : book.mediaType === 'audiobook'
                          ? `🎧 ${t('common.audiobook')}`
                          : `📖 ${t('common.ebook')}`}
                    </td>
                    <td className="px-3 py-2 whitespace-nowrap">
                      <span className={`inline-block px-2 py-0.5 rounded text-[10px] font-medium ${statusColors[book.status] || 'bg-slate-300 dark:bg-zinc-700 text-slate-600 dark:text-zinc-400'}`}>
                        {statusLabelKeys[book.status] ? t(statusLabelKeys[book.status]) : book.status}
                      </span>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
        ) : (
        <div className="grid grid-cols-2 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-5 xl:grid-cols-6 gap-4">
          {pageItems.map(book => (
            <div
              key={book.id}
              className={`border rounded-lg bg-slate-100 dark:bg-zinc-900 overflow-hidden group text-left transition-colors ${selectedIds.has(book.id) ? 'border-emerald-500' : 'border-slate-200 dark:border-zinc-800 hover:border-emerald-500'}`}
            >
              <div className="aspect-[2/3] bg-slate-200 dark:bg-zinc-800 relative">
                <input
                  type="checkbox"
                  checked={selectedIds.has(book.id)}
                  onChange={() => toggleSelect(book.id)}
                  className="absolute top-2 left-2 z-10 rounded-full border-slate-400 dark:border-zinc-600 text-emerald-500 focus:ring-emerald-500 focus:ring-offset-0 bg-white/80 dark:bg-zinc-900/80"
                  title={`Select ${book.title}`}
                  onClick={e => e.stopPropagation()}
                />
                <Link to={`/book/${book.id}`} className="block w-full h-full">
                  {book.imageUrl ? (
                    <img src={book.imageUrl} alt={book.title} className="w-full h-full object-cover" />
                  ) : (
                    <div className="w-full h-full flex items-center justify-center p-3 text-center">
                      <span className="text-sm text-slate-500 dark:text-zinc-600">{book.title}</span>
                    </div>
                  )}
                </Link>
              </div>
              <div className="p-2">
                <h3 className="text-xs font-medium truncate" title={book.title}>{book.title}</h3>
                {book.author && (
                  <p className="text-[10px] text-slate-500 dark:text-zinc-500 truncate mt-0.5">{book.author.authorName}</p>
                )}
                <div className="flex items-center gap-1 mt-1 flex-wrap">
                  <span className={`px-1.5 py-0.5 rounded text-[10px] font-medium ${statusColors[book.status] || 'bg-slate-300 dark:bg-zinc-700 text-slate-600 dark:text-zinc-400'}`}>
                    {statusLabelKeys[book.status] ? t(statusLabelKeys[book.status]) : book.status}
                  </span>
                  {(book.mediaType === 'audiobook' || book.mediaType === 'both') && (
                    <span className="px-1.5 py-0.5 rounded text-[10px] font-medium bg-indigo-100 text-indigo-800 dark:bg-indigo-950 dark:text-indigo-300">{t('books.audioLabel')}</span>
                  )}
                  {book.mediaType === 'both' && (
                    <span className="px-1.5 py-0.5 rounded text-[10px] font-medium bg-amber-100 text-amber-800 dark:bg-amber-950 dark:text-amber-300">{t('common.ebook')}</span>
                  )}
                  {!book.language && (
                    <span
                      className="px-1.5 py-0.5 rounded text-[10px] font-medium bg-amber-200 text-amber-900 dark:bg-amber-900/50 dark:text-amber-300"
                      title="Metadata source did not report a language for this book."
                    >
                      ?
                    </span>
                  )}
                </div>
                <div className="flex items-center justify-between mt-0.5">
                  {book.releaseDate && (
                    <p className="text-[10px] text-slate-600 dark:text-zinc-500">{new Date(book.releaseDate).getFullYear()}</p>
                  )}
                  {book.filePath && (
                    <a
                      href={`/api/v1/book/${book.id}/file`}
                      onClick={e => e.stopPropagation()}
                      className="text-[10px] text-emerald-400 hover:text-emerald-300"
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
          { label: t('books.setEbook'), onClick: () => runBulk('set_media_type', 'ebook') },
          { label: t('books.setAudiobook'), onClick: () => runBulk('set_media_type', 'audiobook') },
          { label: t('common.delete'), onClick: () => runBulk('delete'), variant: 'danger' },
        ]}
      />
    </div>
  )
}
