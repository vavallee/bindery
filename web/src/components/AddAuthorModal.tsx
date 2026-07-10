import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, AddAuthorRequest, Author, AuthorConflictBody, AuthorMonitorMode, MediaType, MetadataProfile, RootFolder } from '../api/client'
import { splitAuthorSearchResults } from './addAuthorTitleGuard'
import { canLinkAuthorMetadata, hasSparseMetadata } from '../util/authorMetadata'

interface Props {
  onClose: () => void
  onAdded: () => void
}

const AUTO_GRAB_STORAGE_KEY = 'addAuthor.autoGrab'
const DEFAULT_MONITOR_MODE: AuthorMonitorMode = 'all'
const DEFAULT_MONITOR_LATEST_COUNT = 1

function isAuthorMonitorMode(value: string): value is AuthorMonitorMode {
  return value === 'all' || value === 'future' || value === 'latest' || value === 'none'
}

function loadAutoGrabDefault(): boolean {
  try {
    const stored = localStorage.getItem(AUTO_GRAB_STORAGE_KEY)
    if (stored === null) return true
    return stored === 'true'
  } catch {
    return true
  }
}

function basePath(): string {
  return (window as unknown as { __BINDERY_BASE__?: string }).__BINDERY_BASE__ ?? ''
}

function conflictBody(err: unknown): AuthorConflictBody | null {
  if (err && typeof err === 'object' && 'body' in err) {
    const body = (err as { body?: unknown }).body
    if (body && typeof body === 'object') return body as AuthorConflictBody
  }
  return null
}


