import { useEffect, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { api, Book, HistoryEvent, SearchResult } from '../api/client'

function formatSize(n: number): string {
  if (!n || n <= 0) return ''
  if (n >= 1073741824) return (n / 1073741824).toFixed(1) + ' GB'
  if (n >= 1048576) return (n / 1048576).toFixed(0) + ' MB'
  return (n / 1024).toFixed(0) + ' KB'
}

function formatDuration(seconds?: number): string {
  if (!seconds || seconds <= 0) return ''
  const h = Math.floor(seconds / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  if (h > 0) return `${h}h ${m}m`
  return `${m}m`
}

const statusColors: Record<string, string> = {
  wanted: 'bg-amber-500/20 text-amber-700 dark:text-amber-400',
  downloading: 'bg-blue-500/20 text-blue-700 dark:text-blue-400',
  downloaded: 'bg-purple-500/20 text-purple-700 dark:text-purple-400',
  imported: 'bg-emerald-500/20 text-emerald-700 dark:text-emerald-400',
  skipped: 'bg-slate-300 dark:bg-zinc-700 text-slate-600 dark:text-zinc-400',
}

export default function BookDetailPage() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const bookId = Number(id)

  const [book, setBook] = useState<Book | null>(null)
  const [events, setEvents] = useState<HistoryEvent[]>([])
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [searching, setSearching] = useState(false)
  const [results, setResults] = useState<SearchResult[] | null>(null)
  const [grabbing, setGrabbing] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [asinDraft, setAsinDraft] = useState('')
  const [enriching, setEnriching] = useState(false)
  const [deletingFile, setDeletingFile] = useState(false)
  const [deletingBook, setDeletingBook] = useState(false)

  useEffect(() => {
    let cancelled = false
    setLoading(true)
    Promise.all([
      api.getBook(bookId).then(b => { if (!cancelled) { setBook(b); setAsinDraft(b.asin || '') } }),
      api.listHistory({ bookId }).then(setEvents).catch(() => {}),
    ])
      .catch(err => setError(err instanceof Error ? err.message : 'Failed to load'))
      .finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, [bookId])

  const saveField = async (patch: Partial<Book>) => {
    if (!book) return
    setSaving(true)
    setError(null)
    try {
      const updated = await api.updateBook(book.id, patch)
      setBook(updated)
      if (patch.asin !== undefined) setAsinDraft(updated.asin || '')
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Save failed')
    } finally {
      setSaving(false)
    }
  }

  const runSearch = async () => {
    if (!book) return
    setSearching(true)
    setResults(null)
    setError(null)
    try {
      setResults(await api.searchBook(book.id))
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Search failed')
    } finally {
      setSearching(false)
    }
  }

  const grab = async (r: SearchResult) => {
    if (!book) return
    setGrabbing(r.guid)
    setError(null)
    try {
      await api.grab({
        guid: r.guid,
        title: r.title,
        nzbUrl: r.nzbUrl,
        size: r.size,
        bookId: book.id,
      })
      // Refresh book + history
      const [b, h] = await Promise.all([
        api.getBook(book.id),
        api.listHistory({ bookId: book.id }),
      ])
      setBook(b)
      setEvents(h)
      setResults(null)
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Grab failed')
    } finally {
      setGrabbing(null)
    }
  }

  const deleteFile = async () => {
    if (!book || !book.filePath) return
    const label = book.mediaType === 'audiobook' ? 'the audiobook folder' : 'this file'
    if (!window.confirm(`Permanently delete ${label} from disk?\n\n${book.filePath}\n\nThe book record stays; it will flip back to "wanted".`)) return
    setDeletingFile(true)
    setError(null)
    try {
      const updated = await api.deleteBookFile(book.id)
      setBook(updated)
      const h = await api.listHistory({ bookId: book.id }).catch(() => events)
      setEvents(h)
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Delete failed')
    } finally {
      setDeletingFile(false)
    }
  }

  const deleteBook = async () => {
    if (!book) return
    const hasFiles = !!book.filePath
    const msg = hasFiles
      ? `Delete "${book.title}" AND its files on disk?\n\n${book.filePath}\n\nThis cannot be undone.`
      : `Delete "${book.title}"?\n\nThis cannot be undone.`
    if (!window.confirm(msg)) return
    setDeletingBook(true)
    setError(null)
    try {
      await api.deleteBook(book.id, hasFiles)
      navigate(`/author/${book.authorId}`)
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Delete failed')
      setDeletingBook(false)
    }
  }

  const enrich = async () => {
    if (!book || !book.asin) return
    setEnriching(true)
    setError(null)
    try {
      const updated = await api.enrichAudiobook(book.id)
      setBook(updated)
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Enrich failed')
    } finally {
      setEnriching(false)
    }
  }

  if (loading) return <div className="text-slate-600 dark:text-zinc-500">Loading…</div>
  if (!book) return <div className="text-slate-600 dark:text-zinc-500">Book not found</div>

  const mt = book.mediaType || 'ebook'
  const typeBtn = (type: 'ebook' | 'audiobook') =>
    `px-3 py-1.5 rounded text-sm font-medium transition-colors ${
      mt === type
        ? 'bg-emerald-600 text-white'
        : 'bg-slate-200 dark:bg-zinc-800 text-slate-700 dark:text-zinc-300 hover:bg-slate-300 dark:hover:bg-zinc-700'
    }`

  return (
    <div className="max-w-4xl">
      <div className="mb-4 flex items-center gap-3 text-sm">
        <button onClick={() => navigate(-1)} className="text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white">← Back</button>
      </div>

      <div className="flex flex-col sm:flex-row gap-6 mb-8">
        <div className="w-40 flex-shrink-0">
          {book.imageUrl ? (
            <img src={book.imageUrl} alt={book.title} className="w-full rounded-lg shadow-lg" />
          ) : (
            <div className="aspect-[2/3] bg-slate-200 dark:bg-zinc-800 rounded-lg flex items-center justify-center p-4 text-center text-sm text-slate-500 dark:text-zinc-600">
              {book.title}
            </div>
          )}
        </div>
        <div className="min-w-0 flex-1">
          <h2 className="text-2xl font-bold mb-1">{book.title}</h2>
          {book.author?.authorName && (
            <Link to={`/author/${book.authorId}`} className="text-sm text-emerald-500 hover:text-emerald-400">
              {book.author.authorName}
            </Link>
          )}
          <div className="flex flex-wrap gap-2 mt-3 text-xs">
            <span className={`inline-block px-2 py-0.5 rounded font-medium ${statusColors[book.status] || 'bg-slate-300 dark:bg-zinc-700 text-slate-600 dark:text-zinc-400'}`}>
              {book.status}
            </span>
            {book.releaseDate && (
              <span className="text-slate-600 dark:text-zinc-500">{new Date(book.releaseDate).getFullYear()}</span>
            )}
            {book.narrator && (
              <span className="text-slate-600 dark:text-zinc-500">Narrated by {book.narrator}</span>
            )}
            {book.durationSeconds ? (
              <span className="text-slate-600 dark:text-zinc-500">· {formatDuration(book.durationSeconds)}</span>
            ) : null}
          </div>
          {book.description && (
            <p className="mt-4 text-sm text-slate-700 dark:text-zinc-300 leading-relaxed">{book.description}</p>
          )}
          {book.filePath && (
            <div className="mt-3 flex items-center gap-4 text-sm">
              <a
                href={`/api/v1/book/${book.id}/file`}
                className="text-emerald-500 hover:text-emerald-400"
              >
                Download file
              </a>
              <button
                onClick={deleteFile}
                disabled={deletingFile || deletingBook}
                className="text-red-500 hover:text-red-400 disabled:opacity-40"
                title={`Remove ${book.mediaType === 'audiobook' ? 'folder' : 'file'} from disk; keep the book record`}
              >
                {deletingFile ? 'Deleting…' : 'Delete file'}
              </button>
            </div>
          )}
          <div className="mt-3 text-xs text-slate-500 dark:text-zinc-500 break-all">
            {book.filePath}
          </div>
          <div className="mt-3">
            <button
              onClick={deleteBook}
              disabled={deletingBook || deletingFile}
              className="text-xs text-red-600 dark:text-red-500 hover:text-red-500 dark:hover:text-red-400 disabled:opacity-40"
            >
              {deletingBook ? 'Deleting book…' : book.filePath ? 'Delete book + files' : 'Delete book'}
            </button>
          </div>
        </div>
      </div>

      {error && (
        <div className="mb-4 px-3 py-2 bg-red-100 dark:bg-red-950/30 border border-red-300 dark:border-red-900 rounded text-sm text-red-800 dark:text-red-300">
          {error}
        </div>
      )}

      <section className="mb-6 p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900">
        <h3 className="text-sm font-semibold mb-3 text-slate-800 dark:text-zinc-200">Format</h3>
        <div className="flex gap-2 mb-4">
          <button onClick={() => saveField({ mediaType: 'ebook' })} disabled={saving} className={typeBtn('ebook')}>📖 Ebook</button>
          <button onClick={() => saveField({ mediaType: 'audiobook' })} disabled={saving} className={typeBtn('audiobook')}>🎧 Audiobook</button>
        </div>

        {mt === 'audiobook' && (
          <div className="flex items-end gap-2 mb-4">
            <div className="flex-1">
              <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">ASIN (Audible identifier)</label>
              <input
                value={asinDraft}
                onChange={e => setAsinDraft(e.target.value.toUpperCase())}
                placeholder="B08GB58KD5"
                className="w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-1.5 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600"
              />
            </div>
            <button
              onClick={() => saveField({ asin: asinDraft })}
              disabled={saving || asinDraft === (book.asin || '')}
              className="px-3 py-1.5 bg-slate-200 dark:bg-zinc-800 hover:bg-slate-300 dark:hover:bg-zinc-700 rounded text-xs font-medium disabled:opacity-40"
            >
              Save ASIN
            </button>
            <button
              onClick={enrich}
              disabled={!book.asin || enriching}
              className="px-3 py-1.5 bg-indigo-600 hover:bg-indigo-500 rounded text-xs font-medium disabled:opacity-40"
              title={book.asin ? 'Fetch narrator, duration, cover from audnex' : 'Set an ASIN first'}
            >
              {enriching ? 'Fetching…' : 'Enrich from audnex'}
            </button>
          </div>
        )}

        <button
          onClick={runSearch}
          disabled={searching}
          className="w-full sm:w-auto px-4 py-2 bg-emerald-600 hover:bg-emerald-500 disabled:opacity-50 rounded text-sm font-medium"
        >
          {searching ? 'Searching all indexers…' : `Search ${mt === 'audiobook' ? 'audiobook' : 'ebook'} indexers`}
        </button>
      </section>

      {results !== null && results.length === 0 && (
        <div className="mb-6 text-center py-6 text-sm text-slate-600 dark:text-zinc-500 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900">
          No results on any indexer.
        </div>
      )}

      {results !== null && results.length > 0 && (
        <section className="mb-6">
          <h3 className="text-sm font-semibold mb-2 text-slate-800 dark:text-zinc-200">Results ({results.length})</h3>
          <div className="space-y-1">
            {results.slice(0, 20).map(r => (
              <div key={r.guid} className="flex items-center justify-between p-2 bg-slate-100 dark:bg-zinc-900 border border-slate-200 dark:border-zinc-800 rounded text-xs">
                <div className="min-w-0 mr-3">
                  <span className="truncate block text-slate-800 dark:text-zinc-200">{r.title}</span>
                  <span className="text-slate-500 dark:text-zinc-500 truncate block">
                    {r.indexerName} · {formatSize(r.size)} · {r.grabs} grabs
                  </span>
                </div>
                <button
                  onClick={() => grab(r)}
                  disabled={grabbing !== null}
                  className="px-3 py-2 bg-emerald-600 hover:bg-emerald-500 disabled:opacity-50 rounded text-[11px] font-medium flex-shrink-0"
                >
                  {grabbing === r.guid ? 'Grabbing…' : 'Grab'}
                </button>
              </div>
            ))}
          </div>
        </section>
      )}

      {events.length > 0 && (
        <section>
          <h3 className="text-sm font-semibold mb-2 text-slate-800 dark:text-zinc-200">History</h3>
          <div className="border border-slate-200 dark:border-zinc-800 rounded-lg overflow-hidden divide-y divide-slate-200 dark:divide-zinc-800">
            {events.map(ev => (
              <div key={ev.id} className="flex items-start gap-3 p-3 bg-slate-100 dark:bg-zinc-900 text-xs">
                <span className="px-2 py-0.5 rounded bg-slate-200 dark:bg-zinc-800 text-slate-700 dark:text-zinc-300 font-medium flex-shrink-0">
                  {ev.eventType}
                </span>
                <span className="text-slate-800 dark:text-zinc-200 flex-1 break-words min-w-0">{ev.sourceTitle || '—'}</span>
                <span className="text-slate-600 dark:text-zinc-500 whitespace-nowrap flex-shrink-0">
                  {new Date(ev.createdAt).toLocaleString()}
                </span>
              </div>
            ))}
          </div>
        </section>
      )}
    </div>
  )
}
