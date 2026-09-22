import { FormEvent, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useConfirmDialog } from '../../components/useConfirmDialog'
import { api, QualityProfile } from '../../api/client'
import { inputCls, labelCls } from './formStyles'
import { dangerLink } from '../../components/buttons'
import FormatList from './FormatList'
import { EditorItem, partitionItems } from './qualitySummary'

// EBOOK_FORMATS and AUDIOBOOK_FORMATS mirror formatTokens in
// internal/indexer/release.go, split by indexer.MediaTypeForFormat. Every
// token release parsing can emit must have a row here: since #1693 the
// allow-list is authoritative, so a format this editor cannot offer can never
// be allowed for an author with a configured profile (#1700). Within each
// list the order is best first, following models.QualityRank where a rank
// exists; it is only the order chips are offered in, the profile's own order
// is whatever the user saves. The drift guard TestQualityEditorFormatVocabulary
// (internal/indexer/quality_editor_vocab_test.go) fails the Go suite if
// either side of this mirror changes without the other.
// The split is the model (#2733): a profile is one ordered list per media
// type, ranked from the top, and the server derives the media type of each
// entry from its token, so the wire shape stays a single items array.
const EBOOK_FORMATS = ['azw3', 'epub', 'mobi', 'azw', 'pdf', 'fb2', 'lit', 'djvu', 'cbz', 'cbr', 'rtf', 'txt'] as const
const AUDIOBOOK_FORMATS = ['flac', 'm4b', 'm4a', 'mp3', 'ogg'] as const

// Seed for a brand-new profile: the mainstream ebook containers only, best
// first. The long tail (txt, comic archives, ogg, …) is offered through the
// "+ Add" chips instead, so a fresh profile does not silently allow plain-text
// dumps or formats the user never asked for.
const DEFAULT_EBOOK_ITEMS = ['azw3', 'epub', 'mobi', 'pdf'] as const

function defaultItems(): EditorItem[] {
  return DEFAULT_EBOOK_ITEMS.map(q => ({ quality: q, allowed: true }))
}

function normalisedItems(items?: EditorItem[]): EditorItem[] {
  if (!items || items.length === 0) return defaultItems()
  return items.map(i => ({ quality: i.quality, allowed: !!i.allowed }))
}

export default function QualityTab() {
  const { t } = useTranslation()
  const [profiles, setProfiles] = useState<QualityProfile[]>([])
  const [editing, setEditing] = useState<QualityProfile | null>(null)
  const [creating, setCreating] = useState(false)

  const reload = () => api.listQualityProfiles().then(setProfiles).catch(console.error)

  useEffect(() => {
    reload()
  }, [])

  return (
    <div>
      <div className="flex justify-between items-center mb-4">
        <h3 className="text-lg font-semibold">{t('settings.quality.heading')}</h3>
        <button
          type="button"
          onClick={() => setCreating(true)}
          className="px-3 py-1.5 bg-emerald-600 hover:bg-emerald-500 rounded text-xs font-medium text-white"
        >
          {t('settings.quality.newProfile')}
        </button>
      </div>
      <p className="text-xs text-slate-600 dark:text-zinc-500 mb-4">
        {t('settings.quality.description')}
      </p>
      {creating && (
        <QualityProfileForm
          onClose={() => setCreating(false)}
          onSaved={() => { setCreating(false); reload() }}
        />
      )}
      {profiles.length === 0 && !creating ? (
        <p className="text-slate-600 dark:text-zinc-500 text-sm">{t('settings.quality.empty')}</p>
      ) : (
        <div className="space-y-3">
          {profiles.map(p => (
            editing?.id === p.id ? (
              <QualityProfileForm
                key={p.id}
                profile={p}
                onClose={() => setEditing(null)}
                onSaved={() => { setEditing(null); reload() }}
              />
            ) : (
              <ProfileRow
                key={p.id}
                profile={p}
                onEdit={() => setEditing(p)}
                onDeleted={reload}
              />
            )
          ))}
        </div>
      )}
    </div>
  )
}

