import { useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, Author, Book } from '../api/client'
import { isbnFromQuery, resolveBookQuery } from '../api/booklookup'
import { splitAuthorSearchResults } from './addAuthorTitleGuard'
import { groupAddResults } from './addToLibraryGrouping'
import AddAuthorConfirm from './AddAuthorConfirm'
import { AuthorAddDefaults, loadAuthorAddDefaults } from './authorAddDefaults'
import AddBookConfirm from './AddBookConfirm'
import { authorProviderKey } from '../util/authorMetadata'
import { providerDisplayName } from '../util/metadataSource'

// What the modal reports back once something is in the library. Callers that
// only refresh a list ignore the payload; the header search navigates to it.
export type AddedToLibrary =
  | { kind: 'author'; author: Author }
  | { kind: 'book'; book: Book }

interface Props {
  onClose: () => void
  onAdded: (added: AddedToLibrary) => void
  // Pre-fills the search box and runs the search on open. The header library
  // search hands its query over this way (#2551) so a miss in the library
  // becomes an add without retyping.
  initialQuery?: string
  // Which button opened the dialog. Only the placeholder changes: the search
  // itself always fans out to authors and books, because the user's intent is
  // resolved by what they pick, not by what they said up front (#1227).
  mode?: 'author' | 'book'
}

type Selected =
  | { kind: 'author'; author: Author }
  | { kind: 'book'; book: Book }

// An Audible/Amazon ASIN: 10 characters starting with B. Mirrors the check in
// resolveBookQuery, which owns the dispatch; this only decides whether the
// author search is worth running at all.
const ASIN_RE = /^B[0-9A-Z]{9}$/i

function basePath(): string {
  return (window as unknown as { __BINDERY_BASE__?: string }).__BINDERY_BASE__ ?? ''
}

function isIdentifierQuery(q: string): boolean {
  return isbnFromQuery(q) !== null || ASIN_RE.test(q)
}

