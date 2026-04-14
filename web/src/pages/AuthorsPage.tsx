import { useEffect, useState, useMemo } from 'react'
import { Link } from 'react-router-dom'
import { api, Author } from '../api/client'
import AddAuthorModal from '../components/AddAuthorModal'
import Pagination from '../components/Pagination'
import { usePagination } from '../components/usePagination'
import ViewToggle from '../components/ViewToggle'
import { useView } from '../components/useView'

type SortMode = 'az' | 'za' | 'recent'

export default function AuthorsPage() {
  const [authors, setAuthors] = useState<Author[]>([])
  const [loading, setLoading] = useState(true)
  const [showAdd, setShowAdd] = useState(false)
  const [search, setSearch] = useState('')
  const [sort, setSort] = useState<SortMode>('az')
  const [view, setView] = useView('authors', 'grid')

  const load = () => {
    setLoading(true)
    api.listAuthors().then(setAuthors).catch(console.error).finally(() => setLoading(false))
  }

  useEffect(() => { load() }, [])

  const handleDelete = async (id: number) => {
    // Peek at the author's books so the confirm can offer to sweep files.
    // Small extra roundtrip, but the list view doesn't otherwise carry
    // per-book filePaths and we don't want to silently orphan them.
    let withFiles = 0
    let total = 0
    try {
      const books = await api.listBooks({ authorId: id })
      total = books.length
      withFiles = books.filter(b => b.filePath).length
    } catch { /* fall through to no-file-sweep default */ }
    const msg = withFiles > 0
      ? `Delete this author, ${total} book(s), AND ${withFiles} file(s)/folder(s) on disk?\n\nThis cannot be undone.`
      : `Delete this author and all their books?`
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
    if (search.trim()) {
      const q = search.trim().toLowerCase()
      list = list.filter(a =>
        a.authorName.toLowerCase().includes(q) ||
        (a.description && a.description.toLowerCase().includes(q))
      )
    }
    if (sort === 'az') list = [...list].sort((a, b) => a.authorName.localeCompare(b.authorName))
    else if (sort === 'za') list = [...list].sort((a, b) => b.authorName.localeCompare(a.authorName))
    // 'recent' keeps server order (typically by id desc)
    return list
  }, [authors, search, sort])

  const { pageItems, paginationProps, reset } = usePagination(filtered, 50)

  useEffect(() => { reset() }, [search, sort, reset])

  const sortBtnCls = (active: boolean) =>
    `px-3 py-1 rounded-md text-xs font-medium transition-colors ${active ? 'bg-slate-300 dark:bg-zinc-700 text-slate-900 dark:text-white' : 'text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white hover:bg-slate-200/50 dark:hover:bg-zinc-800/50'}`

  return (
    <div>
      <div className="flex items-center justify-between mb-4">
        <h2 className="text-2xl font-bold">Authors</h2>
        <div className="flex items-center gap-3">
          <ViewToggle view={view} onChange={setView} />
          <button
            onClick={() => setShowAdd(true)}
            className="px-4 py-2 bg-emerald-600 hover:bg-emerald-500 rounded-md text-sm font-medium transition-colors"
          >
            + Add Author
          </button>
        </div>
      </div>

      {/* Search & Sort controls */}
      <div className="flex flex-col sm:flex-row gap-3 mb-6">
        <input
          type="search"
          value={search}
          onChange={e => setSearch(e.target.value)}
          placeholder="Search authors..."
          className="flex-1 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600 placeholder-slate-400 dark:placeholder-zinc-600"
        />
        <div className="flex gap-1">
          <button onClick={() => setSort('az')} className={sortBtnCls(sort === 'az')}>A–Z</button>
          <button onClick={() => setSort('za')} className={sortBtnCls(sort === 'za')}>Z–A</button>
          <button onClick={() => setSort('recent')} className={sortBtnCls(sort === 'recent')}>Recent</button>
        </div>
      </div>

      {loading ? (
        <div className="text-slate-600 dark:text-zinc-500">Loading...</div>
      ) : filtered.length === 0 && authors.length === 0 ? (
        <div className="text-center py-16 text-slate-600 dark:text-zinc-500">
          <p className="text-lg mb-2">No authors yet</p>
          <p className="text-sm">Click "Add Author" to start tracking your favorite authors</p>
        </div>
      ) : filtered.length === 0 ? (
        <div className="text-center py-16 text-slate-600 dark:text-zinc-500">
          <p>No authors match "{search}"</p>
        </div>
      ) : view === 'table' ? (
        <div className="border border-slate-200 dark:border-zinc-800 rounded-lg overflow-hidden">
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="bg-slate-100 dark:bg-zinc-900 border-b border-slate-200 dark:border-zinc-800">
                  <th className="text-left px-3 py-2 text-xs font-medium text-slate-600 dark:text-zinc-400 uppercase">Name</th>
                  <th className="text-left px-3 py-2 text-xs font-medium text-slate-600 dark:text-zinc-400 uppercase">Books</th>
                  <th className="text-left px-3 py-2 text-xs font-medium text-slate-600 dark:text-zinc-400 uppercase">Rating</th>
                  <th className="text-left px-3 py-2 text-xs font-medium text-slate-600 dark:text-zinc-400 uppercase">Monitored</th>
                  <th className="px-3 py-2" />
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-200 dark:divide-zinc-800">
                {pageItems.map(author => (
                  <tr key={author.id} className="bg-slate-100/50 dark:bg-zinc-900/50 hover:bg-slate-200/50 dark:hover:bg-zinc-800/50">
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
                        {author.monitored ? 'Yes' : 'No'}
                      </button>
                    </td>
                    <td className="px-3 py-2 text-right whitespace-nowrap">
                      <button
                        onClick={() => api.refreshAuthor(author.id).then(load)}
                        className="text-xs text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white mr-3"
                      >
                        Refresh
                      </button>
                      <button
                        onClick={() => handleDelete(author.id)}
                        className="text-xs text-red-400 hover:text-red-300"
                      >
                        Delete
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
            <div key={author.id} className="border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900 overflow-hidden hover:border-emerald-500 transition-colors">
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
                    {author.description || 'No description available'}
                  </p>
                </div>
              </Link>
              <div className="flex items-center justify-between px-4 py-2 bg-slate-200/50 dark:bg-zinc-800/50 border-t border-slate-200 dark:border-zinc-800">
                <button
                  onClick={() => handleToggleMonitored(author)}
                  className={`text-xs px-2 py-1 rounded ${author.monitored ? 'bg-emerald-500/20 text-emerald-400' : 'bg-slate-300 dark:bg-zinc-700 text-slate-600 dark:text-zinc-400'}`}
                >
                  {author.monitored ? 'Monitored' : 'Unmonitored'}
                </button>
                <div className="flex gap-2">
                  <button
                    onClick={() => api.refreshAuthor(author.id).then(load)}
                    className="text-xs text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white"
                    title="Refresh metadata"
                  >
                    Refresh
                  </button>
                  <button
                    onClick={() => handleDelete(author.id)}
                    className="text-xs text-red-400 hover:text-red-300"
                  >
                    Delete
                  </button>
                </div>
              </div>
            </div>
          ))}
        </div>
      )}
      <Pagination {...paginationProps} />

      {showAdd && <AddAuthorModal onClose={() => setShowAdd(false)} onAdded={load} />}
    </div>
  )
}
