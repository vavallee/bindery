import { FormEvent, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, ApiError, Author, AuthorConflictBody, RelinkAuthorLinkCandidate } from '../api/client'
import { metadataSourceLink } from '../util/metadataSource'
import { useModal } from './useModal'

interface Props {
  author: Author
  onClose: () => void
  onLinked: (author: Author) => void
}

function providerLabel(author: Author): string {
  const provider = (author.metadataProvider || '').toLowerCase()
  if (provider === 'hardcover' || author.foreignAuthorId.startsWith('hc:')) return 'Hardcover'
  if (provider === 'openlibrary' || author.foreignAuthorId.startsWith('OL')) return 'OpenLibrary'
  if (provider === 'dnb' || author.foreignAuthorId.startsWith('dnb:')) return 'DNB'
  if (provider === 'nb' || author.foreignAuthorId.startsWith('nb:')) return 'Nasjonalbiblioteket'
  if (provider) return provider
  return 'Metadata'
}

function basePath(): string {
  return (window as unknown as { __BINDERY_BASE__?: string }).__BINDERY_BASE__ ?? ''
}

function conflictBody(err: unknown): AuthorConflictBody | null {
  if (err instanceof ApiError && err.body && typeof err.body === 'object') {
    return err.body as AuthorConflictBody
  }
  if (err && typeof err === 'object' && 'body' in err) {
    const body = (err as { body?: unknown }).body
    if (body && typeof body === 'object') return body as AuthorConflictBody
  }
  return null
}

