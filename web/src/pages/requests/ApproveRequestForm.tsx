import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'
import type { ApproveRequestChoices, LibraryRequest } from '../../api/client'
import { AuthorAddDefaults, loadAuthorAddDefaults } from '../../components/authorAddDefaults'
import { primaryFill } from '../../components/buttons'

interface Props {
  request: LibraryRequest
  onApproved: (updated: LibraryRequest) => void
  onCancel: () => void
}

// The admin's choices for one approval, inline under the request. An author
// request takes the same profile, root folder, monitoring and format choices
// the Add Author dialog offers, prefilled from the instance defaults; a book
// request takes the format and whether to search. The requester's format
// choice is the starting value, not a constraint.
export default function ApproveRequestForm({ request, onApproved, onCancel }: Props) {
  const { t } = useTranslation()
  const isAuthor = request.kind === 'author'
  const [defaults, setDefaults] = useState<AuthorAddDefaults | null>(null)
  const [profileId, setProfileId] = useState<number | null>(null)
  const [rootFolderId, setRootFolderId] = useState<number | null>(null)
  const [monitorMode, setMonitorMode] = useState('all')
  const [mediaType, setMediaType] = useState(request.mediaType)
  const [searchOnAdd, setSearchOnAdd] = useState(!isAuthor)
  const [sending, setSending] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    loadAuthorAddDefaults().then(d => {
      if (cancelled) return
      setDefaults(d)
      setProfileId(d.profiles.length > 0 ? d.profiles[0].id : null)
      setRootFolderId(d.rootFolderId)
      setMonitorMode(d.monitorMode)
      if (!request.mediaType && isAuthor) setMediaType(d.mediaType)
    })
    return () => { cancelled = true }
  }, [isAuthor, request.mediaType])

  const submit = async () => {
    setSending(true)
    setError(null)
    const choices: ApproveRequestChoices = { searchOnAdd, mediaType }
    if (isAuthor) {
      choices.metadataProfileId = profileId
      choices.rootFolderId = rootFolderId
      choices.monitorMode = monitorMode
    }
    try {
      onApproved(await api.approveRequest(request.id, choices))
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : t('requests.admin.approveFailed'))
    } finally {
      setSending(false)
    }
  }

  const selectClass = 'w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded-md px-2 py-1.5 text-sm'
  const fieldId = (name: string) => `approve-${request.id}-${name}`

  return (
    <form
      aria-label={t('requests.admin.approveFormLabel', { title: request.title })}
      onSubmit={e => { e.preventDefault(); void submit() }}
      className="mt-3 p-3 rounded-md border border-slate-300 dark:border-zinc-700 bg-slate-100 dark:bg-zinc-900 space-y-3"
    >
      <div className="grid gap-3 sm:grid-cols-2">
        {isAuthor && (
          <>
            <div>
              <label htmlFor={fieldId('profile')} className="block text-xs text-fg-muted mb-1">{t('requests.admin.metadataProfile')}</label>
              <select id={fieldId('profile')} value={profileId ?? ''} onChange={e => setProfileId(e.target.value ? Number(e.target.value) : null)} className={selectClass} disabled={!defaults}>
                {(defaults?.profiles ?? []).map(p => <option key={p.id} value={p.id}>{p.name}</option>)}
              </select>
            </div>
            <div>
              <label htmlFor={fieldId('root')} className="block text-xs text-fg-muted mb-1">{t('requests.admin.rootFolder')}</label>
              <select id={fieldId('root')} value={rootFolderId ?? ''} onChange={e => setRootFolderId(e.target.value ? Number(e.target.value) : null)} className={selectClass} disabled={!defaults}>
                <option value="">{t('requests.admin.defaultRootFolder')}</option>
                {(defaults?.rootFolders ?? []).map(rf => <option key={rf.id} value={rf.id}>{rf.path}</option>)}
              </select>
            </div>
            <div>
              <label htmlFor={fieldId('monitor')} className="block text-xs text-fg-muted mb-1">{t('requests.admin.monitorMode')}</label>
              <select id={fieldId('monitor')} value={monitorMode} onChange={e => setMonitorMode(e.target.value)} className={selectClass}>
                <option value="all">{t('monitorMode.all', 'All books')}</option>
                <option value="future">{t('monitorMode.future', 'Future books only (grows on refresh)')}</option>
                <option value="latest">{t('monitorMode.latest', 'Latest only')}</option>
                <option value="none">{t('monitorMode.none', 'None')}</option>
              </select>
            </div>
          </>
        )}
        <div>
          <label htmlFor={fieldId('format')} className="block text-xs text-fg-muted mb-1">{t('requests.admin.format')}</label>
          <select id={fieldId('format')} value={mediaType} onChange={e => setMediaType(e.target.value)} className={selectClass}>
            <option value="">{t('requests.anyFormat')}</option>
            <option value="ebook">{t('common.ebook')}</option>
            <option value="audiobook">{t('common.audiobook')}</option>
            <option value="both">{t('common.both')}</option>
          </select>
        </div>
      </div>
      <label className="flex items-center gap-2 text-sm">
        <input type="checkbox" checked={searchOnAdd} onChange={e => setSearchOnAdd(e.target.checked)} />
        {t('requests.admin.searchOnAdd')}
      </label>
      {error && <p role="alert" className="text-sm text-red-700 dark:text-red-300">{error}</p>}
      <div className="flex justify-end gap-2">
        <button type="button" onClick={onCancel} className="px-3 py-1.5 text-sm text-fg-muted">{t('common.cancel')}</button>
        <button type="submit" disabled={sending || (isAuthor && !defaults)} className={`px-3 py-1.5 ${primaryFill} disabled:opacity-50 rounded text-sm font-medium`}>
          {sending ? t('requests.admin.approving') : t('requests.admin.confirmApprove')}
        </button>
      </div>
    </form>
  )
}
