import { useEffect, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { api, Book, HistoryEvent, SearchResult, SearchDebug } from '../api/client'
import SearchDebugPanel from '../components/SearchDebugPanel'

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
  const [searchDebug, setSearchDebug] = useState<SearchDebug | null>(null)
  const [hasIndexers, setHasIndexers] = useState<boolean | null>(null)
  const [grabbing, setGrabbing] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [asinDraft, setAsinDraft] = useState('')
  const [enriching, setEnriching] = useState(false)
  const [deletingFile, setDeletingFile] = useState(false)
  const [deletingBook, setDeletingBook] = useState(false)
  const [togglingExclude, setTogglingExclude] = useState(false)

  useEffect(() => {
    if (book?.title) {
      document.title = `${book.title} · Bindery`
      return () => { document.title = 'Bindery' }
    }
  }, [book?.title])

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
    setSearchDebug(null)
    setError(null)
    try {
      const [r, indexers] = await Promise.all([
        api.searchBook(book.id),
        api.listIndexers(),
      ])
      setHasIndexers(indexers.length > 0)
      setResults(r.results)
      setSearchDebug(r.debug ?? null)
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
        protocol: r.protocol,
        mediaType: book.mediaType,
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

  const deleteFile = async (format?: 'ebook' | 'audiobook') => {
    if (!book) return
    const hasEbook = !!book.ebookFilePath
    const hasAudiobook = !!book.audiobookFilePath
    const hasLegacy = !!book.filePath && !hasEbook && !hasAudiobook
    if (!hasEbook && !hasAudiobook && !hasLegacy) return

    let label: string
    let path: string
    if (format === 'ebook' && hasEbook) {
      label = 'the ebook file'; path = book.ebookFilePath
    } else if (format === 'audiobook' && hasAudiobook) {
      label = 'the audiobook folder'; path = book.audiobookFilePath
    } else {
      label = book.mediaType === 'audiobook' ? 'the audiobook folder' : 'this file'
      path = book.filePath
    }
    if (!window.confirm(`Permanently delete ${label} from disk?\n\n${path}\n\nThe book record stays; it will flip back to "wanted".`)) return
    setDeletingFile(true)
    setError(null)
    try {
      const params = format ? `?format=${format}` : ''
      const updated = await api.deleteBookFile(book.id, params)
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
    const hasFiles = !!(book.filePath || book.ebookFilePath || book.audiobookFilePath)
    const fileSummary = [book.ebookFilePath, book.audiobookFilePath].filter(Boolean).join('\n') || book.filePath
    const msg = hasFiles
      ? `Delete "${book.title}" AND its files on disk?\n\n${fileSummary}\n\nThis cannot be undone.`
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

  const toggleExclude = async () => {
    if (!book) return
    setTogglingExclude(true)
    try {
      const updated = await api.toggleExcluded(book.id)
      setBook(updated)
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to toggle exclude')
    } finally {
      setTogglingExclude(false)
    }
  }

  if (loading) return <div className="text-slate-600 dark:text-zinc-500">Loading…</div>
  if (!book) return <div className="text-slate-600 dark:text-zinc-500">Book not found</div>

  const mt = book.mediaType || 'ebook'
  const typeBtn = (type: 'ebook' | 'audiobook' | 'both') =>
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
            {book.excluded && (
              <span className="inline-block px-2 py-0.5 rounded font-medium bg-amber-500/20 text-amber-700 dark:text-amber-400">
                Excluded
              </span>
            )}
            {book.releaseDate && (
              <span className="text-slate-600 dark:text-zinc-500">
                {new Date(book.releaseDate).toLocaleDateString(undefined, { year: 'numeric', month: 'long', day: 'numeric' })}
              </span>
            )}
            {book.language ? (
              <span className="text-slate-600 dark:text-zinc-500">{book.language}</span>
            ) : (
              <span
                className="inline-block px-2 py-0.5 rounded font-medium bg-amber-500/20 text-amber-700 dark:text-amber-400"
                title="Metadata source did not report a language for this book. It bypassed the language filter."
              >
                Language unknown
              </span>
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
          {mt === 'both' ? (
            <div className="mt-3 space-y-1.5 text-sm">
              {book.ebookFilePath && (
                <div className="flex items-center gap-3">
                  <span className="text-xs text-slate-500 dark:text-zinc-500 break-all flex-1">📖 {book.ebookFilePath}</span>
                  <button onClick={() => deleteFile('ebook')} disabled={deletingFile || deletingBook}
                    className="text-red-500 hover:text-red-400 disabled:opacity-40 text-xs flex-shrink-0">Delete ebook</button>
                </div>
              )}
              {book.audiobookFilePath && (
                <div className="flex items-center gap-3">
                  <span className="text-xs text-slate-500 dark:text-zinc-500 break-all flex-1">🎧 {book.audiobookFilePath}</span>
                  <button onClick={() => deleteFile('audiobook')} disabled={deletingFile || deletingBook}
                    className="text-red-500 hover:text-red-400 disabled:opacity-40 text-xs flex-shrink-0">Delete audiobook</button>
                </div>
              )}
            </div>
          ) : (book.filePath || book.ebookFilePath) ? (
            <div className="mt-3 flex items-center gap-4 text-sm">
              <a href={`/api/v1/book/${book.id}/file`} className="text-emerald-500 hover:text-emerald-400">
                Download file
              </a>
              <button
                onClick={() => deleteFile()}
                disabled={deletingFile || deletingBook}
                className="text-red-500 hover:text-red-400 disabled:opacity-40"
                title={`Remove ${book.mediaType === 'audiobook' ? 'folder' : 'file'} from disk; keep the book record`}
              >
                {deletingFile ? 'Deleting…' : 'Delete file'}
              </button>
              <span className="text-xs text-slate-500 dark:text-zinc-500 break-all">{book.filePath || book.ebookFilePath}</span>
            </div>
          ) : null}
          <div className="mt-3 flex items-center gap-3">
            <button
              onClick={toggleExclude}
              disabled={togglingExclude}
              className={`text-xs font-medium px-3 py-1.5 rounded transition-colors disabled:opacity-40 ${
                book.excluded
                  ? 'bg-amber-500/20 text-amber-700 dark:text-amber-400 hover:bg-amber-500/30'
                  : 'bg-slate-200 dark:bg-zinc-800 text-slate-700 dark:text-zinc-300 hover:bg-slate-300 dark:hover:bg-zinc-700'
              }`}
              title={book.excluded ? 'Un-exclude this book from searches and the Wanted page' : 'Exclude this book from searches and the Wanted page'}
            >
              {togglingExclude ? '…' : book.excluded ? 'Un-exclude' : 'Exclude'}
            </button>
            {book.excluded && (
              <span className="text-xs text-amber-600 dark:text-amber-400 font-medium">Excluded from searches</span>
            )}
            <button
              onClick={deleteBook}
              disabled={deletingBook || deletingFile}
              className="text-xs text-red-600 dark:text-red-500 hover:text-red-500 dark:hover:text-red-400 disabled:opacity-40"
            >
              {deletingBook ? 'Deleting book…' : (book.filePath || book.ebookFilePath || book.audiobookFilePath) ? 'Delete book + files' : 'Delete book'}
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
          <button onClick={() => saveField({ mediaType: 'both' })} disabled={saving} className={typeBtn('both')}>📖🎧 Both</button>
        </div>

        {mt === 'both' && (
          <div className="mb-4 grid grid-cols-2 gap-2 text-xs">
            <div className={`px-3 py-2 rounded border ${book.ebookFilePath ? 'border-emerald-500/50 bg-emerald-500/5 text-emerald-700 dark:text-emerald-400' : 'border-slate-200 dark:border-zinc-700 text-slate-500 dark:text-zinc-500'}`}>
              <span className="font-medium">📖 Ebook</span>
              <span className="ml-2">{book.ebookFilePath ? '✓ on disk' : 'needed'}</span>
            </div>
            <div className={`px-3 py-2 rounded border ${book.audiobookFilePath ? 'border-emerald-500/50 bg-emerald-500/5 text-emerald-700 dark:text-emerald-400' : 'border-slate-200 dark:border-zinc-700 text-slate-500 dark:text-zinc-500'}`}>
              <span className="font-medium">🎧 Audiobook</span>
              <span className="ml-2">{book.audiobookFilePath ? '✓ on disk' : 'needed'}</span>
            </div>
          </div>
        )}

        {(mt === 'audiobook' || mt === 'both') && (
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
          {searching
            ? 'Searching all indexers…'
            : mt === 'audiobook'
              ? 'Search audiobook indexers'
              : mt === 'both'
                ? 'Search ebook + audiobook indexers'
                : 'Search ebook indexers'}
        </button>
      </section>

      {results !== null && results.length === 0 && (
        <div className="mb-6 text-center py-6 text-sm text-slate-600 dark:text-zinc-500 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900">
          {hasIndexers === false
            ? <>No indexers configured — add one in <Link to="/settings" className="underline">Settings</Link>.</>
            : 'No results on any indexer — expand Search details below to see why.'}
        </div>
      )}

      {searchDebug && (
        <SearchDebugPanel
          debug={searchDebug}
          resultCount={results?.length ?? 0}
          defaultOpen={results !== null && results.length === 0}
        />
      )}

      {results !== null && results.length > 0 && (
        <section className="mb-6">
          <h3 className="text-sm font-semibold mb-2 text-slate-800 dark:text-zinc-200">Results ({results.length})</h3>
          <div className="space-y-1">
            {results.slice(0, 20).map(r => (
              <div key={r.guid} className={`flex items-center justify-between p-2 border rounded text-xs ${r.approved === false ? 'bg-slate-50 dark:bg-zinc-950 border-slate-200 dark:border-zinc-800 opacity-60' : 'bg-slate-100 dark:bg-zinc-900 border-slate-200 dark:border-zinc-800'}`}>
                <div className="min-w-0 mr-3">
                  <span className="truncate block text-slate-800 dark:text-zinc-200">{r.title}</span>
                  <span className="text-slate-500 dark:text-zinc-500 truncate block">
                    {r.indexerName} · {formatSize(r.size)} · {r.grabs} grabs
                    {r.rejection && <span className="ml-2 text-amber-600 dark:text-amber-400">· {r.rejection}</span>}
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
