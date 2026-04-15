import { useEffect, useRef, useState, useMemo } from 'react'
import { Link } from 'react-router-dom'
import { api, Book, SearchResult } from '../api/client'
import BulkActionBar from '../components/BulkActionBar'
import Pagination from '../components/Pagination'
import { usePagination } from '../components/usePagination'

export default function WantedPage() {
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
  const selectAllRef = useRef<HTMLInputElement>(null)

  const load = () => {
    api.listWanted().then(setBooks).catch(console.error).finally(() => setLoading(false))
  }

  useEffect(() => {
    load()
  }, [])

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
      setResults(res)
      setShowResults(book.id)
    } catch (err) {
      console.error(err)
    } finally {
      setSearchingId(null)
    }
  }

  const changeMediaType = async (book: Book, mediaType: 'ebook' | 'audiobook') => {
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
        <h2 className="text-2xl font-bold">Wanted</h2>
        <span className="text-sm text-slate-600 dark:text-zinc-500">{filtered.length} of {books.length}</span>
      </div>

      <input
        type="search"
        value={search}
        onChange={e => setSearch(e.target.value)}
        placeholder="Search by title or author..."
        className="w-full mb-4 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600 placeholder-slate-400 dark:placeholder-zinc-600"
      />

      {loading ? (
        <div className="text-slate-600 dark:text-zinc-500">Loading...</div>
      ) : books.length === 0 ? (
        <div className="text-center py-16 text-slate-600 dark:text-zinc-500">
          <p>No wanted books. Add an author to start tracking.</p>
        </div>
      ) : filtered.length === 0 ? (
        <div className="text-center py-16 text-slate-600 dark:text-zinc-500">
          <p>No books match your search.</p>
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
            <span className="text-xs text-slate-500 dark:text-zinc-500">Select all on this page</span>
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
                      title={`Select ${book.title}`}
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
                          onChange={e => changeMediaType(book, e.target.value as 'ebook' | 'audiobook')}
                          className="bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded text-[11px] px-1.5 py-0.5 focus:outline-none"
                          title="Change media type"
                        >
                          <option value="ebook">📖 Ebook</option>
                          <option value="audiobook">🎧 Audiobook</option>
                        </select>
                      </div>
                      {book.releaseDate && (
                        <p className="text-xs text-slate-600 dark:text-zinc-500">{new Date(book.releaseDate).getFullYear()}</p>
                      )}
                    </div>
                  </div>
                  <div className="flex items-center gap-2 flex-shrink-0">
                    <button
                      onClick={() => unmonitor(book)}
                      disabled={unmonitoringId === book.id}
                      className="px-2 py-1.5 bg-slate-200 dark:bg-zinc-800 hover:bg-amber-100 dark:hover:bg-amber-900/30 hover:text-amber-700 dark:hover:text-amber-400 rounded text-xs font-medium disabled:opacity-50 transition-colors"
                      title="Stop monitoring this book"
                    >
                      {unmonitoringId === book.id ? '…' : 'Unmonitor'}
                    </button>
                    <button
                      onClick={() => searchBook(book)}
                      disabled={searchingId === book.id}
                      className="px-3 py-1.5 bg-slate-200 dark:bg-zinc-800 hover:bg-slate-300 dark:hover:bg-zinc-700 rounded text-xs font-medium disabled:opacity-50"
                    >
                      {searchingId === book.id ? 'Searching...' : 'Search'}
                    </button>
                  </div>
                </div>

                {showResults === book.id && results.length === 0 && (
                  <div className="mt-1 mb-3 px-3 py-2 bg-slate-200/50 dark:bg-zinc-800/50 rounded text-xs text-slate-600 dark:text-zinc-500">
                    No results found on any indexer.
                  </div>
                )}

                {showResults === book.id && results.length > 0 && (
                  <div className="mt-1 mb-3 space-y-1">
                    {results.slice(0, 10).map(r => (
                      <div key={r.guid} className="flex items-center justify-between p-2 bg-slate-200/50 dark:bg-zinc-800/50 rounded text-xs">
                        <div className="min-w-0 mr-3">
                          <span className="truncate block">{r.title}</span>
                          <span className="text-slate-600 dark:text-zinc-500 truncate block">{r.indexerName} &middot; {formatSize(r.size)} &middot; {r.grabs} grabs</span>
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
                          {grabbedGuid === r.guid ? '✓ Grabbed' : grabbingGuid === r.guid ? 'Grabbing…' : 'Grab'}
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
          { label: 'Search', onClick: () => runBulk('search') },
          { label: 'Unmonitor', onClick: () => runBulk('unmonitor') },
          { label: 'Blocklist', onClick: () => runBulk('blocklist'), variant: 'danger' },
        ]}
      />
    </div>
  )
}
