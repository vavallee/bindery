import { useState } from 'react'
import { api, Book } from '../api/client'

interface Props {
  onClose: () => void
  onAdded: (book: Book) => void
}

export default function AddBookModal({ onClose, onAdded }: Props) {
  const [query, setQuery] = useState('')
  const [results, setResults] = useState<Book[]>([])
  const [searching, setSearching] = useState(false)
  const [searchError, setSearchError] = useState<string | null>(null)
  const [adding, setAdding] = useState<string | null>(null)
  const [added, setAdded] = useState<Set<string>>(new Set())
  const [searchOnAdd, setSearchOnAdd] = useState(true)

  const search = async () => {
    const q = query.trim()
    if (!q) return
    setSearching(true)
    setSearchError(null)
    try {
      // ISBN lookup takes priority when the query looks like an ISBN.
      if (/^97[89]\d{10}$|^\d{9}[\dX]$/.test(q.replace(/[-\s]/g, ''))) {
        const book = await api.lookupISBN(q.replace(/[-\s]/g, ''))
        setResults([book])
      } else {
        const books = await api.searchBooks(q)
        setResults(books)
      }
    } catch (err) {
      setSearchError(err instanceof Error ? err.message : 'Search failed')
      setResults([])
    } finally {
      setSearching(false)
    }
  }

  const addBook = async (book: Book) => {
    if (!book.foreignBookId || !book.author?.foreignAuthorId) return
    setAdding(book.foreignBookId)
    try {
      const created = await api.addBook({
        foreignBookId: book.foreignBookId,
        foreignAuthorId: book.author.foreignAuthorId,
        authorName: book.author.authorName,
        searchOnAdd,
      })
      setAdded(prev => new Set(prev).add(book.foreignBookId))
      onAdded(created)
    } catch (err: unknown) {
      alert(err instanceof Error ? err.message : 'Failed to add book')
    } finally {
      setAdding(null)
    }
  }

  return (
    <div className="fixed inset-0 bg-black/60 flex items-center justify-center p-4 z-50" onClick={onClose}>
      <div className="bg-slate-100 dark:bg-zinc-900 border border-slate-300 dark:border-zinc-700 rounded-lg w-full max-w-lg shadow-2xl max-h-[90vh] flex flex-col" onClick={e => e.stopPropagation()}>
        <div className="p-4 border-b border-slate-200 dark:border-zinc-800">
          <h3 className="text-lg font-semibold">Add Book</h3>
          <p className="text-xs text-slate-500 dark:text-zinc-500 mt-0.5">Search by title or ISBN to add a specific book to your wanted list.</p>
        </div>

        <div className="p-4 flex-1 overflow-y-auto">
          <label className="flex items-start gap-2 text-sm mb-3 cursor-pointer select-none">
            <input
              type="checkbox"
              checked={searchOnAdd}
              onChange={e => setSearchOnAdd(e.target.checked)}
              className="accent-emerald-500 mt-0.5 flex-shrink-0"
            />
            <span>
              <span className="font-medium">Search indexers after adding</span>
              <span className="block text-xs text-slate-600 dark:text-zinc-400 mt-0.5">Attempt to grab the book automatically once it's added to wanted.</span>
            </span>
          </label>

          <div className="flex gap-2">
            <input
              type="text"
              value={query}
              onChange={e => setQuery(e.target.value)}
              onKeyDown={e => e.key === 'Enter' && search()}
              placeholder="Title or ISBN (e.g. Dune, 9780441478125)"
              className="flex-1 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded-md px-3 py-2 text-sm focus:outline-none focus:border-emerald-500"
              autoFocus
            />
            <button
              onClick={search}
              disabled={searching || !query.trim()}
              className="px-4 py-2 bg-emerald-600 hover:bg-emerald-500 disabled:opacity-50 rounded-md text-sm font-medium"
            >
              {searching ? 'Searching…' : 'Search'}
            </button>
          </div>

          <div className="mt-4 space-y-2 max-h-[50vh] overflow-y-auto">
            {results.map(book => {
              const key = book.foreignBookId || book.title
              const isAdded = added.has(book.foreignBookId)
              const isAdding = adding === book.foreignBookId
              const canAdd = !!book.author?.foreignAuthorId
              return (
                <div key={key} className="flex items-center gap-3 p-3 rounded-md bg-slate-200/50 dark:bg-zinc-800/50 hover:bg-slate-200 dark:hover:bg-zinc-800">
                  {book.imageUrl && (
                    <img src={book.imageUrl} alt="" className="w-10 h-14 object-cover rounded flex-shrink-0" />
                  )}
                  <div className="flex-1 min-w-0">
                    <div className="font-medium text-sm truncate">{book.title}</div>
                    {book.author && (
                      <div className="text-xs text-slate-600 dark:text-zinc-500">{book.author.authorName}</div>
                    )}
                    {book.releaseDate && (
                      <div className="text-xs text-slate-500 dark:text-zinc-600">{new Date(book.releaseDate).getFullYear()}</div>
                    )}
                  </div>
                  <button
                    onClick={() => addBook(book)}
                    disabled={isAdded || isAdding || !canAdd}
                    className={`px-3 py-1 rounded text-xs font-medium flex-shrink-0 ${isAdded ? 'bg-emerald-700 text-white opacity-75 cursor-default' : 'bg-emerald-600 hover:bg-emerald-500 disabled:opacity-50'}`}
                    title={!canAdd ? 'Author metadata unavailable — try a more specific search' : undefined}
                  >
                    {isAdded ? 'Added ✓' : isAdding ? 'Adding…' : 'Add'}
                  </button>
                </div>
              )
            })}
            {searchError && (
              <p className="text-sm text-red-400 text-center py-4">{searchError}</p>
            )}
            {results.length === 0 && !searching && !searchError && query && (
              <p className="text-sm text-slate-600 dark:text-zinc-500 text-center py-4">No results found.</p>
            )}
          </div>
        </div>

        <div className="p-4 border-t border-slate-200 dark:border-zinc-800 flex justify-end">
          <button onClick={onClose} className="px-4 py-2 text-sm text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white">Close</button>
        </div>
      </div>
    </div>
  )
}