export default function AuthorMetadataLinkModal({ author, onClose, onLinked }: Props) {
  const { t } = useTranslation()
  const [query, setQuery] = useState(author.authorName)
  const [results, setResults] = useState<RelinkAuthorLinkCandidate[]>([])
  const [searching, setSearching] = useState(false)
  const [linking, setLinking] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [conflict, setConflict] = useState<AuthorConflictBody | null>(null)
  const searchRequestId = useRef(0)

  const visibleResults = useMemo(
    () => results.filter(candidate => candidate.foreignAuthorId !== author.foreignAuthorId),
    [author.foreignAuthorId, results],
  )

  const runSearch = useCallback(async (term: string, resetConflict = false) => {
    const query = term.trim()
    if (!query) return
    const requestId = searchRequestId.current + 1
    searchRequestId.current = requestId
    setSearching(true)
    setError(null)
    if (resetConflict) setConflict(null)
    try {
      const candidates = await api.searchAuthorLinkCandidates(author.id, query)
      if (searchRequestId.current !== requestId) return
      setResults(candidates)
    } catch (err) {
      if (searchRequestId.current !== requestId) return
      setResults([])
      setError(err instanceof Error ? err.message : t('authorMetadataLink.searchFailed', 'Search failed'))
    } finally {
      if (searchRequestId.current === requestId) setSearching(false)
    }
  }, [author.id, t])

  useEffect(() => {
    void runSearch(author.authorName)
    return () => { searchRequestId.current += 1 }
  }, [author.authorName, runSearch])

  const search = async (event?: FormEvent) => {
    event?.preventDefault()
    await runSearch(query, true)
  }

  const link = async (candidate: RelinkAuthorLinkCandidate) => {
    setLinking(candidate.foreignAuthorId)
    setError(null)
    setConflict(null)
    try {
      const updated = await api.relinkAuthorUpstream(author.id, {
        foreignAuthorId: candidate.foreignAuthorId,
        authorName: candidate.authorName,
      })
      onLinked(updated)
      onClose()
    } catch (err) {
      const body = conflictBody(err)
      setConflict(body)
      setError(err instanceof Error ? err.message : t('authorMetadataLink.linkFailed', 'Link failed'))
    } finally {
      setLinking(null)
    }
  }

  const { titleId, panelProps } = useModal({ onClose, canClose: linking === null })

  return (
    <div className="modal-overlay fixed inset-0 bg-black/60 flex items-center justify-center p-4 z-50" onClick={onClose}>
      <div {...panelProps} className="bg-slate-100 dark:bg-zinc-900 border border-slate-300 dark:border-zinc-700 rounded-lg w-full max-w-xl shadow-2xl modal-max-h flex flex-col" onClick={e => e.stopPropagation()}>
        <div className="p-4 border-b border-slate-200 dark:border-zinc-800">
          <h3 id={titleId} className="text-lg font-semibold">{t('authorMetadataLink.title', 'Link metadata')}</h3>
        </div>

        <div className="p-4 flex-1 overflow-y-auto">
          <form onSubmit={search} className="flex gap-2">
            <input
              enterKeyHint="search"
              value={query}
              onChange={event => setQuery(event.target.value)}
              className="flex-1 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded-md px-3 py-2 text-sm focus:outline-none focus:border-emerald-500"
              placeholder={t('authorMetadataLink.searchPlaceholder', 'Search by author name…')}
              autoFocus
            />
            <button
              type="submit"
              disabled={searching || !query.trim()}
              className="px-4 py-2 bg-emerald-600 hover:bg-emerald-500 disabled:opacity-50 rounded-md text-sm font-medium"
            >
              {searching ? t('authorMetadataLink.searching', 'Searching…') : t('authorMetadataLink.search', 'Search')}
            </button>
          </form>

          {error && (
            <div className="mt-3 px-3 py-2 bg-red-100 dark:bg-red-950/30 border border-red-300 dark:border-red-900 rounded text-sm text-red-800 dark:text-red-300">
              <div>{error}</div>
              {conflict?.canonicalAuthorId && (
                <a
                  href={`${basePath()}/author/${conflict.canonicalAuthorId}`}
                  className="inline-block mt-2 text-xs font-medium underline"
                >
                  {t('authorMetadataLink.openExisting', 'Open existing author')}
                </a>
              )}
            </div>
          )}

          <div className="mt-4 space-y-2">
            {visibleResults.map(candidate => (
              <div
                key={candidate.foreignAuthorId}
                className="flex items-center gap-3 justify-between p-3 rounded-md bg-slate-200/50 dark:bg-zinc-800/50"
              >
                <div className="flex items-center gap-3 min-w-0">
                  {candidate.imageUrl ? (
                    <img loading="lazy" decoding="async" src={candidate.imageUrl} alt={candidate.authorName} className="w-12 h-12 rounded-full object-cover flex-shrink-0" />
                  ) : (
                    <div className="w-12 h-12 rounded-full bg-slate-300 dark:bg-zinc-700 flex items-center justify-center text-sm font-bold text-slate-600 dark:text-zinc-300 flex-shrink-0">
                      {candidate.authorName.charAt(0).toUpperCase()}
                    </div>
                  )}
                  <div className="min-w-0">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="font-medium text-sm truncate">{candidate.authorName}</span>
                      <span className="px-1.5 py-0.5 rounded bg-slate-300 dark:bg-zinc-700 text-[10px] uppercase text-slate-700 dark:text-zinc-300">
                        {providerLabel(candidate)}
                      </span>
                      {/*
                        A record this author was linked to before (#2688). It stays
                        selectable, because moving back to it is the whole point; the
                        marker is only there so you can tell it apart from a record you
                        have never used.
                      */}
                      {candidate.previouslyLinked && (
                        <span className="px-1.5 py-0.5 rounded border border-slate-400 dark:border-zinc-600 text-[10px] text-slate-600 dark:text-zinc-400">
                          {t('authorMetadataLink.previouslyLinked', 'previously linked')}
                        </span>
                      )}
                    </div>
                    <div className="text-xs text-slate-600 dark:text-zinc-500 flex flex-wrap gap-x-3">
                      {candidate.disambiguation && <span>{candidate.disambiguation}</span>}
                      {candidate.statistics?.bookCount ? <span>{t('authorMetadataLink.books', { count: candidate.statistics.bookCount, defaultValue: '{{count}} books' })}</span> : null}
                      {candidate.ratingsCount ? <span>{t('authorMetadataLink.ratings', { count: candidate.ratingsCount, defaultValue: '{{count}} ratings' })}</span> : null}
                      {/*
                        Linking is irreversible enough to be worth checking first, and
                        several candidates can share a name (#1754). Reuses the shared
                        helper, which returns null for providers whose stored IDs have
                        no stable public page, so this never renders a dead link.
                      */}
                      {(() => {
                        const source = metadataSourceLink(candidate.foreignAuthorId, 'author')
                        if (!source) return null // provider has no stable public page
                        return (
                          <a
                            href={source.url}
                            target="_blank"
                            rel="noopener noreferrer"
                            onClick={event => event.stopPropagation()}
                            className="underline hover:text-slate-900 dark:hover:text-zinc-300"
                          >
                            {t('authorMetadataLink.verify', { source: source.label, defaultValue: 'View on {{source}}' })}
                          </a>
                        )
                      })()}
                    </div>
                  </div>
                </div>
                <button
                  type="button"
                  onClick={() => link(candidate)}
                  disabled={linking !== null}
                  className="px-3 py-1.5 bg-emerald-600 hover:bg-emerald-500 disabled:opacity-50 rounded text-xs font-medium flex-shrink-0"
                >
                  {linking === candidate.foreignAuthorId ? t('authorMetadataLink.linking', 'Linking…') : t('authorMetadataLink.link', 'Link')}
                </button>
              </div>
            ))}
            {!searching && visibleResults.length === 0 && !error && (
              <p className="text-sm text-slate-600 dark:text-zinc-500 text-center py-6">
                {t('authorMetadataLink.noResults', 'No alternate metadata candidates found')}
              </p>
            )}
          </div>
        </div>

        <div className="p-4 border-t border-slate-200 dark:border-zinc-800 flex justify-end">
          <button onClick={onClose} className="px-4 py-2 text-sm text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white">{t('common.cancel')}</button>
        </div>
      </div>
    </div>
  )
}
