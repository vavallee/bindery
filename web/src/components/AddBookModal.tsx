import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, Book } from '../api/client'

interface Props {
  onClose: () => void
  onAdded: (book: Book) => void
}

export default function AddBookModal({ onClose, onAdded }: Props) {
  const { t } = useTranslation()
  const [query, setQuery] = useState('')
  const [results, setResults] = useState<Book[]>([])
  const [searching, setSearching] = useState(false)
  const [searchError, setSearchError] = useState<string | null>(null)
  const [addError, setAddError] = useState<string | null>(null)
  const [adding, setAdding] = useState<string | null>(null)
  const [added, setAdded] = useState<Set<string>>(new Set())
  const [searchOnAdd, setSearchOnAdd] = useState(true)
  // '' = keep the provider's media type / the default.media_type setting.
  const [mediaType, setMediaType] = useState('')

  const search = async () => {
    const q = query.trim()
    if (!q) return
    setSearching(true)
    setSearchError(null)
    setAddError(null)
    try {
      // ISBN lookup takes priority when the query looks like an ISBN.
      if (/^97[89]\d{10}$|^\d{9}[\dX]$/.test(q.replace(/[-\s]/g, ''))) {
        const book = await api.lookupISBN(q.replace(/[-\s]/g, ''))
        setResults([book])
      } else if (/^B[0-9A-Z]{9}$/i.test(q)) {
        // ASIN (Audible/Amazon) lookup — a 10-char token starting with B.
        // Checked after ISBN; the two patterns don't overlap.
        const book = await api.lookupASIN(q.toUpperCase())
        setResults([book])
      } else {
        const books = await api.searchBooks(q)
        // Guard against a `null` body (e.g. an empty search the backend
        // encoded as `null` instead of `[]`) so the render's `.map()` can't crash.
        setResults(books ?? [])
      }
    } catch (err) {
      setSearchError(err instanceof Error ? err.message : t('addBookModal.searchFailed'))
      setResults([])
    } finally {
      setSearching(false)
    }
  }

  const addBook = async (book: Book) => {
    if (!book.foreignBookId || !book.author?.authorName) return
    setAdding(book.foreignBookId)
    setAddError(null)
    try {
      const created = await api.addBook({
        foreignBookId: book.foreignBookId,
        // foreignAuthorId may be empty (e.g. DNB results) — the backend
        // resolves the author by ISBN against OpenLibrary in that case.
        foreignAuthorId: book.author.foreignAuthorId ?? '',
        authorName: book.author.authorName,
        searchOnAdd,
        ...(mediaType ? { mediaType } : {}),
      })
      setAdded(prev => new Set(prev).add(book.foreignBookId))
      onAdded(created)
    } catch (err: unknown) {
      setAddError(err instanceof Error ? err.message : t('addBookModal.addFailed'))
    } finally {
      setAdding(null)
    }
  }

  return (
    <div className="fixed inset-0 bg-black/60 flex items-center justify-center p-4 z-50" onClick={onClose}>
      <div role="dialog" aria-modal="true" aria-labelledby="add-book-title" className="bg-slate-100 dark:bg-zinc-900 border border-slate-300 dark:border-zinc-700 rounded-lg w-full max-w-lg shadow-2xl max-h-[90vh] flex flex-col" onClick={e => e.stopPropagation()}>
        <div className="p-4 border-b border-slate-200 dark:border-zinc-800">
          <h3 id="add-book-title" className="text-lg font-semibold">{t('addBookModal.title')}</h3>
          <p className="text-xs text-fg-muted mt-0.5">{t('addBookModal.description')}</p>
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
              <span className="font-medium">{t('addBookModal.autoSearchLabel')}</span>
              <span className="block text-xs text-fg-muted mt-0.5">{t('addBookModal.autoSearchHint')}</span>
            </span>
          </label>

          <label className="flex items-center gap-2 text-sm mb-3 select-none">
            <span className="font-medium">{t('addBookModal.format')}</span>
            <select
              aria-label={t('addBookModal.formatLabel')}
              value={mediaType}
              onChange={e => setMediaType(e.target.value)}
              className="text-xs bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-2 py-1 focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600"
              title={t('addBookModal.formatHint')}
            >
              <option value="">{t('addBookModal.defaultFormat')}</option>
              <option value="ebook">{t('common.ebook')}</option>
              <option value="audiobook">{t('common.audiobook')}</option>
              <option value="both">{t('common.both')}</option>
            </select>
          </label>

          <div className="flex gap-2">
            <input
              type="text"
              value={query}
              onChange={e => setQuery(e.target.value)}
              onKeyDown={e => e.key === 'Enter' && search()}
              placeholder={t('addBookModal.searchPlaceholder')}
              className="flex-1 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded-md px-3 py-2 text-sm focus:outline-none focus:border-emerald-500"
              autoFocus
            />
            <button
              onClick={search}
              disabled={searching || !query.trim()}
              className="px-4 py-2 bg-emerald-600 hover:bg-emerald-500 disabled:opacity-50 rounded-md text-sm font-medium"
            >
              {searching ? t('addBookModal.searching') : t('common.search')}
            </button>
          </div>

          <div className="mt-4 space-y-2 max-h-[50vh] overflow-y-auto">
            {results.map(book => {
              const key = book.foreignBookId || book.title
              const isAdded = added.has(book.foreignBookId)
              const isAdding = adding === book.foreignBookId
              // Author *name* is enough — when foreignAuthorId is missing
              // (DNB results), the backend resolves it by ISBN at add-time.
              const canAdd = !!book.author?.authorName
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
                    title={!canAdd ? t('addBookModal.authorMissing') : undefined}
                  >
                    {isAdded ? t('addBookModal.added') : isAdding ? t('addBookModal.adding') : t('common.add')}
                  </button>
                </div>
              )
            })}
            {searchError && (
              <p role="alert" className="text-sm text-red-700 dark:text-red-300 text-center py-4">{searchError}</p>
            )}
            {results.length === 0 && !searching && !searchError && query && (
              <p className="text-sm text-fg-muted text-center py-4">{t('common.noResults')}</p>
            )}
          </div>
          {addError && <div role="alert" className="mt-3 px-3 py-2 bg-red-100 dark:bg-red-950/30 border border-red-300 dark:border-red-900 rounded text-sm text-red-800 dark:text-red-300">{addError}</div>}
        </div>

        <div className="p-4 border-t border-slate-200 dark:border-zinc-800 flex justify-end">
          <button onClick={onClose} className="px-4 py-2 text-sm text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white">{t('common.cancel')}</button>
        </div>
      </div>
    </div>
  )
}
