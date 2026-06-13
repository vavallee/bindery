import { FormEvent, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, AuthorMonitorMode, MetadataProfile } from '../../api/client'
import { inputCls } from './formStyles'
import { dangerLink } from '../../components/buttons'
import Toggle from './Toggle'

function isAuthorMonitorMode(value: string): value is AuthorMonitorMode {
  return value === 'all' || value === 'future' || value === 'latest' || value === 'none'
}

// KNOWN_LANGUAGES are the ISO 639-2/B codes exposed in the profile editor.
// We keep the list short rather than dumping the full ISO catalogue because
// indexers and metadata providers only reliably tag a handful of majors, and
// a long list invites typos and half-implemented filters.
const KNOWN_LANGUAGES: Array<{ code: string; label: string }> = [
  { code: 'eng', label: 'English' },
  { code: 'fre', label: 'French' },
  { code: 'ger', label: 'German' },
  { code: 'dut', label: 'Dutch' },
  { code: 'spa', label: 'Spanish' },
  { code: 'ita', label: 'Italian' },
  { code: 'por', label: 'Portuguese' },
  { code: 'jpn', label: 'Japanese' },
  { code: 'chi', label: 'Chinese' },
  { code: 'rus', label: 'Russian' },
]

function formatLanguageList(csv: string): string {
  if (!csv || csv.trim() === '' || csv.trim().toLowerCase() === 'any') return 'any'
  return csv.split(',').map(c => {
    const code = c.trim().toLowerCase()
    const known = KNOWN_LANGUAGES.find(k => k.code === code)
    return known ? known.label : code
  }).join(', ')
}

