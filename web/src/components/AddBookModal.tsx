import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, Book } from '../api/client'
import { resolveBookQuery } from '../api/booklookup'
import { metadataSourceLink, providerDisplayName, providerFromBookForeignId } from '../util/metadataSource'
import MetadataLinksMenu from './MetadataLinksMenu'

interface Props {
  onClose: () => void
  onAdded: (book: Book) => void
}

export default function AddBookModal({ onClose, onAdded }: Props) {
  const { t } = useTranslation()
  const [query, setQuery] = useState('')
  const [results, setResults] = useState<Book[]>([])
  const [selectedBook, setSelectedBook] = useState<Book | null>(null)
  const [searching, setSearching] = useState(false)
  const [searchError, setSearchError] = useState<string | null>(null)
  const [addError, setAddError] = useState<string | null>(null)
  const [adding, setAdding] = useState<string | null>(null)
  const [searchOnAdd, setSearchOnAdd] = useState(true)
  const confirmationHeadingRef = useRef<HTMLHeadingElement>(null)
  // '' = keep the provider's media type / the default.media_type setting.
  const [mediaType, setMediaType] = useState('')

  useEffect(() => {
    if (selectedBook) confirmationHeadingRef.current?.focus()
  }, [selectedBook])

  const search = async () => {
    const q = query.trim()
    if (!q) return
    setSearching(true)
    setSearchError(null)
    setAddError(null)
    try {
      // ISBN / ASIN / free-text dispatch lives in resolveBookQuery so Manual
      // Import's metadata search accepts exactly the same inputs.
      setResults(await resolveBookQuery(q))
    } catch (err) {
      setSearchError(err instanceof Error ? err.message : t('addBookModal.searchFailed'))
      setResults([])
    } finally {
      setSearching(false)
    }
  }

  const addBook = async () => {
    const book = selectedBook
    if (!book) return
    if (!book.foreignBookId) return
    setAdding(book.foreignBookId)
    setAddError(null)
    try {
      const created = await api.addBook({
        foreignBookId: book.foreignBookId,
        // foreignAuthorId may be empty (e.g. DNB results) — the backend
        // resolves the author by ISBN against OpenLibrary in that case.
        // authorName may be empty too (an ISBN edition whose provider record
        // carries no author): the backend falls back to the book id, and
        // answers 422 with a "add the author manually first" hint when even
        // that fails. That is a better outcome than refusing to send (#2187).
        foreignAuthorId: book.author?.foreignAuthorId ?? '',
        authorName: book.author?.authorName ?? '',
        searchOnAdd,
        ...(mediaType ? { mediaType } : {}),
      })
      onAdded(created)
    } catch (err: unknown) {
      setAddError(err instanceof Error ? err.message : t('addBookModal.addFailed'))
    } finally {
      setAdding(null)
    }
  }

  const selectBook = (book: Book) => {
    setSelectedBook(book)
    setAddError(null)
  }

  const backToResults = () => {
    setSelectedBook(null)
    setAddError(null)
  }

  const selectedProvider = selectedBook
    ? providerDisplayName(selectedBook.metadataProvider || providerFromBookForeignId(selectedBook.foreignBookId))
    : ''
  const selectedISBNs = selectedBook?.isbns ?? []
  const selectedSourceLink = selectedBook ? metadataSourceLink(selectedBook.foreignBookId, 'book') : null
  const visibleISBNs = selectedISBNs.slice(0, 3)
  const remainingISBNs = selectedISBNs.slice(3)

  const mediaTypeLabel = (value: string) => {
    if (value === 'audiobook') return t('common.audiobook')
    if (value === 'both') return t('common.both')
    if (value === 'ebook') return t('common.ebook')
    return value
  }

  return (
    <div className="fixed inset-0 bg-black/60 flex items-center justify-center p-4 z-50" onClick={onClose}>
      <div role="dialog" aria-modal="true" aria-labelledby="add-book-title" className="bg-slate-100 dark:bg-zinc-900 border border-slate-300 dark:border-zinc-700 rounded-lg w-full max-w-lg shadow-2xl max-h-[90vh] flex flex-col" onClick={e => e.stopPropagation()}>
        <div className="p-4 border-b border-slate-200 dark:border-zinc-800">
          <h3 id="add-book-title" className="text-lg font-semibold">{t('addBookModal.title')}</h3>
          <p className="text-xs text-fg-muted mt-0.5">{t('addBookModal.description')}</p>
        </div>

        <div className="p-4 flex-1 overflow-y-auto">
          {!selectedBook ? <>
            <div className="flex gap-2">
              <input
                type="text"
                value={query}
                onChange={e => setQuery(e.target.value)}
                onKeyDown={e => e.key === 'Enter' && search()}
                placeholder={t('addBookModal.searchPlaceholder')}
                className="flex-1 min-w-0 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded-md px-3 py-2 text-sm focus:outline-none focus:border-emerald-500"
                autoFocus
              />
              <button
                onClick={search}
                disabled={searching || !query.trim()}
                className="px-4 py-2 bg-emerald-600 hover:bg-emerald-500 disabled:opacity-50 disabled:cursor-not-allowed rounded-md text-sm font-medium text-white"
              >
                {searching ? t('addBookModal.searching') : t('common.search')}
              </button>
            </div>

            <div className="mt-4 space-y-2 max-h-[50vh] overflow-y-auto">
              {results.map(book => {
                const key = book.foreignBookId || book.title
                const canAdd = !!book.foreignBookId
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
                      type="button"
                      onClick={() => selectBook(book)}
                      disabled={!canAdd}
                      aria-label={canAdd ? t('addBookModal.selectBook', { title: book.title }) : undefined}
                      className="px-3 py-1 bg-emerald-600 hover:bg-emerald-500 disabled:opacity-50 disabled:cursor-not-allowed rounded text-xs font-medium text-white flex-shrink-0"
                      title={!canAdd ? t('addBookModal.idMissing') : undefined}
                    >
                      {t('addBookModal.select')}
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
          </> : <>
            <div className="flex items-start gap-4 rounded-md border border-slate-300 dark:border-zinc-700 bg-slate-200/50 dark:bg-zinc-800/50 p-4">
              {selectedBook.imageUrl ? (
                <img
                  src={selectedBook.imageUrl}
                  alt={t('addBookModal.coverAlt', { title: selectedBook.title })}
                  className="w-28 aspect-[2/3] object-cover rounded-md flex-shrink-0"
                />
              ) : (
                <div className="w-28 aspect-[2/3] rounded-md bg-slate-300 dark:bg-zinc-800 flex flex-col items-center justify-center gap-2 px-2 text-center text-slate-600 dark:text-zinc-400 flex-shrink-0">
                  <svg className="w-8 h-8" fill="none" stroke="currentColor" viewBox="0 0 24 24" strokeWidth={1.5} aria-hidden="true">
                    <path strokeLinecap="round" strokeLinejoin="round" d="M12 6.042A8.967 8.967 0 0 0 6 3.75c-1.052 0-2.062.18-3 .512v14.25A8.987 8.987 0 0 1 6 18c2.305 0 4.408.867 6 2.292m0-14.25a8.966 8.966 0 0 1 6-2.292c1.052 0 2.062.18 3 .512v14.25A8.987 8.987 0 0 0 18 18a8.967 8.967 0 0 0-6 2.292m0-14.25v14.25" />
                  </svg>
                  <span className="text-xs">{t('addBookModal.noCover')}</span>
                </div>
              )}
              <div className="min-w-0 flex-1">
                <h4 ref={confirmationHeadingRef} tabIndex={-1} className="rounded-sm font-semibold leading-snug break-words focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-emerald-500">{selectedBook.title}</h4>
                {selectedBook.author?.authorName && <p className="mt-1 text-sm text-fg-muted">{selectedBook.author.authorName}</p>}
                <dl className="mt-3 grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-xs">
                  {selectedBook.releaseDate && <>
                    <dt className="text-fg-muted">{t('addBookModal.published')}</dt>
                    <dd>{new Date(selectedBook.releaseDate).getFullYear()}</dd>
                  </>}
                  {selectedBook.language && <>
                    <dt className="text-fg-muted">{t('addBookModal.language')}</dt>
                    <dd>{selectedBook.language}</dd>
                  </>}
                  {selectedBook.mediaType && <>
                    <dt className="text-fg-muted">{t('addBookModal.resultFormat')}</dt>
                    <dd>{mediaTypeLabel(selectedBook.mediaType)}</dd>
                  </>}
                  {selectedProvider && <>
                    <dt className="text-fg-muted">{t('addBookModal.source')}</dt>
                    <dd className="flex items-center gap-2">
                      <span>{selectedProvider}</span>
                      {selectedSourceLink && <MetadataLinksMenu links={[selectedSourceLink]} />}
                    </dd>
                  </>}
                  <dt className="text-fg-muted">{t('addBookModal.providerId')}</dt>
                  <dd className="font-mono break-all">{selectedBook.foreignBookId}</dd>
                  {selectedBook.asin && <>
                    <dt className="text-fg-muted">{t('addBookModal.asin')}</dt>
                    <dd className="font-mono break-all">{selectedBook.asin}</dd>
                  </>}
                </dl>
              </div>
            </div>

            {visibleISBNs.length > 0 && (
              <div
                className="mt-3 text-[11px] leading-5 text-slate-500 dark:text-zinc-500"
                title={t('addBookModal.identifiersHint')}
              >
                <div className="font-medium">{t('addBookModal.isbns')}</div>
                <div className="flex flex-wrap gap-x-2 font-mono">
                  {visibleISBNs.map(isbn => <span key={isbn}>{isbn}</span>)}
                </div>
                {remainingISBNs.length > 0 && (
                  <details>
                    <summary className="cursor-pointer select-none text-accent-text hover:underline underline-offset-2">
                      {t('addBookModal.showMoreIdentifiers', { count: remainingISBNs.length })}
                    </summary>
                    <div className="flex flex-wrap gap-x-2 font-mono">
                      {remainingISBNs.map(isbn => <span key={isbn}>{isbn}</span>)}
                    </div>
                  </details>
                )}
              </div>
            )}

            <div className="mt-4 space-y-3 border-t border-slate-300 dark:border-zinc-700 pt-4">
              <label className="flex items-center gap-2 text-sm select-none">
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

              <label className="flex items-start gap-2 text-sm cursor-pointer select-none">
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
            </div>

            {addError && <div role="alert" className="mt-3 px-3 py-2 bg-red-100 dark:bg-red-950/30 border border-red-300 dark:border-red-900 rounded text-sm text-red-800 dark:text-red-300">{addError}</div>}
          </>}
        </div>

        <div className="p-4 border-t border-slate-200 dark:border-zinc-800 flex justify-end gap-2">
          {selectedBook && <button type="button" onClick={backToResults} disabled={adding !== null} className="mr-auto px-4 py-2 text-sm text-fg-muted hover:text-slate-900 dark:hover:text-white disabled:opacity-50">{t('addBookModal.backToResults')}</button>}
          <button onClick={onClose} className="px-4 py-2 text-sm text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white">{t('common.cancel')}</button>
          {selectedBook && <button type="button" onClick={addBook} disabled={adding !== null} className="px-4 py-2 bg-emerald-600 hover:bg-emerald-500 disabled:opacity-50 disabled:cursor-not-allowed rounded-md text-sm font-medium text-white">{adding ? t('addBookModal.adding') : t('addBookModal.confirmAdd')}</button>}
        </div>
      </div>
    </div>
  )
}
