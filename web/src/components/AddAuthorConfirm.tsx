import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, AddAuthorRequest, Author, AuthorConflictBody, AuthorMonitorMode, MonitorNewItems, MediaType } from '../api/client'
import { AuthorAddDefaults, DEFAULT_MONITOR_LATEST_COUNT, DEFAULT_MONITOR_MODE } from './authorAddDefaults'
import { useNeedsSetup } from './useNeedsSetup'
import { authorProviderKey } from '../util/authorMetadata'
import { providerDisplayName } from '../util/metadataSource'

// The confirm step for an author picked in AddToLibraryModal: monitoring
// controls, the outcome sentence and the add call. Renders the dialog body and
// footer as a fragment so it slots into the modal's flex column.
interface Props {
  author: Author
  // Profiles, root folders and the install defaults, loaded once by the modal
  // when it opens (loadAuthorAddDefaults) so selecting a row does not start
  // five requests, and null while they are still in flight. The Add button
  // stays disabled until they arrive: an add posted before them would carry
  // a null profile and a null root folder.
  defaults: AuthorAddDefaults | null
  // The configured primary metadata provider, when one is explicitly set, so
  // a record that would sync from elsewhere is flagged before the add (#2237).
  primaryProvider: string | null
  onBack: () => void
  onClose: () => void
  onAdded: (author: Author) => void
}

const AUTO_GRAB_STORAGE_KEY = 'addAuthor.autoGrab'

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

