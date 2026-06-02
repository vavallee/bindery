import { FormEvent, useEffect, useMemo, useState } from 'react'
import { api, Author, Book, Series } from '../api/client'

interface Props {
  series: Series
  onClose: () => void
  onLinked: (series: Series) => void
}

export default function AddSeriesBookModal({ series, onClose, onLinked }: Props) {
  const [books, setBooks] = useState<Book[]>([])
  const [authors, setAuthors] = useState<Author[]>([])
  const [loading, setLoading] = useState(true)
  const [query, setQuery] = useState('')
  const [bookId, setBookId] = useState('')
  const [position, setPosition] = useState('')
  const [primarySeries, setPrimarySeries] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let active = true
    Promise.all([api.listBooks(), api.listAuthors()])
      .then(([bookList, authorList]) => {
        if (!active) return
        setBooks(bookList.items)
        setAuthors(authorList.items)
      })
      .catch(err => {
        if (active) setError(err instanceof Error ? err.message : 'Failed to load books')
      })
      .finally(() => {
        if (active) setLoading(false)
      })
    return () => { active = false }
  }, [])

  const linkedBookIds = useMemo(() => new Set((series.books ?? []).map(entry => entry.bookId)), [series.books])

  const authorNames = useMemo(() => {
    const names = new Map<number, string>()
    for (const author of authors) names.set(author.id, author.authorName)
    return names
  }, [authors])

  const availableBooks = useMemo(() => {
    const q = query.trim().toLowerCase()
    return books.filter(book => {
      if (linkedBookIds.has(book.id)) return false
      if (!q) return true
      const authorName = book.author?.authorName || authorNames.get(book.authorId) || ''
      return book.title.toLowerCase().includes(q) || authorName.toLowerCase().includes(q)
    })
  }, [authorNames, books, linkedBookIds, query])

  const selectedBook = books.find(book => String(book.id) === bookId)

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (!selectedBook) {
      setError('Select a book')
      return
    }
    const trimmedPosition = position.trim()
    if (!trimmedPosition) {
      setError('Position is required')
      return
    }
    setSaving(true)
    setError(null)
    try {
      const updated = await api.linkBookToSeries(series.id, {
        bookId: selectedBook.id,
        positionInSeries: trimmedPosition,
        primarySeries,
      })
      onLinked(updated)
      onClose()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to add book')
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="fixed inset-0 bg-black/60 flex items-center justify-center p-4 z-50" onClick={onClose}>
      <form
        role="dialog"
        aria-modal="true"
        aria-label={`Add book to ${series.title}`}
        onSubmit={submit}
        className="bg-slate-100 dark:bg-zinc-900 border border-slate-300 dark:border-zinc-700 rounded-lg w-full max-w-lg shadow-2xl max-h-[90vh] flex flex-col"
        onClick={e => e.stopPropagation()}
      >
        <div className="p-4 border-b border-slate-200 dark:border-zinc-800">
          <h3 className="text-lg font-semibold">Add Book to Series</h3>
          <p className="text-xs text-slate-500 dark:text-zinc-500 mt-0.5 truncate">{series.title}</p>
        </div>

        <div className="p-4 flex-1 overflow-y-auto space-y-4">
          <input
            type="search"
            value={query}
            onChange={e => setQuery(e.target.value)}
            placeholder="Search library books..."
            className="w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded-md px-3 py-2 text-sm focus:outline-none focus:border-emerald-500"
            autoFocus
          />

          <div className="space-y-2 max-h-64 overflow-y-auto">
            {loading ? (
              <p className="text-sm text-slate-600 dark:text-zinc-500 text-center py-4">Loading...</p>
            ) : availableBooks.length === 0 ? (
              <p className="text-sm text-slate-600 dark:text-zinc-500 text-center py-4">No matching books found.</p>
            ) : (
              availableBooks.map(book => {
                const authorName = book.author?.authorName || authorNames.get(book.authorId) || ''
                const selected = String(book.id) === bookId
                return (
                  <label
                    key={book.id}
                    className={`flex items-center gap-3 p-3 rounded-md cursor-pointer ${
                      selected
                        ? 'bg-emerald-500/10 ring-1 ring-emerald-500/40'
                        : 'bg-slate-200/50 dark:bg-zinc-800/50 hover:bg-slate-200 dark:hover:bg-zinc-800'
                    }`}
                  >
                    <input
                      type="radio"
                      name="series-book"
                      value={book.id}
                      checked={selected}
                      onChange={e => setBookId(e.target.value)}
                      className="text-emerald-500 focus:ring-emerald-500"
                    />
                    {book.imageUrl ? (
                      <img src={book.imageUrl} alt="" className="w-8 h-10 object-cover rounded flex-shrink-0" />
                    ) : (
                      <div className="w-8 h-10 bg-slate-300 dark:bg-zinc-700 rounded flex-shrink-0" />
                    )}
                    <span className="min-w-0">
                      <span className="block text-sm font-medium truncate">{book.title}</span>
                      {authorName && <span className="block text-xs text-slate-600 dark:text-zinc-500 truncate">{authorName}</span>}
                    </span>
                  </label>
                )
              })
            )}
          </div>

          <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
            <label className="block text-sm">
              <span className="block font-medium text-slate-700 dark:text-zinc-300 mb-1">Position</span>
              <input
                type="text"
                value={position}
                onChange={e => setPosition(e.target.value)}
                placeholder="1"
                className="w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded-md px-3 py-2 text-sm focus:outline-none focus:border-emerald-500"
              />
            </label>
            <label className="flex items-center gap-2 text-sm mt-6">
              <input
                type="checkbox"
                checked={primarySeries}
                onChange={e => setPrimarySeries(e.target.checked)}
                className="accent-emerald-500"
              />
              Primary series
            </label>
          </div>

          {error && <p className="text-sm text-red-500">{error}</p>}
        </div>

        <div className="p-4 border-t border-slate-200 dark:border-zinc-800 flex justify-end gap-2">
          <button
            type="button"
            onClick={onClose}
            className="px-4 py-2 text-sm text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white"
          >
            Cancel
          </button>
          <button
            type="submit"
            disabled={saving || !bookId || !position.trim()}
            className="px-4 py-2 bg-emerald-600 hover:bg-emerald-500 disabled:opacity-50 rounded-md text-sm font-medium"
          >
            {saving ? 'Adding...' : 'Add'}
          </button>
        </div>
      </form>
    </div>
  )
}
