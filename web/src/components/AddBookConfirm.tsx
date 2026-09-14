import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, Book, BookConflictBody } from '../api/client'
import { metadataSourceLink, providerDisplayName, providerFromBookForeignId } from '../util/metadataSource'
import MetadataLinksMenu from './MetadataLinksMenu'

// The confirm step for a book picked in AddToLibraryModal: the large cover,
// identifiers, format and search-on-add, and the add call. Renders the dialog
// body and footer as a fragment so it slots into the modal's flex column.
interface Props {
  book: Book
  // The ISBN the user searched for, when the query was one. Shown apart from
  // the identifiers the provider reports, which may not include it.
  searchedISBN: string | null
  onBack: () => void
  onClose: () => void
  onAdded: (book: Book) => void
}

function basePath(): string {
  return (window as unknown as { __BINDERY_BASE__?: string }).__BINDERY_BASE__ ?? ''
}

// The 409 from POST /author/book carries the library row (#1227); anything
// else is a plain failure.
function conflictBody(err: unknown): BookConflictBody | null {
  if (err && typeof err === 'object' && 'body' in err) {
    const body = (err as { body?: unknown }).body
    if (body && typeof body === 'object' && 'existingBookId' in body) return body as BookConflictBody
  }
  return null
}

export default function AddBookConfirm({ book, searchedISBN, onBack, onClose, onAdded }: Props) {
  const { t } = useTranslation()
  const [addError, setAddError] = useState<string | null>(null)
  const [addConflict, setAddConflict] = useState<BookConflictBody | null>(null)
  const [adding, setAdding] = useState(false)
  const [searchOnAdd, setSearchOnAdd] = useState(true)
  // '' = keep the provider's media type / the default.media_type setting.
  const [mediaType, setMediaType] = useState('')
  const headingRef = useRef<HTMLHeadingElement>(null)

  useEffect(() => {
    headingRef.current?.focus()
  }, [])

  const addBook = async () => {
    if (!book.foreignBookId) return
    setAdding(true)
    setAddError(null)
    setAddConflict(null)
    try {
      const created = await api.addBook({
        foreignBookId: book.foreignBookId,
        // foreignAuthorId may be empty (e.g. DNB results): the backend
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
      const conflict = conflictBody(err)
      setAddConflict(conflict)
      if (conflict) {
        // The server's message carries the useful part (which library it is
        // in, or that the format can be changed from the book page); the key
        // is only the fallback for a bodyless 409.
        setAddError(conflict.error || t('addToLibrary.book.alreadyInLibrary'))
      } else {
        setAddError(err instanceof Error ? err.message : t('addToLibrary.book.addFailed'))
      }
    } finally {
      setAdding(false)
    }
  }

  const provider = providerDisplayName(book.metadataProvider || providerFromBookForeignId(book.foreignBookId))
  const isbns = book.isbns ?? []
  const sourceLink = metadataSourceLink(book.foreignBookId, 'book')
  const visibleISBNs = isbns.slice(0, 3)
  const remainingISBNs = isbns.slice(3)

  const mediaTypeLabel = (value: string) => {
    if (value === 'audiobook') return t('common.audiobook')
    if (value === 'both') return t('common.both')
    if (value === 'ebook') return t('common.ebook')
    return value
  }

  return (
    <>
      <div className="p-4 flex-1 overflow-y-auto">
        <div className="flex items-start gap-4 rounded-md border border-slate-300 dark:border-zinc-700 bg-slate-200/50 dark:bg-zinc-800/50 p-4">
          {book.imageUrl ? (
            <img
              src={book.imageUrl}
              alt={t('addToLibrary.book.coverAlt', { title: book.title })}
              className="w-28 aspect-[2/3] object-cover rounded-md flex-shrink-0"
            />
          ) : (
            <div className="w-28 aspect-[2/3] rounded-md bg-slate-300 dark:bg-zinc-800 flex flex-col items-center justify-center gap-2 px-2 text-center text-slate-600 dark:text-zinc-400 flex-shrink-0">
              <svg className="w-8 h-8" fill="none" stroke="currentColor" viewBox="0 0 24 24" strokeWidth={1.5} aria-hidden="true">
                <path strokeLinecap="round" strokeLinejoin="round" d="M12 6.042A8.967 8.967 0 0 0 6 3.75c-1.052 0-2.062.18-3 .512v14.25A8.987 8.987 0 0 1 6 18c2.305 0 4.408.867 6 2.292m0-14.25a8.966 8.966 0 0 1 6-2.292c1.052 0 2.062.18 3 .512v14.25A8.987 8.987 0 0 0 18 18a8.967 8.967 0 0 0-6 2.292m0-14.25v14.25" />
              </svg>
              <span className="text-xs">{t('addToLibrary.book.noCover')}</span>
            </div>
          )}
          <div className="min-w-0 flex-1">
            <h4 ref={headingRef} tabIndex={-1} className="rounded-sm font-semibold leading-snug break-words focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-emerald-500">{book.title}</h4>
            {book.author?.authorName && <p className="mt-1 text-sm text-fg-muted">{book.author.authorName}</p>}
            <dl className="mt-3 grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-xs">
              {book.releaseDate && <>
                <dt className="text-fg-muted">{t('addToLibrary.book.published')}</dt>
                <dd>{new Date(book.releaseDate).getFullYear()}</dd>
              </>}
              {book.language && <>
                <dt className="text-fg-muted">{t('addToLibrary.book.language')}</dt>
                <dd>{book.language}</dd>
              </>}
              {book.mediaType && <>
                <dt className="text-fg-muted">{t('addToLibrary.book.resultFormat')}</dt>
                <dd>{mediaTypeLabel(book.mediaType)}</dd>
              </>}
              {provider && <>
                <dt className="text-fg-muted">{t('addToLibrary.book.source')}</dt>
                <dd className="flex items-center gap-2">
                  <span>{provider}</span>
                  {sourceLink && <MetadataLinksMenu links={[sourceLink]} />}
                </dd>
              </>}
              <dt className="text-fg-muted">{t('addToLibrary.book.providerId')}</dt>
              <dd className="font-mono break-all">{book.foreignBookId}</dd>
              {book.asin && <>
                <dt className="text-fg-muted">{t('addToLibrary.book.asin')}</dt>
                <dd className="font-mono break-all">{book.asin}</dd>
              </>}
            </dl>
          </div>
        </div>

        {searchedISBN && (
          <div className="mt-3 text-[11px] leading-5 text-slate-500 dark:text-zinc-500">
            <div className="font-medium">{t('addToLibrary.book.searchedIsbn')}</div>
            <div className="font-mono">{searchedISBN}</div>
          </div>
        )}

        {visibleISBNs.length > 0 && (
          <div
            className="mt-3 text-[11px] leading-5 text-slate-500 dark:text-zinc-500"
            title={t('addToLibrary.book.identifiersHint')}
          >
            <div className="font-medium">{t('addToLibrary.book.isbns')}</div>
            <div className="flex flex-wrap gap-x-2 font-mono">
              {visibleISBNs.map(isbn => <span key={isbn}>{isbn}</span>)}
            </div>
            {remainingISBNs.length > 0 && (
              <details>
                <summary className="cursor-pointer select-none text-accent-text hover:underline underline-offset-2">
                  {t('addToLibrary.book.showMoreIdentifiers', { count: remainingISBNs.length })}
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
            <span className="font-medium">{t('addToLibrary.book.format')}</span>
            <select
              aria-label={t('addToLibrary.book.formatLabel')}
              value={mediaType}
              onChange={e => setMediaType(e.target.value)}
              className="text-xs bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-2 py-1 focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600"
              title={t('addToLibrary.book.formatHint')}
            >
              <option value="">{t('addToLibrary.book.defaultFormat')}</option>
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
              <span className="font-medium">{t('addToLibrary.book.autoSearchLabel')}</span>
              <span className="block text-xs text-fg-muted mt-0.5">{t('addToLibrary.book.autoSearchHint')}</span>
            </span>
          </label>
        </div>

        {addError && (
          <div role="alert" className="mt-3 px-3 py-2 bg-red-100 dark:bg-red-950/30 border border-red-300 dark:border-red-900 rounded text-sm text-red-800 dark:text-red-300">
            <div>{addError}</div>
            {addConflict?.existingBookId && (
              <div className="mt-2 text-xs font-medium">
                <a href={`${basePath()}/book/${addConflict.existingBookId}`} className="underline">{t('addToLibrary.book.openExisting')}</a>
              </div>
            )}
          </div>
        )}
      </div>

      <div className="p-4 border-t border-slate-200 dark:border-zinc-800 flex justify-end gap-2">
        <button type="button" onClick={onBack} disabled={adding} className="mr-auto px-4 py-2 text-sm text-fg-muted hover:text-slate-900 dark:hover:text-white disabled:opacity-50">{t('addToLibrary.backToResults')}</button>
        <button type="button" onClick={onClose} className="px-4 py-2 text-sm text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white">{t('common.cancel')}</button>
        <button type="button" onClick={addBook} disabled={adding} className="px-4 py-2 bg-emerald-600 hover:bg-emerald-500 disabled:opacity-50 disabled:cursor-not-allowed rounded-md text-sm font-medium text-white">{adding ? t('addToLibrary.adding') : t('addToLibrary.book.confirmAdd')}</button>
      </div>
    </>
  )
}
