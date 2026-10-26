import { FormEvent, useEffect, useRef, useState, useId } from 'react'
import { useTranslation } from 'react-i18next'
import { useConfirmDialog } from '../../components/useConfirmDialog'
import { api, AuthorMonitorMode, MetadataProfile } from '../../api/client'
import { inputCls } from './formStyles'
import { dangerLink, primaryFill } from '../../components/buttons'
import Toggle from './Toggle'

function isAuthorMonitorMode(value: string): value is AuthorMonitorMode {
  return value === 'all' || value === 'future' || value === 'latest' || value === 'none'
}

// KNOWN_LANGUAGES are the ISO 639-2/B codes exposed in the profile editor.
// The list is not the full ISO catalogue: it is exactly the set of codes the
// release-name language filter can produce (releaseLanguageTags in
// internal/indexer/searcher.go). That equality matters in one direction in
// particular — a language the filter can DROP but the editor cannot OFFER is a
// language no operator can allow, so their releases disappear with no setting
// to bring them back. Nine codes were in that state until #2273, which is why
// this list grew rather than the filter shrinking.
// TestMetadataEditorLanguageVocabulary in internal/indexer fails on drift.
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
  { code: 'kor', label: 'Korean' },
  { code: 'ara', label: 'Arabic' },
  { code: 'swe', label: 'Swedish' },
  { code: 'nor', label: 'Norwegian' },
  { code: 'dan', label: 'Danish' },
  { code: 'pol', label: 'Polish' },
  { code: 'cze', label: 'Czech' },
  { code: 'tur', label: 'Turkish' },
  { code: 'hin', label: 'Hindi' },
]

// metadata.enrichers_disabled holds the names of the sources turned off,
// comma separated (#1890, #2015). Empty, the default, leaves everything on.
const ENRICHERS_DISABLED_KEY = 'metadata.enrichers_disabled'

const ENRICHER_ROWS = [
  { name: 'openlibrary', labelKey: 'settings.general.enricherOpenlibraryLabel', label: 'OpenLibrary', hintKey: 'settings.general.enricherOpenlibraryHint', hint: 'Descriptions, covers and subjects.' },
  { name: 'hardcover', labelKey: 'settings.general.enricherHardcoverLabel', label: 'Hardcover', hintKey: 'settings.general.enricherHardcoverHint', hint: 'Ratings, curated genres and series. Idle until an API token is stored.' },
  { name: 'googlebooks', labelKey: 'settings.general.enricherGooglebooksLabel', label: 'Google Books', hintKey: 'settings.general.enricherGooglebooksHint', hint: 'Descriptions and covers. Only used once a Google Books API key is stored.' },
  { name: 'dnb', labelKey: 'settings.general.enricherDnbLabel', label: 'DNB (Deutsche Nationalbibliothek)', hintKey: 'settings.general.enricherDnbHint', hint: 'German language details and covers. Safe to turn off if you read no German.' },
]

const AUDIOBOOK_SOURCE_ROWS = [
  { name: 'audible', labelKey: 'settings.general.audibleLabel', label: 'Audible catalogue', hintKey: 'settings.general.audibleHint', hint: 'Adds audiobooks to an author\'s catalogue. It uses an unpublished Amazon endpoint, not a documented API.' },
  { name: 'audnex', labelKey: 'settings.general.audnexLabel', label: 'audnex', hintKey: 'settings.general.audnexHint', hint: 'Narrator, runtime and cover for audiobooks. A community proxy over the same Audible data.' },
]

const ALL_TOGGLEABLE = [...ENRICHER_ROWS, ...AUDIOBOOK_SOURCE_ROWS].map(r => r.name)

function parseDisabledSources(value: string): string[] {
  return value.split(',').map(s => s.trim().toLowerCase()).filter(Boolean)
}

// languageLabel names a language in the UI language. ICU accepts the 639-2/B
// codes above; the English label covers a browser without Intl.DisplayNames.
function languageLabel(code: string, label: string, lng = 'en'): string {
  try {
    const name = new Intl.DisplayNames([lng], { type: 'language' }).of(code)
    if (name && name !== code) return name.charAt(0).toLocaleUpperCase(lng) + name.slice(1)
  } catch {
    // fall through to the English label
  }
  return label
}

