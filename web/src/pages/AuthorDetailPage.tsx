import { useEffect, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { api, Author, Book } from '../api/client'

const statusColors: Record<string, string> = {
  wanted: 'bg-amber-500/20 text-amber-700 dark:text-amber-400',
  downloading: 'bg-blue-500/20 text-blue-700 dark:text-blue-400',
  downloaded: 'bg-purple-500/20 text-purple-700 dark:text-purple-400',
  imported: 'bg-emerald-500/20 text-emerald-700 dark:text-emerald-400',
  skipped: 'bg-slate-300 dark:bg-zinc-700 text-slate-600 dark:text-zinc-400',
}

export default function AuthorDetailPage() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const authorId = Number(id)

  const [author, setAuthor] = useState<Author | null>(null)
  const [books, setBooks] = useState<Book[]>([])
  const [loading, setLoading] = useState(true)
  const [refreshing, setRefreshing] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    setLoading(true)
    Promise.all([
      api.getAuthor(authorId),
      api.listBooks({ authorId }),
    ])
      .then(([a, bs]) => { if (!cancelled) { setAuthor(a); setBooks(bs) } })
      .catch(err => setError(err instanceof Error ? err.message : 'Failed to load'))
      .finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, [authorId])

  const handleRefresh = async () => {
    if (!author) return
    setRefreshing(true)
    try {
      await api.refreshAuthor(author.id)
      const [a, bs] = await Promise.all([api.getAuthor(authorId), api.listBooks({ authorId })])
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
      await api.updateAuthor(author.id, { monitored: !author.monitored } as Partial<Author>)
      setAuthor({ ...author, monitored: !author.monitored })
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Update failed')
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

  if (loading) return <div className="text-slate-600 dark:text-zinc-500">Loading…</div>
  if (!author) return <div className="text-slate-600 dark:text-zinc-500">Author not found</div>

  const counts = {
    total: books.length,
    imported: books.filter(b => b.status === 'imported').length,
    wanted: books.filter(b => b.status === 'wanted').length,
    audiobook: books.filter(b => b.mediaType === 'audiobook').length,
  }

  return (
    <div className="max-w-5xl">
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
              onClick={handleDelete}
              className="px-3 py-1.5 text-red-600 dark:text-red-400 hover:text-red-500 text-xs font-medium"
            >
              Delete
            </button>
          </div>
        </div>
      </div>

      {error && (
        <div className="mb-4 px-3 py-2 bg-red-100 dark:bg-red-950/30 border border-red-300 dark:border-red-900 rounded text-sm text-red-800 dark:text-red-300">
          {error}
        </div>
      )}

      <section>
        <h3 className="text-lg font-semibold mb-3">Books</h3>
        {books.length === 0 ? (
          <p className="text-sm text-slate-600 dark:text-zinc-500">No books tracked for this author yet.</p>
        ) : (
          <div className="grid grid-cols-2 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-5 gap-4">
            {books.map(book => (
              <Link
                key={book.id}
                to={`/book/${book.id}`}
                className="border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900 overflow-hidden group hover:border-emerald-500 transition-colors"
              >
                <div className="aspect-[2/3] bg-slate-200 dark:bg-zinc-800 relative">
                  {book.imageUrl ? (
                    <img src={book.imageUrl} alt={book.title} className="w-full h-full object-cover" />
                  ) : (
                    <div className="w-full h-full flex items-center justify-center p-3 text-center">
                      <span className="text-sm text-slate-500 dark:text-zinc-600">{book.title}</span>
                    </div>
                  )}
                  <div className={`absolute top-2 right-2 px-2 py-0.5 rounded text-[10px] font-medium ${statusColors[book.status] || 'bg-slate-300 dark:bg-zinc-700 text-slate-600 dark:text-zinc-400'}`}>
                    {book.status}
                  </div>
                  {book.mediaType === 'audiobook' && (
                    <div className="absolute top-2 left-2 px-1.5 py-0.5 rounded text-[10px] font-medium bg-indigo-600/90 text-white">🎧</div>
                  )}
                </div>
                <div className="p-2">
                  <h4 className="text-xs font-medium truncate" title={book.title}>{book.title}</h4>
                  {book.releaseDate && (
                    <p className="text-[10px] text-slate-600 dark:text-zinc-500">{new Date(book.releaseDate).getFullYear()}</p>
                  )}
                </div>
              </Link>
            ))}
          </div>
        )}
      </section>
    </div>
  )
}
