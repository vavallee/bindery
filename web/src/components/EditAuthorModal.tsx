import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, Author, AuthorMonitorMode, MetadataProfile, QualityProfile, RootFolder, UpdateAuthorRequest } from '../api/client'

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

  const [qualityProfileId, setQualityProfileId] = useState<number | null>(author.qualityProfileId ?? null)
  const [metadataProfileId, setMetadataProfileId] = useState<number | null>(author.metadataProfileId ?? null)
  const [rootFolderId, setRootFolderId] = useState<number | null>(author.rootFolderId ?? null)
  const [audiobookRootFolderId, setAudiobookRootFolderId] = useState<number | null>(author.audiobookRootFolderId ?? null)
  const [monitorMode, setMonitorMode] = useState<AuthorMonitorMode>(author.monitorMode ?? 'all')
  const [monitorLatestCount, setMonitorLatestCount] = useState(author.monitorLatestCount ?? 1)
  const [applyMonitorModeToExisting, setApplyMonitorModeToExisting] = useState(false)

  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)

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
    if (monitorLatestCount !== (author.monitorLatestCount ?? 1)) {
      patch.monitorLatestCount = monitorLatestCount
    }
    if (applyMonitorModeToExisting) {
      patch.applyMonitorModeToExisting = true
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
                </select>
              </div>
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