export default function MetadataTab() {
  const { t } = useTranslation()
  const [profiles, setProfiles] = useState<MetadataProfile[]>([])
  const [editing, setEditing] = useState<MetadataProfile | null>(null)
  const [creating, setCreating] = useState(false)
  const [settings, setSettings] = useState<Record<string, string>>({})

  const reload = () => api.listMetadataProfiles().then(setProfiles).catch(console.error)

  useEffect(() => {
    reload()
    api.listSettings().then(list => {
      const map: Record<string, string> = {}
      list.forEach(s => { map[s.key] = s.value })
      setSettings(map)
    }).catch(console.error)
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  return (
    <div className="space-y-8">
      {/* Library Defaults */}
      <section>
        <h3 className="text-base font-semibold mb-3 text-slate-800 dark:text-zinc-200">Library Defaults</h3>

        {/* Metadata provider */}
        <div className="space-y-4">
        <div className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900">
          <label className="block text-sm font-medium text-slate-800 dark:text-zinc-200 mb-1">
            {t('settings.general.metadataProviderLabel', 'Primary metadata provider')}
          </label>
          <p className="text-xs text-slate-600 dark:text-zinc-500 mb-2">
            {t('settings.general.metadataProviderHint', 'Selects the source used for author and book search. DNB (Deutsche Nationalbibliothek) is recommended for German, Austrian, and Swiss catalogues — it covers German-language publications since 1913 where OpenLibrary coverage is thin. OpenLibrary remains the default for other regions. The non-primary provider is always used as an enricher.')}
          </p>
          <select
            value={settings['metadata.primary_provider'] ?? 'openlibrary'}
            onChange={async e => {
              const next = e.target.value
              setSettings(s => ({ ...s, 'metadata.primary_provider': next }))
              await api.setSetting('metadata.primary_provider', next).catch(console.error)
            }}
            className="bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded-md px-3 py-2 text-sm focus:outline-none focus:border-emerald-500"
          >
            <option value="openlibrary">{t('settings.general.metadataProviderOpenlibrary', 'OpenLibrary (default)')}</option>
            <option value="dnb">{t('settings.general.metadataProviderDnb', 'DNB — Deutsche Nationalbibliothek (German/DACH)')}</option>
          </select>
        </div>

        {/* Author defaults */}
        <div className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900 space-y-4">
          <div>
            <label className="block text-sm font-medium text-slate-800 dark:text-zinc-200 mb-1">
              {t('settings.general.defaultMediaTypeLabel', 'Default media type')}
            </label>
            <p className="text-xs text-slate-600 dark:text-zinc-500 mb-2">
              {t('settings.general.defaultMediaTypeHint', 'Applied to new authors when no explicit choice is made. Existing authors are unaffected — use the Authors page bulk action to migrate them.')}
            </p>
            <select
              value={settings['default.media_type'] ?? 'ebook'}
              onChange={async e => {
                const next = e.target.value
                setSettings(s => ({ ...s, 'default.media_type': next }))
                await api.setSetting('default.media_type', next).catch(console.error)
              }}
              className="bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded-md px-3 py-2 text-sm focus:outline-none focus:border-emerald-500"
            >
              <option value="ebook">{t('mediaType.ebook', 'Ebook')}</option>
              <option value="audiobook">{t('mediaType.audiobook', 'Audiobook')}</option>
              <option value="both">{t('mediaType.both', 'Both')}</option>
            </select>
          </div>
          <div>
            <label className="block text-sm font-medium text-slate-800 dark:text-zinc-200 mb-1">
              {t('settings.general.defaultMonitorModeLabel', 'Default monitor mode')}
            </label>
            <p className="text-xs text-slate-600 dark:text-zinc-500 mb-2">
              {t('settings.general.defaultMonitorModeHint', 'Applied to newly added authors. Existing authors keep their current mode unless edited.')}
            </p>
            <select
              value={isAuthorMonitorMode(settings['author.default_monitor_mode'] ?? '') ? settings['author.default_monitor_mode'] : 'all'}
              onChange={async e => {
                const next = e.target.value as AuthorMonitorMode
                setSettings(s => ({ ...s, 'author.default_monitor_mode': next }))
                await api.setSetting('author.default_monitor_mode', next).catch(console.error)
              }}
              className="bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded-md px-3 py-2 text-sm focus:outline-none focus:border-emerald-500"
            >
              <option value="all">{t('monitorMode.all', 'All books')}</option>
              <option value="future">{t('monitorMode.future', 'Future books only')}</option>
              <option value="latest">{t('monitorMode.latest', 'Latest only')}</option>
              <option value="none">{t('monitorMode.none', 'None')}</option>
            </select>
          </div>
          {(settings['author.default_monitor_mode'] ?? 'all') === 'latest' && (
            <div>
              <label className="block text-sm font-medium text-slate-800 dark:text-zinc-200 mb-1">
                {t('settings.general.defaultMonitorLatestCountLabel', 'Latest book count')}
              </label>
              <input
                type="number"
                min={1}
                value={settings['author.default_monitor_latest_count'] ?? '1'}
                onChange={async e => {
                  const next = e.target.value
                  setSettings(s => ({ ...s, 'author.default_monitor_latest_count': next }))
                  if (/^[1-9]\d*$/.test(next)) {
                    await api.setSetting('author.default_monitor_latest_count', next).catch(console.error)
                  }
                }}
                className="w-28 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded-md px-3 py-2 text-sm focus:outline-none focus:border-emerald-500"
              />
            </div>
          )}
        </div>

        {/* Auto-grab */}
        <div className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900">
          <div className="flex items-center justify-between">
            <div>
              <label className="block text-sm font-medium text-slate-800 dark:text-zinc-200">{t('settings.general.autoGrabLabel')}</label>
              <p className="text-xs text-slate-600 dark:text-zinc-500 mt-0.5">{t('settings.general.autoGrabHint')}</p>
            </div>
            <Toggle
              checked={(settings['autoGrab.enabled'] ?? 'true') !== 'false'}
              onChange={async () => {
                const current = (settings['autoGrab.enabled'] ?? 'true').toLowerCase()
                const next = current === 'false' ? 'true' : 'false'
                setSettings(s => ({ ...s, 'autoGrab.enabled': next }))
                await api.setSetting('autoGrab.enabled', next).catch(console.error)
              }}
              title={(settings['autoGrab.enabled'] ?? 'true') !== 'false' ? t('common.disable') : t('common.enable')}
            />
          </div>
        </div>

        {/* Recommendations */}
        <div className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900">
          <div className="flex items-center justify-between">
            <div>
              <label className="block text-sm font-medium text-slate-800 dark:text-zinc-200">{t('settings.general.recommendationsLabel')}</label>
              <p className="text-xs text-slate-600 dark:text-zinc-500 mt-0.5">{t('settings.general.recommendationsHint')}</p>
            </div>
            <Toggle
              checked={(settings['recommendations.enabled'] ?? 'false') === 'true'}
              onChange={async () => {
                const current = (settings['recommendations.enabled'] ?? 'false').toLowerCase()
                const next = current === 'true' ? 'false' : 'true'
                setSettings(s => ({ ...s, 'recommendations.enabled': next }))
                await api.setSetting('recommendations.enabled', next).catch(console.error)
              }}
              title={(settings['recommendations.enabled'] ?? 'false') === 'true' ? t('common.disable') : t('common.enable')}
            />
          </div>
        </div>
        </div>
      </section>

      <section>
        <div className="flex justify-between items-center mb-4">
          <h3 className="text-lg font-semibold">{t('settings.metadata.heading')}</h3>
          <button onClick={() => setCreating(true)} className="px-3 py-1.5 bg-emerald-600 hover:bg-emerald-500 rounded text-xs font-medium">
            {t('settings.metadata.newProfile')}
          </button>
        </div>
        <p className="text-xs text-slate-600 dark:text-zinc-500 mb-4">
          {t('settings.metadata.description')}
        </p>
        {creating && (
          <MetadataProfileForm
            onClose={() => setCreating(false)}
            onSaved={() => { setCreating(false); reload() }}
          />
        )}
        {profiles.length === 0 && !creating ? (
          <p className="text-slate-600 dark:text-zinc-500 text-sm">{t('settings.metadata.empty')}</p>
        ) : (
          <div className="space-y-3">
            {profiles.map(p => (
              editing?.id === p.id ? (
                <MetadataProfileForm
                  key={p.id}
                  profile={p}
                  onClose={() => setEditing(null)}
                  onSaved={() => { setEditing(null); reload() }}
                />
              ) : (
                <div key={p.id} className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900">
                  <div className="flex items-start justify-between">
                    <div className="min-w-0">
                      <h4 className="font-medium text-sm">{p.name}</h4>
                      <div className="flex flex-wrap gap-3 mt-2 text-xs text-slate-600 dark:text-zinc-400">
                        <span>{t('settings.metadata.minPopularity')} <span className="text-slate-800 dark:text-zinc-200">{p.minPopularity === 0 ? 'none' : p.minPopularity}</span></span>
                        <span>{t('settings.metadata.minPages')} <span className="text-slate-800 dark:text-zinc-200">{p.minPages === 0 ? 'none' : p.minPages}</span></span>
                        <span>{t('settings.metadata.languages')} <span className="text-slate-800 dark:text-zinc-200">{formatLanguageList(p.allowedLanguages)}</span></span>
                      </div>
                      <div className="flex flex-wrap gap-1.5 mt-2">
                        {p.skipMissingDate && <span className="text-[10px] px-2 py-0.5 bg-slate-200 dark:bg-zinc-800 text-slate-600 dark:text-zinc-400 rounded">{t('settings.metadata.skipMissingDate')}</span>}
                        {p.skipMissingIsbn && <span className="text-[10px] px-2 py-0.5 bg-slate-200 dark:bg-zinc-800 text-slate-600 dark:text-zinc-400 rounded">{t('settings.metadata.skipMissingIsbn')}</span>}
                        {p.skipPartBooks && <span className="text-[10px] px-2 py-0.5 bg-slate-200 dark:bg-zinc-800 text-slate-600 dark:text-zinc-400 rounded">{t('settings.metadata.skipPartBooks')}</span>}
                      </div>
                    </div>
                    <div className="flex items-center gap-3 flex-shrink-0">
                      <button onClick={() => setEditing(p)} className="text-xs text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white">{t('common.edit')}</button>
                      <button
                        onClick={async () => {
                          if (!confirm(t('settings.metadata.deleteConfirm'))) return
                          await api.deleteMetadataProfile(p.id)
                          reload()
                        }}
                        className={`text-xs ${dangerLink}`}
                      >
                        {t('common.delete')}
                      </button>
                    </div>
                  </div>
                </div>
              )
            ))}
          </div>
        )}
      </section>
    </div>
  )
}

function MetadataProfileForm({ profile, onClose, onSaved }: { profile?: MetadataProfile; onClose: () => void; onSaved: () => void }) {
  const { t } = useTranslation()
  const [name, setName] = useState(profile?.name ?? '')
  const [minPopularity, setMinPopularity] = useState(profile?.minPopularity ?? 0)
  const [minPages, setMinPages] = useState(profile?.minPages ?? 0)
  const [skipMissingDate, setSkipMissingDate] = useState(profile?.skipMissingDate ?? false)
  const [skipMissingIsbn, setSkipMissingIsbn] = useState(profile?.skipMissingIsbn ?? false)
  const [skipPartBooks, setSkipPartBooks] = useState(profile?.skipPartBooks ?? false)
  const [unknownLanguageBehavior, setUnknownLanguageBehavior] = useState<'pass' | 'fail'>(profile?.unknownLanguageBehavior ?? 'pass')
  const initialLangs = profile?.allowedLanguages
    ? profile.allowedLanguages.split(',').map(c => c.trim().toLowerCase()).filter(Boolean)
    : ['eng']
  const [languages, setLanguages] = useState<string[]>(initialLangs)
  const [saving, setSaving] = useState(false)
  const [err, setErr] = useState<string | null>(null)

  const toggleLang = (code: string) => {
    setLanguages(prev => prev.includes(code) ? prev.filter(c => c !== code) : [...prev, code])
  }

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setErr(null)
    setSaving(true)
    try {
      const payload: Partial<MetadataProfile> = {
        name: name.trim(),
        minPopularity,
        minPages,
        skipMissingDate,
        skipMissingIsbn,
        skipPartBooks,
        allowedLanguages: languages.join(','),
        unknownLanguageBehavior,
      }
      if (profile) {
        await api.updateMetadataProfile(profile.id, payload)
      } else {
        await api.addMetadataProfile(payload)
      }
      onSaved()
    } catch (e: unknown) {
      setErr(e instanceof Error ? e.message : t('settings.metadata.saveFail'))
    } finally {
      setSaving(false)
    }
  }

  return (
    <form onSubmit={submit} className="p-4 border border-slate-300 dark:border-zinc-700 rounded-lg bg-slate-50 dark:bg-zinc-900/50 space-y-4">
      <div>
        <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">{t('settings.metadata.formName')}</label>
        <input value={name} onChange={e => setName(e.target.value)} required className={inputCls} placeholder={t('settings.metadata.formNamePlaceholder')} />
      </div>
      <div>
        <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-2">{t('settings.metadata.formLanguages')}</label>
        <div className="flex flex-wrap gap-2">
          {KNOWN_LANGUAGES.map(l => {
            const on = languages.includes(l.code)
            return (
              <button
                type="button"
                key={l.code}
                onClick={() => toggleLang(l.code)}
                className={`text-xs px-2.5 py-1 rounded border transition-colors ${on
                  ? 'bg-emerald-500/20 border-emerald-500/40 text-emerald-300'
                  : 'bg-slate-200 dark:bg-zinc-800 border-slate-300 dark:border-zinc-700 text-slate-600 dark:text-zinc-400 hover:border-slate-400 dark:hover:border-zinc-600'}`}
              >
                {l.label}
              </button>
            )
          })}
        </div>
        <p className="text-[11px] text-slate-500 dark:text-zinc-500 mt-2">
          {t('settings.metadata.formLanguagesHint')}
        </p>
      </div>
      <div>
        <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">{t('settings.metadata.formUnknownLanguage')}</label>
        <select
          value={unknownLanguageBehavior}
          onChange={e => setUnknownLanguageBehavior(e.target.value as 'pass' | 'fail')}
          className={inputCls}
        >
          <option value="pass">{t('settings.metadata.formUnknownLanguagePass')}</option>
          <option value="fail">{t('settings.metadata.formUnknownLanguageFail')}</option>
        </select>
        <p className="text-[11px] text-slate-500 dark:text-zinc-500 mt-2">
          {t('settings.metadata.formUnknownLanguageHint')}
        </p>
      </div>
      <div className="grid grid-cols-2 gap-3">
        <div>
          <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">{t('settings.metadata.formMinPopularity')}</label>
          <input type="number" min={0} value={minPopularity} onChange={e => setMinPopularity(Number(e.target.value))} className={inputCls} />
        </div>
        <div>
          <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">{t('settings.metadata.formMinPages')}</label>
          <input type="number" min={0} value={minPages} onChange={e => setMinPages(Number(e.target.value))} className={inputCls} />
        </div>
      </div>
      <div className="flex flex-wrap gap-4 text-xs">
        <label className="flex items-center gap-2 cursor-pointer">
          <input type="checkbox" checked={skipMissingDate} onChange={e => setSkipMissingDate(e.target.checked)} />
          {t('settings.metadata.formSkipMissingDate')}
        </label>
        <label className="flex items-center gap-2 cursor-pointer">
          <input type="checkbox" checked={skipMissingIsbn} onChange={e => setSkipMissingIsbn(e.target.checked)} />
          {t('settings.metadata.formSkipMissingIsbn')}
        </label>
        <label className="flex items-center gap-2 cursor-pointer">
          <input type="checkbox" checked={skipPartBooks} onChange={e => setSkipPartBooks(e.target.checked)} />
          {t('settings.metadata.formSkipPartBooks')}
        </label>
      </div>
      {err && <div className="text-xs text-red-400">{err}</div>}
      <div className="flex justify-end gap-2">
        <button type="button" onClick={onClose} className="px-3 py-1.5 text-xs text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white">
          {t('common.cancel')}
        </button>
        <button type="submit" disabled={saving} className="px-3 py-1.5 bg-emerald-600 hover:bg-emerald-500 disabled:opacity-50 rounded text-xs font-medium">
          {saving ? t('common.saving') : profile ? t('settings.metadata.saveChanges') : t('settings.metadata.createProfile')}
        </button>
      </div>
    </form>
  )
}
