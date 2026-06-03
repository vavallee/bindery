import { FormEvent, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, BINDERY_BASE, AuthConfig, AuthStatus, AuthorMonitorMode, HardcoverTestResult, RootFolder, SystemStatus } from '../../api/client'
import AuthSettings from '../../settings/AuthSettings'
import ThemeToggle from '../../components/ThemeToggle'
import LanguageSwitcher from '../../components/LanguageSwitcher'
import ClipboardManualFallback from '../../components/ClipboardManualFallback'
import { useClipboardCopy } from '../../components/useClipboardCopy'
import { useAuth } from '../../auth/AuthContext'
import { inputCls } from './formStyles'
import Toggle from './Toggle'

function formatBackupSize(bytes: number): string {
  if (!bytes || bytes <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.min(units.length - 1, Math.floor(Math.log(bytes) / Math.log(1024)))
  return `${(bytes / Math.pow(1024, i)).toFixed(1)} ${units[i]}`
}

function formatRelativeTime(iso: string): string {
  const t = Date.parse(iso)
  if (isNaN(t)) return ''
  const diffSec = Math.round((Date.now() - t) / 1000)
  const abs = Math.abs(diffSec)
  if (abs < 60) return diffSec >= 0 ? 'just now' : 'in a moment'
  const mins = Math.round(diffSec / 60)
  if (Math.abs(mins) < 60) return mins >= 0 ? `${mins}m ago` : `in ${-mins}m`
  const hrs = Math.round(diffSec / 3600)
  if (Math.abs(hrs) < 24) return hrs >= 0 ? `${hrs}h ago` : `in ${-hrs}h`
  const days = Math.round(diffSec / 86400)
  return days >= 0 ? `${days}d ago` : `in ${-days}d`
}

function isAuthorMonitorMode(value: string): value is AuthorMonitorMode {
  return value === 'all' || value === 'future' || value === 'latest' || value === 'none'
}

export default function GeneralTab() {
  const { t } = useTranslation()
  const { isAdmin } = useAuth()
  const opdsClipboard = useClipboardCopy()
  const [settings, setSettings] = useState<Record<string, string>>({})
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState<string | null>(null)
  const [backups, setBackups] = useState<Array<{ name: string; size: number; modTime: string }>>([])
  const [creatingBackup, setCreatingBackup] = useState(false)
  const [deletingBackup, setDeletingBackup] = useState<string | null>(null)
  const [scanningLibrary, setScanningLibrary] = useState(false)
  const [scanMessage, setScanMessage] = useState<string | null>(null)
  const scanStartedAt = useRef<number>(0)
  const [lastScan, setLastScan] = useState<{
    ran_at: string
    files_found: number
    reconciled: number
    unmatched: number
    tag_read_failed?: number
    unmatched_files?: Array<{ path: string; parsed_title: string; parsed_author: string }>
    library_dir?: string
    audiobook_dir?: string
    scanned_paths?: string[]
    no_files_found?: boolean
  } | null>(null)
  const [storage, setStorage] = useState<{ downloadDir: string; audiobookDownloadDir: string; libraryDir: string; audiobookDir: string } | null>(null)
  const [systemStatus, setSystemStatus] = useState<SystemStatus | null>(null)
  const [hardcoverToken, setHardcoverToken] = useState('')
  const [hardcoverTestResult, setHardcoverTestResult] = useState<(HardcoverTestResult & { testing?: boolean }) | null>(null)
  const [rootFolders, setRootFolders] = useState<RootFolder[]>([])
  const [newDefaultFolderPath, setNewDefaultFolderPath] = useState('')
  const [newDefaultFolderError, setNewDefaultFolderError] = useState('')
  const [addingDefaultFolder, setAddingDefaultFolder] = useState(false)

  useEffect(() => {
    api.listSettings()
      .then(list => {
        const map: Record<string, string> = {}
        list.forEach(s => { map[s.key] = s.value })
        setSettings(map)
      })
      .catch(console.error)
      .finally(() => setLoading(false))
    api.listBackups().then(setBackups).catch(console.error)
    api.libraryScanStatus().then(setLastScan).catch(() => {/* no prior scan — ignore 404 */})
    api.getStorage().then(setStorage).catch(console.error)
    api.status().then(setSystemStatus).catch(console.error)
    api.listRootFolders().then(setRootFolders).catch(console.error)
  }, [])

  const refreshSystemStatus = async () => {
    try {
      setSystemStatus(await api.status())
    } catch (err) {
      console.error(err)
    }
  }

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

  const saveHardcoverToken = async () => {
    setSaving('hardcover.api_token')
    setHardcoverTestResult(null)
    try {
      await api.setSetting('hardcover.api_token', hardcoverToken.trim())
      setHardcoverToken('')
      await refreshSystemStatus()
    } catch (err) {
      console.error(err)
    } finally {
      setSaving(null)
    }
  }

  const clearHardcoverToken = async () => {
    setSaving('hardcover.api_token')
    setHardcoverTestResult(null)
    try {
      await api.setSetting('hardcover.api_token', '')
      setHardcoverToken('')
      await refreshSystemStatus()
    } catch (err) {
      console.error(err)
    } finally {
      setSaving(null)
    }
  }

  const testHardcover = async () => {
    setHardcoverTestResult({
      ok: false,
      tokenConfigured: hardcoverTokenConfigured,
      searchResults: 0,
      catalogOk: false,
      testing: true,
    })
    try {
      const result = await api.testHardcover()
      setHardcoverTestResult(result)
    } catch (err) {
      setHardcoverTestResult({
        ok: false,
        tokenConfigured: hardcoverTokenConfigured,
        searchResults: 0,
        catalogOk: false,
        error: err instanceof Error ? err.message : 'Hardcover API test failed',
      })
    }
  }

  const toggleEnhancedHardcover = async () => {
    const current = (settings['hardcover.enhanced_series_enabled'] ?? 'false').toLowerCase()
    const next = current === 'true' ? 'false' : 'true'
    setSettings(s => ({ ...s, 'hardcover.enhanced_series_enabled': next }))
    setSaving('hardcover.enhanced_series_enabled')
    try {
      await api.setSetting('hardcover.enhanced_series_enabled', next)
      await refreshSystemStatus()
    } catch (err) {
      console.error(err)
    } finally {
      setSaving(null)
    }
  }

  const handleBackup = async () => {
    setCreatingBackup(true)
    try {
      const result = await api.createBackup()
      setBackups(prev => [result, ...prev])
      alert(`Backup created: ${result.name}`)
    } catch (err) {
      alert('Backup failed: ' + (err instanceof Error ? err.message : 'Unknown error'))
    } finally {
      setCreatingBackup(false)
    }
  }

  const handleDeleteBackup = async (filename: string) => {
    if (!confirm(`Delete backup ${filename}?`)) return
    setDeletingBackup(filename)
    try {
      await api.deleteBackup(filename)
      setBackups(prev => prev.filter(b => b.name !== filename))
    } catch (err) {
      alert('Delete failed: ' + (err instanceof Error ? err.message : 'Unknown error'))
    } finally {
      setDeletingBackup(null)
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

  const enhancedHardcoverAdminEnabled = (settings['hardcover.enhanced_series_enabled'] ?? 'false').toLowerCase() === 'true'
  const hardcoverTokenConfigured = systemStatus?.hardcoverTokenConfigured ?? false
  const enhancedHardcoverEnabled = systemStatus?.enhancedHardcoverApi ?? false
  const enhancedHardcoverReason = systemStatus?.enhancedHardcoverDisabledReason
  const enhancedHardcoverStatus = enhancedHardcoverEnabled
    ? t('settings.general.enhancedHardcoverStatusEnabled', 'Enabled')
    : enhancedHardcoverReason === 'env_disabled'
      ? t('settings.general.enhancedHardcoverStatusEnvDisabled', 'Disabled by BINDERY_ENHANCED_HARDCOVER_API=false.')
      : enhancedHardcoverReason === 'missing_token'
        ? t('settings.general.enhancedHardcoverStatusMissingToken', 'Add a Hardcover API token to enable this feature.')
        : t('settings.general.enhancedHardcoverStatusAdminDisabled', 'Turn this on to enable enhanced series features.')

  return (
    <div className="space-y-8">
      {/* Appearance */}
      <section>
        <h3 className="text-base font-semibold mb-3 text-slate-800 dark:text-zinc-200">{t('settings.general.appearance')}</h3>
        <div className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900">
          <div className="flex items-center justify-between">
            <div>
              <label className="block text-sm font-medium text-slate-800 dark:text-zinc-200">{t('settings.general.theme')}</label>
              <p className="text-xs text-slate-600 dark:text-zinc-500 mt-1">{t('settings.general.themeHint')}</p>
            </div>
            <ThemeToggle />
          </div>
          <div className="flex items-center justify-between border-t border-slate-200 dark:border-zinc-800 pt-3 mt-3">
            <div>
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
              Use <strong>Hardlink</strong> or <strong>Copy</strong> to keep the source file intact for torrent seeding.
              Hardlink requires the download folder and library to be on the same filesystem/volume.
              Use <strong>External</strong> if another tool (Calibre, Grimmory, etc.) manages your library — Bindery grabs the download and stops; your tool processes it, then Bindery reconciles on the next library scan.
            </p>
            <div className="flex gap-2 flex-wrap">
              {(['move', 'copy', 'hardlink', 'external'] as const).map(m => (
                <button
                  key={m}
                  onClick={async () => {
                    setSettings(s => ({ ...s, 'import.mode': m }))
                    await api.setSetting('import.mode', m).catch(console.error)
                  }}
                  className={`px-3 py-1.5 rounded text-xs font-medium border transition-colors ${
                    (settings['import.mode'] ?? 'move') === m
                      ? 'bg-emerald-600 border-emerald-600 text-white'
                      : 'border-slate-300 dark:border-zinc-700 text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white'
                  }`}
                >
                  {m.charAt(0).toUpperCase() + m.slice(1)}
                </button>
              ))}
            </div>
          </div>
          <div>
            <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">{t('settings.general.bookTemplate')}</label>
            <div className="flex gap-2">
              <input
                value={settings['naming.bookTemplate'] ?? ''}
                onChange={e => setSettings(s => ({ ...s, 'naming.bookTemplate': e.target.value }))}
                placeholder="{Author}/{Title} ({Year})/{Title} - {Author}.{ext}"
                className="flex-1 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600"
              />
              <button
                onClick={() => saveSetting('naming.bookTemplate')}
                disabled={saving === 'naming.bookTemplate'}
                className="px-3 py-2 bg-emerald-600 hover:bg-emerald-500 rounded text-xs font-medium disabled:opacity-50"
              >
                {saving === 'naming.bookTemplate' ? t('common.saving') : t('common.save')}
              </button>
            </div>
          </div>
          <div>
            <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">{t('settings.general.audiobookTemplate')}</label>
            <p className="text-xs text-slate-600 dark:text-zinc-500 mb-2">{t('settings.general.audiobookTemplateHint')}</p>
            <div className="flex gap-2">
              <input
                value={settings['naming_template_audiobook'] ?? ''}
                onChange={e => setSettings(s => ({ ...s, 'naming_template_audiobook': e.target.value }))}
                placeholder="{Author}/{Title} ({Year})"
                className="flex-1 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600"
              />
              <button
                onClick={() => saveSetting('naming_template_audiobook')}
                disabled={saving === 'naming_template_audiobook'}
                className="px-3 py-2 bg-emerald-600 hover:bg-emerald-500 rounded text-xs font-medium disabled:opacity-50"
              >
                {saving === 'naming_template_audiobook' ? t('common.saving') : t('common.save')}
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
                onClick={() => saveSetting('search.preferredLanguage')}
                disabled={saving === 'search.preferredLanguage'}
                className="px-3 py-2 bg-emerald-600 hover:bg-emerald-500 rounded text-xs font-medium disabled:opacity-50"
              >
                {saving === 'search.preferredLanguage' ? t('common.saving') : t('common.save')}
              </button>
            </div>
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
              {(lastScan.no_files_found ?? lastScan.files_found === 0) && (
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

      {/* Default library location */}
      <section>
        <h3 className="text-base font-semibold mb-3 text-slate-800 dark:text-zinc-200">Default library location</h3>
        <div className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900 space-y-3">
          <div>
            <label className="block text-sm font-medium text-slate-800 dark:text-zinc-200 mb-1">Default root folder</label>
            <p className="text-xs text-slate-600 dark:text-zinc-500 mb-2">
              When an author has no per-author root folder set, Bindery uses this location. Leave unset to fall back to <code className="font-mono bg-slate-200 dark:bg-zinc-800 px-1 rounded">BINDERY_LIBRARY_DIR</code>.
            </p>
            <select
              value={settings['library.defaultRootFolderId'] ?? ''}
              onChange={async e => {
                const next = e.target.value
                setSettings(s => ({ ...s, 'library.defaultRootFolderId': next }))
                await api.setSetting('library.defaultRootFolderId', next).catch(console.error)
              }}
              className="w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded-md px-3 py-2 text-sm focus:outline-none focus:border-emerald-500"
            >
              <option value="">— Unset (use BINDERY_LIBRARY_DIR) —</option>
              {rootFolders.map(rf => (
                <option key={rf.id} value={String(rf.id)}>{rf.path}</option>
              ))}
            </select>
          </div>
          {!addingDefaultFolder ? (
            <button
              onClick={() => { setAddingDefaultFolder(true); setNewDefaultFolderError('') }}
              className="text-xs text-emerald-600 dark:text-emerald-400 hover:underline"
            >
              + Add root folder
            </button>
          ) : (
            <div className="space-y-2">
              <label className="block text-xs text-slate-600 dark:text-zinc-400">New root folder path</label>
              <div className="flex gap-2">
                <input
                  value={newDefaultFolderPath}
                  onChange={e => setNewDefaultFolderPath(e.target.value)}
                  placeholder="/mnt/books"
                  className="flex-1 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-emerald-500 font-mono"
                />
                <button
                  onClick={async () => {
                    if (!newDefaultFolderPath.trim()) return
                    setNewDefaultFolderError('')
                    try {
                      const created = await api.addRootFolder(newDefaultFolderPath.trim())
                      setRootFolders(prev => [...prev, created])
                      setSettings(s => ({ ...s, 'library.defaultRootFolderId': String(created.id) }))
                      await api.setSetting('library.defaultRootFolderId', String(created.id)).catch(console.error)
                      setNewDefaultFolderPath('')
                      setAddingDefaultFolder(false)
                    } catch (err) {
                      setNewDefaultFolderError(err instanceof Error ? err.message : 'Failed to add folder')
                    }
                  }}
                  className="px-3 py-2 bg-emerald-600 hover:bg-emerald-500 rounded text-xs font-medium"
                >
                  Add
                </button>
                <button
                  onClick={() => { setAddingDefaultFolder(false); setNewDefaultFolderPath(''); setNewDefaultFolderError('') }}
                  className="px-3 py-2 bg-slate-300 dark:bg-zinc-700 hover:bg-slate-400 dark:hover:bg-zinc-600 rounded text-xs font-medium"
                >
                  Cancel
                </button>
              </div>
              {newDefaultFolderError && <p className="text-xs text-red-500">{newDefaultFolderError}</p>}
            </div>
          )}
        </div>
      </section>

      {/* Metadata provider */}
      <section>
        <h3 className="text-base font-semibold mb-3 text-slate-800 dark:text-zinc-200">{t('settings.general.metadataProvider', 'Metadata Provider')}</h3>
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
      </section>

      {/* Author defaults */}
      <section>
        <h3 className="text-base font-semibold mb-3 text-slate-800 dark:text-zinc-200">{t('settings.general.authorDefaults', 'Author defaults')}</h3>
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
      </section>

      {/* Auto-grab */}
      <section>
        <h3 className="text-base font-semibold mb-3 text-slate-800 dark:text-zinc-200">{t('settings.general.autoGrab')}</h3>
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
      </section>

      {/* Recommendations */}
      <section>
        <h3 className="text-base font-semibold mb-3 text-slate-800 dark:text-zinc-200">{t('settings.general.recommendations')}</h3>
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
      </section>

      {/* OIDC providers */}
      <AuthSettings />

      {/* API Keys */}
      <section>
        <h3 className="text-base font-semibold mb-3 text-slate-800 dark:text-zinc-200">{t('settings.general.apiKeys')}</h3>
        <div className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900 space-y-4">
          <div>
            <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">{t('settings.general.googleBooksKey')}</label>
            <div className="flex gap-2">
              <input
                value={settings['googlebooks.apiKey'] ?? ''}
                onChange={e => setSettings(s => ({ ...s, 'googlebooks.apiKey': e.target.value }))}
                placeholder="AIza..."
                type="password"
                className="flex-1 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600"
              />
              <button
                onClick={() => saveSetting('googlebooks.apiKey')}
                disabled={saving === 'googlebooks.apiKey'}
                className="px-3 py-2 bg-emerald-600 hover:bg-emerald-500 rounded text-xs font-medium disabled:opacity-50"
              >
                {saving === 'googlebooks.apiKey' ? t('common.saving') : t('common.save')}
              </button>
            </div>
          </div>

          <div className="border-t border-slate-200 dark:border-zinc-800 pt-4 space-y-3">
            <div>
              <div className="flex items-center justify-between gap-3 mb-1">
                <label className="block text-xs text-slate-600 dark:text-zinc-400">
                  {t('settings.general.hardcoverApiToken', 'Hardcover API Token')}
                </label>
                <span className={`text-[11px] font-medium ${hardcoverTokenConfigured ? 'text-emerald-600 dark:text-emerald-400' : 'text-slate-500 dark:text-zinc-500'}`}>
                  {hardcoverTokenConfigured
                    ? t('settings.general.hardcoverTokenConfigured', 'Token configured')
                    : t('settings.general.hardcoverTokenNotConfigured', 'No token configured')}
                </span>
              </div>
              <div className="flex gap-2">
                <input
                  value={hardcoverToken}
                  onChange={e => setHardcoverToken(e.target.value)}
                  placeholder={hardcoverTokenConfigured
                    ? t('settings.general.hardcoverApiTokenConfiguredPlaceholder', 'Saved token is hidden. Enter a new token to rotate it.')
                    : t('settings.general.hardcoverApiTokenPlaceholder', 'Paste a Hardcover API token')}
                  type="password"
                  className="flex-1 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600"
                />
                <button
                  onClick={saveHardcoverToken}
                  disabled={saving === 'hardcover.api_token' || !hardcoverToken.trim()}
                  className="px-3 py-2 bg-emerald-600 hover:bg-emerald-500 rounded text-xs font-medium disabled:opacity-50"
                  aria-label={t('settings.general.hardcoverSaveToken', 'Save Hardcover API token')}
                >
                  {saving === 'hardcover.api_token' ? t('common.saving') : t('common.save')}
                </button>
                {hardcoverTokenConfigured && (
                  <button
                    onClick={clearHardcoverToken}
                    disabled={saving === 'hardcover.api_token'}
                    className="px-3 py-2 bg-slate-300 dark:bg-zinc-700 hover:bg-slate-400 dark:hover:bg-zinc-600 rounded text-xs font-medium disabled:opacity-50"
                    aria-label={t('settings.general.hardcoverClearToken', 'Clear Hardcover API token')}
                  >
                    {t('settings.general.hardcoverClearToken', 'Clear')}
                  </button>
                )}
                <button
                  onClick={testHardcover}
                  disabled={!hardcoverTokenConfigured || hardcoverTestResult?.testing || saving === 'hardcover.api_token'}
                  className="px-3 py-2 bg-slate-300 dark:bg-zinc-700 hover:bg-slate-400 dark:hover:bg-zinc-600 rounded text-xs font-medium disabled:opacity-50"
                  aria-label={t('settings.general.hardcoverTestApi', 'Test Hardcover API')}
                >
                  {hardcoverTestResult?.testing ? t('common.testing', 'Testing...') : t('common.test', 'Test')}
                </button>
              </div>
              <a
                href="https://hardcover.app/account/api"
                target="_blank"
                rel="noopener noreferrer"
                className="inline-block mt-1 text-xs text-emerald-600 dark:text-emerald-400 hover:underline"
              >
                {t('settings.general.hardcoverApiTokenLink', 'Create or copy a Hardcover API token')}
              </a>
              {hardcoverTestResult && !hardcoverTestResult.testing && (
                <p className={`mt-1 text-xs ${hardcoverTestResult.ok ? 'text-emerald-600 dark:text-emerald-400' : 'text-rose-600 dark:text-rose-400'}`}>
                  {hardcoverTestResult.ok
                    ? (hardcoverTestResult.message || t('settings.general.hardcoverTestOk', 'Hardcover API connected.'))
                    : (hardcoverTestResult.error || t('settings.general.hardcoverTestFailed', 'Hardcover API test failed.'))}
                </p>
              )}
            </div>

            <div className="flex items-center justify-between gap-4">
              <div className="min-w-0">
                <label className="block text-sm font-medium text-slate-800 dark:text-zinc-200">
                  {t('settings.general.enhancedHardcoverSeries', 'Enhanced Hardcover series')}
                </label>
                <p className="text-xs text-slate-600 dark:text-zinc-500 mt-0.5">
                  {t('settings.general.enhancedHardcoverSeriesHint', 'Use Hardcover-backed series matching, catalog diffs, and missing-book fill.')}
                </p>
                <p className={`text-xs mt-1 ${enhancedHardcoverEnabled ? 'text-emerald-600 dark:text-emerald-400' : 'text-slate-600 dark:text-zinc-500'}`}>
                  {enhancedHardcoverStatus}
                </p>
              </div>
              <Toggle
                checked={enhancedHardcoverAdminEnabled}
                onChange={toggleEnhancedHardcover}
                disabled={saving === 'hardcover.enhanced_series_enabled'}
                title={enhancedHardcoverAdminEnabled ? t('common.disable') : t('common.enable')}
              />
            </div>
          </div>
        </div>
      </section>

      {/* OPDS */}
      <section>
        <h3 className="text-base font-semibold mb-3 text-slate-800 dark:text-zinc-200">{t('settings.general.opds')}</h3>
        <div className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900">
          <div>
            <span className="block text-xs font-medium text-slate-600 dark:text-zinc-400 mb-1">{t('settings.general.opdsFeedUrl')}</span>
            <div className="flex items-center gap-2">
              <code className="flex-1 text-xs bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 px-2 py-1.5 rounded font-mono break-all">
                {window.location.origin}{BINDERY_BASE}/opds
              </code>
              <button
                onClick={() => opdsClipboard.copy(window.location.origin + BINDERY_BASE + '/opds')}
                className="px-3 py-1.5 bg-slate-600 hover:bg-slate-500 rounded text-xs font-medium flex-shrink-0"
              >
                {opdsClipboard.status === 'copied' ? t('common.copied', 'Copied') : t('settings.general.copy')}
              </button>
            </div>
            {opdsClipboard.status === 'manual' && (
              <ClipboardManualFallback text={opdsClipboard.manualText} className="mt-2" />
            )}
            <p className="text-xs text-slate-500 dark:text-zinc-500 mt-1.5">{t('settings.general.opdsHint')}</p>
          </div>
        </div>
      </section>

      {/* Log retention */}
      <section>
        <h3 className="text-base font-semibold mb-3 text-slate-800 dark:text-zinc-200">{t('settings.general.logRetention')}</h3>
        <div className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900">
          <div className="flex items-center justify-between gap-4">
            <div>
              <label className="block text-sm font-medium text-slate-800 dark:text-zinc-200">{t('settings.general.logRetentionLabel')}</label>
              <p className="text-xs text-slate-600 dark:text-zinc-500 mt-0.5">{t('settings.general.logRetentionHint')}</p>
            </div>
            <div className="flex items-center gap-2 flex-shrink-0">
              <input
                type="number"
                min={1}
                max={365}
                value={settings['log.retention_days'] ?? '14'}
                onChange={e => setSettings(s => ({ ...s, 'log.retention_days': e.target.value }))}
                className="w-20 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-2 py-1 text-sm text-right"
              />
              <span className="text-sm text-slate-600 dark:text-zinc-400">{t('settings.general.days')}</span>
              <button
                onClick={() => saveSetting('log.retention_days')}
                disabled={saving === 'log.retention_days'}
                className="px-3 py-1.5 bg-emerald-600 hover:bg-emerald-500 rounded text-sm font-medium disabled:opacity-50"
              >
                {saving === 'log.retention_days' ? t('common.saving') : t('common.save')}
              </button>
            </div>
          </div>
        </div>
      </section>

      {/* Backup */}
      <section>
        <h3 className="text-base font-semibold mb-3 text-slate-800 dark:text-zinc-200">{t('settings.general.backup')}</h3>
        <div className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900 space-y-3">
          <div className="flex items-center justify-between">
            <div>
              <p className="text-sm text-slate-700 dark:text-zinc-300">{t('settings.general.backupCreate')}</p>
              <p className="text-xs text-slate-600 dark:text-zinc-500 mt-0.5">{t('settings.general.backupHint')}</p>
            </div>
            <button
              onClick={handleBackup}
              disabled={creatingBackup}
              className="px-4 py-2 bg-emerald-600 hover:bg-emerald-500 rounded text-sm font-medium disabled:opacity-50 flex-shrink-0"
            >
              {creatingBackup ? t('settings.general.backupCreating') : t('settings.general.backupButton')}
            </button>
          </div>
          {backups.length > 0 && (
            <div className="mt-3 border-t border-slate-200 dark:border-zinc-800 pt-3">
              <p className="text-xs text-slate-600 dark:text-zinc-500 mb-2">{t('settings.general.existingBackups')}</p>
              <ul className="space-y-1">
                {backups.map(b => (
                  <li key={b.name} className="flex items-center justify-between text-xs text-slate-600 dark:text-zinc-400">
                    <span>
                      <span className="font-mono">{b.name}</span>
                      <span className="ml-2 text-slate-500 dark:text-zinc-500">{formatBackupSize(b.size)} · {formatRelativeTime(b.modTime)}</span>
                    </span>
                    <button
                      onClick={() => handleDeleteBackup(b.name)}
                      disabled={deletingBackup === b.name}
                      className="ml-4 text-red-600 dark:text-red-400 hover:underline disabled:opacity-50"
                    >
                      {deletingBackup === b.name ? '…' : t('common.delete')}
                    </button>
                  </li>
                ))}
              </ul>
            </div>
          )}
        </div>
      </section>

      <section>
        <h3 className="text-base font-semibold mb-3 text-slate-800 dark:text-zinc-200">{t('settings.general.telemetry')}</h3>
        <div className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900 space-y-3">
          <div className="flex items-start justify-between gap-4">
            <div>
              <label className="block text-sm font-medium text-slate-800 dark:text-zinc-200">{t('settings.general.telemetryLabel')}</label>
              <p className="text-xs text-slate-600 dark:text-zinc-500 mt-0.5">{t('settings.general.telemetryHint')}</p>
            </div>
            <Toggle
              checked={(settings['telemetry.enabled'] ?? 'true') !== 'false'}
              onChange={async () => {
                const current = (settings['telemetry.enabled'] ?? 'true').toLowerCase()
                const next = current !== 'false' ? 'false' : 'true'
                setSettings(s => ({ ...s, 'telemetry.enabled': next }))
                await api.setSetting('telemetry.enabled', next).catch(console.error)
              }}
              title={(settings['telemetry.enabled'] ?? 'true') !== 'false' ? t('common.disable') : t('common.enable')}
            />
          </div>
          <p className="text-xs text-slate-500 dark:text-zinc-600">
            {t('settings.general.telemetryDetail')}
          </p>
        </div>
      </section>
      </>)}
    </div>
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
          <p className="text-xs text-slate-600 dark:text-zinc-500 mb-2">
            <strong>Enabled</strong>: always require login. <strong>Local only</strong>: skip login for requests from private IPs (home network). <strong>Disabled</strong>: no authentication — only safe behind a trusted reverse proxy.
          </p>
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
        </div>

        <div className="border-t border-slate-200 dark:border-zinc-800 pt-4">
          <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">API Key</label>
          <p className="text-xs text-slate-600 dark:text-zinc-500 mb-2">
            Pass as <code className="font-mono">X-Api-Key</code> header or <code className="font-mono">?apikey=</code> query parameter. Used by external integrations (Tautulli, custom scripts, etc.).
          </p>
          <div className="flex gap-2">
            <code className="flex-1 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm font-mono text-slate-700 dark:text-zinc-300 truncate">
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
