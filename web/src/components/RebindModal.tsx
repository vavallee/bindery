import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, ApiError, Book } from '../api/client'
import { resolveBookQuery } from '../api/booklookup'
import { metadataSourceLink, providerDisplayName, providerFromBookForeignId } from '../util/metadataSource'
import { btn, btnSize } from './buttons'
import MetadataLinksMenu from './MetadataLinksMenu'

interface AuthorMismatch {
  currentAuthor: string
  upstreamAuthor: string
}

interface Props {
  book: Book
  onClose: () => void
  onSuccess: (updated: Book) => void
}

const inputClass = 'w-full rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-800 px-3 py-2 text-sm focus-visible:outline-2 focus-visible:outline-emerald-600 disabled:opacity-50'

function basePath(): string {
  return (window as unknown as { __BINDERY_BASE__?: string }).__BINDERY_BASE__ ?? ''
}

function isValidRebindTarget(provider: string, id: string): boolean {
  if (provider === 'openlibrary') return /^OL\d+W$/.test(id)
  if (provider === 'hardcover') return /^hc:[a-zA-Z0-9][a-zA-Z0-9-]*$/.test(id)
  return false
}

export default function RebindModal({ book, onClose, onSuccess }: Props) {
  const { t } = useTranslation()
  const dialog = useRef<HTMLDialogElement>(null)
  const queryInput = useRef<HTMLInputElement>(null)
  const searchRequest = useRef({ id: 0 })
  const [mode, setMode] = useState<'search' | 'manual'>('search')
  const [query, setQuery] = useState(book.title)
  const [results, setResults] = useState<Book[]>([])
  const [searched, setSearched] = useState(false)
  const [searching, setSearching] = useState(false)
  const [selected, setSelected] = useState<Book | null>(null)
  const [provider, setProvider] = useState<'openlibrary' | 'hardcover'>('openlibrary')
  const [foreignId, setForeignId] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [mismatch, setMismatch] = useState<AuthorMismatch | null>(null)

  useEffect(() => {
    const element = dialog.current!
    const previouslyFocused = document.activeElement instanceof HTMLElement ? document.activeElement : null
    const requests = searchRequest.current
    element.showModal()
    queryInput.current?.focus()
    return () => {
      requests.id++
      element.close()
      previouslyFocused?.focus()
    }
  }, [])

  const targetId = mode === 'search' ? selected?.foreignBookId.trim() || '' : foreignId.trim()
  const targetProvider = mode === 'search' ? providerFromBookForeignId(targetId) : provider
  const targetIsValid = isValidRebindTarget(targetProvider, targetId)
    && !(mode === 'search' && selected?.libraryBookId && selected.libraryBookId !== book.id)

  const resetSearch = () => {
    searchRequest.current.id++
    setSearching(false)
    setSearched(false)
    setResults([])
    setSelected(null)
    setError(null)
  }

  const search = async (event: React.FormEvent) => {
    event.preventDefault()
    if (!query.trim() || searching) return
    const request = ++searchRequest.current.id
    setSearching(true)
    setSearched(false)
    setResults([])
    setSelected(null)
    setError(null)
    try {
      const found = await resolveBookQuery(query)
      if (request !== searchRequest.current.id) return
      // Re-bind supports these providers. Keep the search/lookup's original
      // identifiers; never turn another provider's result into an OL record.
      setResults(found.filter(candidate => {
        if (!candidate.foreignBookId?.trim()) return false
        const id = candidate.foreignBookId.trim()
        // Editions remain viewable upstream, but cannot be selected for rebind.
        return isValidRebindTarget(providerFromBookForeignId(id), id) || /^OL\d+M$/.test(id)
      }))
      setSearched(true)
    } catch (err) {
      if (request === searchRequest.current.id) {
        setError(err instanceof Error ? err.message : t('bookRebind.searchFailed'))
      }
    } finally {
      if (request === searchRequest.current.id) setSearching(false)
    }
  }

  const submit = async (force: boolean) => {
    if (!targetIsValid || submitting || (targetProvider !== 'openlibrary' && targetProvider !== 'hardcover')) return
    setSubmitting(true)
    setError(null)
    setMismatch(null)
    try {
      const updated = await api.rebindBook(book.id, targetProvider, targetId, force)
      onSuccess(updated)
    } catch (err: unknown) {
      if (err instanceof ApiError && err.status === 409) {
        const body = err.body as { force_required?: boolean; current_author?: string; upstream_author?: string; error?: string } | null
        if (body?.force_required) {
          setMismatch({ currentAuthor: body.current_author ?? '', upstreamAuthor: body.upstream_author ?? '' })
        } else {
          setError(t('bookRebind.conflict'))
        }
      } else {
        setError(err instanceof Error ? err.message : t('bookRebind.failed'))
      }
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <dialog
      ref={dialog}
      aria-labelledby="book-rebind-title"
      aria-describedby="book-rebind-description"
      className="m-auto max-h-[90dvh] w-[calc(100%-2rem)] max-w-2xl overflow-y-auto rounded-lg border border-slate-300 bg-white p-0 text-slate-900 shadow-xl backdrop:bg-black/60 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-100"
      onCancel={event => { event.preventDefault(); if (!submitting) onClose() }}
      onClick={event => { if (event.target === event.currentTarget && !submitting) onClose() }}
    >
      <div className="p-5 sm:p-6">
        <h2 id="book-rebind-title" className="text-lg font-semibold">{t('bookRebind.title')}</h2>
        <p id="book-rebind-description" className="mt-1 text-sm text-slate-600 dark:text-zinc-400">
          {t('bookRebind.description', { title: book.title })}
        </p>

        {mismatch ? (
          <div className="mt-5 space-y-3" role="alert">
            <h3 className="font-semibold text-amber-800 dark:text-amber-300">{t('bookRebind.authorMismatch')}</h3>
            <p className="text-sm">{t('bookRebind.authorMismatchHint', { current: mismatch.currentAuthor, upstream: mismatch.upstreamAuthor })}</p>
          </div>
        ) : (
          <>
            <div className="my-5 flex flex-wrap gap-2" role="group" aria-label={t('bookRebind.method')}>
              {(['search', 'manual'] as const).map(value => (
                <button
                  key={value}
                  type="button"
                  aria-pressed={mode === value}
                  disabled={submitting}
                  className={`${mode === value ? btn.secondary : btn.ghost} ${btnSize.md} min-h-10`}
                  onClick={() => { resetSearch(); setMode(value) }}
                >{t(`bookRebind.${value}`)}</button>
              ))}
            </div>

            {mode === 'search' ? (
              <div>
                <form onSubmit={search}>
                  <label htmlFor="rebind-query" className="mb-1 block text-sm font-medium">{t('bookRebind.query')}</label>
                  <div className="flex flex-col gap-2 sm:flex-row">
                    <input
                      ref={queryInput}
                      id="rebind-query"
                      value={query}
                      onChange={event => { resetSearch(); setQuery(event.target.value) }}
                      disabled={submitting}
                      className={`${inputClass} min-w-0 flex-1`}
                    />
                    <button type="submit" className={`${btn.secondary} ${btnSize.md} min-h-10`} disabled={!query.trim() || searching || submitting}>
                      {t(searching ? 'bookRebind.searching' : 'bookRebind.searchButton')}
                    </button>
                  </div>
                  <p className="mt-2 text-xs text-slate-600 dark:text-zinc-400">{t('bookRebind.searchHint')}</p>
                </form>
                <div role="status" className="mt-4 text-sm text-slate-600 dark:text-zinc-400">
                  {searching ? t('bookRebind.searching') : searched ? t(results.length ? 'bookRebind.resultCount' : 'bookRebind.noResults', { count: results.length }) : null}
                </div>
                {results.length > 0 && (
                  <fieldset className="mt-2 divide-y divide-slate-200 dark:divide-zinc-700" disabled={submitting}>
                    <legend className="sr-only">{t('bookRebind.choose')}</legend>
                    {results.map(candidate => {
                      const sourceLink = metadataSourceLink(candidate.foreignBookId, 'book')
                      const isEdition = /^OL\d+M$/i.test(candidate.foreignBookId.trim())
                      const isOtherLibraryBook = !!candidate.libraryBookId && candidate.libraryBookId !== book.id
                      return (
                        <div key={candidate.foreignBookId} className="rounded p-3 hover:bg-slate-100 dark:hover:bg-zinc-800">
                          <label className="flex cursor-pointer items-start gap-3 rounded focus-within:outline-2 focus-within:outline-emerald-600">
                            <input type="radio" name="rebind-result" className="mt-1 accent-emerald-600" disabled={isEdition || isOtherLibraryBook} checked={selected === candidate} onChange={() => { setSelected(candidate); setError(null) }} />
                            {candidate.imageUrl && <img src={candidate.imageUrl} alt="" loading="lazy" className="h-16 w-11 shrink-0 rounded object-cover" />}
                            <span className="min-w-0 flex-1 text-sm">
                              <span className="block break-words font-medium">{candidate.title}</span>
                              <span className="block text-slate-600 dark:text-zinc-400">{candidate.author?.authorName || t('bookRebind.unknownAuthor')}</span>
                              <span className="block break-words text-xs text-slate-600 dark:text-zinc-400">
                                {providerDisplayName(providerFromBookForeignId(candidate.foreignBookId))} · {candidate.foreignBookId}
                                {candidate.releaseDate ? ` · ${candidate.releaseDate.slice(0, 4)}` : ''}
                              </span>
                              {isEdition && <span className="mt-1 block text-xs text-slate-600 dark:text-zinc-400">{t('bookRebind.editionHint')}</span>}
                            </span>
                          </label>
                          {!!candidate.libraryBookId && (
                            <div className="ml-7 mt-2 flex items-center gap-2 text-xs">
                              <span className="rounded-full bg-slate-200 px-2 py-0.5 dark:bg-zinc-700">{t('addToLibrary.inLibrary')}</span>
                              <a href={`${basePath()}/book/${candidate.libraryBookId}`} aria-label={t('addToLibrary.openBook', { title: candidate.title })} className="rounded underline focus-visible:outline-2 focus-visible:outline-emerald-600">
                                {t('addToLibrary.open')}
                              </a>
                            </div>
                          )}
                          {sourceLink && <div className="ml-7 mt-2 text-xs"><MetadataLinksMenu links={[sourceLink]} /></div>}
                        </div>
                      )
                    })}
                  </fieldset>
                )}
              </div>
            ) : (
              <form id="rebind-manual" className="space-y-4" onSubmit={event => { event.preventDefault(); void submit(false) }}>
                <div>
                  <label htmlFor="rebind-provider" className="mb-1 block text-sm font-medium">{t('bookRebind.provider')}</label>
                  <select id="rebind-provider" value={provider} onChange={event => { setProvider(event.target.value as typeof provider); setError(null) }} disabled={submitting} className={inputClass}>
                    <option value="openlibrary">OpenLibrary</option>
                    <option value="hardcover">Hardcover</option>
                  </select>
                </div>
                <div>
                  <label htmlFor="rebind-id" className="mb-1 block text-sm font-medium">{t('bookRebind.identifier')}</label>
                  <input id="rebind-id" value={foreignId} onChange={event => { setForeignId(event.target.value); setError(null) }} disabled={submitting} placeholder={provider === 'openlibrary' ? 'OL12345W' : 'hc:12345'} className={inputClass} />
                  <p className="mt-2 text-xs text-slate-600 dark:text-zinc-400">{t(`bookRebind.${provider}Hint`)}</p>
                </div>
              </form>
            )}
          </>
        )}
        {error && <p role="alert" className="mt-4 text-sm text-red-700 dark:text-red-400">{error}</p>}
      </div>
      <div className="sticky bottom-0 flex justify-end gap-2 border-t border-slate-200 bg-white px-5 py-4 dark:border-zinc-700 dark:bg-zinc-900 sm:px-6">
        <button type="button" onClick={() => mismatch ? setMismatch(null) : onClose()} disabled={submitting} className={`${btn.secondary} ${btnSize.md} min-h-10`}>
          {t(mismatch ? 'bookRebind.back' : 'common.cancel')}
        </button>
        <button type={mode === 'manual' && !mismatch ? 'submit' : 'button'} form={mode === 'manual' && !mismatch ? 'rebind-manual' : undefined} onClick={mode === 'manual' && !mismatch ? undefined : () => submit(!!mismatch)} disabled={submitting || !targetIsValid} className={`${btn.primary} ${btnSize.md} min-h-10`}>
          {t(submitting ? 'bookRebind.submitting' : mismatch ? 'bookRebind.force' : 'bookDetail.rebind')}
        </button>
      </div>
    </dialog>
  )
}