function ProfileRow({
  profile,
  onEdit,
  onDeleted,
}: {
  profile: QualityProfile
  onEdit: () => void
  onDeleted: () => void
}) {
  const { t } = useTranslation()
  const { confirm, confirmDialog } = useConfirmDialog()
  const [deleting, setDeleting] = useState(false)
  const [err, setErr] = useState<string | null>(null)
  const lists = partitionItems(profile.items ?? [], EBOOK_FORMATS, AUDIOBOOK_FORMATS)

  const handleDelete = async () => {
    if (!await confirm({
      title: t('common.confirmTitle'),
      body: t('settings.quality.deleteConfirm', { name: profile.name }),
      confirmLabel: t('common.delete'),
    })) return
    setErr(null)
    setDeleting(true)
    try {
      await api.deleteQualityProfile(profile.id)
      onDeleted()
    } catch (e) {
      setErr(e instanceof Error ? e.message : t('settings.quality.deleteFail'))
    } finally {
      setDeleting(false)
    }
  }

  return (
    <div className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900">
      {confirmDialog}
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <h4 className="font-medium text-sm">{profile.name}</h4>
          {profile.items && profile.items.length > 0 && (
            <div className="mt-2 space-y-1.5">
              {(['ebook', 'audio'] as const).map(kind => {
                const list = lists[kind]
                if (list.length === 0) return null
                const heading = kind === 'ebook' ? t('settings.quality.ebookList') : t('settings.quality.audiobookList')
                return (
                  <div key={kind}>
                    <p className="text-[10px] text-slate-500 dark:text-zinc-600 mb-1">
                      {heading}
                      {list.length > 1 && <span className="ml-1.5 text-slate-400 dark:text-zinc-700">{t('settings.quality.bestFirst')}</span>}
                    </p>
                    <ul aria-label={heading} className="flex flex-wrap gap-1.5">
                      {list.map((item, i) => (
                        <li
                          key={item.quality}
                          className={`text-[10px] px-2 py-0.5 rounded ${item.allowed
                            ? 'bg-emerald-100 text-emerald-800 dark:bg-emerald-950 dark:text-emerald-300'
                            : 'bg-slate-200 dark:bg-zinc-800 text-slate-500 dark:text-zinc-500'}`}
                        >
                          {i + 1}. {item.quality}
                        </li>
                      ))}
                    </ul>
                  </div>
                )
              })}
            </div>
          )}
        </div>
        <div className="flex items-center gap-3 flex-shrink-0">
          <button
            type="button"
            onClick={onEdit}
            className="text-xs text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white"
          >
            {t('common.edit')}
          </button>
          <button
            type="button"
            onClick={handleDelete}
            disabled={deleting}
            className={`text-xs disabled:opacity-50 ${dangerLink}`}
          >
            {deleting ? t('common.deleting') : t('common.delete')}
          </button>
        </div>
      </div>
      {err && <p className="mt-2 text-xs text-rose-600 dark:text-rose-400">{err}</p>}
    </div>
  )
}

function QualityProfileForm({
  profile,
  onClose,
  onSaved,
}: {
  profile?: QualityProfile
  onClose: () => void
  onSaved: () => void
}) {
  const { t } = useTranslation()
  const [name, setName] = useState(profile?.name ?? '')
  const [lists, setLists] = useState(() => partitionItems(normalisedItems(profile?.items), EBOOK_FORMATS, AUDIOBOOK_FORMATS))
  const [saving, setSaving] = useState(false)
  const [err, setErr] = useState<string | null>(null)

  // Ebook entries first, then audiobook, then anything a third party client
  // stored that neither list knows. The server derives each entry's media
  // type from its token, so only the order inside each list matters.
  const items: EditorItem[] = [...lists.ebook, ...lists.audio, ...lists.other]

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setErr(null)
    if (!name.trim()) {
      setErr(t('settings.quality.errorName'))
      return
    }
    if (items.length === 0) {
      setErr(t('settings.quality.errorNoFormats'))
      return
    }
    if (!items.some(i => i.allowed)) {
      setErr(t('settings.quality.errorNoAllowed'))
      return
    }
    setSaving(true)
    try {
      const payload: Partial<QualityProfile> = {
        name: name.trim(),
        items,
      }
      // cutoff and upgradeAllowed are no longer editable here (#2373): nothing
      // in Bindery has ever read either one. The columns and the API fields
      // stay, so an edit round-trips whatever the profile already held rather
      // than blanking a value some other client may still be writing.
      if (profile) {
        payload.cutoff = profile.cutoff
        payload.upgradeAllowed = profile.upgradeAllowed
        await api.updateQualityProfile(profile.id, payload)
      } else {
        await api.addQualityProfile(payload)
      }
      onSaved()
    } catch (e) {
      setErr(e instanceof Error ? e.message : t('settings.quality.saveFail'))
    } finally {
      setSaving(false)
    }
  }

  return (
    <form
      onSubmit={submit}
      className="p-4 border border-slate-300 dark:border-zinc-700 rounded-lg bg-slate-50 dark:bg-zinc-900/50 space-y-4"
    >
      <div>
        <label className={labelCls}>{t('settings.quality.formName')}</label>
        <input
          value={name}
          onChange={e => setName(e.target.value)}
          required
          className={inputCls}
          placeholder={t('settings.quality.formNamePlaceholder')}
        />
      </div>

      <div>
        <label className={labelCls}>{t('settings.quality.formPreference')}</label>
        <p className="text-[11px] text-slate-500 dark:text-zinc-500 mb-2">
          {t('settings.quality.formPreferenceHint')}
        </p>
        <div className="space-y-4">
          <FormatList
            kind="ebook"
            items={lists.ebook}
            available={EBOOK_FORMATS}
            onChange={ebook => setLists(prev => ({ ...prev, ebook }))}
          />
          <FormatList
            kind="audio"
            items={lists.audio}
            available={AUDIOBOOK_FORMATS}
            onChange={audio => setLists(prev => ({ ...prev, audio }))}
          />
        </div>
      </div>

      {err && <p className="text-xs text-rose-600 dark:text-rose-400">{err}</p>}

      <div className="flex justify-end gap-2">
        <button
          type="button"
          onClick={onClose}
          disabled={saving}
          className="px-3 py-1.5 text-xs text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white disabled:opacity-50"
        >
          {t('common.cancel')}
        </button>
        <button
          type="submit"
          disabled={saving || !name.trim()}
          className="px-3 py-1.5 bg-emerald-600 hover:bg-emerald-500 disabled:opacity-50 rounded text-xs font-medium text-white"
        >
          {saving ? t('common.saving') : profile ? t('settings.quality.saveChanges') : t('settings.quality.createProfile')}
        </button>
      </div>
    </form>
  )
}

