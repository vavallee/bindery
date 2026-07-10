import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, Author, AuthorMonitorMode, MetadataProfile, MonitorNewItems, QualityProfile, RootFolder, Series, UpdateAuthorRequest } from '../api/client'

interface Props {
  author: Author
  onClose: () => void
  onSaved: (author: Author) => void
}

export default function EditAuthorModal({ author, onClose, onSaved }: Props) {
  const { t } = useTranslation()

  const [qualityProfiles, setQualityProfiles] = useState<QualityProfile[]>([])
  const [metadataProfiles, setMetadataProfiles] = useState<MetadataProfile[]>([])
  const [rootFolders, setRootFolders] = useState<RootFolder[]>([])
  const [authorSeries, setAuthorSeries] = useState<Series[]>([])
  const [seriesLoaded, setSeriesLoaded] = useState(false)

  const [qualityProfileId, setQualityProfileId] = useState<number | null>(author.qualityProfileId ?? null)
  const [metadataProfileId, setMetadataProfileId] = useState<number | null>(author.metadataProfileId ?? null)
  const [rootFolderId, setRootFolderId] = useState<number | null>(author.rootFolderId ?? null)
  const [audiobookRootFolderId, setAudiobookRootFolderId] = useState<number | null>(author.audiobookRootFolderId ?? null)
  const [monitorMode, setMonitorMode] = useState<AuthorMonitorMode>(author.monitorMode ?? 'all')
  const [monitorNewItems, setMonitorNewItems] = useState<MonitorNewItems>(author.monitorNewItems ?? 'all')
  const [monitorLatestCount, setMonitorLatestCount] = useState(author.monitorLatestCount ?? 1)
  const [applyMonitorModeToExisting, setApplyMonitorModeToExisting] = useState(false)
  const [monitoredSeriesIds, setMonitoredSeriesIds] = useState<number[]>(author.monitoredSeriesIds ?? [])

  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // Genre override (#1446): applies immediately (separate from Save) since it
  // fans out to every book row and locks the genres field on each.
  const [genreOverride, setGenreOverride] = useState('')
  const [applyingGenres, setApplyingGenres] = useState(false)
  const [genreApplied, setGenreApplied] = useState<number | null>(null)

  const applyGenres = async () => {
    const genres = genreOverride.split(',').map(g => g.trim()).filter(Boolean)
    if (genres.length === 0) return
    setApplyingGenres(true)
    setError(null)
    setGenreApplied(null)
    try {
      const { updated } = await api.applyAuthorGenres(author.id, genres)
      setGenreApplied(updated)
    } catch (err) {
      setError(err instanceof Error ? err.message : t('editAuthorModal.genreOverrideFail', 'Genre apply failed'))
    } finally {
      setApplyingGenres(false)
    }
  }

  useEffect(() => {
    let cancelled = false
    Promise.all([
      api.listQualityProfiles().catch(() => [] as QualityProfile[]),
      api.listMetadataProfiles().catch(() => [] as MetadataProfile[]),
      api.listRootFolders().catch(() => [] as RootFolder[]),
    ])
      .then(([qps, mps, rfs]) => {
        if (cancelled) return
        setQualityProfiles(qps)
        setMetadataProfiles(mps)
        setRootFolders(rfs)
      })
      .finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, [])

  // Lazy-load the author's series only when the user actually picks series
  // mode. The list can be hundreds of rows for prolific authors and most
  // edits never touch it.
  useEffect(() => {
    if (monitorMode !== 'series' || seriesLoaded) return
    let cancelled = false
    api.listAuthorSeries(author.id)
      .then(list => { if (!cancelled) { setAuthorSeries(list); setSeriesLoaded(true) } })
      .catch(() => { if (!cancelled) { setAuthorSeries([]); setSeriesLoaded(true) } })
    return () => { cancelled = true }
  }, [monitorMode, seriesLoaded, author.id])

  const selectedSet = useMemo(() => new Set(monitoredSeriesIds), [monitoredSeriesIds])

  const toggleSeries = (id: number) => {
    setMonitoredSeriesIds(prev => prev.includes(id) ? prev.filter(x => x !== id) : [...prev, id])
  }

  // The Hardcover-fill behaviour relies on the selected series having a
  // catalog link; surface a warning when any selection is missing one so the
  // user knows the apply pass will only act on locally-known books for those.
  const selectedWithoutHardcover = useMemo(
    () => authorSeries.filter(s => selectedSet.has(s.id) && !s.hardcoverLink),
    [authorSeries, selectedSet],
  )

  const save = async () => {
    // Build a patch with only the fields that actually changed — sending
    // unchanged values is functionally fine but produces noisy log lines.
    const patch: UpdateAuthorRequest = {}
    if (qualityProfileId !== (author.qualityProfileId ?? null)) {
      patch.qualityProfileId = qualityProfileId
    }
    if (metadataProfileId !== (author.metadataProfileId ?? null)) {
      patch.metadataProfileId = metadataProfileId
    }
    if (rootFolderId !== (author.rootFolderId ?? null)) {
      patch.rootFolderId = rootFolderId
    }
    if (audiobookRootFolderId !== (author.audiobookRootFolderId ?? null)) {
      // A null audiobookRootFolderId is indistinguishable from "omitted" once
      // serialised, so resetting to the global dir is signalled by an explicit
      // flag the backend honours separately.
      if (audiobookRootFolderId === null) {
        patch.clearAudiobookRootFolder = true
      } else {
        patch.audiobookRootFolderId = audiobookRootFolderId
      }
    }
    if (monitorMode !== (author.monitorMode ?? 'all')) {
      patch.monitorMode = monitorMode
    }
    if (monitorNewItems !== (author.monitorNewItems ?? 'all')) {
      patch.monitorNewItems = monitorNewItems
    }
    if (monitorLatestCount !== (author.monitorLatestCount ?? 1)) {
      patch.monitorLatestCount = monitorLatestCount
    }
    if (applyMonitorModeToExisting) {
      patch.applyMonitorModeToExisting = true
    }
    // Only include the series selection on the wire when the user is in
    // series mode and the set actually differs from what came back from the
    // Get. An undefined field leaves the existing selection untouched.
    if (monitorMode === 'series') {
      const original = (author.monitoredSeriesIds ?? []).slice().sort()
      const current = monitoredSeriesIds.slice().sort()
      const changed = original.length !== current.length || original.some((id, i) => id !== current[i])
      if (changed || monitorMode !== (author.monitorMode ?? 'all')) {
        patch.monitoredSeriesIds = monitoredSeriesIds
      }
    }

    if (Object.keys(patch).length === 0) {
      onClose()
      return
    }

    setSaving(true)
    setError(null)
    try {
      const updated = await api.updateAuthor(author.id, patch)
      onSaved(updated)
      onClose()
    } catch (err) {
      setError(err instanceof Error ? err.message : t('editAuthorModal.saveFail', 'Failed to save'))
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="fixed inset-0 bg-black/60 flex items-center justify-center p-4 z-50" onClick={onClose}>
      <div className="bg-slate-100 dark:bg-zinc-900 border border-slate-300 dark:border-zinc-700 rounded-lg w-full max-w-lg shadow-2xl max-h-[90vh] flex flex-col" onClick={e => e.stopPropagation()}>
        <div className="p-4 border-b border-slate-200 dark:border-zinc-800">
          <h3 className="text-lg font-semibold">{t('editAuthorModal.title', 'Edit Author')}</h3>
          <p className="text-xs text-slate-600 dark:text-zinc-500 mt-1">{author.authorName}</p>
        </div>

        <div className="p-4 flex-1 overflow-y-auto">
          {loading ? (
            <p className="text-sm text-slate-600 dark:text-zinc-500">{t('common.loading', 'Loading...')}</p>
          ) : (
            <>
              {qualityProfiles.length > 0 && (
                <div className="mb-3">
                  <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">{t('editAuthorModal.qualityProfile', 'Quality profile')}</label>
                  <select
                    value={qualityProfileId ?? ''}
                    onChange={e => setQualityProfileId(e.target.value ? Number(e.target.value) : null)}
                    className="w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded-md px-3 py-2 text-sm focus:outline-none focus:border-emerald-500"
                  >
                    {qualityProfiles.map(p => (
                      <option key={p.id} value={p.id}>{p.name}</option>
                    ))}
                  </select>
                </div>
              )}
              {metadataProfiles.length > 0 && (
                <div className="mb-3">
                  <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">{t('editAuthorModal.metadataProfile', 'Metadata profile')}</label>
                  <select
                    value={metadataProfileId ?? ''}
                    onChange={e => setMetadataProfileId(e.target.value ? Number(e.target.value) : null)}
                    className="w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded-md px-3 py-2 text-sm focus:outline-none focus:border-emerald-500"
                  >
                    {metadataProfiles.map(p => (
                      <option key={p.id} value={p.id}>{p.name}</option>
                    ))}
                  </select>
                </div>
              )}
              {rootFolders.length > 0 && (
                <div className="mb-3">
                  <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">{t('editAuthorModal.rootFolder', 'Root folder')}</label>
                  <select
                    value={rootFolderId ?? ''}
                    onChange={e => setRootFolderId(e.target.value ? Number(e.target.value) : null)}
                    className="w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded-md px-3 py-2 text-sm focus:outline-none focus:border-emerald-500"
                  >
                    {rootFolders.map(rf => (
                      <option key={rf.id} value={rf.id}>{rf.path}</option>
                    ))}
                  </select>
                </div>
              )}
              {rootFolders.length > 0 && (
                <div className="mb-3">
                  <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">{t('editAuthorModal.audiobookRootFolder', 'Audiobook root folder')}</label>
                  <select
                    value={audiobookRootFolderId ?? ''}
                    onChange={e => setAudiobookRootFolderId(e.target.value ? Number(e.target.value) : null)}
                    className="w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded-md px-3 py-2 text-sm focus:outline-none focus:border-emerald-500"
                  >
                    <option value="">{t('editAuthorModal.audiobookRootFolderDefault', 'Use global audiobook folder')}</option>
                    {rootFolders.map(rf => (
                      <option key={rf.id} value={rf.id}>{rf.path}</option>
                    ))}
                  </select>
                  <p className="text-xs text-slate-500 dark:text-zinc-500 mt-1">{t('editAuthorModal.audiobookRootFolderHint', "Where this author's audiobooks are stored. Separate from the ebook root folder.")}</p>
                </div>
              )}
              <div className="mb-3">
                <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">{t('editAuthorModal.monitorMode', 'Monitor mode')}</label>
                <select
                  value={monitorMode}
                  onChange={e => setMonitorMode(e.target.value as AuthorMonitorMode)}
                  className="w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded-md px-3 py-2 text-sm focus:outline-none focus:border-emerald-500"
                >
                  <option value="all">{t('monitorMode.all', 'All books')}</option>
                  <option value="future">{t('monitorMode.future', 'Future books only')}</option>
                  <option value="latest">{t('monitorMode.latest', 'Latest only')}</option>
                  <option value="none">{t('monitorMode.none', 'None')}</option>
                  <option value="series">{t('monitorMode.series', 'By series')}</option>
                </select>
              </div>
              <div className="mb-3">
                <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">{t('editAuthorModal.monitorNewItems', 'Monitor newly discovered books')}</label>
                <select
                  value={monitorNewItems}
                  onChange={e => setMonitorNewItems(e.target.value as MonitorNewItems)}
                  className="w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded-md px-3 py-2 text-sm focus:outline-none focus:border-emerald-500"
                >
                  <option value="all">{t('monitorNewItems.all', 'Follow monitor mode')}</option>
                  <option value="none">{t('monitorNewItems.none', 'Add as unmonitored')}</option>
                </select>
                <p className="text-xs text-slate-500 dark:text-zinc-500 mt-1">{t('editAuthorModal.monitorNewItemsHint', 'Applies to books found by a metadata refresh after the author was added. "Add as unmonitored" stops a refresh from mass-monitoring the back-catalogue.')}</p>
              </div>
              {monitorMode === 'series' && (
                <div className="mb-3">
                  <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">{t('editAuthorModal.monitoredSeries', 'Monitored series')}</label>
                  {!seriesLoaded ? (
                    <p className="text-xs text-slate-500 dark:text-zinc-500">{t('common.loading', 'Loading...')}</p>
                  ) : authorSeries.length === 0 ? (
                    <p className="text-xs text-slate-500 dark:text-zinc-500">
                      {t('editAuthorModal.monitoredSeriesEmpty', 'No series found for this author yet. Refresh the author to pull series data first.')}
                    </p>
                  ) : (
                    <div className="max-h-48 overflow-y-auto border border-slate-300 dark:border-zinc-700 rounded-md p-2 bg-slate-50 dark:bg-zinc-950">
                      {authorSeries.map(s => (
                        <label key={s.id} className="flex items-start gap-2 text-sm py-1 cursor-pointer select-none">
                          <input
                            type="checkbox"
                            checked={selectedSet.has(s.id)}
                            onChange={() => toggleSeries(s.id)}
                            className="accent-emerald-500 mt-0.5 flex-shrink-0"
                          />
                          <span className="flex-1">
                            <span className="font-medium">{s.title}</span>
                            {!s.hardcoverLink && (
                              <span className="ml-2 text-xs text-amber-700 dark:text-amber-400">
                                {t('editAuthorModal.seriesNoHardcoverLink', '(no Hardcover link)')}
                              </span>
                            )}
                          </span>
                        </label>
                      ))}
                    </div>
                  )}
                  {selectedWithoutHardcover.length > 0 && (
                    <p className="text-xs text-amber-700 dark:text-amber-400 mt-2">
                      {t('editAuthorModal.seriesNoHardcoverWarning', 'Some selected series have no Hardcover link. Only books Bindery already knows about will be monitored — missing entries will not be auto-filled.')}
                    </p>
                  )}
                </div>
              )}
              {monitorMode === 'latest' && (
                <div className="mb-3">
                  <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">{t('editAuthorModal.monitorLatestCount', 'Latest book count')}</label>
                  <input
                    type="number"
                    min={1}
                    value={monitorLatestCount}
                    onChange={e => setMonitorLatestCount(Math.max(1, Number(e.target.value) || 1))}
                    className="w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded-md px-3 py-2 text-sm focus:outline-none focus:border-emerald-500"
                  />
                </div>
              )}
              <label className="flex items-start gap-2 text-sm mb-3 cursor-pointer select-none">
                <input
                  type="checkbox"
                  checked={applyMonitorModeToExisting}
                  onChange={e => setApplyMonitorModeToExisting(e.target.checked)}
                  className="accent-emerald-500 mt-0.5 flex-shrink-0"
                />
                <span>
                  <span className="font-medium">{t('editAuthorModal.applyMonitorModeToExisting', 'Apply monitor mode to existing books')}</span>
                  <span className="block text-xs text-slate-600 dark:text-zinc-400 mt-0.5">{t('editAuthorModal.applyMonitorModeToExistingHint', 'Otherwise this only affects books discovered in future refreshes.')}</span>
                </span>
              </label>
              <div className="mt-4 pt-4 border-t border-slate-200 dark:border-zinc-800">
                <label className="block text-sm font-medium mb-1" htmlFor="author-genre-override">
                  {t('editAuthorModal.genreOverride', 'Genre override')}
                </label>
                <p className="text-xs text-slate-600 dark:text-zinc-400 mb-2">
                  {t('editAuthorModal.genreOverrideHint', 'Set the same genres on every book by this author and lock them so metadata refresh keeps your values. Comma-separated.')}
                </p>
                <div className="flex gap-2">
                  <input
                    id="author-genre-override"
                    type="text"
                    value={genreOverride}
                    onChange={e => setGenreOverride(e.target.value)}
                    placeholder="Fantasy, Epic"
                    className="flex-1 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded-md px-3 py-2 text-sm focus:outline-none focus:border-emerald-500"
                  />
                  <button
                    type="button"
                    onClick={applyGenres}
                    disabled={applyingGenres || genreOverride.trim() === ''}
                    className="px-3 py-2 text-sm bg-slate-300 dark:bg-zinc-700 hover:bg-slate-400 dark:hover:bg-zinc-600 disabled:opacity-50 rounded-md font-medium"
                  >
                    {applyingGenres ? '…' : t('editAuthorModal.genreOverrideApply', 'Apply to all books')}
                  </button>
                </div>
                {genreApplied !== null && (
                  <p className="text-xs text-emerald-600 dark:text-emerald-400 mt-1">
                    {t('editAuthorModal.genreOverrideDone', 'Updated {{count}} book(s).', { count: genreApplied })}
                  </p>
                )}
              </div>
              {error && (
                <p className="text-sm text-red-600 dark:text-red-400 mt-2">{error}</p>
              )}
            </>
          )}
        </div>

        <div className="p-4 border-t border-slate-200 dark:border-zinc-800 flex justify-end gap-2">
          <button
            onClick={onClose}
            disabled={saving}
            className="px-4 py-2 text-sm text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white disabled:opacity-50"
          >
            {t('common.cancel', 'Cancel')}
          </button>
          <button
            onClick={save}
            disabled={loading || saving}
            className="px-4 py-2 bg-emerald-600 hover:bg-emerald-500 disabled:opacity-50 rounded-md text-sm font-medium text-white"
          >
            {saving ? t('common.saving', 'Saving...') : t('common.save', 'Save')}
          </button>
        </div>
      </div>
    </div>
  )
}
