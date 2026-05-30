import { FormEvent, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, QualityProfile } from '../../api/client'
import { inputCls, labelCls } from './formStyles'

// EBOOK_FORMATS and AUDIOBOOK_FORMATS are the format keys the rest of the
// backend (decision.QualityRank, QualityFromFilename) already understands.
// Two short lists keeps the UI honest: nothing should be selectable that the
// scanner cannot label on disk. Split ebook/audiobook so a profile editor can
// be focused; the model itself has no ebook-vs-audiobook column, but a
// profile that mixes both is allowed if someone really wants to.
const EBOOK_FORMATS = ['pdf', 'mobi', 'epub', 'azw3'] as const
const AUDIOBOOK_FORMATS = ['mp3', 'm4a', 'm4b', 'flac'] as const
const ALL_FORMATS = [...EBOOK_FORMATS, ...AUDIOBOOK_FORMATS] as const

interface EditorItem {
  quality: string
  allowed: boolean
}

function defaultItems(): EditorItem[] {
  // Worst → best for ebooks. The decision spec compares cutoff and current
  // by index, so order matters: later = better.
  return EBOOK_FORMATS.map(q => ({ quality: q, allowed: true }))
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
  const [deleting, setDeleting] = useState(false)
  const [err, setErr] = useState<string | null>(null)

  const handleDelete = async () => {
    if (!confirm(t('settings.quality.deleteConfirm', { name: profile.name }))) return
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
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <h4 className="font-medium text-sm">{profile.name}</h4>
          <div className="flex flex-wrap items-center gap-3 mt-2 text-xs text-slate-600 dark:text-zinc-400">
            <span>
              {t('settings.quality.cutoff')}{' '}
              <span className="text-slate-800 dark:text-zinc-200">{profile.cutoff}</span>
            </span>
            {profile.upgradeAllowed && (
              <span className="text-emerald-600 dark:text-emerald-400">
                {t('settings.quality.upgradesAllowed')}
              </span>
            )}
          </div>
          {profile.items && profile.items.length > 0 && (
            <div className="flex flex-wrap gap-1.5 mt-2">
              {profile.items.map((item, i) => (
                <span
                  key={`${item.quality}-${i}`}
                  className={`text-[10px] px-2 py-0.5 rounded ${item.allowed
                    ? 'bg-emerald-100 text-emerald-800 dark:bg-emerald-950 dark:text-emerald-300'
                    : 'bg-slate-200 dark:bg-zinc-800 text-slate-500 dark:text-zinc-500'}`}
                >
                  {item.quality}
                </span>
              ))}
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
            className="text-xs text-red-600 dark:text-red-400 hover:text-red-700 dark:hover:text-red-300 disabled:opacity-50"
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
  const [upgradeAllowed, setUpgradeAllowed] = useState(profile?.upgradeAllowed ?? true)
  const [items, setItems] = useState<EditorItem[]>(normalisedItems(profile?.items))
  const [cutoff, setCutoff] = useState(profile?.cutoff ?? '')
  const [saving, setSaving] = useState(false)
  const [err, setErr] = useState<string | null>(null)

  // Cutoff must always be an allowed format. If the current cutoff isn't
  // allowed (or empty), snap it to the highest-ranked allowed item.
  useEffect(() => {
    const allowed = items.filter(i => i.allowed).map(i => i.quality)
    if (allowed.length === 0) {
      if (cutoff !== '') setCutoff('')
      return
    }
    if (!allowed.includes(cutoff)) {
      setCutoff(allowed[allowed.length - 1])
    }
  }, [items, cutoff])

  const toggleAllowed = (quality: string) => {
    setItems(prev => {
      const exists = prev.some(i => i.quality === quality)
      if (exists) {
        return prev.map(i => i.quality === quality ? { ...i, allowed: !i.allowed } : i)
      }
      return [...prev, { quality, allowed: true }]
    })
  }

  const ensureItem = (quality: string) => {
    setItems(prev => prev.some(i => i.quality === quality)
      ? prev
      : [...prev, { quality, allowed: false }])
  }

  const move = (index: number, direction: -1 | 1) => {
    const target = index + direction
    if (target < 0 || target >= items.length) return
    setItems(prev => {
      const next = [...prev]
      ;[next[index], next[target]] = [next[target], next[index]]
      return next
    })
  }

  const removeItem = (quality: string) => {
    setItems(prev => prev.filter(i => i.quality !== quality))
  }

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
    if (!cutoff) {
      setErr(t('settings.quality.errorCutoffRequired'))
      return
    }
    const allowed = items.filter(i => i.allowed).map(i => i.quality)
    if (!allowed.includes(cutoff)) {
      setErr(t('settings.quality.errorCutoffNotAllowed'))
      return
    }
    setSaving(true)
    try {
      const payload: Partial<QualityProfile> = {
        name: name.trim(),
        upgradeAllowed,
        cutoff,
        items,
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

  // Formats not currently in the items list — offered as "+ Add" chips so the
  // user can extend the preference order without leaving the form.
  const present = new Set(items.map(i => i.quality))
  const missing = ALL_FORMATS.filter(f => !present.has(f))

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
        <ul className="space-y-1.5">
          {items.map((item, i) => (
            <li
              key={item.quality}
              className="flex items-center gap-2 px-3 py-1.5 rounded border border-slate-200 dark:border-zinc-800 bg-slate-100 dark:bg-zinc-900"
            >
              <label className="flex items-center gap-2 cursor-pointer text-xs flex-1 min-w-0">
                <input
                  type="checkbox"
                  checked={item.allowed}
                  onChange={() => toggleAllowed(item.quality)}
                  className="rounded border-slate-300 dark:border-zinc-700 text-emerald-600 focus:ring-emerald-500"
                />
                <span className={item.allowed ? 'text-slate-800 dark:text-zinc-200' : 'text-slate-500 dark:text-zinc-500'}>
                  {item.quality}
                </span>
                {(AUDIOBOOK_FORMATS as readonly string[]).includes(item.quality) && (
                  <span
                    className="text-[10px] px-1.5 py-0.5 rounded bg-indigo-100 text-indigo-800 dark:bg-indigo-950 dark:text-indigo-300"
                  >
                    {t('common.audiobook')}
                  </span>
                )}
              </label>
              <button
                type="button"
                onClick={() => move(i, -1)}
                disabled={i === 0}
                aria-label={t('settings.quality.moveUp')}
                className="text-xs px-1.5 py-0.5 text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white disabled:opacity-30"
              >
                {'↑'}
              </button>
              <button
                type="button"
                onClick={() => move(i, 1)}
                disabled={i === items.length - 1}
                aria-label={t('settings.quality.moveDown')}
                className="text-xs px-1.5 py-0.5 text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white disabled:opacity-30"
              >
                {'↓'}
              </button>
              <button
                type="button"
                onClick={() => removeItem(item.quality)}
                aria-label={t('common.remove')}
                className="text-xs px-1.5 py-0.5 text-red-600 dark:text-red-400 hover:text-red-700 dark:hover:text-red-300"
              >
                {'×'}
              </button>
            </li>
          ))}
        </ul>
        {missing.length > 0 && (
          <div className="flex flex-wrap gap-1.5 mt-2">
            <span className="text-[11px] text-slate-500 dark:text-zinc-500 self-center">
              {t('settings.quality.formAddFormat')}
            </span>
            {missing.map(f => (
              <button
                type="button"
                key={f}
                onClick={() => ensureItem(f)}
                className="text-[11px] px-2 py-0.5 rounded border border-slate-300 dark:border-zinc-700 bg-slate-200 dark:bg-zinc-800 text-slate-700 dark:text-zinc-300 hover:border-slate-400 dark:hover:border-zinc-600"
              >
                + {f}
              </button>
            ))}
          </div>
        )}
      </div>

      <div>
        <label className={labelCls}>{t('settings.quality.formCutoff')}</label>
        <select
          value={cutoff}
          onChange={e => setCutoff(e.target.value)}
          className={inputCls}
          disabled={items.filter(i => i.allowed).length === 0}
        >
          {items.filter(i => i.allowed).length === 0 && (
            <option value="">{t('settings.quality.formCutoffNoOptions')}</option>
          )}
          {items.filter(i => i.allowed).map(i => (
            <option key={i.quality} value={i.quality}>{i.quality}</option>
          ))}
        </select>
        <p className="text-[11px] text-slate-500 dark:text-zinc-500 mt-2">
          {t('settings.quality.formCutoffHint')}
        </p>
      </div>

      <label className="flex items-center gap-2 cursor-pointer text-xs">
        <input
          type="checkbox"
          checked={upgradeAllowed}
          onChange={e => setUpgradeAllowed(e.target.checked)}
          className="rounded border-slate-300 dark:border-zinc-700 text-emerald-600 focus:ring-emerald-500"
        />
        <span>{t('settings.quality.formUpgradeAllowed')}</span>
      </label>

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