export default function AddAuthorModal({ onClose, onAdded }: Props) {
  const { t } = useTranslation()
  const [query, setQuery] = useState('')
  const [results, setResults] = useState<Author[]>([])
  const [hiddenResults, setHiddenResults] = useState<Author[]>([])
  const [showHiddenResults, setShowHiddenResults] = useState(false)
  const [selectedAuthor, setSelectedAuthor] = useState<Author | null>(null)
  const [searching, setSearching] = useState(false)
  const [searchError, setSearchError] = useState<string | null>(null)
  const [addError, setAddError] = useState<string | null>(null)
  const [addConflict, setAddConflict] = useState<AuthorConflictBody | null>(null)
  const [adding, setAdding] = useState<string | null>(null)
  const [profiles, setProfiles] = useState<MetadataProfile[]>([])
  const [profileId, setProfileId] = useState<number | null>(null)
  const [rootFolders, setRootFolders] = useState<RootFolder[]>([])
  const [rootFolderId, setRootFolderId] = useState<number | null>(null)
  const [searchOnAdd, setSearchOnAdd] = useState(loadAutoGrabDefault)
  const [mediaType, setMediaType] = useState<MediaType>('ebook')
  const [monitorMode, setMonitorMode] = useState<AuthorMonitorMode>(DEFAULT_MONITOR_MODE)
  const [monitorLatestCount, setMonitorLatestCount] = useState(DEFAULT_MONITOR_LATEST_COUNT)
  const [monitorOptionsChanged, setMonitorOptionsChanged] = useState(false)

  useEffect(() => {
    api.listMetadataProfiles().then(ps => {
      setProfiles(ps)
      // Default to the first profile — which is the seeded "Standard"
      // profile on a fresh install — so the language filter kicks in
      // without the user having to pick one.
      if (ps.length > 0) setProfileId(ps[0].id)
    }).catch(console.error)
    api.listRootFolders().then(rfs => {
      setRootFolders(rfs)
      if (rfs.length > 0) setRootFolderId(rfs[0].id)
    }).catch(console.error)
    // Seed the media-type dropdown with the global default setting so the
    // user only has to override it when they want something different.
    api.getSetting('default.media_type')
      .then(s => {
        if (s.value === 'ebook' || s.value === 'audiobook' || s.value === 'both') {
          setMediaType(s.value)
        }
      })
      .catch(() => { /* 404 = unset; keep ebook default */ })
    api.getSetting('author.default_monitor_mode')
      .then(s => {
        if (isAuthorMonitorMode(s.value)) setMonitorMode(s.value)
      })
      .catch(() => { /* unset; keep all-books default */ })
    api.getSetting('author.default_monitor_latest_count')
      .then(s => {
        const n = Number(s.value)
        if (Number.isInteger(n) && n > 0) setMonitorLatestCount(n)
      })
      .catch(() => { /* unset; keep latest count default */ })
  }, [])

  const search = async () => {
    const q = query.trim()
    if (!q) return
    setSearching(true)
    setSearchError(null)
    setAddError(null)
    setAddConflict(null)
    setShowHiddenResults(false)
    try {
      const [authors, books] = await Promise.all([
        api.searchAuthors(q),
        api.searchBooks(q).catch(() => []),
      ])
      const split = splitAuthorSearchResults(authors, books, q)
      setResults(split.visible)
      setHiddenResults(split.hidden)
    } catch (err) {
      setSearchError(err instanceof Error ? err.message : 'Search failed — try again')
      setResults([])
      setHiddenResults([])
    } finally {
      setSearching(false)
    }
  }

  const addAuthor = async () => {
    if (!selectedAuthor) return
    const author = selectedAuthor
    setAdding(author.foreignAuthorId)
    setAddError(null)
    setAddConflict(null)
    try {
      const request: AddAuthorRequest = {
        foreignAuthorId: author.foreignAuthorId,
        authorName: author.authorName,
        monitored: true,
        searchOnAdd,
        metadataProfileId: profileId,
        rootFolderId: rootFolderId,
        mediaType,
      }
      if (monitorOptionsChanged) {
        request.monitorMode = monitorMode
        request.monitorLatestCount = monitorLatestCount
      }
      await api.addAuthor(request)
      try {
        localStorage.setItem(AUTO_GRAB_STORAGE_KEY, String(searchOnAdd))
      } catch {
        // ignore storage failures (private mode, quota, etc.)
      }
      onAdded()
      onClose()
    } catch (err: unknown) {
      setAddError(err instanceof Error ? err.message : t('addAuthorModal.addFail'))
      setAddConflict(conflictBody(err))
    } finally {
      setAdding(null)
    }
  }

  const showConflictFindMetadata = addConflict?.canonicalAuthorId && (canLinkAuthorMetadata(addConflict.canonicalAuthor) || hasSparseMetadata(addConflict.canonicalAuthor))

  return (
    <div className="fixed inset-0 bg-black/60 flex items-center justify-center p-4 z-50" onClick={onClose}>
      <div role="dialog" aria-modal="true" aria-labelledby="add-author-title" className="bg-slate-100 dark:bg-zinc-900 border border-slate-300 dark:border-zinc-700 rounded-lg w-full max-w-lg shadow-2xl max-h-[90vh] flex flex-col" onClick={e => e.stopPropagation()}>
        <div className="p-4 border-b border-slate-200 dark:border-zinc-800">
          <h3 id="add-author-title" className="text-lg font-semibold">{t('addAuthorModal.title')}</h3>
        </div>

        <div className="p-4 flex-1 overflow-y-auto">
          {!selectedAuthor ? <>
            <div className="flex gap-2">
            <input
              type="text"
              value={query}
              onChange={e => setQuery(e.target.value)}
              onKeyDown={e => e.key === 'Enter' && search()}
              placeholder={t('addAuthorModal.searchPlaceholder')}
              className="flex-1 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded-md px-3 py-2 text-sm focus:outline-none focus:border-emerald-500"
              autoFocus
            />
            <button
              onClick={search}
              disabled={searching || !query.trim()}
              className="px-4 py-2 bg-emerald-600 hover:bg-emerald-500 disabled:opacity-50 disabled:cursor-not-allowed rounded-md text-sm font-medium"
            >
              {searching ? t('addAuthorModal.searching') : t('addAuthorModal.search')}
            </button>
          </div>

          <div className="mt-4 max-h-80 overflow-y-auto space-y-2">
            {(showHiddenResults ? [...results, ...hiddenResults] : results).map(author => (
              <div
                key={author.foreignAuthorId}
                className="flex items-center justify-between p-3 rounded-md bg-slate-200/50 dark:bg-zinc-800/50 hover:bg-slate-200 dark:hover:bg-zinc-800"
              >
                <div className="min-w-0">
                  <div className="font-medium text-sm">{author.authorName}</div>
                  <div className="text-xs text-slate-600 dark:text-zinc-500 flex flex-wrap gap-x-3">
                    {author.disambiguation && <span>{t('addAuthorModal.topWork')} {author.disambiguation}</span>}
                    {author.statistics?.bookCount ? <span title={t('addAuthorModal.booksTooltip')}>{t('addAuthorModal.books', { count: author.statistics.bookCount })}</span> : null}
                    {author.ratingsCount ? <span>{t('addAuthorModal.ratings', { count: author.ratingsCount })}</span> : null}
                  </div>
                </div>
                <button
                  onClick={() => {
                    setSelectedAuthor(author)
                    setAddError(null)
                    setAddConflict(null)
                  }}
                  className="px-3 py-1 bg-emerald-600 hover:bg-emerald-500 disabled:opacity-50 rounded text-xs font-medium"
                >
                  {t('addAuthorModal.select')}
                </button>
              </div>
            ))}
            {hiddenResults.length > 0 && !showHiddenResults && (
              <button
                type="button"
                onClick={() => setShowHiddenResults(true)}
                className="w-full px-3 py-2 rounded-md border border-slate-300 dark:border-zinc-700 text-xs font-medium text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white hover:bg-slate-200/60 dark:hover:bg-zinc-800/60 transition-colors"
              >
                {t('addAuthorModal.showHiddenResults', {
                  count: hiddenResults.length,
                  defaultValue: `Show ${hiddenResults.length} hidden result${hiddenResults.length === 1 ? '' : 's'}`,
                })}
              </button>
            )}
            {searchError && (
              <p className="text-sm text-red-400 text-center py-4">{t('addAuthorModal.searchError', { error: searchError })}</p>
            )}
            {results.length === 0 && hiddenResults.length === 0 && !searching && !searchError && query && (
              <p className="text-sm text-slate-600 dark:text-zinc-500 text-center py-4">{t('addAuthorModal.noResults')}</p>
            )}
          </div>
          </> : <>
            <div className="rounded-md border border-slate-300 dark:border-zinc-700 bg-slate-200/50 dark:bg-zinc-800/50 p-3">
              <div className="font-medium">{selectedAuthor.authorName}</div>
              {selectedAuthor.disambiguation && <div className="mt-0.5 text-sm text-fg-muted">{selectedAuthor.disambiguation}</div>}
            </div>

            <details className="mt-4 rounded-md border border-slate-300 dark:border-zinc-700">
              <summary className="cursor-pointer select-none px-3 py-2 text-sm font-medium hover:bg-slate-200/60 dark:hover:bg-zinc-800/60">
                {t('addAuthorModal.customizeMonitoring')}
              </summary>
              <div className="space-y-3 border-t border-slate-300 dark:border-zinc-700 p-3">
                {profiles.length > 1 && (
                  <div>
                    <label htmlFor="add-author-profile" className="block text-xs text-fg-muted mb-1">{t('addAuthorModal.metadataProfile')}</label>
                    <select id="add-author-profile" value={profileId ?? ''} onChange={e => setProfileId(e.target.value ? Number(e.target.value) : null)} className="w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded-md px-3 py-2 text-sm focus:outline-none focus:border-emerald-500">
                      {profiles.map(p => <option key={p.id} value={p.id}>{p.name}</option>)}
                    </select>
                  </div>
                )}
                {rootFolders.length > 0 && (
                  <div>
                    <label htmlFor="add-author-root" className="block text-xs text-fg-muted mb-1">{t('addAuthorModal.rootFolder')}</label>
                    <select id="add-author-root" value={rootFolderId ?? ''} onChange={e => setRootFolderId(e.target.value ? Number(e.target.value) : null)} className="w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded-md px-3 py-2 text-sm focus:outline-none focus:border-emerald-500">
                      {rootFolders.map(rf => <option key={rf.id} value={rf.id}>{rf.path}</option>)}
                    </select>
                  </div>
                )}
                <div>
                  <label htmlFor="add-author-media" className="block text-xs text-fg-muted mb-1">{t('addAuthorModal.mediaType')}</label>
                  <select id="add-author-media" value={mediaType} onChange={e => setMediaType(e.target.value as MediaType)} className="w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded-md px-3 py-2 text-sm focus:outline-none focus:border-emerald-500">
                    <option value="ebook">{t('mediaType.ebook', 'Ebook')}</option>
                    <option value="audiobook">{t('mediaType.audiobook', 'Audiobook')}</option>
                    <option value="both">{t('mediaType.both', 'Both')}</option>
                  </select>
                </div>
                <div>
                  <label htmlFor="add-author-monitor-mode" className="block text-xs text-fg-muted mb-1">{t('addAuthorModal.monitorMode')}</label>
                  <select id="add-author-monitor-mode" value={monitorMode} onChange={e => { setMonitorMode(e.target.value as AuthorMonitorMode); setMonitorOptionsChanged(true) }} className="w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded-md px-3 py-2 text-sm focus:outline-none focus:border-emerald-500">
                    <option value="all">{t('monitorMode.all', 'All books')}</option>
                    <option value="future">{t('monitorMode.future', 'Future books only')}</option>
                    <option value="latest">{t('monitorMode.latest', 'Latest only')}</option>
                    <option value="none">{t('monitorMode.none', 'None')}</option>
                  </select>
                </div>
                {monitorMode === 'latest' && (
                  <div>
                    <label htmlFor="add-author-latest-count" className="block text-xs text-fg-muted mb-1">{t('addAuthorModal.monitorLatestCount')}</label>
                    <input id="add-author-latest-count" type="number" min={1} value={monitorLatestCount} onChange={e => { setMonitorLatestCount(Math.max(1, Number(e.target.value) || 1)); setMonitorOptionsChanged(true) }} className="w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded-md px-3 py-2 text-sm focus:outline-none focus:border-emerald-500" />
                  </div>
                )}
                <label className="flex items-start gap-2 text-sm cursor-pointer select-none">
                  <input type="checkbox" checked={searchOnAdd} onChange={e => setSearchOnAdd(e.target.checked)} className="accent-emerald-500 mt-0.5 flex-shrink-0" />
                  <span>
                    <span className="font-medium">{t('addAuthorModal.autoGrabLabel')}</span>
                    <span className="block text-xs text-fg-muted mt-0.5">{t('addAuthorModal.autoGrabHint')}</span>
                  </span>
                </label>
              </div>
            </details>

            {addError && (
              <div role="alert" className="mt-3 px-3 py-2 bg-red-100 dark:bg-red-950/30 border border-red-300 dark:border-red-900 rounded text-sm text-red-800 dark:text-red-300">
                <div>{addError}</div>
                {addConflict?.canonicalAuthorId && (
                  <div className="mt-2 flex flex-wrap gap-3 text-xs font-medium">
                    <a href={`${basePath()}/author/${addConflict.canonicalAuthorId}`} className="underline">{t('addAuthorModal.openExisting')}</a>
                    {showConflictFindMetadata && <a href={`${basePath()}/author/${addConflict.canonicalAuthorId}?linkMetadata=1`} className="underline">{t('addAuthorModal.findMetadata')}</a>}
                  </div>
                )}
              </div>
            )}
          </>}
        </div>

        <div className="p-4 border-t border-slate-200 dark:border-zinc-800 flex justify-end gap-2">
          {selectedAuthor && <button type="button" onClick={() => setSelectedAuthor(null)} className="mr-auto px-4 py-2 text-sm text-fg-muted hover:text-slate-900 dark:hover:text-white">{t('addAuthorModal.backToResults')}</button>}
          <button onClick={onClose} className="px-4 py-2 text-sm text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white">{t('common.cancel')}</button>
          {selectedAuthor && <button type="button" onClick={addAuthor} disabled={adding !== null} className="px-4 py-2 bg-emerald-600 hover:bg-emerald-500 disabled:opacity-50 disabled:cursor-not-allowed rounded-md text-sm font-medium">{adding ? t('addAuthorModal.adding') : t('addAuthorModal.confirmAdd')}</button>}
        </div>
      </div>
    </div>
  )
}
