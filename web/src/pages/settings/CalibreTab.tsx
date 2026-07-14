import { useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import {
  api,
  CalibreImportProgress,
  CalibreImportRun,
  CalibreRollbackResult,
  CalibreSyncProgress,
} from '../../api/client'
import Toggle from './Toggle'
import { useSaveResult } from './useSaveResult'

export default function CalibreTab() {
  const [settings, setSettings] = useState<Record<string, string>>({})
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState<string | null>(null)

  useEffect(() => {
    api.listSettings()
      .then(list => {
        const map: Record<string, string> = {}
        list.forEach(s => { map[s.key] = s.value })
        setSettings(map)
      })
      .catch(console.error)
      .finally(() => setLoading(false))
  }, [])

  const saveSetting = async (key: string): Promise<string | null> => {
    setSaving(key)
    try {
      await api.setSetting(key, settings[key] ?? '')
      return null
    } catch (err) {
      return err instanceof Error ? err.message : 'Save failed'
    } finally {
      setSaving(null)
    }
  }

  if (loading) return <div className="text-slate-600 dark:text-zinc-500">Loading…</div>

  return (
    <div className="space-y-8">
      <CalibreSection settings={settings} setSettings={setSettings} saveSetting={saveSetting} saving={saving} />
    </div>
  )
}

// CalibreSection renders the calibre.* settings fields plus a Test button
// that hits /calibre/test.
function CalibreSection({
  settings,
  setSettings,
  saveSetting,
  saving,
}: {
  settings: Record<string, string>
  setSettings: (fn: (prev: Record<string, string>) => Record<string, string>) => void
  saveSetting: (key: string) => Promise<string | null>
  saving: string | null
}) {
  const [testing, setTesting] = useState(false)
  const [testResult, setTestResult] = useState<{ ok: boolean; msg: string } | null>(null)
  const [saveError, setSaveError] = useState<{ key: string; msg: string } | null>(null)
  const [libraryPathSaveResult, libraryPathSave] = useSaveResult()
  const [binaryPathSaveResult, binaryPathSave] = useSaveResult()
  const [pluginUrlSaveResult, pluginUrlSave] = useSaveResult()
  const [pushRemapSaveResult, pushRemapSave] = useSaveResult()
  const [pluginKeySaveResult, pluginKeySave] = useSaveResult()
  const [cwaPathSaveResult, cwaPathSave] = useSaveResult()
  const [importProgress, setImportProgress] = useState<CalibreImportProgress | null>(null)
  const [importError, setImportError] = useState<string | null>(null)
  const [syncProgress, setSyncProgress] = useState<CalibreSyncProgress | null>(null)
  const [syncError, setSyncError] = useState<string | null>(null)
  const [syncModalOpen, setSyncModalOpen] = useState(false)
  const [bridgeReachable, setBridgeReachable] = useState<boolean | null>(null)
  // Recent imports + rollback (issue #643).
  const [runs, setRuns] = useState<CalibreImportRun[]>([])
  const [rollbackRun, setRollbackRun] = useState<CalibreImportRun | null>(null)

  const saveSettingWithErrorThrowing = async (key: string) => {
    setSaveError(null)
    setTestResult(null)
    const err = await saveSetting(key)
    if (err) {
      setSaveError({ key, msg: err })
      throw new Error(err)
    }
  }

  // Legacy fallback: a pre-migration DB with `calibre.enabled=true` but no
  // mode set should still render as 'calibredb' so the user's existing
  // setup is visible in the UI.
  const rawMode = settings['calibre.mode'] ?? ''
  const legacyEnabled = (settings['calibre.enabled'] ?? 'false').toLowerCase() === 'true'
  const mode: 'off' | 'calibredb' | 'plugin' =
    rawMode === 'calibredb' || rawMode === 'off' || rawMode === 'plugin'
      ? rawMode
      : legacyEnabled
      ? 'calibredb'
      : 'off'
  const libraryImportEnabled = (settings['calibre.library_import_enabled'] ?? 'false').toLowerCase() === 'true'
  const syncOnStartup = (settings['calibre.sync_on_startup'] ?? 'false').toLowerCase() === 'true'
  const lastImportAt = settings['calibre.last_import_at'] ?? ''

  // Hydrate progress on mount so navigating back mid-import still shows
  // the live bar instead of a dead "Import library" button.
  const refreshRuns = useCallback(() => {
    api.calibreRuns(10).then(setRuns).catch(() => {})
  }, [])

  useEffect(() => {
    api.calibreImportStatus().then(setImportProgress).catch(() => {})
    api.calibreSyncStatus().then(p => {
      setSyncProgress(p)
      // If a sync is already running when the tab mounts, surface the
      // modal so the user can watch it finish instead of wondering what
      // the disabled button is doing.
      if (p.running) setSyncModalOpen(true)
    }).catch(() => {})
    refreshRuns()
  }, [refreshRuns])

  // Refresh runs after an import finishes so the new run shows up without
  // requiring a manual click.
  useEffect(() => {
    if (!importProgress) return
    if (importProgress.running) return
    refreshRuns()
  }, [importProgress, refreshRuns])

  // Silently probe plugin reachability so the "Push all to Calibre"
  // button can enable/disable without the user having to click Test
  // first. Re-probes whenever mode, url, or api key changes.
  const pluginURL = settings['calibre.plugin_url'] ?? ''
  const pluginKey = settings['calibre.plugin_api_key'] ?? ''
  useEffect(() => {
    if (mode !== 'plugin' || !pluginURL) {
      setBridgeReachable(null)
      return
    }
    let cancelled = false
    api.testCalibre()
      .then(() => { if (!cancelled) setBridgeReachable(true) })
      .catch(() => { if (!cancelled) setBridgeReachable(false) })
    return () => { cancelled = true }
  }, [mode, pluginURL, pluginKey])

  // Poll while an import is running.
  useEffect(() => {
    if (!importProgress?.running) return
    const id = setInterval(() => {
      api.calibreImportStatus().then(setImportProgress).catch(() => {})
    }, 1000)
    return () => clearInterval(id)
  }, [importProgress?.running])

  // Poll while a bulk sync is running. 2s matches the task spec.
  useEffect(() => {
    if (!syncProgress?.running) return
    const id = setInterval(() => {
      api.calibreSyncStatus().then(setSyncProgress).catch(() => {})
    }, 2000)
    return () => clearInterval(id)
  }, [syncProgress?.running])

  const startImport = async () => {
    setImportError(null)
    try {
      const p = await api.calibreImportStart()
      setImportProgress(p)
    } catch (err) {
      setImportError(err instanceof Error ? err.message : 'Import failed to start')
    }
  }

  const startSync = async () => {
    setSyncError(null)
    setSyncModalOpen(true)
    try {
      const p = await api.calibreSyncStart()
      setSyncProgress(p)
    } catch (err) {
      setSyncError(err instanceof Error ? err.message : 'Push failed to start')
    }
  }

  const runTest = async () => {
    setTesting(true)
    setTestResult(null)
    const isPlugin = mode === 'plugin'
    try {
      const r = await api.testCalibre()
      const prefix = isPlugin ? '✓ Plugin reachable' : '✓ calibredb reachable'
      const detail = r.version || r.message
      setTestResult({ ok: true, msg: detail ? `${prefix} — ${detail}` : prefix })
      // Mirror into bridgeReachable so the Push-all button flips to enabled
      // on a successful manual test, without waiting for the silent probe
      // to re-fire (which only triggers on mode/url/key *changes*).
      if (isPlugin) setBridgeReachable(true)
    } catch (err) {
      const reason = err instanceof Error ? err.message : 'Test failed'
      const prefix = isPlugin ? '✗ Could not reach plugin' : '✗ calibredb unreachable'
      setTestResult({ ok: false, msg: `${prefix} — ${reason}` })
      if (isPlugin) setBridgeReachable(false)
    } finally {
      setTesting(false)
    }
  }

  const setMode = async (next: 'off' | 'calibredb' | 'plugin') => {
    setSettings(s => ({ ...s, 'calibre.mode': next }))
    await api.setSetting('calibre.mode', next).catch(console.error)
  }

  return (
    <section>
      <h3 className="text-base font-semibold mb-3 text-slate-800 dark:text-zinc-200">Calibre</h3>
      <div className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900 space-y-4">

        {/* Shared library path — used by both write integration and library import */}
        <div>
          <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">Library path</label>
          <p className="text-xs text-slate-600 dark:text-zinc-500 mb-2">
            Directory containing <code className="text-[11px] bg-slate-200 dark:bg-zinc-800 px-1 rounded">metadata.db</code>.
            Used by both the write integration and library import.
          </p>
          <div className="flex gap-2">
            <input
              value={settings['calibre.library_path'] ?? ''}
              onChange={e => setSettings(s => ({ ...s, 'calibre.library_path': e.target.value }))}
              placeholder="/data/calibre-library"
              className="flex-1 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600"
            />
            <button
              onClick={() => libraryPathSave(() => saveSettingWithErrorThrowing('calibre.library_path'))}
              disabled={saving === 'calibre.library_path'}
              className={`px-3 py-2 rounded text-xs font-medium disabled:opacity-50 ${libraryPathSaveResult === 'saved' ? 'bg-emerald-500' : libraryPathSaveResult === 'error' ? 'bg-red-600' : 'bg-emerald-600 hover:bg-emerald-500'}`}
            >
              {libraryPathSaveResult === 'saved' ? 'Saved ✓' : libraryPathSaveResult === 'error' ? 'Error' : saving === 'calibre.library_path' ? 'Saving...' : 'Save'}
            </button>
          </div>
          {saveError?.key === 'calibre.library_path' && (
            <p className="text-xs text-red-600 dark:text-red-400 mt-1">{saveError.msg}</p>
          )}
        </div>

        <div className="pt-1 border-t border-slate-200 dark:border-zinc-800">
          <label className="block text-sm font-medium text-slate-800 dark:text-zinc-200 mb-2 mt-3">Write integration</label>
          <div className="space-y-1.5">
            {([
              { v: 'off',       label: 'Off',           desc: 'No Calibre call on import.' },
              { v: 'calibredb', label: 'calibredb CLI', desc: 'Shell out to calibredb add --with-library. Requires calibredb reachable from the Bindery process.' },
              { v: 'plugin',    label: 'Calibre Bridge plugin', desc: 'POST imported files to the Bindery Bridge plugin running inside Calibre. Use when Calibre runs in a separate container/pod.' },
            ] as const).map(opt => (
              <label key={opt.v} className="flex items-start gap-2 cursor-pointer">
                <input
                  type="radio"
                  name="calibre-mode"
                  value={opt.v}
                  checked={mode === opt.v}
                  onChange={() => setMode(opt.v)}
                  className="mt-1"
                />
                <div>
                  <div className="text-sm text-slate-800 dark:text-zinc-200">{opt.label}</div>
                  <div className="text-xs text-slate-600 dark:text-zinc-500">{opt.desc}</div>
                </div>
              </label>
            ))}
          </div>
        </div>

        {mode === 'calibredb' && (
          <div>
            <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">Binary path (optional)</label>
            <p className="text-xs text-slate-600 dark:text-zinc-500 mb-2">Leave blank to resolve <code className="text-[11px] bg-slate-200 dark:bg-zinc-800 px-1 rounded">calibredb</code> on PATH. Set explicitly when running in a container that bundles Calibre at a pinned location.</p>
            <div className="flex gap-2">
              <input
                value={settings['calibre.binary_path'] ?? ''}
                onChange={e => setSettings(s => ({ ...s, 'calibre.binary_path': e.target.value }))}
                placeholder="/usr/bin/calibredb"
                className="flex-1 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600"
              />
              <button
                onClick={() => binaryPathSave(() => saveSettingWithErrorThrowing('calibre.binary_path'))}
                disabled={saving === 'calibre.binary_path'}
                className={`px-3 py-2 rounded text-xs font-medium disabled:opacity-50 ${binaryPathSaveResult === 'saved' ? 'bg-emerald-500' : binaryPathSaveResult === 'error' ? 'bg-red-600' : 'bg-emerald-600 hover:bg-emerald-500'}`}
              >
                {binaryPathSaveResult === 'saved' ? 'Saved ✓' : binaryPathSaveResult === 'error' ? 'Error' : saving === 'calibre.binary_path' ? 'Saving...' : 'Save'}
              </button>
            </div>
            {saveError?.key === 'calibre.binary_path' && (
              <p className="text-xs text-red-600 dark:text-red-400 mt-1">{saveError.msg}</p>
            )}
          </div>
        )}

        {mode === 'plugin' && (
          <div>
            <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">Plugin URL</label>
            <p className="text-xs text-slate-600 dark:text-zinc-500 mb-2">
              Base URL of the Bindery Bridge plugin&rsquo;s HTTP server running inside Calibre.
            </p>
            <div className="flex gap-2">
              <input
                value={settings['calibre.plugin_url'] ?? ''}
                onChange={e => setSettings(s => ({ ...s, 'calibre.plugin_url': e.target.value }))}
                placeholder="http://calibre.default.svc:8099"
                className="flex-1 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600"
              />
              <button
                onClick={() => pluginUrlSave(() => saveSettingWithErrorThrowing('calibre.plugin_url'))}
                disabled={saving === 'calibre.plugin_url'}
                className={`px-3 py-2 rounded text-xs font-medium disabled:opacity-50 ${pluginUrlSaveResult === 'saved' ? 'bg-emerald-500' : pluginUrlSaveResult === 'error' ? 'bg-red-600' : 'bg-emerald-600 hover:bg-emerald-500'}`}
              >
                {pluginUrlSaveResult === 'saved' ? 'Saved ✓' : pluginUrlSaveResult === 'error' ? 'Error' : saving === 'calibre.plugin_url' ? 'Saving...' : 'Save'}
              </button>
            </div>
            {saveError?.key === 'calibre.plugin_url' && (
              <p className="text-xs text-red-600 dark:text-red-400 mt-1">{saveError.msg}</p>
            )}
          </div>
        )}

        {mode === 'plugin' && (
          <div>
            <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">API key</label>
            <p className="text-xs text-slate-600 dark:text-zinc-500 mb-2">
              Bearer token configured in the plugin&rsquo;s Calibre Preferences dialog.
            </p>
            <div className="flex gap-2">
              <input
                type="password"
                value={settings['calibre.plugin_api_key'] ?? ''}
                onChange={e => setSettings(s => ({ ...s, 'calibre.plugin_api_key': e.target.value }))}
                placeholder="plugin api key"
                className="flex-1 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600"
              />
              <button
                onClick={() => pluginKeySave(() => saveSettingWithErrorThrowing('calibre.plugin_api_key'))}
                disabled={saving === 'calibre.plugin_api_key'}
                className={`px-3 py-2 rounded text-xs font-medium disabled:opacity-50 ${pluginKeySaveResult === 'saved' ? 'bg-emerald-500' : pluginKeySaveResult === 'error' ? 'bg-red-600' : 'bg-emerald-600 hover:bg-emerald-500'}`}
              >
                {pluginKeySaveResult === 'saved' ? 'Saved ✓' : pluginKeySaveResult === 'error' ? 'Error' : saving === 'calibre.plugin_api_key' ? 'Saving...' : 'Save'}
              </button>
            </div>
            {saveError?.key === 'calibre.plugin_api_key' && (
              <p className="text-xs text-red-600 dark:text-red-400 mt-1">{saveError.msg}</p>
            )}
          </div>
        )}

        {mode === 'plugin' && (
          <div>
            <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">Push path remap</label>
            <p className="text-xs text-slate-600 dark:text-zinc-500 mb-2">
              Optional. If the Calibre container mounts your library at a different path than Bindery,
              map Bindery&rsquo;s prefix to Calibre&rsquo;s as <code className="font-mono">from:to</code> pairs
              (comma separated), e.g. <code className="font-mono">/books:/mnt/user/media/books</code>.
              Leave empty when both containers see the library at the same path.
            </p>
            <div className="flex gap-2">
              <input
                value={settings['calibre.push_path_remap'] ?? ''}
                onChange={e => setSettings(s => ({ ...s, 'calibre.push_path_remap': e.target.value }))}
                placeholder="/books:/mnt/user/media/books"
                className="flex-1 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600"
              />
              <button
                onClick={() => pushRemapSave(() => saveSettingWithErrorThrowing('calibre.push_path_remap'))}
                disabled={saving === 'calibre.push_path_remap'}
                className={`px-3 py-2 rounded text-xs font-medium disabled:opacity-50 ${pushRemapSaveResult === 'saved' ? 'bg-emerald-500' : pushRemapSaveResult === 'error' ? 'bg-red-600' : 'bg-emerald-600 hover:bg-emerald-500'}`}
              >
                {pushRemapSaveResult === 'saved' ? 'Saved ✓' : pushRemapSaveResult === 'error' ? 'Error' : saving === 'calibre.push_path_remap' ? 'Saving...' : 'Save'}
              </button>
            </div>
            {saveError?.key === 'calibre.push_path_remap' && (
              <p className="text-xs text-red-600 dark:text-red-400 mt-1">{saveError.msg}</p>
            )}
          </div>
        )}

        {(mode === 'calibredb' || mode === 'plugin') && (
          <div className="flex items-center justify-between pt-1 border-t border-slate-200 dark:border-zinc-800">
            <div className="text-xs">
              {testResult && (
                <span className={testResult.ok ? 'text-emerald-600 dark:text-emerald-400' : 'text-red-600 dark:text-red-400'}>
                  {testResult.msg}
                </span>
              )}
            </div>
            <button
              onClick={runTest}
              disabled={testing}
              className="px-4 py-2 bg-slate-600 hover:bg-slate-500 rounded text-sm font-medium disabled:opacity-50 flex-shrink-0"
            >
              {testing ? 'Testing…' : 'Test connection'}
            </button>
          </div>
        )}

        {/* Bulk push: Bindery → Calibre (plugin only). Pushes every imported
            book's on-disk file to the plugin; 409 is treated as idempotent. */}
        {mode === 'plugin' && (
          <div className="pt-3 border-t border-slate-200 dark:border-zinc-800">
            <div className="flex items-center justify-between gap-4">
              <div>
                <label className="block text-sm font-medium text-slate-800 dark:text-zinc-200">Push all to Calibre</label>
                <p className="text-xs text-slate-600 dark:text-zinc-500 mt-0.5">
                  Send every imported book in Bindery to the Calibre Bridge plugin. Books already in Calibre are skipped (idempotent).
                </p>
                {bridgeReachable === false && (
                  <p className="text-xs text-amber-600 dark:text-amber-400 mt-1">Bridge not reachable — check plugin URL / API key above.</p>
                )}
              </div>
              <button
                onClick={startSync}
                disabled={syncProgress?.running || bridgeReachable !== true}
                className="px-4 py-2 bg-sky-600 hover:bg-sky-500 rounded text-sm font-medium disabled:opacity-50 flex-shrink-0"
                title={bridgeReachable !== true ? 'Enable plugin mode and verify the bridge is reachable first' : ''}
              >
                {syncProgress?.running ? 'Pushing…' : 'Push all to Calibre'}
              </button>
            </div>
          </div>
        )}

        {/* Library import (read side): Calibre → Bindery */}
        <div className="pt-3 border-t border-slate-200 dark:border-zinc-800 space-y-3">
          <div className="flex items-center justify-between">
            <div>
              <label className="block text-sm font-medium text-slate-800 dark:text-zinc-200">Library import</label>
              <p className="text-xs text-slate-600 dark:text-zinc-500 mt-0.5">
                Read your existing Calibre library and import books, authors, and editions into Bindery.
                Works independently of the write mode above.
              </p>
            </div>
            <Toggle
              checked={libraryImportEnabled}
              onChange={async () => {
                const next = libraryImportEnabled ? 'false' : 'true'
                setSettings(s => ({ ...s, 'calibre.library_import_enabled': next }))
                await api.setSetting('calibre.library_import_enabled', next).catch(console.error)
              }}
              title={libraryImportEnabled ? 'Disable library import' : 'Enable library import'}
            />
          </div>

          {libraryImportEnabled && (
            <>
              <div className="flex items-center justify-between">
                <div>
                  <label className="block text-sm font-medium text-slate-800 dark:text-zinc-200">Sync on startup</label>
                  <p className="text-xs text-slate-600 dark:text-zinc-500 mt-0.5">Re-import each time Bindery starts. Safe to leave on — imports are incremental and idempotent.</p>
                </div>
                <Toggle
                  checked={syncOnStartup}
                  onChange={async () => {
                    const next = syncOnStartup ? 'false' : 'true'
                    setSettings(s => ({ ...s, 'calibre.sync_on_startup': next }))
                    await api.setSetting('calibre.sync_on_startup', next).catch(console.error)
                  }}
                  title={syncOnStartup ? 'Disable' : 'Enable'}
                />
              </div>

              <div className="flex items-center justify-between">
                <div className="text-xs text-slate-600 dark:text-zinc-500">
                  {lastImportAt ? (
                    <>Last import: <span className="text-slate-800 dark:text-zinc-200">{new Date(lastImportAt).toLocaleString()}</span></>
                  ) : (
                    <>Never imported.</>
                  )}
                </div>
                <button
                  onClick={startImport}
                  disabled={importProgress?.running || !settings['calibre.library_path']}
                  className="px-4 py-2 bg-sky-600 hover:bg-sky-500 rounded text-sm font-medium disabled:opacity-50 flex-shrink-0"
                >
                  {importProgress?.running ? 'Importing…' : 'Import library'}
                </button>
              </div>

              {importError && (
                <p className="text-xs text-red-600 dark:text-red-400">{importError}</p>
              )}

              {importProgress && (importProgress.running || importProgress.stats || importProgress.error) && (
                <div className="rounded border border-slate-200 dark:border-zinc-800 bg-slate-50 dark:bg-zinc-950 px-3 py-2 space-y-2">
                  {importProgress.running && (
                    <>
                      <div className="flex justify-between text-xs text-slate-600 dark:text-zinc-400">
                        <span>{importProgress.message || 'Working…'}</span>
                        <span>{importProgress.processed} / {importProgress.total || '?'}</span>
                      </div>
                      <div className="h-1.5 bg-slate-200 dark:bg-zinc-800 rounded overflow-hidden">
                        <div
                          className="h-full bg-sky-600 transition-[width] duration-300"
                          style={{
                            width: importProgress.total > 0
                              ? `${Math.min(100, (importProgress.processed / importProgress.total) * 100)}%`
                              : '0%',
                          }}
                        />
                      </div>
                    </>
                  )}
                  {!importProgress.running && importProgress.error && (
                    <p className="text-xs text-red-600 dark:text-red-400">Import failed: {importProgress.error}</p>
                  )}
                  {!importProgress.running && importProgress.stats && (
                    <p className="text-xs text-slate-700 dark:text-zinc-300">
                      Import complete —{' '}
                      <span className="font-medium">{importProgress.stats.authorsAdded}</span> authors added,{' '}
                      <span className="font-medium">{importProgress.stats.booksAdded}</span> books added,{' '}
                      <span className="font-medium">{importProgress.stats.editionsAdded}</span> editions added,{' '}
                      <span className="font-medium">{importProgress.stats.duplicatesMerged}</span> merged,{' '}
                      <span className="font-medium">{importProgress.stats.skipped}</span> skipped.
                    </p>
                  )}
                </div>
              )}

              <CalibreRunsList
                runs={runs}
                onRefresh={refreshRuns}
                onRollback={setRollbackRun}
              />
            </>
          )}
        </div>
      </div>

      <div className="bg-slate-100 dark:bg-zinc-900 rounded-lg p-5 border border-slate-300 dark:border-zinc-800">
        <h3 className="text-base font-semibold mb-3 text-slate-800 dark:text-zinc-200">Calibre-Web-Automated (CWA)</h3>
        <p className="text-xs text-slate-600 dark:text-zinc-500 mb-4">
          When set, every successful ebook import is also copied into this directory so a sibling{' '}
          <a href="https://github.com/crocodilestick/Calibre-Web-Automated" target="_blank" rel="noopener noreferrer" className="text-emerald-700 dark:text-emerald-400 underline">CWA</a>{' '}
          container can ingest it. Bindery keeps its own copy. Leave blank to disable.
        </p>
        <div>
          <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">Ingest folder path</label>
          <p className="text-xs text-slate-600 dark:text-zinc-500 mb-2">Mount the same path into both containers. CWA's docs use <code className="text-[11px] bg-slate-200 dark:bg-zinc-800 px-1 rounded">/cwa-book-ingest</code>.</p>
          <div className="flex gap-2">
            <input
              value={settings['cwa.ingest_path'] ?? ''}
              onChange={e => setSettings(s => ({ ...s, 'cwa.ingest_path': e.target.value }))}
              placeholder="/cwa-book-ingest"
              className="flex-1 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600"
            />
            <button
              onClick={() => cwaPathSave(() => saveSettingWithErrorThrowing('cwa.ingest_path'))}
              disabled={saving === 'cwa.ingest_path'}
              className={`px-3 py-2 rounded text-xs font-medium disabled:opacity-50 ${cwaPathSaveResult === 'saved' ? 'bg-emerald-500' : cwaPathSaveResult === 'error' ? 'bg-red-600' : 'bg-emerald-600 hover:bg-emerald-500'}`}
            >
              {cwaPathSaveResult === 'saved' ? 'Saved ✓' : cwaPathSaveResult === 'error' ? 'Error' : saving === 'cwa.ingest_path' ? 'Saving...' : 'Save'}
            </button>
          </div>
          {saveError?.key === 'cwa.ingest_path' && (
            <p className="text-xs text-red-600 dark:text-red-400 mt-1">{saveError.msg}</p>
          )}
        </div>
      </div>

      {syncModalOpen && (
        <CalibreSyncModal
          progress={syncProgress}
          error={syncError}
          onClose={() => setSyncModalOpen(false)}
        />
      )}

      {rollbackRun && (
        <CalibreRollbackModal
          run={rollbackRun}
          onClose={() => setRollbackRun(null)}
          onApplied={() => {
            refreshRuns()
          }}
        />
      )}
    </section>
  )
}

// CalibreRunsList renders the "Recent imports" panel inside the Library
// import block. Each run gets a per-row Rollback button that pops the
// CalibreRollbackModal.
function CalibreRunsList({
  runs,
  onRefresh,
  onRollback,
}: {
  runs: CalibreImportRun[]
  onRefresh: () => void
  onRollback: (run: CalibreImportRun) => void
}) {
  const { t } = useTranslation()

  const statusLabel = (status: string) => {
    switch (status) {
      case 'running':
        return t('settings.calibre.runs.statusRunning')
      case 'completed':
        return t('settings.calibre.runs.statusCompleted')
      case 'failed':
        return t('settings.calibre.runs.statusFailed')
      case 'rolled_back':
        return t('settings.calibre.runs.statusRolledBack')
      default:
        return status
    }
  }

  return (
    <div className="rounded border border-slate-200 dark:border-zinc-800 bg-slate-50 dark:bg-zinc-950 px-3 py-3 space-y-3">
      <div className="flex items-center justify-between gap-3">
        <div>
          <p className="text-sm font-medium text-slate-800 dark:text-zinc-200">
            {t('settings.calibre.runs.heading')}
          </p>
          <p className="text-xs text-slate-600 dark:text-zinc-500">
            {t('settings.calibre.runs.description')}
          </p>
        </div>
        <button
          onClick={onRefresh}
          className="px-3 py-2 bg-slate-700 hover:bg-slate-600 rounded text-sm font-medium text-white"
        >
          {t('settings.calibre.runs.refresh')}
        </button>
      </div>

      {runs.length === 0 && (
        <p className="text-sm text-slate-500 dark:text-zinc-500">
          {t('settings.calibre.runs.empty')}
        </p>
      )}

      {runs.slice(0, 10).map(run => {
        const rolledBack = run.status === 'rolled_back'
        const running = run.status === 'running'
        return (
          <div
            key={run.id}
            className={`rounded border px-3 py-2 ${
              rolledBack
                ? 'border-slate-200 dark:border-zinc-800 bg-slate-100/70 dark:bg-zinc-900/50 opacity-75'
                : 'border-slate-200 dark:border-zinc-800 bg-white/70 dark:bg-zinc-950/40'
            }`}
          >
            <div className="flex items-center justify-between gap-3">
              <div className="min-w-0">
                <p className="text-sm font-medium text-slate-800 dark:text-zinc-200 truncate">
                  {t('settings.calibre.runs.runLabel', { runId: run.id, status: statusLabel(run.status) })}
                </p>
                <p className="text-[11px] text-slate-500 dark:text-zinc-500">
                  {t('settings.calibre.runs.startedAt', {
                    startedAt: new Date(run.startedAt).toLocaleString(),
                  })}
                  {run.libraryPath ? ` · ${run.libraryPath}` : ''}
                </p>
              </div>
              <button
                onClick={() => onRollback(run)}
                disabled={rolledBack || running}
                className={`px-3 py-1.5 rounded text-xs font-medium text-white disabled:opacity-50 ${
                  rolledBack ? 'bg-slate-500 cursor-not-allowed' : 'bg-amber-600 hover:bg-amber-500'
                }`}
                title={rolledBack ? t('settings.calibre.runs.rolledBack') : ''}
              >
                {rolledBack ? t('settings.calibre.runs.rolledBack') : t('settings.calibre.runs.rollback')}
              </button>
            </div>
          </div>
        )
      })}
    </div>
  )
}

// CalibreRollbackModal shows the rollback preview, lets the admin confirm,
// and surfaces the resulting per-action list. Apply uses amber styling
// rather than red because rollback restores Bindery state — it does not
// delete on-disk files.
function CalibreRollbackModal({
  run,
  onClose,
  onApplied,
}: {
  run: CalibreImportRun
  onClose: () => void
  onApplied: () => void
}) {
  const { t } = useTranslation()
  const [preview, setPreview] = useState<CalibreRollbackResult | null>(null)
  const [previewLoading, setPreviewLoading] = useState(true)
  const [applied, setApplied] = useState<CalibreRollbackResult | null>(null)
  const [applying, setApplying] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    setPreviewLoading(true)
    setError(null)
    api.calibreRunRollbackPreview(run.id)
      .then(setPreview)
      .catch(err => setError(err instanceof Error ? err.message : String(err)))
      .finally(() => setPreviewLoading(false))
  }, [run.id])

  const apply = async () => {
    setApplying(true)
    setError(null)
    try {
      const result = await api.calibreRunRollback(run.id)
      setApplied(result)
      onApplied()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setApplying(false)
    }
  }

  const display = applied ?? preview
  const closable = !applying

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4" role="dialog" aria-modal="true">
      <div className="w-full max-w-2xl rounded-lg bg-white dark:bg-zinc-900 border border-slate-200 dark:border-zinc-800 shadow-xl">
        <div className="flex items-center justify-between px-4 py-3 border-b border-slate-200 dark:border-zinc-800">
          <h3 className="text-base font-semibold text-slate-800 dark:text-zinc-100">
            {t('settings.calibre.runs.modalTitle', { runId: run.id })}
          </h3>
          <button
            onClick={closable ? onClose : undefined}
            disabled={!closable}
            className="text-slate-500 hover:text-slate-700 dark:text-zinc-400 dark:hover:text-zinc-200 disabled:opacity-40"
            title={closable ? '' : t('settings.calibre.runs.applying')}
          >
            ✕
          </button>
        </div>
        <div className="p-4 space-y-3">
          <p className="text-xs text-slate-600 dark:text-zinc-400">{t('settings.calibre.runs.modalIntro')}</p>

          {previewLoading && (
            <p className="text-sm text-slate-500 dark:text-zinc-500">{t('settings.calibre.runs.previewing')}</p>
          )}

          {error && (
            <p className="text-sm text-red-600 dark:text-red-400">
              {t('settings.calibre.runs.error', { error })}
            </p>
          )}

          {display && (
            <>
              <div className="grid grid-cols-2 gap-2 text-xs">
                <div className="rounded border border-slate-200 dark:border-zinc-800 px-2 py-1.5">
                  <div className="text-slate-600 dark:text-zinc-500">{t('settings.calibre.runs.actionsPlanned', { count: display.stats.actionsPlanned })}</div>
                </div>
                <div className="rounded border border-slate-200 dark:border-zinc-800 px-2 py-1.5">
                  <div className="text-slate-600 dark:text-zinc-500">{t('settings.calibre.runs.entitiesDeleted', { count: display.stats.entitiesDeleted })}</div>
                </div>
                <div className="rounded border border-slate-200 dark:border-zinc-800 px-2 py-1.5">
                  <div className="text-slate-600 dark:text-zinc-500">{t('settings.calibre.runs.provenanceUnlinked', { count: display.stats.provenanceUnlinked })}</div>
                </div>
                <div className="rounded border border-slate-200 dark:border-zinc-800 px-2 py-1.5">
                  <div className="text-slate-600 dark:text-zinc-500">{t('settings.calibre.runs.skipped', { count: display.stats.skipped })}</div>
                </div>
              </div>

              {display.stats.filesAffected > 0 && (
                <div className="rounded border border-amber-300 dark:border-amber-900 bg-amber-50 dark:bg-amber-950/20 px-3 py-2 text-xs text-amber-800 dark:text-amber-300">
                  <p className="font-medium">{t('settings.calibre.runs.filesAffected', { count: display.stats.filesAffected })}</p>
                  <p>{display.filesOnDiskWarning || t('settings.calibre.runs.filesOnDiskWarning')}</p>
                </div>
              )}

              <div>
                <p className="text-xs font-semibold uppercase tracking-wide text-slate-700 dark:text-zinc-300 mb-1">
                  {t('settings.calibre.runs.actionsHeading')}
                </p>
                {display.actions.length === 0 ? (
                  <p className="text-xs text-slate-500 dark:text-zinc-500">{t('settings.calibre.runs.noActions')}</p>
                ) : (
                  <div className="max-h-64 overflow-y-auto rounded border border-slate-200 dark:border-zinc-800">
                    <table className="w-full text-xs">
                      <tbody>
                        {display.actions.map(action => (
                          <tr
                            key={`${action.entityType}-${action.externalId}-${action.localId}-${action.action}`}
                            className="border-t border-slate-200 dark:border-zinc-800"
                          >
                            <td className="px-2 py-1 text-slate-800 dark:text-zinc-200 font-medium">
                              {action.action}
                            </td>
                            <td className="px-2 py-1 text-slate-600 dark:text-zinc-400">{action.entityType}</td>
                            <td className="px-2 py-1 text-slate-600 dark:text-zinc-400 truncate">
                              {action.displayName || action.externalId}
                            </td>
                            <td className="px-2 py-1 text-slate-500 dark:text-zinc-500 truncate">
                              {action.reason || ''}
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                )}
              </div>

              {applied && (
                <p className="text-xs text-emerald-700 dark:text-emerald-400">
                  {t('settings.calibre.runs.appliedSummary', {
                    deleted: applied.stats.entitiesDeleted,
                    unlinked: applied.stats.provenanceUnlinked,
                    skipped: applied.stats.skipped,
                    failed: applied.stats.failed,
                  })}
                </p>
              )}
            </>
          )}
        </div>
        <div className="px-4 py-3 border-t border-slate-200 dark:border-zinc-800 flex justify-end gap-2">
          <button
            onClick={closable ? onClose : undefined}
            disabled={!closable}
            className="px-3 py-1.5 bg-slate-600 hover:bg-slate-500 rounded text-sm font-medium text-white disabled:opacity-50"
          >
            {t('settings.calibre.runs.cancel')}
          </button>
          {!applied && (
            <button
              onClick={apply}
              disabled={applying || previewLoading || !!error}
              className="px-3 py-1.5 bg-amber-600 hover:bg-amber-500 rounded text-sm font-medium text-white disabled:opacity-50"
            >
              {applying ? t('settings.calibre.runs.applying') : t('settings.calibre.runs.applyRollback')}
            </button>
          )}
        </div>
      </div>
    </div>
  )
}

// CalibreSyncModal renders the live progress of a bulk "Push all to
// Calibre" job. Stays open while running; once finished, the user
// dismisses it explicitly so they can read the per-book error list.
function CalibreSyncModal({
  progress,
  error,
  onClose,
}: {
  progress: CalibreSyncProgress | null
  error: string | null
  onClose: () => void
}) {
  const stats = progress?.stats
  const total = stats?.total ?? 0
  const processed = stats?.processed ?? 0
  const pct = total > 0 ? Math.min(100, (processed / total) * 100) : 0
  const running = !!progress?.running
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4" role="dialog" aria-modal="true">
      <div className="w-full max-w-xl rounded-lg bg-white dark:bg-zinc-900 border border-slate-200 dark:border-zinc-800 shadow-xl">
        <div className="flex items-center justify-between px-4 py-3 border-b border-slate-200 dark:border-zinc-800">
          <h3 className="text-base font-semibold text-slate-800 dark:text-zinc-100">Push all to Calibre</h3>
          <button
            onClick={onClose}
            disabled={running}
            className="text-slate-500 hover:text-slate-700 dark:text-zinc-400 dark:hover:text-zinc-200 disabled:opacity-40"
            title={running ? 'Wait for the push to finish' : 'Close'}
          >
            ✕
          </button>
        </div>
        <div className="p-4 space-y-3">
          {error && (
            <p className="text-xs text-red-600 dark:text-red-400">{error}</p>
          )}
          {progress && (
            <>
              <div className="flex justify-between text-xs text-slate-600 dark:text-zinc-400">
                <span>{progress.message || (running ? 'Working…' : 'Idle')}</span>
                <span>{processed} / {total || '?'}</span>
              </div>
              <div className="h-1.5 bg-slate-200 dark:bg-zinc-800 rounded overflow-hidden">
                <div className="h-full bg-sky-600 transition-[width] duration-300" style={{ width: `${pct}%` }} />
              </div>
              <div className="grid grid-cols-3 gap-2 text-xs">
                <div className="rounded border border-slate-200 dark:border-zinc-800 px-2 py-1.5">
                  <div className="text-slate-600 dark:text-zinc-500">Pushed</div>
                  <div className="font-semibold text-emerald-600 dark:text-emerald-400">{stats?.pushed ?? 0}</div>
                </div>
                <div className="rounded border border-slate-200 dark:border-zinc-800 px-2 py-1.5">
                  <div className="text-slate-600 dark:text-zinc-500">Already in Calibre</div>
                  <div className="font-semibold text-slate-700 dark:text-zinc-300">{stats?.alreadyInCalibre ?? 0}</div>
                </div>
                <div className="rounded border border-slate-200 dark:border-zinc-800 px-2 py-1.5">
                  <div className="text-slate-600 dark:text-zinc-500">Failed</div>
                  <div className="font-semibold text-red-600 dark:text-red-400">{stats?.failed ?? 0}</div>
                </div>
              </div>
              {!running && progress.error && (
                <p className="text-xs text-red-600 dark:text-red-400">Sync failed: {progress.error}</p>
              )}
              {!running && progress.finishedAt && !progress.error && (
                <p className="text-xs text-emerald-600 dark:text-emerald-400">
                  Done — pushed {stats?.pushed ?? 0}, already in Calibre {stats?.alreadyInCalibre ?? 0}, failed {stats?.failed ?? 0}.
                </p>
              )}
              {progress.errors && progress.errors.length > 0 && (
                <div className="mt-2 max-h-48 overflow-y-auto rounded border border-slate-200 dark:border-zinc-800">
                  <table className="w-full text-xs">
                    <thead className="bg-slate-100 dark:bg-zinc-800 sticky top-0">
                      <tr>
                        <th className="text-left px-2 py-1 text-slate-600 dark:text-zinc-400 font-medium">Title</th>
                        <th className="text-left px-2 py-1 text-slate-600 dark:text-zinc-400 font-medium">Reason</th>
                      </tr>
                    </thead>
                    <tbody>
                      {progress.errors.map((e, i) => (
                        <tr key={`${e.bookId}-${i}`} className="border-t border-slate-200 dark:border-zinc-800">
                          <td className="px-2 py-1 text-slate-800 dark:text-zinc-200">{e.title || `#${e.bookId}`}</td>
                          <td className="px-2 py-1 text-red-600 dark:text-red-400">{e.reason}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </>
          )}
        </div>
        <div className="px-4 py-3 border-t border-slate-200 dark:border-zinc-800 flex justify-end">
          <button
            onClick={onClose}
            disabled={running}
            className="px-3 py-1.5 bg-slate-600 hover:bg-slate-500 rounded text-sm font-medium disabled:opacity-50 text-white"
          >
            {running ? 'Running…' : 'Close'}
          </button>
        </div>
      </div>
    </div>
  )
}
