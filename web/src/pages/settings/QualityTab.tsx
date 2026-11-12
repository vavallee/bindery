import { FormEvent, useCallback, useEffect, useState, useId } from 'react'
import { useTranslation } from 'react-i18next'
import { useConfirmDialog } from '../../components/useConfirmDialog'
import { api, QualityProfile } from '../../api/client'
import { inputCls, labelCls } from './formStyles'
import { dangerLink, primaryFill } from '../../components/buttons'
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
          className={`px-3 py-1.5 ${primaryFill} rounded text-xs font-medium`}
        >
          {t('settings.quality.newProfile')}
        </button>
      </div>
      <p className="text-xs text-fg-muted mb-4">
        {t('settings.quality.description')}
      </p>
      <NewAuthorQualityDefaults profiles={profiles} />
      {creating && (
        <QualityProfileForm
          onClose={() => setCreating(false)}
          onSaved={() => { setCreating(false); reload() }}
        />
      )}
      {profiles.length === 0 && !creating ? (
        <p className="text-fg-muted text-sm">{t('settings.quality.empty')}</p>
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

const DEFAULT_QUALITY_PROFILE_SETTING = 'author.default_quality_profile_id'

// The two halves of #3831 that live next to the profiles. An author with no
// quality profile has no format filter at all, at grab or at import, and until
// this setting existed every author added from the UI ended up that way.
//
// The default applies to new authors only. Existing authors with no profile
// are counted and offered a one time apply, which never runs by itself.
//
// Every API call goes through Promise.resolve().then so a failure of any kind,
// including an older server without these routes, lands in the catch and the
// section simply stays quiet instead of breaking the profile list below it.
function NewAuthorQualityDefaults({ profiles }: { profiles: QualityProfile[] }) {
  const { t } = useTranslation()
  const fid = useId()
  const { confirm, confirmDialog } = useConfirmDialog()
  const [defaultId, setDefaultId] = useState<number | null>(null)
  const [defaultErr, setDefaultErr] = useState<string | null>(null)
  const [unsetCount, setUnsetCount] = useState(0)
  // The profile picked in the notice; null until the user changes it, so it
  // follows the default (or the first profile) until then.
  const [applyChoice, setApplyChoice] = useState<number | null>(null)
  const [applying, setApplying] = useState(false)
  const [applyMsg, setApplyMsg] = useState<string | null>(null)
  const [applyErr, setApplyErr] = useState<string | null>(null)

  const loadCount = useCallback(() =>
    Promise.resolve()
      .then(() => api.countAuthorsWithoutQualityProfile())
      .then(r => setUnsetCount(r?.count ?? 0))
      .catch(() => setUnsetCount(0)), [])

  useEffect(() => {
    let cancelled = false
    Promise.resolve()
      .then(() => api.getSetting(DEFAULT_QUALITY_PROFILE_SETTING))
      .then(s => {
        if (cancelled) return
        const n = Number(s?.value)
        setDefaultId(Number.isInteger(n) && n > 0 ? n : null)
      })
      .catch(() => { /* unset reads as a 404 */ })
    loadCount()
    return () => { cancelled = true }
  }, [loadCount])

  if (profiles.length === 0) return null

  // A default that names a deleted profile shows as None, which is how the
  // server treats it too.
  const effectiveDefault = profiles.some(p => p.id === defaultId) ? defaultId : null
  const chosen = profiles.find(p => p.id === applyChoice) ?? profiles.find(p => p.id === effectiveDefault) ?? profiles[0]

  const saveDefault = async (value: string) => {
    setDefaultId(value ? Number(value) : null)
    setDefaultErr(null)
    try {
      await api.setSetting(DEFAULT_QUALITY_PROFILE_SETTING, value)
    } catch (e) {
      setDefaultErr(e instanceof Error ? e.message : t('settings.quality.defaultProfileSaveFail'))
    }
  }

  const apply = async () => {
    if (!await confirm({
      title: t('common.confirmTitle'),
      body: t('settings.quality.unsetConfirm', { count: unsetCount, name: chosen.name }),
      confirmLabel: t('settings.quality.unsetApply'),
    })) return
    setApplying(true)
    setApplyErr(null)
    setApplyMsg(null)
    try {
      const res = await api.applyQualityProfileToAuthorsWithout(chosen.id)
      setApplyMsg(t('settings.quality.unsetApplied', { count: res.updated }))
      await loadCount()
    } catch (e) {
      setApplyErr(e instanceof Error ? e.message : t('settings.quality.unsetApplyFail'))
    } finally {
      setApplying(false)
    }
  }

  return (
    <section className="mb-6 p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900">
      {confirmDialog}
      <div id="author.default_quality_profile_id">
        <label htmlFor={`${fid}-default`} className="block text-sm font-medium text-slate-800 dark:text-zinc-200 mb-1">
          {t('settings.quality.defaultProfileLabel')}
        </label>
        <p className="text-xs text-fg-muted mb-2">{t('settings.quality.defaultProfileHint')}</p>
        <select
          id={`${fid}-default`}
          value={effectiveDefault ?? ''}
          onChange={e => { void saveDefault(e.target.value) }}
          className="bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded-md px-3 py-2 text-sm"
        >
          <option value="">{t('common.qualityProfileNone')}</option>
          {profiles.map(p => <option key={p.id} value={p.id}>{p.name}</option>)}
        </select>
        {defaultErr && <p role="alert" className="mt-1 text-xs text-red-700 dark:text-red-300">{defaultErr}</p>}
      </div>
      {unsetCount > 0 && (
        <div className="mt-4 px-3 py-3 rounded bg-amber-50 dark:bg-amber-950/40 border border-amber-300 dark:border-amber-700/60 text-amber-900 dark:text-amber-200">
          <p className="text-sm font-medium">{t('settings.quality.unsetNotice', { count: unsetCount })}</p>
          <p className="text-xs mt-1">{t('settings.quality.unsetHint')}</p>
          <div className="mt-2 flex flex-wrap items-end gap-2">
            <div className="min-w-0">
              <label htmlFor={`${fid}-apply`} className={labelCls}>{t('settings.quality.unsetProfileLabel')}</label>
              <select
                id={`${fid}-apply`}
                value={chosen.id}
                onChange={e => setApplyChoice(Number(e.target.value))}
                className="max-w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded-md px-3 py-2 text-sm text-slate-900 dark:text-zinc-100"
              >
                {profiles.map(p => <option key={p.id} value={p.id}>{p.name}</option>)}
              </select>
            </div>
            <button
              type="button"
              onClick={() => { void apply() }}
              disabled={applying}
              className={`px-3 py-2 ${primaryFill} rounded text-xs font-medium disabled:opacity-50`}
            >
              {t('settings.quality.unsetApply')}
            </button>
          </div>
          {applyErr && <p role="alert" className="mt-2 text-xs text-red-700 dark:text-red-300">{applyErr}</p>}
        </div>
      )}
      {applyMsg && <p role="status" className="mt-3 text-xs text-fg-muted">{applyMsg}</p>}
    </section>
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
          {profile.upgradeAllowed && profile.cutoff && (
            <p className="mt-1 text-[11px] text-emerald-700 dark:text-emerald-400">
              {t(profile.removeReplaced ? 'settings.quality.upgradesBadgeRemove' : 'settings.quality.upgradesBadgeKeep', { cutoff: profile.cutoff })}
            </p>
          )}
          {profile.items && profile.items.length > 0 && (
            <div className="mt-2 space-y-1.5">
              {(['ebook', 'audio'] as const).map(kind => {
                const list = lists[kind]
                if (list.length === 0) return null
                const heading = kind === 'ebook' ? t('settings.quality.ebookList') : t('settings.quality.audiobookList')
                return (
                  <div key={kind}>
                    <p className="text-[10px] text-fg-muted mb-1">
                      {heading}
                      {list.length > 1 && <span className="ml-1.5">{t('settings.quality.bestFirst')}</span>}
                    </p>
                    <ul aria-label={heading} className="flex flex-wrap gap-1.5">
                      {list.map((item, i) => (
                        <li
                          key={item.quality}
                          className={`text-[10px] px-2 py-0.5 rounded ${item.allowed
                            ? 'bg-emerald-100 text-emerald-800 dark:bg-emerald-950 dark:text-emerald-300'
                            : 'bg-slate-200 dark:bg-zinc-800 text-fg-muted'}`}
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
            className="text-xs text-fg-muted hover:text-slate-900 dark:hover:text-white"
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
      {err && <p className="mt-2 text-xs text-rose-700 dark:text-rose-400">{err}</p>}
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
  const fid = useId()
  const { t } = useTranslation()
  const [name, setName] = useState(profile?.name ?? '')
  const [lists, setLists] = useState(() => partitionItems(normalisedItems(profile?.items), EBOOK_FORMATS, AUDIOBOOK_FORMATS))
  const [narratorsText, setNarratorsText] = useState((profile?.preferredNarrators ?? []).join('\n'))
  const [editionPref, setEditionPref] = useState<string>(profile?.editionPreference ?? '')
  const [upgradeAllowed, setUpgradeAllowed] = useState(profile?.upgradeAllowed ?? false)
  const [cutoff, setCutoff] = useState(profile?.cutoff ?? '')
  const [removeReplaced, setRemoveReplaced] = useState(profile?.removeReplaced ?? false)
  const [saving, setSaving] = useState(false)
  const [err, setErr] = useState<string | null>(null)

  // The cutoff must be a ticked ebook format. When it is not, the select shows
  // the best ticked one, which is also what is saved.
  const cutoffOptions = lists.ebook.filter(i => i.allowed).map(i => i.quality)
  const effectiveCutoff = cutoffOptions.includes(cutoff) ? cutoff : (cutoffOptions[0] ?? '')

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
    if (upgradeAllowed && !effectiveCutoff) {
      setErr(t('settings.quality.errorCutoffRequired'))
      return
    }
    setSaving(true)
    try {
      // The cutoff and the upgrade switch are read by the ebook upgrade sweep
      // (#2420). With upgrades off the stored cutoff is sent back untouched,
      // so a value another client wrote is not blanked.
      const payload: Partial<QualityProfile> = {
        name: name.trim(),
        items,
        preferredNarrators: narratorsText.split('\n').map(n => n.trim()).filter(Boolean),
        editionPreference: editionPref as QualityProfile['editionPreference'],
        upgradeAllowed,
        cutoff: upgradeAllowed ? effectiveCutoff : (profile?.cutoff ?? effectiveCutoff),
        removeReplaced,
      }
      if (profile) {
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
        <label htmlFor={`${fid}-1`} className={labelCls}>{t('settings.quality.formName')}</label>
        <input id={`${fid}-1`}
          value={name}
          onChange={e => setName(e.target.value)}
          required
          className={inputCls}
          placeholder={t('settings.quality.formNamePlaceholder')}
        />
      </div>

      <div>
        <label className={labelCls}>{t('settings.quality.formPreference')}</label>
        <p className="text-[11px] text-fg-muted mb-2">
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

      <div>
        <label htmlFor={`${fid}-2`} className={labelCls}>{t('settings.quality.editionPreference')}</label>
        <select id={`${fid}-2`} value={editionPref} onChange={e => setEditionPref(e.target.value)} className={inputCls}>
          <option value="">{t('settings.quality.editionPrefOff')}</option>
          <option value="prefer_unabridged">{t('settings.quality.editionPrefPrefer')}</option>
          <option value="require_unabridged">{t('settings.quality.editionPrefRequire')}</option>
        </select>
        <p className="text-[11px] text-fg-muted mt-1">{t('settings.quality.editionPreferenceHint')}</p>
      </div>

      <div>
        <label htmlFor={`${fid}-3`} className={labelCls}>{t('settings.quality.preferredNarrators')}</label>
        <textarea id={`${fid}-3`}
          value={narratorsText}
          onChange={e => setNarratorsText(e.target.value)}
          rows={3}
          className={inputCls}
          placeholder={t('settings.quality.preferredNarratorsPlaceholder')}
        />
        <p className="text-[11px] text-fg-muted mt-1">{t('settings.quality.preferredNarratorsHint')}</p>
      </div>

      <div className="space-y-2">
        <label className="flex items-center gap-2 cursor-pointer text-xs">
          <input
            type="checkbox"
            checked={upgradeAllowed}
            onChange={e => setUpgradeAllowed(e.target.checked)}
            className="rounded border-slate-300 dark:border-zinc-700 text-emerald-600 focus:ring-emerald-500"
          />
          <span>{t('settings.quality.formUpgradeAllowed')}</span>
        </label>
        <p className="text-[11px] text-fg-muted">{t('settings.quality.formUpgradeHint')}</p>
        {upgradeAllowed && (
          <div className="space-y-3 pl-6">
            <div>
              <label htmlFor={`${fid}-cutoff`} className={labelCls}>{t('settings.quality.formCutoff')}</label>
              <select
                id={`${fid}-cutoff`}
                value={effectiveCutoff}
                onChange={e => setCutoff(e.target.value)}
                className={inputCls}
                disabled={cutoffOptions.length === 0}
              >
                {cutoffOptions.length === 0 && (
                  <option value="">{t('settings.quality.formCutoffNoOptions')}</option>
                )}
                {cutoffOptions.map(q => (
                  <option key={q} value={q}>{q}</option>
                ))}
              </select>
              <p className="text-[11px] text-fg-muted mt-1">{t('settings.quality.formCutoffHint')}</p>
            </div>
            <div>
              <label className="flex items-center gap-2 cursor-pointer text-xs">
                <input
                  type="checkbox"
                  checked={removeReplaced}
                  onChange={e => setRemoveReplaced(e.target.checked)}
                  className="rounded border-slate-300 dark:border-zinc-700 text-emerald-600 focus:ring-emerald-500"
                />
                <span>{t('settings.quality.formRemoveReplaced')}</span>
              </label>
              <p className="text-[11px] text-fg-muted mt-1">{t('settings.quality.formRemoveReplacedHint')}</p>
            </div>
          </div>
        )}
      </div>

      {err && <p className="text-xs text-rose-700 dark:text-rose-400">{err}</p>}

      <div className="flex justify-end gap-2">
        <button
          type="button"
          onClick={onClose}
          disabled={saving}
          className="px-3 py-1.5 text-xs text-fg-muted hover:text-slate-900 dark:hover:text-white disabled:opacity-50"
        >
          {t('common.cancel')}
        </button>
        <button
          type="submit"
          disabled={saving || !name.trim()}
          className={`px-3 py-1.5 ${primaryFill} disabled:opacity-50 rounded text-xs font-medium`}
        >
          {saving ? t('common.saving') : profile ? t('settings.quality.saveChanges') : t('settings.quality.createProfile')}
        </button>
      </div>
    </form>
  )
}