export default function AddAuthorConfirm({ author, defaults, primaryProvider, onBack, onClose, onAdded }: Props) {
  const { t } = useTranslation()
  const [addError, setAddError] = useState<string | null>(null)
  const [addConflict, setAddConflict] = useState<AuthorConflictBody | null>(null)
  const [adding, setAdding] = useState(false)
  const profiles = defaults?.profiles ?? []
  const rootFolders = defaults?.rootFolders ?? []
  const [profileId, setProfileId] = useState<number | null>(null)
  const [rootFolderId, setRootFolderId] = useState<number | null>(null)
  const [searchOnAdd, setSearchOnAdd] = useState(loadAutoGrabDefault)
  // Preempt the silent auto-search failure: backend auto-search-on-add runs
  // async, so a missing indexer/client fails with no visible error anywhere.
  // Warn at the moment the user opts into it instead.
  const { needsAny, needsClient } = useNeedsSetup()
  const [mediaType, setMediaType] = useState<MediaType>('ebook')
  const [monitorMode, setMonitorMode] = useState<AuthorMonitorMode>(DEFAULT_MONITOR_MODE)
  const [monitorLatestCount, setMonitorLatestCount] = useState(DEFAULT_MONITOR_LATEST_COUNT)
  const [monitorOptionsChanged, setMonitorOptionsChanged] = useState(false)
  // Sent only when touched, so the install-wide default (#2217) still applies
  // to an untouched dialog.
  const [monitorNewItems, setMonitorNewItems] = useState<MonitorNewItems>('all')
  const [monitorNewItemsChanged, setMonitorNewItemsChanged] = useState(false)
  const headingRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    headingRef.current?.focus()
  }, [])

  // Seed the controls once the defaults land. Untouched controls keep the
  // install defaults; the monitor fields are only posted when changed.
  useEffect(() => {
    if (!defaults) return
    // Default to the first profile, which is the seeded "Standard" profile
    // on a fresh install, so the language filter kicks in without the user
    // having to pick one.
    setProfileId(defaults.profiles.length > 0 ? defaults.profiles[0].id : null)
    setRootFolderId(defaults.rootFolderId)
    setMediaType(defaults.mediaType)
    setMonitorMode(defaults.monitorMode)
    setMonitorLatestCount(defaults.monitorLatestCount)
  }, [defaults])

  const mismatchedProvider = (() => {
    if (!primaryProvider) return null
    const provider = authorProviderKey(author)
    if (!provider || provider === primaryProvider) return null
    return provider
  })()

  const addAuthor = async () => {
    if (!defaults) return
    setAdding(true)
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
      if (monitorNewItemsChanged) {
        request.monitorNewItems = monitorNewItems
      }
      const created = await api.addAuthor(request)
      try {
        localStorage.setItem(AUTO_GRAB_STORAGE_KEY, String(searchOnAdd))
      } catch {
        // ignore storage failures (private mode, quota, etc.)
      }
      onAdded(created)
    } catch (err: unknown) {
      setAddError(err instanceof Error ? err.message : t('addToLibrary.author.addFail'))
      setAddConflict(conflictBody(err))
    } finally {
      setAdding(false)
    }
  }

  const selectClass = 'w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded-md px-3 py-2 text-sm focus:outline-none focus:border-emerald-500'

  return (
    <>
      <div className="p-4 flex-1 overflow-y-auto">
        <div className="rounded-md border border-slate-300 dark:border-zinc-700 bg-slate-200/50 dark:bg-zinc-800/50 p-3">
          <div ref={headingRef} tabIndex={-1} className="font-medium rounded-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-emerald-500">{author.authorName}</div>
          {author.disambiguation && <div className="mt-0.5 text-sm text-fg-muted">{author.disambiguation}</div>}
        </div>

        {mismatchedProvider && (
          <p role="alert" className="mt-3 px-3 py-2 rounded bg-amber-50 dark:bg-amber-950/40 border border-amber-300 dark:border-amber-700/60 text-xs text-amber-900 dark:text-amber-200">
            {t('addToLibrary.author.providerMismatchNotice', {
              linked: providerDisplayName(mismatchedProvider),
              primary: providerDisplayName(primaryProvider),
            })}
          </p>
        )}

        {/* Not collapsed. These controls decide whether adding an author is
            a trickle or a flood, and the product people arrive from
            (Readarr) shows them directly with a help line each. Hidden
            behind a closed disclosure they were found only by people who
            already knew what to look for, and the recurring support
            question "why do I have 100 wanted books" is what that cost. */}
        <section className="mt-4 rounded-md border border-slate-300 dark:border-zinc-700" aria-labelledby="add-author-monitoring-heading">
          <h4 id="add-author-monitoring-heading" className="px-3 py-2 text-sm font-medium">
            {t('addToLibrary.author.customizeMonitoring')}
          </h4>
          <div className="space-y-3 border-t border-slate-300 dark:border-zinc-700 p-3">
            {profiles.length > 1 && (
              <div>
                <label htmlFor="add-author-profile" className="block text-xs text-fg-muted mb-1">{t('addToLibrary.author.metadataProfile')}</label>
                <select id="add-author-profile" value={profileId ?? ''} onChange={e => setProfileId(e.target.value ? Number(e.target.value) : null)} className={selectClass}>
                  {profiles.map(p => <option key={p.id} value={p.id}>{p.name}</option>)}
                </select>
              </div>
            )}
            {rootFolders.length > 0 && (
              <div>
                <label htmlFor="add-author-root" className="block text-xs text-fg-muted mb-1">{t('addToLibrary.author.rootFolder')}</label>
                <select id="add-author-root" value={rootFolderId ?? ''} onChange={e => setRootFolderId(e.target.value ? Number(e.target.value) : null)} className={selectClass}>
                  {rootFolders.map(rf => <option key={rf.id} value={rf.id}>{rf.path}</option>)}
                </select>
              </div>
            )}
            <div>
              <label htmlFor="add-author-media" className="block text-xs text-fg-muted mb-1">{t('addToLibrary.author.mediaType')}</label>
              <select id="add-author-media" value={mediaType} onChange={e => setMediaType(e.target.value as MediaType)} className={selectClass}>
                <option value="ebook">{t('mediaType.ebook', 'Ebook')}</option>
                <option value="audiobook">{t('mediaType.audiobook', 'Audiobook')}</option>
                <option value="both">{t('mediaType.both', 'Both')}</option>
              </select>
            </div>
            <div>
              <label htmlFor="add-author-monitor-mode" className="block text-xs text-fg-muted mb-1">{t('addToLibrary.author.monitorMode')}</label>
              <select id="add-author-monitor-mode" value={monitorMode} onChange={e => { setMonitorMode(e.target.value as AuthorMonitorMode); setMonitorOptionsChanged(true) }} className={selectClass}>
                <option value="all">{t('monitorMode.all', 'All books')}</option>
                <option value="future">{t('monitorMode.future', 'Future books only (grows on refresh)')}</option>
                <option value="latest">{t('monitorMode.latest', 'Latest only')}</option>
                <option value="none">{t('monitorMode.none', 'None')}</option>
              </select>
              <span className="block text-xs text-fg-muted mt-1">{t('addToLibrary.author.monitorModeHint')}</span>
            </div>
            {monitorMode === 'latest' && (
              <div>
                <label htmlFor="add-author-latest-count" className="block text-xs text-fg-muted mb-1">{t('addToLibrary.author.monitorLatestCount')}</label>
                <input id="add-author-latest-count" type="number" min={1} value={monitorLatestCount} onChange={e => { setMonitorLatestCount(Math.max(1, Number(e.target.value) || 1)); setMonitorOptionsChanged(true) }} className={selectClass} />
              </div>
            )}
            <div>
              <label htmlFor="add-author-monitor-new-items" className="block text-xs text-fg-muted mb-1">{t('addToLibrary.author.monitorNewItems')}</label>
              <select id="add-author-monitor-new-items" value={monitorNewItems} onChange={e => { setMonitorNewItems(e.target.value as MonitorNewItems); setMonitorNewItemsChanged(true) }} className={selectClass}>
                <option value="all">{t('monitorNewItems.all', 'Follow monitor mode')}</option>
                <option value="none">{t('monitorNewItems.none', 'Don’t add them')}</option>
              </select>
              <span className="block text-xs text-fg-muted mt-1">{t('addToLibrary.author.monitorNewItemsHint')}</span>
            </div>
            {/* The consequence, stated before the button is pressed. The
                count is the provider's raw work count from the search
                result; the catalogue that actually lands is smaller after
                dedup and filtering, hence "up to". */}
            <p className="text-sm text-slate-700 dark:text-zinc-300 rounded-md bg-slate-200/60 dark:bg-zinc-800/60 px-3 py-2" data-testid="add-author-outcome">
              {(() => {
                const count = author.statistics?.bookCount ?? 0
                const key = count > 0 ? `addToLibrary.author.outcome.${monitorMode}` : `addToLibrary.author.outcomeNoCount.${monitorMode}`
                return t(key, { count, latest: monitorLatestCount })
              })()}
            </p>
            <label className="flex items-start gap-2 text-sm cursor-pointer select-none">
              <input type="checkbox" checked={searchOnAdd} onChange={e => setSearchOnAdd(e.target.checked)} className="accent-emerald-500 mt-0.5 flex-shrink-0" />
              <span>
                <span className="font-medium">{t('addToLibrary.author.autoGrabLabel')}</span>
                <span className="block text-xs text-fg-muted mt-0.5">{t('addToLibrary.author.autoGrabHint')}</span>
              </span>
            </label>
            {searchOnAdd && needsAny && (
              <p className="mt-2 px-3 py-2 rounded bg-amber-50 dark:bg-amber-950/40 border border-amber-300 dark:border-amber-700/60 text-xs text-amber-900 dark:text-amber-200">
                {needsClient
                  ? t('addToLibrary.author.autoGrabNoClient')
                  : t('addToLibrary.author.autoGrabNoIndexer')}
              </p>
            )}
          </div>
        </section>

        {addError && (
          <div role="alert" className="mt-3 px-3 py-2 bg-red-100 dark:bg-red-950/30 border border-red-300 dark:border-red-900 rounded text-sm text-red-800 dark:text-red-300">
            <div>{addError}</div>
            {addConflict?.canonicalAuthorId && (
              <div className="mt-2 flex flex-wrap gap-3 text-xs font-medium">
                <a href={`${basePath()}/author/${addConflict.canonicalAuthorId}`} className="underline">{t('addToLibrary.author.openExisting')}</a>
                {/* A conflict always involves an existing author the user may
                    want to point at a richer provider, regardless of how
                    complete that record is. */}
                <a href={`${basePath()}/author/${addConflict.canonicalAuthorId}?linkMetadata=1`} className="underline">{t('addToLibrary.author.findMetadata')}</a>
              </div>
            )}
          </div>
        )}
      </div>

      <div className="p-4 border-t border-slate-200 dark:border-zinc-800 flex justify-end gap-2">
        <button type="button" onClick={onBack} disabled={adding} className="mr-auto px-4 py-2 text-sm text-fg-muted hover:text-slate-900 dark:hover:text-white disabled:opacity-50">{t('addToLibrary.backToResults')}</button>
        <button type="button" onClick={onClose} className="px-4 py-2 text-sm text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white">{t('common.cancel')}</button>
        <button type="button" onClick={addAuthor} disabled={adding || !defaults} className="px-4 py-2 bg-emerald-600 hover:bg-emerald-500 disabled:opacity-50 disabled:cursor-not-allowed rounded-md text-sm font-medium text-white">{adding ? t('addToLibrary.adding') : t('addToLibrary.author.confirmAdd')}</button>
      </div>
    </>
  )
}
