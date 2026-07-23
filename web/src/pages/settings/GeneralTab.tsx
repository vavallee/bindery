import { FormEvent, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, AuthConfig, AuthStatus, StorageDirStatus, StorageHealth } from '../../api/client'
import AuthSettings from '../../settings/AuthSettings'
import ThemeToggle from '../../components/ThemeToggle'
import LanguageSwitcher from '../../components/LanguageSwitcher'
import ClipboardManualFallback from '../../components/ClipboardManualFallback'
import { useClipboardCopy } from '../../components/useClipboardCopy'
import { useAuth } from '../../auth/AuthContext'
import { inputCls } from './formStyles'
import NamingTemplateField from './NamingTemplateField'
import { useSaveResult } from './useSaveResult'

// Tab identifiers used by onNavigate for soft (no-reload) cross-tab links.
// Kept loose (string) so GeneralTab doesn't need to import SettingsPage's Tab
// union; SettingsPage validates the value.
export interface GeneralTabProps {
  onNavigate?: (tab: string) => void
}

export default function GeneralTab({ onNavigate }: GeneralTabProps = {}) {
  const { t } = useTranslation()
  const { isAdmin } = useAuth()
  const [settings, setSettings] = useState<Record<string, string>>({})
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState<string | null>(null)
  const [dropErr, setDropErr] = useState<string | null>(null)
  const [langErr, setLangErr] = useState<string | null>(null)
  const [scanningLibrary, setScanningLibrary] = useState(false)
  const [scanMessage, setScanMessage] = useState<string | null>(null)
  const scanStartedAt = useRef<number>(0)
  const [lastScan, setLastScan] = useState<{
    ran_at: string
    files_found: number
    reconciled: number
    unmatched: number
    already_tracked?: number
    tag_read_failed?: number
    unmatched_files?: Array<{ path: string; parsed_title: string; parsed_author: string }>
    library_dir?: string
    audiobook_dir?: string
    scanned_paths?: string[]
    no_files_found?: boolean
    scan_error?: string
  } | null>(null)
  const [storage, setStorage] = useState<StorageHealth | null>(null)
  const [langSaveResult, langSave] = useSaveResult()
  const [dropSaveResult, dropSave] = useSaveResult()

  useEffect(() => {
    api.listSettings()
      .then(list => {
        const map: Record<string, string> = {}
        list.forEach(s => { map[s.key] = s.value })
        setSettings(map)
      })
      .catch(console.error)
      .finally(() => setLoading(false))
    api.libraryScanStatus().then(setLastScan).catch(() => {/* no prior scan — ignore 404 */})
    api.getStorage().then(setStorage).catch(console.error)
  }, [])

  const saveSetting = async (key: string) => {
    setSaving(key)
    try {
      await api.setSetting(key, settings[key] ?? '')
    } catch (err) {
      console.error(err)
    } finally {
      setSaving(null)
    }
  }

  // saveDropFolder surfaces the server-side path validation error inline (an
  // unwritable / non-existent drop folder is a common misconfiguration), unlike
  // saveSetting which only logs.
  const saveDropFolder = async () => {
    setSaving('import.drop_folder')
    setDropErr(null)
    try {
      await api.setSetting('import.drop_folder', settings['import.drop_folder'] ?? '')
    } catch (err) {
      const msg = err instanceof Error ? err.message : 'Save failed'
      setDropErr(msg)
      throw err
    } finally {
      setSaving(null)
    }
  }

  // savePreferredLanguage mirrors saveDropFolder: it sets and clears `saving`
  // (so the disabled guard fires and "Saving…" shows) and throws on failure so
  // useSaveResult flips to 'error' AND a visible message is surfaced inline —
  // instead of routing api.setSetting straight into useSaveResult, which set
  // neither `saving` nor any error text.
  const savePreferredLanguage = async () => {
    setSaving('search.preferredLanguage')
    setLangErr(null)
    try {
      await api.setSetting('search.preferredLanguage', settings['search.preferredLanguage'] ?? '')
    } catch (err) {
      const msg = err instanceof Error ? err.message : 'Save failed'
      setLangErr(msg)
      throw err
    } finally {
      setSaving(null)
    }
  }

  useEffect(() => {
    if (!scanningLibrary) return
    const id = setInterval(async () => {
      try {
        const status = await api.libraryScanStatus()
        if (new Date(status.ran_at).getTime() >= scanStartedAt.current) {
          setLastScan(status)
          setScanMessage(null)
          setScanningLibrary(false)
        }
      } catch {
        // result not written yet — keep polling
      }
    }, 2000)
    // Stop after 2 minutes regardless; the scan surely finished or something went wrong.
    const ceiling = setTimeout(() => {
      setScanMessage('Scan started — check back shortly for results.')
      setScanningLibrary(false)
    }, 120_000)
    return () => {
      clearInterval(id)
      clearTimeout(ceiling)
    }
  }, [scanningLibrary])

  const handleScan = async () => {
    scanStartedAt.current = Date.now()
    setScanningLibrary(true)
    setScanMessage('Scanning…')
    setLastScan(null)
    try {
      await api.triggerLibraryScan()
    } catch (err) {
      setScanMessage('Scan failed: ' + (err instanceof Error ? err.message : 'Unknown error'))
      setScanningLibrary(false)
    }
  }

  if (loading) return <div className="text-slate-600 dark:text-zinc-500">{t('common.loading')}</div>

  return (
    <div className="space-y-8">
      {/* Appearance */}
      <section>
        <h3 className="text-base font-semibold mb-3 text-slate-800 dark:text-zinc-200">{t('settings.general.appearance')}</h3>
        <div className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900">
          <div className="flex items-center justify-between gap-3">
            <div className="min-w-0">
              <label className="block text-sm font-medium text-slate-800 dark:text-zinc-200">{t('settings.general.theme')}</label>
              <p className="text-xs text-slate-600 dark:text-zinc-500 mt-1">{t('settings.general.themeHint')}</p>
            </div>
            <ThemeToggle />
          </div>
          <div className="flex items-center justify-between gap-3 border-t border-slate-200 dark:border-zinc-800 pt-3 mt-3">
            <div className="min-w-0">
              <label className="block text-sm font-medium text-slate-800 dark:text-zinc-200">{t('settings.general.language')}</label>
              <p className="text-xs text-slate-600 dark:text-zinc-500 mt-1">{t('settings.general.languageHint')}</p>
            </div>
            <LanguageSwitcher />
          </div>
        </div>
      </section>

      {/* Security — visible to all authenticated users for their own password
          change; admin-only sub-controls are gated inside the component. */}
      <SecuritySection />

      {isAdmin && (<>
      {/* Naming */}
      <section>
        <h3 className="text-base font-semibold mb-3 text-slate-800 dark:text-zinc-200">{t('settings.general.fileNaming')}</h3>
        <div className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900 space-y-3">
          <div>
            <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">Import Mode</label>
            <p className="text-xs text-slate-600 dark:text-zinc-500 mb-2">
              How Bindery places completed downloads into the library.
              <strong>Auto</strong> (the default) hardlinks when the download folder and library share a volume, otherwise copies — either way the source stays in place so torrent seeding keeps working.
              <strong>Move</strong> relocates the source out of the download folder, which breaks seeding.
              Use <strong>Hardlink</strong> or <strong>Copy</strong> to force keeping the source file intact; Hardlink requires the download folder and library to be on the same filesystem/volume.
              Use <strong>External</strong> if another tool (Calibre, Grimmory, etc.) manages your library — Bindery grabs the download and stops; your tool processes it, then Bindery reconciles on the next library scan.
            </p>
            <div className="flex gap-2 flex-wrap">
              {(['auto', 'move', 'copy', 'hardlink', 'external'] as const).map(m => (
                <button
                  key={m}
                  onClick={async () => {
                    setSettings(s => ({ ...s, 'import.mode': m }))
                    await api.setSetting('import.mode', m).catch(console.error)
                  }}
                  className={`px-3 py-1.5 rounded text-xs font-medium border transition-colors ${
                    (settings['import.mode'] ?? 'auto') === m
                      ? 'bg-emerald-600 border-emerald-600 text-white'
                      : 'border-slate-300 dark:border-zinc-700 text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white'
                  }`}
                >
                  {m.charAt(0).toUpperCase() + m.slice(1)}
                </button>
              ))}
            </div>
          </div>
          {['copy', 'hardlink'].includes(settings['import.mode'] ?? 'auto') && (
            <div className="border-t border-slate-200 dark:border-zinc-800 pt-3">
              <label className="flex items-start gap-2 cursor-pointer">
                <input
                  type="checkbox"
                  className="mt-0.5"
                  checked={settings['import.audiobook.flatten_multi_disc'] === 'true'}
                  onChange={async e => {
                    const v = e.target.checked ? 'true' : 'false'
                    setSettings(s => ({ ...s, 'import.audiobook.flatten_multi_disc': v }))
                    await api.setSetting('import.audiobook.flatten_multi_disc', v).catch(console.error)
                  }}
                />
                <span>
                  <span className="text-xs font-medium text-slate-700 dark:text-zinc-300">
                    {t('settings.general.flattenMultiDisc', 'Flatten multi-disc audiobooks')}
                  </span>
                  <span className="block text-xs text-slate-600 dark:text-zinc-500">
                    {t('settings.general.flattenMultiDiscHint', 'When a completed audiobook download has multiple disc folders (Disc 1, CD 2, …), place its tracks into one flat folder named Part 001, Part 002, … so audiobook players sort them correctly. Available only in Copy or Hardlink mode; the source is never moved and torrents keep seeding. Single-disc audiobooks are unaffected.')}
                  </span>
                </span>
              </label>
            </div>
          )}
          {(settings['import.mode'] ?? 'auto') === 'external' && (
            <div className="border-t border-slate-200 dark:border-zinc-800 pt-3 space-y-3">
              <div>
                <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">{t('settings.general.dropFolder', 'Drop folder')}</label>
                <p className="text-xs text-slate-600 dark:text-zinc-500 mb-2">
                  {t('settings.general.dropFolderHint', 'Optional. In External mode, Bindery renames each finished download into this folder for a watch-folder tool (Calibre-Web-Automated, Storyteller) to ingest, instead of leaving it in the download dir. The source is never moved, so torrents keep seeding. Bindery still reconciles the managed copy on the next library scan. Leave empty to hand off in place.')}
                </p>
                <div className="flex gap-2">
                  <input
                    value={settings['import.drop_folder'] ?? ''}
                    onChange={e => { setSettings(s => ({ ...s, 'import.drop_folder': e.target.value })); setDropErr(null) }}
                    placeholder="/cwa-book-ingest"
                    className={inputCls + ' flex-1'}
                  />
                  <button
                    onClick={() => dropSave(saveDropFolder)}
                    disabled={saving === 'import.drop_folder'}
                    className={`px-3 py-2 rounded text-xs font-medium disabled:opacity-50 ${dropSaveResult === 'saved' ? 'bg-emerald-500' : dropSaveResult === 'error' ? 'bg-red-600' : 'bg-emerald-600 hover:bg-emerald-500'}`}
                  >
                    {dropSaveResult === 'saved' ? 'Saved ✓' : dropSaveResult === 'error' ? 'Error' : saving === 'import.drop_folder' ? t('common.saving') : t('common.save')}
                  </button>
                </div>
                {dropErr && <p className="text-xs text-red-600 dark:text-red-400 mt-1">{dropErr}</p>}
              </div>
              <div className="flex gap-6 flex-wrap">
                <div>
                  <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">{t('settings.general.dropLayout', 'Layout')}</label>
                  <select
                    value={settings['import.drop_layout'] ?? 'flat'}
                    onChange={async e => {
                      const v = e.target.value
                      setSettings(s => ({ ...s, 'import.drop_layout': v }))
                      await api.setSetting('import.drop_layout', v).catch(console.error)
                    }}
                    className={inputCls}
                  >
                    <option value="flat">{t('settings.general.dropLayoutFlat', 'Flat (file in folder root)')}</option>
                    <option value="templated">{t('settings.general.dropLayoutTemplated', 'Templated ({Author}/{Title}…)')}</option>
                  </select>
                </div>
                <div>
                  <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">{t('settings.general.dropLinkMode', 'Placement')}</label>
                  <select
                    value={settings['import.drop_link_mode'] ?? 'copy'}
                    onChange={async e => {
                      const v = e.target.value
                      setSettings(s => ({ ...s, 'import.drop_link_mode': v }))
                      await api.setSetting('import.drop_link_mode', v).catch(console.error)
                    }}
                    className={inputCls}
                  >
                    <option value="copy">{t('settings.general.dropLinkCopy', 'Copy')}</option>
                    <option value="hardlink">{t('settings.general.dropLinkHardlink', 'Hardlink (same filesystem)')}</option>
                  </select>
                </div>
              </div>
            </div>
          )}
          <NamingTemplateField
            label={t('settings.general.bookTemplate')}
            kind="book"
            placeholder="{Author}/{Title} ({Year})/{Title} - {Author}.{ext}"
            value={settings['naming.bookTemplate'] ?? ''}
            onChange={v => setSettings(s => ({ ...s, 'naming.bookTemplate': v }))}
            onSave={() => saveSetting('naming.bookTemplate')}
            saving={saving === 'naming.bookTemplate'}
          />
          <NamingTemplateField
            label={t('settings.general.audiobookTemplate')}
            hint={t('settings.general.audiobookTemplateHint')}
            kind="audiobook"
            placeholder="{Author}/{Title} ({Year})"
            value={settings['naming_template_audiobook'] ?? ''}
            onChange={v => setSettings(s => ({ ...s, 'naming_template_audiobook': v }))}
            onSave={() => saveSetting('naming_template_audiobook')}
            saving={saving === 'naming_template_audiobook'}
          />
          <div>
            <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">
              {t('settings.general.audiobookFileTemplate', 'Audiobook file naming (per track)')}
            </label>
            <p className="text-xs text-slate-600 dark:text-zinc-500 mb-2">
              {t('settings.general.audiobookFileTemplateHint', 'Leave empty to keep the download’s original file layout. Set a template to rename every audiobook track in playback order — it must include {Part}.')}
            </p>
            <div className="flex gap-2">
              <input
                type="text"
                value={settings['naming.audiobook_file_template'] ?? ''}
                onChange={e => setSettings(s => ({ ...s, 'naming.audiobook_file_template': e.target.value }))}
                placeholder="{Title} - Part {Part:3}.{ext}"
                className="flex-1 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm font-mono focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600"
              />
              <button
                onClick={() => saveSetting('naming.audiobook_file_template')}
                disabled={saving === 'naming.audiobook_file_template'}
                className="px-3 py-2 rounded text-xs font-medium disabled:opacity-50 bg-emerald-600 hover:bg-emerald-500"
              >
                {saving === 'naming.audiobook_file_template' ? t('common.saving') : t('common.save')}
              </button>
            </div>
          </div>
        </div>
      </section>

      {/* Downloads */}
      <section>
        <h3 className="text-base font-semibold mb-3 text-slate-800 dark:text-zinc-200">{t('settings.general.downloads')}</h3>
        <div className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900 space-y-3">
          <div>
            <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">{t('settings.general.preferredLanguage')}</label>
            <p className="text-xs text-slate-600 dark:text-zinc-500 mb-2">{t('settings.general.preferredLanguageHint')}</p>
            <div className="flex gap-2">
              <select
                value={settings['search.preferredLanguage'] ?? 'en'}
                onChange={e => setSettings(s => ({ ...s, 'search.preferredLanguage': e.target.value }))}
                className="flex-1 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600"
              >
                <option value="any">{t('settings.general.preferredLanguageAny')}</option>
                <option value="en">{t('settings.general.preferredLanguageEn')}</option>
              </select>
              <button
                onClick={() => langSave(savePreferredLanguage)}
                disabled={saving === 'search.preferredLanguage'}
                className={`px-3 py-2 rounded text-xs font-medium disabled:opacity-50 ${langSaveResult === 'saved' ? 'bg-emerald-500' : langSaveResult === 'error' ? 'bg-red-600' : 'bg-emerald-600 hover:bg-emerald-500'}`}
              >
                {langSaveResult === 'saved' ? 'Saved ✓' : langSaveResult === 'error' ? 'Error' : saving === 'search.preferredLanguage' ? t('common.saving') : t('common.save')}
              </button>
            </div>
            {langErr && <p className="text-xs text-red-600 dark:text-red-400 mt-1">{langErr}</p>}
          </div>
        </div>
      </section>

      {/* Storage */}
      <section>
        <h3 className="text-base font-semibold mb-3 text-slate-800 dark:text-zinc-200">{t('settings.general.storage')}</h3>
        <div className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900 space-y-3">
          <p className="text-xs text-slate-600 dark:text-zinc-500">{t('settings.general.storageHint')}</p>
          <div>
            <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">
              {t('settings.general.downloadDir')} <code className="font-mono bg-slate-200 dark:bg-zinc-800 px-1 rounded">BINDERY_DOWNLOAD_DIR</code>
              <StorageHealthBadge status={dirStatus(storage, 'download')} loading={!storage} />
            </label>
            <input
              readOnly
              value={storage?.downloadDir ?? ''}
              className={`${inputCls} font-mono opacity-80 cursor-default`}
            />
          </div>
          <div>
            <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">
              {t('settings.general.audiobookDownloadDir')} <code className="font-mono bg-slate-200 dark:bg-zinc-800 px-1 rounded">BINDERY_AUDIOBOOK_DOWNLOAD_DIR</code>
              {storage?.audiobookDownloadDir && <StorageHealthBadge status={dirStatus(storage, 'audiobook-download')} loading={false} />}
            </label>
            <input
              readOnly
              value={storage?.audiobookDownloadDir || (storage?.downloadDir ?? '')}
              placeholder={storage?.downloadDir ?? ''}
              className={`${inputCls} font-mono opacity-80 cursor-default`}
            />
            {storage && !storage.audiobookDownloadDir && (
              <p className="text-xs text-slate-500 dark:text-zinc-500 mt-1">{t('settings.general.audiobookDownloadDirFallback')}</p>
            )}
          </div>
          <div>
            <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">
              {t('settings.general.libraryDir')} <code className="font-mono bg-slate-200 dark:bg-zinc-800 px-1 rounded">BINDERY_LIBRARY_DIR</code>
              <StorageHealthBadge status={dirStatus(storage, 'library')} loading={!storage} />
            </label>
            <input
              readOnly
              value={storage?.libraryDir ?? ''}
              className={`${inputCls} font-mono opacity-80 cursor-default`}
            />
          </div>
          <div>
            <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">
              {t('settings.general.audiobookDir')} <code className="font-mono bg-slate-200 dark:bg-zinc-800 px-1 rounded">BINDERY_AUDIOBOOK_DIR</code>
              {storage?.audiobookDir && <StorageHealthBadge status={dirStatus(storage, 'audiobook')} loading={false} />}
            </label>
            <input
              readOnly
              value={storage?.audiobookDir || (storage?.libraryDir ?? '')}
              placeholder={storage?.libraryDir ?? ''}
              className={`${inputCls} font-mono opacity-80 cursor-default`}
            />
            {storage && !storage.audiobookDir && (
              <p className="text-xs text-slate-500 dark:text-zinc-500 mt-1">{t('settings.general.audiobookDirFallback')}</p>
            )}
          </div>
          {storage && (
            storage.hardlinkable ? (
              <p className="text-xs text-emerald-700 dark:text-emerald-400 border-t border-slate-200 dark:border-zinc-800 pt-3">
                {t('settings.general.storageHardlinkOk')}
              </p>
            ) : (
              <p className="text-xs text-amber-600 dark:text-amber-400 border-t border-slate-200 dark:border-zinc-800 pt-3">
                {t('settings.general.storageHardlinkWarning')}
                {storage.hardlinkReason && (
                  <span className="block mt-1 text-slate-600 dark:text-zinc-400">{storage.hardlinkReason}</span>
                )}
              </p>
            )
          )}
        </div>
      </section>

      {/* Library */}
      <section>
        <h3 className="text-base font-semibold mb-3 text-slate-800 dark:text-zinc-200">{t('settings.general.library')}</h3>
        <div className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900">
          <div className="flex items-center justify-between">
            <div>
              <p className="text-sm text-slate-700 dark:text-zinc-300">{t('settings.general.scanLibrary')}</p>
              <p className="text-xs text-slate-600 dark:text-zinc-500 mt-0.5">{t('settings.general.scanLibraryHint')}</p>
              {scanMessage && (
                <p className="text-xs text-amber-600 dark:text-amber-400 mt-1">{scanMessage}</p>
              )}
            </div>
            <button
              onClick={handleScan}
              disabled={scanningLibrary}
              className="px-4 py-2 bg-slate-600 hover:bg-slate-500 rounded text-sm font-medium disabled:opacity-50 flex-shrink-0"
            >
              {scanningLibrary ? t('settings.general.scanning') : t('settings.general.scanLibraryButton')}
            </button>
          </div>
          {lastScan && (
            <div className="mt-3 border-t border-slate-200 dark:border-zinc-800 pt-3 text-xs text-slate-600 dark:text-zinc-400">
              <p className="font-medium text-slate-700 dark:text-zinc-300 mb-1">{t('settings.general.lastScan')}</p>
              <div className="flex gap-4">
                <span>{t('settings.general.filesFound')} <span className="font-mono text-slate-800 dark:text-zinc-200">{lastScan.files_found}</span></span>
                <span>{t('settings.general.reconciled')} <span className="font-mono text-emerald-700 dark:text-emerald-400">{lastScan.reconciled}</span></span>
                <span>{t('settings.general.unmatched')} <span className="font-mono text-slate-800 dark:text-zinc-200">{lastScan.unmatched}</span></span>
                {(lastScan.already_tracked ?? 0) > 0 && (
                  <span>{t('settings.general.alreadyTracked')} <span className="font-mono text-slate-800 dark:text-zinc-200">{lastScan.already_tracked}</span></span>
                )}
              </div>
              <p className="mt-1 text-slate-500 dark:text-zinc-500">
                {new Date(lastScan.ran_at).toLocaleString()}
              </p>
              {(() => {
                const paths = (lastScan.scanned_paths && lastScan.scanned_paths.length > 0)
                  ? lastScan.scanned_paths
                  : [lastScan.library_dir, lastScan.audiobook_dir].filter((p): p is string => !!p)
                if (paths.length === 0) return null
                return (
                  <p className="mt-1 text-slate-500 dark:text-zinc-500">
                    {t('settings.general.scannedPaths')}{' '}
                    {paths.map((p, i) => (
                      <span key={i} className="font-mono text-slate-700 dark:text-zinc-300 break-all">
                        {p}{i < paths.length - 1 ? ', ' : ''}
                      </span>
                    ))}
                  </p>
                )
              })()}
              {lastScan.scan_error ? (
                <p className="mt-2 text-amber-600 dark:text-amber-400">
                  {lastScan.scan_error}
                </p>
              ) : (lastScan.no_files_found ?? lastScan.files_found === 0) && (
                <p className="mt-2 text-amber-600 dark:text-amber-400">
                  {t('settings.general.scanNoFilesWarning', {
                    path: lastScan.library_dir || (lastScan.scanned_paths && lastScan.scanned_paths[0]) || '?',
                  })}
                </p>
              )}
              {lastScan.files_found > 0 && lastScan.unmatched > 0 && lastScan.reconciled === 0 && (
                <p className="mt-2 text-amber-600 dark:text-amber-400">
                  {t('settings.general.scanAllUnmatchedHint')}
                </p>
              )}
              {lastScan.unmatched_files && lastScan.unmatched_files.length > 0 && (
                <details className="mt-3">
                  <summary className="cursor-pointer font-medium text-slate-700 dark:text-zinc-300 hover:text-slate-900 dark:hover:text-zinc-100">
                    Unmatched files ({lastScan.unmatched_files.length}{lastScan.unmatched_files.length >= 1000 && lastScan.unmatched > 1000 ? ' of ' + lastScan.unmatched : ''})
                  </summary>
                  <div className="mt-2 max-h-80 overflow-y-auto border border-slate-200 dark:border-zinc-800 rounded bg-slate-50 dark:bg-zinc-950/50">
                    <table className="w-full text-xs">
                      <thead className="sticky top-0 bg-slate-100 dark:bg-zinc-900 border-b border-slate-200 dark:border-zinc-800">
                        <tr>
                          <th className="text-left p-2 font-medium">Path</th>
                          <th className="text-left p-2 font-medium">Parsed Title</th>
                          <th className="text-left p-2 font-medium">Parsed Author</th>
                        </tr>
                      </thead>
                      <tbody>
                        {lastScan.unmatched_files.map((file, idx) => (
                          <tr key={idx} className="border-b border-slate-100 dark:border-zinc-900 hover:bg-slate-100 dark:hover:bg-zinc-900/50">
                            <td className="p-2 font-mono text-xs break-all">{file.path}</td>
                            <td className="p-2">{file.parsed_title || '—'}</td>
                            <td className="p-2">{file.parsed_author || '—'}</td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                </details>
              )}
            </div>
          )}
        </div>
      </section>

      {/* Wanted search interval */}
      <section>
        <h3 className="text-base font-semibold mb-3 text-slate-800 dark:text-zinc-200">{t('settings.general.search')}</h3>
        <div className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900">
          <div>
            <label className="block text-sm font-medium text-slate-800 dark:text-zinc-200 mb-1">
              {t('settings.general.searchIntervalLabel')}
            </label>
            <p className="text-xs text-slate-600 dark:text-zinc-500 mb-2">
              {t('settings.general.searchIntervalHint')}
            </p>
            <select
              value={settings['search.interval'] ?? '12h'}
              onChange={async e => {
                const next = e.target.value
                setSettings(s => ({ ...s, 'search.interval': next }))
                await api.setSetting('search.interval', next).catch(console.error)
              }}
              className="bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded-md px-3 py-2 text-sm focus:outline-none focus:border-emerald-500"
            >
              <option value="6h">6 hours</option>
              <option value="12h">12 hours (default)</option>
              <option value="24h">24 hours</option>
              <option value="48h">48 hours</option>
              <option value="72h">72 hours</option>
              <option value="168h">7 days</option>
            </select>
            <p className="text-xs text-slate-500 dark:text-zinc-600 mt-1">
              {t('settings.general.searchIntervalRestart')}
            </p>
          </div>
        </div>
      </section>

      {/* Default library location */}
      <section>
        <h3 className="text-base font-semibold mb-3 text-slate-800 dark:text-zinc-200">{t('settings.general.defaultLibraryLocation')}</h3>
        <div className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900">
          <p className="text-xs text-slate-600 dark:text-zinc-500 mb-3">
            {t('settings.general.defaultLibraryLocationHint')}
          </p>
          <button
            onClick={() => onNavigate ? onNavigate('rootfolders') : window.location.assign('/settings?tab=rootfolders')}
            className="text-sm text-emerald-600 dark:text-emerald-400 hover:underline"
          >
            {t('settings.general.defaultLibraryLocationLink')}
          </button>
        </div>
      </section>

      </>)}
    </div>
  )
}

// dirStatus looks up the health entry for a named directory in the storage
// response. Returns undefined while loading or when the dir is unconfigured.
function dirStatus(storage: StorageHealth | null, name: string): StorageDirStatus | undefined {
  return storage?.dirs.find(d => d.name === name)
}

// StorageHealthBadge renders a green OK / red failing pill next to a configured
// directory, surfacing the exists+writable health Bindery checks at startup
// (#1183) instead of only logging it.
function StorageHealthBadge({ status, loading }: { status: StorageDirStatus | undefined; loading: boolean }) {
  const { t } = useTranslation()
  if (loading) {
    return <span className="ml-2 text-[11px] font-normal text-slate-500 dark:text-zinc-500">{t('settings.general.storageHealthChecking')}</span>
  }
  if (!status) return null

  const ok = status.exists && status.writable
  const label = ok
    ? t('settings.general.storageHealthOk')
    : !status.exists
      ? t('settings.general.storageHealthMissing')
      : t('settings.general.storageHealthReadOnly')
  const cls = ok
    ? 'bg-emerald-100 text-emerald-800 dark:bg-emerald-900/40 dark:text-emerald-300'
    : 'bg-red-100 text-red-800 dark:bg-red-900/40 dark:text-red-300'

  return (
    <span
      title={status.reason || undefined}
      className={`ml-2 inline-block px-1.5 py-0.5 rounded text-[10px] font-medium align-middle ${cls}`}
    >
      {label}
      {!ok && status.reason ? <span className="font-normal"> — {status.reason}</span> : null}
    </span>
  )
}

function SecuritySection() {
  const { t } = useTranslation()
  const { status, refresh, isAdmin } = useAuth()
  const [cfg, setCfg] = useState<AuthConfig | null>(null)
  const [showKey, setShowKey] = useState(false)
  const [regenerating, setRegenerating] = useState(false)
  const [rotatingSecret, setRotatingSecret] = useState(false)
  const [savingMode, setSavingMode] = useState(false)
  const apiKeyClipboard = useClipboardCopy()

  const loadCfg = () => {
    api.authConfig().then(setCfg).catch(console.error)
  }

  useEffect(() => { loadCfg() }, [])

  const regenerate = async () => {
    if (!confirm('Regenerate the API key? Existing integrations using the old key will stop working.')) return
    setRegenerating(true)
    try {
      const r = await api.authRegenerateApiKey()
      setCfg(c => c ? { ...c, apiKey: r.apiKey } : c)
      setShowKey(true)
    } catch (e) {
      alert('Regenerate failed: ' + (e instanceof Error ? e.message : 'unknown'))
    } finally {
      setRegenerating(false)
    }
  }

  const rotateSessionSecret = async () => {
    if (!confirm('Rotate the session signing secret? Existing logins keep working during a rotation window via the previous secret; a second rotation closes that window.')) return
    setRotatingSecret(true)
    try {
      await api.authRotateSessionSecret()
      alert('Session secret rotated. New logins sign with the new secret; existing sessions remain valid until the next rotation.')
    } catch (e) {
      alert('Rotate failed: ' + (e instanceof Error ? e.message : 'unknown'))
    } finally {
      setRotatingSecret(false)
    }
  }

  const setMode = async (mode: AuthStatus['mode']) => {
    setSavingMode(true)
    try {
      await api.authSetMode(mode)
      await refresh()
      loadCfg()
    } catch (e) {
      alert('Mode change failed: ' + (e instanceof Error ? e.message : 'unknown'))
    } finally {
      setSavingMode(false)
    }
  }

  const copyKey = async () => {
    if (!cfg?.apiKey) return
    await apiKeyClipboard.copy(cfg.apiKey)
  }

  if (!cfg) return null

  return (
    <section>
      <h3 className="text-base font-semibold mb-3 text-slate-800 dark:text-zinc-200">Security</h3>
      <div className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900 space-y-5">
        {isAdmin && (<>
        <div>
          <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">Authentication Mode</label>
          <select
            value={cfg.mode}
            onChange={e => setMode(e.target.value as AuthStatus['mode'])}
            disabled={savingMode}
            className="w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600"
          >
            <option value="enabled">Enabled</option>
            <option value="local-only">Local only (bypass for private IPs)</option>
            <option value="disabled">Disabled (no auth)</option>
          </select>
          {cfg.mode === 'enabled' && <p className="text-xs text-slate-500 dark:text-zinc-500 mt-1">Always requires login, regardless of network origin.</p>}
          {cfg.mode === 'local-only' && <p className="text-xs text-slate-500 dark:text-zinc-500 mt-1">Login is bypassed for requests from private / LAN IP ranges. Still requires login from the internet.</p>}
          {cfg.mode === 'disabled' && <p className="text-xs text-amber-600 dark:text-amber-400 mt-1">No authentication — only safe when Bindery is behind a trusted reverse proxy that enforces access control.</p>}
        </div>

        <div className="border-t border-slate-200 dark:border-zinc-800 pt-4">
          <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">API Key</label>
          <p className="text-xs text-slate-600 dark:text-zinc-500 mb-2">
            Pass as <code className="font-mono">X-Api-Key</code> header or <code className="font-mono">?apikey=</code> query parameter. Used by external integrations (Tautulli, custom scripts, etc.).
          </p>
          <div className="flex flex-wrap gap-2">
            <code className="flex-1 min-w-[12rem] bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm font-mono text-slate-700 dark:text-zinc-300 truncate">
              {showKey ? cfg.apiKey : '••••••••••••••••••••••••••••••••'}
            </code>
            <button onClick={() => setShowKey(s => !s)} className="px-3 py-2 bg-slate-200 dark:bg-zinc-800 hover:bg-slate-300 dark:hover:bg-zinc-700 rounded text-xs font-medium">
              {showKey ? 'Hide' : 'Show'}
            </button>
            <button onClick={copyKey} className="px-3 py-2 bg-slate-200 dark:bg-zinc-800 hover:bg-slate-300 dark:hover:bg-zinc-700 rounded text-xs font-medium">
              {apiKeyClipboard.status === 'copied' ? t('common.copied', 'Copied') : t('common.copy', 'Copy')}
            </button>
            <button onClick={regenerate} disabled={regenerating} className="px-3 py-2 bg-amber-600 hover:bg-amber-500 rounded text-xs font-medium disabled:opacity-50">
              {regenerating ? '...' : 'Regenerate'}
            </button>
          </div>
          {apiKeyClipboard.status === 'manual' && (
            <ClipboardManualFallback text={apiKeyClipboard.manualText} className="mt-2" />
          )}
          {!showKey && (
            <p className="text-[11px] text-slate-500 dark:text-zinc-500 mt-1">
              Regenerating invalidates the current key — update all external integrations afterward.
            </p>
          )}
        </div>

        <div className="border-t border-slate-200 dark:border-zinc-800 pt-4">
          <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">Session Secret</label>
          <p className="text-xs text-slate-600 dark:text-zinc-500 mb-2">
            Signs session cookies. Rotating it generates a new secret while keeping the previous one valid for a rotation window, so existing logins are not dropped immediately. Rotate twice to fully invalidate sessions signed with the old secret.
          </p>
          <button onClick={rotateSessionSecret} disabled={rotatingSecret} className="px-3 py-2 bg-amber-600 hover:bg-amber-500 rounded text-xs font-medium disabled:opacity-50">
            {rotatingSecret ? '...' : 'Rotate session secret'}
          </button>
        </div>
        </>)}

        {status?.authenticated && (
          <div className={isAdmin ? 'border-t border-slate-200 dark:border-zinc-800 pt-4' : ''}>
            <ChangePasswordForm username={cfg.username} />
          </div>
        )}

        {isAdmin && (
          <div className="border-t border-slate-200 dark:border-zinc-800 pt-4">
            <AuthSettings />
          </div>
        )}
      </div>
    </section>
  )
}

function ChangePasswordForm({ username }: { username: string }) {
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [confirmPw, setConfirmPw] = useState('')
  const [error, setError] = useState('')
  const [success, setSuccess] = useState(false)
  const [submitting, setSubmitting] = useState(false)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setError('')
    setSuccess(false)
    if (next !== confirmPw) { setError('New passwords do not match'); return }
    if (next.length < 8) { setError('Password must be at least 8 characters'); return }
    setSubmitting(true)
    try {
      await api.authChangePassword(current, next)
      setCurrent(''); setNext(''); setConfirmPw('')
      setSuccess(true)
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : 'Change failed')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <form onSubmit={submit} className="space-y-2">
      <label className="block text-xs text-slate-600 dark:text-zinc-400">Change password for <strong>{username}</strong></label>
      <input type="password" autoComplete="current-password" placeholder="Current password" value={current} onChange={e => setCurrent(e.target.value)} className={inputCls} />
      <input type="password" autoComplete="new-password" placeholder="New password" value={next} onChange={e => setNext(e.target.value)} className={inputCls} />
      <input type="password" autoComplete="new-password" placeholder="Confirm new password" value={confirmPw} onChange={e => setConfirmPw(e.target.value)} className={inputCls} />
      {error && <div className="text-xs text-red-600 dark:text-red-400">{error}</div>}
      {success && <div className="text-xs text-emerald-600 dark:text-emerald-400">Password updated</div>}
      <button type="submit" disabled={submitting || !current || !next} className="px-3 py-2 bg-emerald-600 hover:bg-emerald-500 rounded text-xs font-medium disabled:opacity-50">
        {submitting ? 'Updating…' : 'Change password'}
      </button>
    </form>
  )
}