function formatLanguageList(csv: string, any: string, lng?: string): string {
  if (!csv || csv.trim() === '' || csv.trim().toLowerCase() === 'any') return any
  return csv.split(',').map(c => {
    const code = c.trim().toLowerCase()
    const known = KNOWN_LANGUAGES.find(k => k.code === code)
    return known ? languageLabel(known.code, known.label, lng) : code
  }).join(', ')
}

export default function MetadataTab() {
  const fid = useId()
  const { t, i18n } = useTranslation()
  const { confirm, confirmDialog } = useConfirmDialog()
  const [profiles, setProfiles] = useState<MetadataProfile[]>([])
  const [editing, setEditing] = useState<MetadataProfile | null>(null)
  const [creating, setCreating] = useState(false)
  const [settings, setSettings] = useState<Record<string, string>>({})
  // Hardcover authenticates every GraphQL query, so it can only be the primary
  // provider once a token is stored. The token itself is a secret setting and
  // never comes back from listSettings — /system/status reports whether one
  // exists.
  const [hardcoverTokenConfigured, setHardcoverTokenConfigured] = useState(false)

  const disabledSources = parseDisabledSources(settings[ENRICHERS_DISABLED_KEY] ?? '')
  // A stored hardcover primary with no token runs as OpenLibrary on the server.
  const storedPrimary = settings['metadata.primary_provider'] || 'openlibrary'
  const effectivePrimary = storedPrimary === 'hardcover' && !hardcoverTokenConfigured ? 'openlibrary' : storedPrimary
  // One save at a time: a click while a save is in flight is ignored (the boxes
  // are disabled meanwhile), so the order requests arrive in cannot store a
  // stale list. A failed save puts the box back to the last saved value.
  const [savingSources, setSavingSources] = useState(false)
  const [sourcesError, setSourcesError] = useState(false)
  const savingSourcesRef = useRef(false)
  const toggleSource = async (name: string) => {
    if (savingSourcesRef.current) return
    const previous = settings[ENRICHERS_DISABLED_KEY] ?? ''
    const next = disabledSources.includes(name)
      ? disabledSources.filter(n => n !== name)
      : [...disabledSources, name]
    const value = ALL_TOGGLEABLE.filter(n => next.includes(n)).join(',')
    savingSourcesRef.current = true
    setSavingSources(true)
    setSourcesError(false)
    setSettings(s => ({ ...s, [ENRICHERS_DISABLED_KEY]: value }))
    try {
      await api.setSetting(ENRICHERS_DISABLED_KEY, value)
    } catch (err) {
      console.error(err)
      setSettings(s => ({ ...s, [ENRICHERS_DISABLED_KEY]: previous }))
      setSourcesError(true)
    } finally {
      savingSourcesRef.current = false
      setSavingSources(false)
    }
  }

  const reload = () => api.listMetadataProfiles().then(setProfiles).catch(console.error)

  useEffect(() => {
    reload()
    api.listSettings().then(list => {
      const map: Record<string, string> = {}
      list.forEach(s => { map[s.key] = s.value })
      setSettings(map)
    }).catch(console.error)
    api.status().then(s => setHardcoverTokenConfigured(s.hardcoverTokenConfigured)).catch(console.error)
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  return (
    <div className="space-y-8">
      {confirmDialog}
      {/* Library Defaults */}
      <section>
        <h3 className="text-base font-semibold mb-3 text-slate-800 dark:text-zinc-200">{t('settings.metadata.libraryDefaults', 'Library Defaults')}</h3>

        {/* Metadata provider */}
        <div className="space-y-4">
        <div className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900">
          <label htmlFor={`${fid}-5`} className="block text-sm font-medium text-slate-800 dark:text-zinc-200 mb-1">
            {t('settings.general.metadataProviderLabel', 'Primary metadata provider')}
          </label>
          <p className="text-xs text-fg-muted mb-2">
            {t('settings.general.metadataProviderHint', 'Selects the source used for author and book search, and decides what an author\'s catalogue looks like. DNB (Deutsche Nationalbibliothek) is recommended for German, Austrian, and Swiss catalogues — it covers German-language publications since 1913 where OpenLibrary coverage is thin. Nasjonalbiblioteket (the National Library of Norway) is recommended for Norwegian catalogues — it holds every Norwegian publication by legal deposit, under its original Norwegian title where OpenLibrary often has only the English translation. BnF (Bibliothèque nationale de France) is recommended for French-language libraries: legal deposit puts nearly every book published in France in its catalogue, under the French edition title, translator included, where OpenLibrary often has only the original English title. Contains data from the Bibliothèque nationale de France under the Licence Ouverte / Open Licence 2.0, with the date each record was retrieved shown on its book and author pages; BnF does not endorse Bindery. Hardcover is a curated catalogue: fewer translation editions, omnibus bundles, and non-book entries than OpenLibrary, at the cost of a thinner long tail. OpenLibrary remains the default. Whichever you pick, the other providers are still used as enrichers, except Nasjonalbiblioteket and BnF, which are only queried when chosen as the primary.')}
          </p>
          <select id={`${fid}-5`}
            value={settings['metadata.primary_provider'] ?? 'openlibrary'}
            onChange={async e => {
              const next = e.target.value
              setSettings(s => ({ ...s, 'metadata.primary_provider': next }))
              await api.setSetting('metadata.primary_provider', next).catch(console.error)
            }}
            className="w-full max-w-md bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded-md px-3 py-2 text-sm"
          >
            <option value="openlibrary">{t('settings.general.metadataProviderOpenlibrary', 'OpenLibrary (default)')}</option>
            <option value="dnb">{t('settings.general.metadataProviderDnb', 'DNB — Deutsche Nationalbibliothek (German/DACH)')}</option>
            <option value="nb">{t('settings.general.metadataProviderNb', 'Nasjonalbiblioteket — National Library of Norway (Norwegian)')}</option>
            <option value="bnf">{t('settings.general.metadataProviderBnf', 'BnF, Bibliothèque nationale de France (French)')}</option>
            <option value="hardcover" disabled={!hardcoverTokenConfigured}>
              {hardcoverTokenConfigured
                ? t('settings.general.metadataProviderHardcover', 'Hardcover (curated catalogue)')
                : t('settings.general.metadataProviderHardcoverNoToken', 'Hardcover — requires an API token (Settings → API Keys)')}
            </option>
          </select>
          {/* The Licence Ouverte asks for credit to the source and the date of
              retrieval wherever the data is shown with its provider (#3144). */}
          {settings['metadata.primary_provider'] === 'bnf' && (
            <p className="text-xs text-fg-muted mt-2" data-testid="bnf-credit">
              {t('settings.general.metadataProviderBnfCredit', 'Contains data from the Bibliothèque nationale de France (BnF) under the Licence Ouverte / Open Licence 2.0, with the date each record was retrieved shown on its book and author pages. BnF does not endorse Bindery. Only BnF\'s catalogue records are used: no cover images are fetched, and BnF is contacted only while it is the primary provider.')}
            </p>
          )}
          {/* The save rebuilds the metadata providers on the spot (#1771), so
              there is no restart to mention. What still surprises a user
              flipping this selector is that the catalogue source is chosen per
              author from their stored foreign ID: authors already in the
              library keep their existing provider. Without saying so the
              predictable outcome is flip, see no change, flip back. */}
          <p className="text-xs text-fg-muted mt-1">
            {t('settings.general.metadataProviderLiveHint', 'Takes effect from the next lookup, with no restart. Authors already in your library stay linked to the provider they were added from; the new primary applies to authors you add afterwards. To move an existing author, use "Link metadata" on their page.')}
          </p>
        </div>

        {/* Metadata sources that can be turned off (#1890, #2015). One stored
            list, metadata.enrichers_disabled, so a checked box means the source
            is NOT in it. The primary stays on whatever the list says. */}
        <div className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900" data-testid="metadata-sources">
          <h4 className="text-sm font-medium text-slate-800 dark:text-zinc-200 mb-1">
            {t('settings.general.enrichersHeading', 'Enrichers')}
          </h4>
          <p className="text-xs text-fg-muted mb-2">
            {t('settings.general.enrichersHint', 'These providers fill in what the primary leaves empty. Untick one and Bindery stops using it as an enricher, which means fewer requests but less fill in: DNB also supplies covers through its cover service, and Hardcover also supplies curated genres, series and edition language evidence. Takes effect from the next lookup, with no restart.')}
          </p>
          <div className="space-y-2">
            {ENRICHER_ROWS.map(row => {
              const isPrimary = row.name === effectivePrimary
              return (
                <label key={row.name} className="flex items-start gap-2 text-sm text-slate-800 dark:text-zinc-200">
                  <input
                    type="checkbox"
                    className="mt-1"
                    aria-label={t(row.labelKey, row.label)}
                    checked={isPrimary || !disabledSources.includes(row.name)}
                    disabled={isPrimary || savingSources}
                    onChange={() => toggleSource(row.name)}
                  />
                  <span>
                    {t(row.labelKey, row.label)}
                    <span className="block text-xs text-fg-muted">
                      {isPrimary
                        ? t('settings.general.enricherPrimaryNote', 'This is the primary provider, so it cannot be turned off.')
                        : t(row.hintKey, row.hint)}
                    </span>
                  </span>
                </label>
              )
            })}
          </div>

          <h4 className="text-sm font-medium text-slate-800 dark:text-zinc-200 mt-4 mb-1">
            {t('settings.general.audiobookSourcesHeading', 'Audiobook sources')}
          </h4>
          <div className="space-y-2">
            {AUDIOBOOK_SOURCE_ROWS.map(row => (
              <label key={row.name} className="flex items-start gap-2 text-sm text-slate-800 dark:text-zinc-200">
                <input
                  type="checkbox"
                  className="mt-1"
                  aria-label={t(row.labelKey, row.label)}
                  checked={!disabledSources.includes(row.name)}
                  disabled={savingSources}
                  onChange={() => toggleSource(row.name)}
                />
                <span>
                  {t(row.labelKey, row.label)}
                  <span className="block text-xs text-fg-muted">{t(row.hintKey, row.hint)}</span>
                </span>
              </label>
            ))}
          </div>
          {sourcesError && (
            <p className="text-xs text-red-700 dark:text-red-400 mt-2" role="alert">
              {t('settings.general.sourcesSaveFailed', 'Could not save that change, so it was put back. Try again.')}
            </p>
          )}
        </div>

        {/* Author defaults */}
        <div className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900 space-y-4">
          <div>
            <label htmlFor={`${fid}-6`} className="block text-sm font-medium text-slate-800 dark:text-zinc-200 mb-1">
              {t('settings.general.defaultMediaTypeLabel', 'Default media type')}
            </label>
            <p className="text-xs text-fg-muted mb-2">
              {t('settings.general.defaultMediaTypeHint', 'Applied to new authors, and to books you add on their own, when no explicit choice is made. Existing rows are unaffected: change authors with the Authors page bulk action, and books from the Books page with Set ebook, Set audiobook or Set both, which also covers your wanted list once Books is filtered to Wanted.')}
            </p>
            <select id={`${fid}-6`}
              value={settings['default.media_type'] ?? 'ebook'}
              onChange={async e => {
                const next = e.target.value
                setSettings(s => ({ ...s, 'default.media_type': next }))
                await api.setSetting('default.media_type', next).catch(console.error)
              }}
              className="bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded-md px-3 py-2 text-sm"
            >
              <option value="ebook">{t('mediaType.ebook', 'Ebook')}</option>
              <option value="audiobook">{t('mediaType.audiobook', 'Audiobook')}</option>
              <option value="both">{t('mediaType.both', 'Both')}</option>
            </select>
            {(settings['default.media_type'] ?? 'ebook') !== 'both' && (
              <label className="mt-3 flex items-start gap-2 text-sm text-slate-800 dark:text-zinc-200">
                <input
                  type="checkbox"
                  className="mt-0.5"
                  checked={(settings['default.media_type_strict'] ?? 'false') === 'true'}
                  onChange={async e => {
                    const next = e.target.checked ? 'true' : 'false'
                    setSettings(s => ({ ...s, 'default.media_type_strict': next }))
                    await api.setSetting('default.media_type_strict', next).catch(console.error)
                  }}
                />
                <span>
                  {t('settings.general.strictMediaTypeLabel', 'Restrict new books to this media type')}
                  <span className="block text-xs text-fg-muted">
                    {t('settings.general.strictMediaTypeHint', 'Skip catalogue books available only in the other format, and narrow dual-format books to this one. Prevents monitoring books you can never grab. Governs catalogue population only: a book you add yourself is always created in the format you picked.')}
                  </span>
                </span>
              </label>
            )}
          </div>
          <div>
            <label htmlFor={`${fid}-7`} className="block text-sm font-medium text-slate-800 dark:text-zinc-200 mb-1">
              {t('settings.general.defaultMonitorModeLabel', 'Default monitor mode')}
            </label>
            <p className="text-xs text-fg-muted mb-2">
              {t('settings.general.defaultMonitorModeHint', 'Applied to newly added authors. Existing authors keep their current mode unless edited.')}
            </p>
            <select id={`${fid}-7`}
              value={isAuthorMonitorMode(settings['author.default_monitor_mode'] ?? '') ? settings['author.default_monitor_mode'] : 'all'}
              onChange={async e => {
                const next = e.target.value as AuthorMonitorMode
                setSettings(s => ({ ...s, 'author.default_monitor_mode': next }))
                await api.setSetting('author.default_monitor_mode', next).catch(console.error)
              }}
              className="bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded-md px-3 py-2 text-sm"
            >
              <option value="all">{t('monitorMode.all', 'All books')}</option>
              <option value="future">{t('monitorMode.future', 'Future books only (grows on refresh)')}</option>
              <option value="latest">{t('monitorMode.latest', 'Latest only')}</option>
              <option value="none">{t('monitorMode.none', 'None')}</option>
            </select>
          </div>
          {(settings['author.default_monitor_mode'] ?? 'all') === 'latest' && (
            <div>
              <label htmlFor={`${fid}-8`} className="block text-sm font-medium text-slate-800 dark:text-zinc-200 mb-1">
                {t('settings.general.defaultMonitorLatestCountLabel', 'Latest book count')}
              </label>
              <input id={`${fid}-8`}
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
                className="w-28 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded-md px-3 py-2 text-sm"
              />
            </div>
          )}
        </div>

        {/* Auto-grab */}
        <div className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900">
          <div className="flex items-center justify-between">
            <div>
              <label className="block text-sm font-medium text-slate-800 dark:text-zinc-200">{t('settings.general.autoGrabLabel')}</label>
              <p className="text-xs text-fg-muted mt-0.5">{t('settings.general.autoGrabHint')}</p>
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
              <p className="text-xs text-fg-muted mt-0.5">{t('settings.general.recommendationsHint')}</p>
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

        {/* Author photos and bios from Wikidata (#3287). Opt in, like NB and
            BnF: while off, Wikimedia is sent no request. */}
        <div className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900">
          <div className="flex items-center justify-between gap-4">
            <div>
              <label className="block text-sm font-medium text-slate-800 dark:text-zinc-200">{t('settings.general.authorWikidataLabel', 'Author photos and bios from Wikidata')}</label>
              <p className="text-xs text-fg-muted mt-0.5">{t('settings.general.authorWikidataHint', 'Fills an author\'s missing photo and bio from Wikimedia Commons and Wikipedia, found on Wikidata by the author\'s library authority ID (NORAF, GND, BnF or OpenLibrary), never by name. Lookups ride on the daily author refresh, and each photo and bio is credited on the author\'s page. While this is off, Wikimedia is sent no request.')}</p>
            </div>
            <Toggle
              checked={(settings['metadata.author_enrichment.wikidata'] ?? 'false') === 'true'}
              onChange={async () => {
                const current = (settings['metadata.author_enrichment.wikidata'] ?? 'false').toLowerCase()
                const next = current === 'true' ? 'false' : 'true'
                setSettings(s => ({ ...s, 'metadata.author_enrichment.wikidata': next }))
                await api.setSetting('metadata.author_enrichment.wikidata', next).catch(console.error)
              }}
              title={(settings['metadata.author_enrichment.wikidata'] ?? 'false') === 'true' ? t('common.disable') : t('common.enable')}
            />
          </div>
        </div>
        </div>
      </section>

      <section>
        <div className="flex justify-between items-center mb-4">
          <h3 className="text-lg font-semibold">{t('settings.metadata.heading')}</h3>
          <button onClick={() => setCreating(true)} className={`px-3 py-1.5 ${primaryFill} rounded text-xs font-medium`}>
            {t('settings.metadata.newProfile')}
          </button>
        </div>
        <p className="text-xs text-fg-muted mb-4">
          {t('settings.metadata.description')}
        </p>
        {creating && (
          <MetadataProfileForm
            onClose={() => setCreating(false)}
            onSaved={() => { setCreating(false); reload() }}
          />
        )}
        {profiles.length === 0 && !creating ? (
          <p className="text-fg-muted text-sm">{t('settings.metadata.empty')}</p>
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
                      <div className="flex flex-wrap gap-3 mt-2 text-xs text-fg-muted">
                        <span>{t('settings.metadata.minPages')} <span className="text-slate-800 dark:text-zinc-200">{p.minPages === 0 ? 'none' : p.minPages}</span></span>
                        <span>{t('settings.metadata.minEditionCount')} <span className="text-slate-800 dark:text-zinc-200">{p.minEditionCount === 0 ? 'none' : p.minEditionCount}</span></span>
                        <span>{t('settings.metadata.languages')} <span className="text-slate-800 dark:text-zinc-200">{formatLanguageList(p.allowedLanguages, t('settings.metadata.languagesAny', 'any'), i18n?.resolvedLanguage)}</span></span>
                      </div>
                      <div className="flex flex-wrap gap-1.5 mt-2">
                        {p.skipMissingDate && <span className="text-[10px] px-2 py-0.5 bg-slate-200 dark:bg-zinc-800 text-fg-muted rounded">{t('settings.metadata.skipMissingDate')}</span>}
                        {p.skipMissingIsbn && <span className="text-[10px] px-2 py-0.5 bg-slate-200 dark:bg-zinc-800 text-fg-muted rounded">{t('settings.metadata.skipMissingIsbn')}</span>}
                        {p.skipPartBooks && <span className="text-[10px] px-2 py-0.5 bg-slate-200 dark:bg-zinc-800 text-fg-muted rounded">{t('settings.metadata.skipPartBooks')}</span>}
                      </div>
                    </div>
                    <div className="flex items-center gap-3 flex-shrink-0">
                      <button onClick={() => setEditing(p)} className="text-xs text-fg-muted hover:text-slate-900 dark:hover:text-white">{t('common.edit')}</button>
                      <button
                        onClick={async () => {
                          if (!await confirm({
                            title: t('common.confirmTitle'),
                            body: t('settings.metadata.deleteConfirm'),
                            confirmLabel: t('common.delete'),
                          })) return
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
  const fid = useId()
  const { t, i18n } = useTranslation()
  const [name, setName] = useState(profile?.name ?? '')
  // Round-tripped, not edited. MinPopularity is no longer enforced anywhere
  // (#2450) and has no control, but the field is still accepted over the API,
  // so sending the stored value back keeps a profile saved from this form from
  // silently blanking a value a third-party client may still be reading. Same
  // shape as #2373, which kept quality_profiles.cutoff for the same reason.
  const minPopularity = profile?.minPopularity ?? 0
  const [minPages, setMinPages] = useState(profile?.minPages ?? 0)
  const [minEditionCount, setMinEditionCount] = useState(profile?.minEditionCount ?? 0)
  const [skipMissingDate, setSkipMissingDate] = useState(profile?.skipMissingDate ?? false)
  const [skipMissingIsbn, setSkipMissingIsbn] = useState(profile?.skipMissingIsbn ?? false)
  const [skipPartBooks, setSkipPartBooks] = useState(profile?.skipPartBooks ?? false)
  const [unknownLanguageBehavior, setUnknownLanguageBehavior] = useState<'pass' | 'fail'>(profile?.unknownLanguageBehavior ?? 'pass')
  // English is the default for a new profile only. An existing profile opens
  // with what it stores, and an empty list means any language: defaulting it
  // to English showed a filter that was not applied, and saving the form
  // applied it (#3212).
  const initialLangs = profile
    ? (profile.allowedLanguages ?? '').split(',').map(c => c.trim().toLowerCase()).filter(c => c && c !== 'any')
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
        minEditionCount,
        skipMissingDate,
        skipMissingIsbn,
        skipPartBooks,
        // The server fills an empty list with English on create, so a new
        // profile with nothing ticked says "any" outright (#3293). An edit
        // keeps the empty list it always stored.
        allowedLanguages: !profile && languages.length === 0 ? 'any' : languages.join(','),
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
        <label htmlFor={`${fid}-1`} className="block text-xs text-fg-muted mb-1">{t('settings.metadata.formName')}</label>
        <input id={`${fid}-1`} value={name} onChange={e => setName(e.target.value)} required className={inputCls} placeholder={t('settings.metadata.formNamePlaceholder')} />
      </div>
      <div>
        <label className="block text-xs text-fg-muted mb-2">{t('settings.metadata.formLanguages')}</label>
        <div className="flex flex-wrap gap-2">
          {KNOWN_LANGUAGES.map(l => {
            const on = languages.includes(l.code)
            return (
              <button
                type="button"
                key={l.code}
                aria-pressed={on}
                onClick={() => toggleLang(l.code)}
                className={`text-xs px-2.5 py-1 rounded border transition-colors ${on
                  ? 'bg-emerald-500/20 border-emerald-500/40 text-emerald-800 dark:text-emerald-300'
                  : 'bg-slate-200 dark:bg-zinc-800 border-slate-300 dark:border-zinc-700 text-fg-muted hover:border-slate-400 dark:hover:border-zinc-600'}`}
              >
                {languageLabel(l.code, l.label, i18n?.resolvedLanguage)}
              </button>
            )
          })}
        </div>
        <p className="text-[11px] text-fg-muted mt-2">
          {t('settings.metadata.formLanguagesHint')}
        </p>
      </div>
      <div>
        <label htmlFor={`${fid}-2`} className="block text-xs text-fg-muted mb-1">{t('settings.metadata.formUnknownLanguage')}</label>
        <select id={`${fid}-2`}
          value={unknownLanguageBehavior}
          onChange={e => setUnknownLanguageBehavior(e.target.value as 'pass' | 'fail')}
          className={inputCls}
        >
          <option value="pass">{t('settings.metadata.formUnknownLanguagePass')}</option>
          <option value="fail">{t('settings.metadata.formUnknownLanguageFail')}</option>
        </select>
        <p className="text-[11px] text-fg-muted mt-2">
          {t('settings.metadata.formUnknownLanguageHint')}
        </p>
      </div>
      <div className="grid grid-cols-2 gap-3">
        <div>
          <label htmlFor={`${fid}-3`} className="block text-xs text-fg-muted mb-1">{t('settings.metadata.formMinPages')}</label>
          <input id={`${fid}-3`} type="number" min={0} value={minPages} onChange={e => setMinPages(Number(e.target.value))} className={inputCls} />
        </div>
        <div>
          <label htmlFor={`${fid}-4`} className="block text-xs text-fg-muted mb-1">{t('settings.metadata.formMinEditionCount')}</label>
          <input id={`${fid}-4`} type="number" min={0} value={minEditionCount} onChange={e => setMinEditionCount(Number(e.target.value))} className={inputCls} />
        </div>
      </div>
      {minEditionCount > 0 && (
        <p className="text-[11px] text-fg-muted">
          {t('settings.metadata.formMinEditionCountHint')}
        </p>
      )}
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
      {err && <div className="text-xs text-red-700 dark:text-red-400">{err}</div>}
      <div className="flex justify-end gap-2">
        <button type="button" onClick={onClose} className="px-3 py-1.5 text-xs text-fg-muted hover:text-slate-900 dark:hover:text-white">
          {t('common.cancel')}
        </button>
        <button type="submit" disabled={saving} className={`px-3 py-1.5 ${primaryFill} disabled:opacity-50 rounded text-xs font-medium`}>
          {saving ? t('common.saving') : profile ? t('settings.metadata.saveChanges') : t('settings.metadata.createProfile')}
        </button>
      </div>
    </form>
  )
}