export default function AddToLibraryModal({ onClose, onAdded, initialQuery, mode }: Props) {
  const { t } = useTranslation()
  const [query, setQuery] = useState(initialQuery ?? '')
  const [authors, setAuthors] = useState<Author[]>([])
  const [hiddenAuthors, setHiddenAuthors] = useState<Author[]>([])
  const [showHidden, setShowHidden] = useState(false)
  const [books, setBooks] = useState<Book[]>([])
  const [searched, setSearched] = useState(false)
  const [searching, setSearching] = useState(false)
  const [searchError, setSearchError] = useState<string | null>(null)
  const [partialError, setPartialError] = useState<string | null>(null)
  const [searchedISBN, setSearchedISBN] = useState<string | null>(null)
  const [selected, setSelected] = useState<Selected | null>(null)
  // The configured primary metadata provider, when one is explicitly set.
  // Used to flag author results that would sync from another provider (#2237).
  const [primaryProvider, setPrimaryProvider] = useState<string | null>(null)
  // What the author confirm step needs, fetched once per open rather than on
  // every row selection. Null until it arrives; the step disables Add
  // meanwhile.
  const [authorDefaults, setAuthorDefaults] = useState<AuthorAddDefaults | null>(null)
  const dialogRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    let cancelled = false
    loadAuthorAddDefaults().then(d => { if (!cancelled) setAuthorDefaults(d) })
    return () => { cancelled = true }
  }, [])

  useEffect(() => {
    api.getSetting('metadata.primary_provider')
      .then(s => {
        const value = (s.value || '').trim().toLowerCase()
        // Mirror MetadataPrimaryProviders on the backend; anything else means
        // no explicit choice, so no notice.
        if (value === 'openlibrary' || value === 'dnb' || value === 'hardcover') setPrimaryProvider(value)
      })
      .catch(() => { /* unset; no provider notice needed */ })
  }, [])

  // Provider searches run on Enter or the Search button only, never on a
  // keystroke: every call here is a live request to a metadata provider with
  // no quota of its own.
  const search = async (term = query) => {
    const q = term.trim()
    if (!q || searching) return
    setSearching(true)
    setSearchError(null)
    setPartialError(null)
    setShowHidden(false)
    try {
      if (isIdentifierQuery(q)) {
        // An ISBN or ASIN names one edition; the author endpoint has nothing
        // to say about it. resolveBookQuery owns the lookup dispatch so this
        // dialog and Manual Import accept exactly the same inputs.
        const found = await resolveBookQuery(q)
        setAuthors([])
        setHiddenAuthors([])
        setBooks(found)
        setSearchedISBN(isbnFromQuery(q))
      } else {
        let authorError: unknown = null
        let bookError: unknown = null
        const [foundAuthors, foundBooks] = await Promise.all([
          api.searchAuthors(q).then(r => r ?? []).catch((err: unknown) => { authorError = err; return [] as Author[] }),
          api.searchBooks(q).then(r => r ?? []).catch((err: unknown) => { bookError = err; return [] as Book[] }),
        ])
        const failure = authorError ?? bookError
        if (failure && foundAuthors.length === 0 && foundBooks.length === 0) {
          throw failure
        }
        // The title guard keeps a book title that the author endpoint echoes
        // back as an "author" out of the author rows; the book itself still
        // shows as a book.
        const split = splitAuthorSearchResults(foundAuthors, foundBooks, q)
        setAuthors(split.visible)
        setHiddenAuthors(split.hidden)
        setBooks(foundBooks)
        setSearchedISBN(null)
        if (failure) {
          setPartialError(failure instanceof Error ? failure.message : String(failure))
        }
      }
    } catch (err) {
      setSearchError(err instanceof Error ? err.message : String(err))
      setAuthors([])
      setHiddenAuthors([])
      setBooks([])
      setSearchedISBN(null)
    } finally {
      setSearched(true)
      setSearching(false)
    }
  }

  const initialSearchRan = useRef(false)
  useEffect(() => {
    if (initialSearchRan.current || !initialQuery?.trim()) return
    initialSearchRan.current = true
    void search(initialQuery)
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  const mismatchedProvider = (author: Author): string | null => {
    if (!primaryProvider) return null
    const provider = authorProviderKey(author)
    if (!provider || provider === primaryProvider) return null
    return provider
  }

  const handleDialogKeyDown = (event: React.KeyboardEvent<HTMLDivElement>) => {
    if (event.key === 'Escape') {
      event.preventDefault()
      onClose()
      return
    }
    if (event.key !== 'Tab') return

    const focusable = dialogRef.current?.querySelectorAll<HTMLElement>(
      'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), summary, textarea:not([disabled]), [tabindex]:not([tabindex="-1"])',
    )
    if (!focusable?.length) return
    const first = focusable[0]
    const last = focusable[focusable.length - 1]
    const activeElement = document.activeElement as HTMLElement | null
    const activeIsFocusable = activeElement ? Array.from(focusable).includes(activeElement) : false
    if (event.shiftKey && (activeElement === first || !activeIsFocusable)) {
      event.preventDefault()
      last.focus()
    } else if (!event.shiftKey && (activeElement === last || !activeIsFocusable)) {
      event.preventDefault()
      first.focus()
    }
  }

  // Focus leaves the dialog after an overlay click or when a row that had
  // focus is replaced, and from the body the dialog's own handler never sees
  // the key. Escape must still close and Tab must still land inside, so a
  // document listener covers keys whose target is outside the dialog; keys
  // inside it stay with handleDialogKeyDown so inner menus can stop them.
  useEffect(() => {
    const onDocumentKeyDown = (event: KeyboardEvent) => {
      const dialog = dialogRef.current
      if (!dialog || (event.target instanceof Node && dialog.contains(event.target))) return
      if (event.key === 'Escape') {
        event.preventDefault()
        onClose()
        return
      }
      if (event.key !== 'Tab') return
      const focusable = dialog.querySelectorAll<HTMLElement>(
        'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), summary, textarea:not([disabled]), [tabindex]:not([tabindex="-1"])',
      )
      if (!focusable.length) return
      event.preventDefault()
      ;(event.shiftKey ? focusable[focusable.length - 1] : focusable[0]).focus()
    }
    document.addEventListener('keydown', onDocumentKeyDown)
    return () => document.removeEventListener('keydown', onDocumentKeyDown)
  }, [onClose])

  const placeholder = mode === 'author'
    ? t('addToLibrary.searchPlaceholderAuthor')
    : mode === 'book'
      ? t('addToLibrary.searchPlaceholderBook')
      : t('addToLibrary.searchPlaceholder')

  const rows = useMemo(
    () => groupAddResults(showHidden ? [...authors, ...hiddenAuthors] : authors, books),
    [showHidden, authors, hiddenAuthors, books],
  )
  const hasRows = rows.length > 0

  const badgeClass = 'px-2 py-0.5 rounded-full bg-slate-300/70 dark:bg-zinc-700 text-[11px] font-medium text-slate-700 dark:text-zinc-300'
  const openClass = 'px-3 py-1 rounded text-xs font-medium border border-slate-400 dark:border-zinc-600 hover:bg-slate-200 dark:hover:bg-zinc-800'
  const selectClass = 'px-3 py-1 bg-emerald-600 hover:bg-emerald-500 disabled:opacity-50 disabled:cursor-not-allowed rounded text-xs font-medium text-white flex-shrink-0'

  // Keys carry the list index as well as the id: providers occasionally return
  // the same record twice, and a duplicate key makes React drop a row.
  const renderAuthorRow = (author: Author, index: number) => {
    const mismatch = mismatchedProvider(author)
    return (
      <div
        key={`author:${author.foreignAuthorId}:${index}`}
        data-testid="add-result-author"
        className="flex items-center justify-between gap-3 p-3 rounded-md bg-slate-200/50 dark:bg-zinc-800/50 hover:bg-slate-200 dark:hover:bg-zinc-800"
      >
        <div className="min-w-0">
          <div className="font-medium text-sm">{author.authorName}</div>
          <div className="text-xs text-slate-600 dark:text-zinc-500 flex flex-wrap gap-x-3">
            {author.disambiguation && <span>{t('addToLibrary.topWork')} {author.disambiguation}</span>}
            {author.statistics?.bookCount ? <span title={t('addToLibrary.booksTooltip')}>{t('addToLibrary.books', { count: author.statistics.bookCount })}</span> : null}
            {author.ratingsCount ? <span>{t('addToLibrary.ratings', { count: author.ratingsCount })}</span> : null}
            {mismatch && <span className="text-amber-600 dark:text-amber-400">{t('addToLibrary.resultProvider', { provider: providerDisplayName(mismatch) })}</span>}
          </div>
        </div>
        {author.libraryAuthorId ? (
          <div className="flex items-center gap-2 flex-shrink-0">
            <span className={badgeClass}>{t('addToLibrary.inLibrary')}</span>
            <a
              href={`${basePath()}/author/${author.libraryAuthorId}`}
              aria-label={t('addToLibrary.openAuthor', { name: author.authorName })}
              className={openClass}
            >
              {t('addToLibrary.open')}
            </a>
          </div>
        ) : (
          <button
            type="button"
            onClick={() => setSelected({ kind: 'author', author })}
            aria-label={t('addToLibrary.selectAuthor', { name: author.authorName })}
            className={selectClass}
          >
            {t('addToLibrary.select')}
          </button>
        )}
      </div>
    )
  }

  const renderBookRow = (book: Book, index: number) => {
    const canAdd = !!book.foreignBookId
    return (
      <div
        key={`book:${book.foreignBookId}:${index}`}
        data-testid="add-result-book"
        className="flex items-center gap-3 p-3 rounded-md bg-slate-200/50 dark:bg-zinc-800/50 hover:bg-slate-200 dark:hover:bg-zinc-800"
      >
        {book.imageUrl ? (
          <img src={book.imageUrl} alt="" className="w-14 h-20 object-cover rounded flex-shrink-0" />
        ) : (
          <div aria-hidden="true" className="w-14 h-20 rounded bg-slate-300 dark:bg-zinc-800 flex-shrink-0" />
        )}
        <div className="flex-1 min-w-0">
          <div className="font-medium text-sm truncate">{book.title}</div>
          {book.author?.authorName && (
            <div className="text-xs text-slate-600 dark:text-zinc-500">{book.author.authorName}</div>
          )}
          {book.releaseDate && (
            <div className="text-xs text-slate-500 dark:text-zinc-600">{new Date(book.releaseDate).getFullYear()}</div>
          )}
        </div>
        {book.libraryBookId ? (
          <div className="flex items-center gap-2 flex-shrink-0">
            <span className={badgeClass}>{t('addToLibrary.inLibrary')}</span>
            <a
              href={`${basePath()}/book/${book.libraryBookId}`}
              aria-label={t('addToLibrary.openBook', { title: book.title })}
              className={openClass}
            >
              {t('addToLibrary.open')}
            </a>
          </div>
        ) : (
          <button
            type="button"
            onClick={() => setSelected({ kind: 'book', book })}
            disabled={!canAdd}
            aria-label={canAdd ? t('addToLibrary.selectBook', { title: book.title }) : undefined}
            className={selectClass}
            title={!canAdd ? t('addToLibrary.idMissing') : undefined}
          >
            {t('addToLibrary.select')}
          </button>
        )}
      </div>
    )
  }

  return (
    <div className="fixed inset-0 bg-black/60 flex items-center justify-center p-4 z-50" onClick={onClose}>
      <div ref={dialogRef} role="dialog" aria-modal="true" aria-labelledby="add-to-library-title" className="bg-slate-100 dark:bg-zinc-900 border border-slate-300 dark:border-zinc-700 rounded-lg w-full max-w-lg shadow-2xl max-h-[90vh] flex flex-col" onClick={e => e.stopPropagation()} onKeyDown={handleDialogKeyDown}>
        <div className="p-4 border-b border-slate-200 dark:border-zinc-800">
          <h3 id="add-to-library-title" className="text-lg font-semibold">{t('addToLibrary.title')}</h3>
          <p className="text-xs text-fg-muted mt-0.5">{t('addToLibrary.description')}</p>
        </div>

        {selected?.kind === 'author' ? (
          <AddAuthorConfirm
            author={selected.author}
            defaults={authorDefaults}
            primaryProvider={primaryProvider}
            onBack={() => setSelected(null)}
            onClose={onClose}
            onAdded={author => { onAdded({ kind: 'author', author }); onClose() }}
          />
        ) : selected?.kind === 'book' ? (
          <AddBookConfirm
            book={selected.book}
            searchedISBN={searchedISBN}
            onBack={() => setSelected(null)}
            onClose={onClose}
            onAdded={book => { onAdded({ kind: 'book', book }); onClose() }}
          />
        ) : <>
          <div className="p-4 flex-1 overflow-y-auto">
            <div className="flex gap-2">
              <input
                type="text"
                value={query}
                onChange={e => setQuery(e.target.value)}
                onKeyDown={e => e.key === 'Enter' && void search()}
                placeholder={placeholder}
                className="flex-1 min-w-0 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded-md px-3 py-2 text-sm focus:outline-none focus:border-emerald-500"
                autoFocus
              />
              <button
                type="button"
                onClick={() => void search()}
                disabled={searching || !query.trim()}
                className="px-4 py-2 bg-emerald-600 hover:bg-emerald-500 disabled:opacity-50 disabled:cursor-not-allowed rounded-md text-sm font-medium text-white"
              >
                {searching ? t('addToLibrary.searching') : t('common.search')}
              </button>
            </div>

            <div className="mt-4 space-y-2 max-h-[50vh] overflow-y-auto">
              {rows.map((row, i) => {
                if (row.kind === 'author') return renderAuthorRow(row.author, i)
                if (row.kind === 'book') return renderBookRow(row.book, i)
                return (
                  <div key="divider" role="separator" className="flex items-center gap-2 pt-2 text-xs font-medium uppercase tracking-wide text-fg-muted">
                    <span>{t('addToLibrary.booksDivider')}</span>
                    <span className="flex-1 border-t border-slate-300 dark:border-zinc-700" />
                  </div>
                )
              })}
              {hiddenAuthors.length > 0 && !showHidden && (
                <button
                  type="button"
                  onClick={() => setShowHidden(true)}
                  className="w-full px-3 py-2 rounded-md border border-slate-300 dark:border-zinc-700 text-xs font-medium text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white hover:bg-slate-200/60 dark:hover:bg-zinc-800/60 transition-colors"
                >
                  {t('addToLibrary.showHiddenResults', { count: hiddenAuthors.length })}
                </button>
              )}
              {partialError && (
                <p className="text-xs text-amber-700 dark:text-amber-300 text-center py-2">{t('addToLibrary.partialError', { error: partialError })}</p>
              )}
              {searchError && (
                <p role="alert" className="text-sm text-red-700 dark:text-red-300 text-center py-4">{t('addToLibrary.searchError', { error: searchError })}</p>
              )}
              {searched && !hasRows && hiddenAuthors.length === 0 && !searching && !searchError && (
                <p className="text-sm text-fg-muted text-center py-4">{t('addToLibrary.noResults')}</p>
              )}
            </div>
          </div>

          <div className="p-4 border-t border-slate-200 dark:border-zinc-800 flex justify-end gap-2">
            <button type="button" onClick={onClose} className="px-4 py-2 text-sm text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white">{t('common.cancel')}</button>
          </div>
        </>}
      </div>
    </div>
  )
}
