import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, BINDERY_BASE, HardcoverTestResult, SystemStatus } from '../../api/client'
import ClipboardManualFallback from '../../components/ClipboardManualFallback'
import { useClipboardCopy } from '../../components/useClipboardCopy'
import Toggle from './Toggle'

export default function ApiKeysTab() {
  const { t } = useTranslation()
  const opdsClipboard = useClipboardCopy()
  const [settings, setSettings] = useState<Record<string, string>>({})
  const [loading, setLoading] = useState(true)
  const [hardcoverToken, setHardcoverToken] = useState('')
  const [hardcoverTestResult, setHardcoverTestResult] = useState<(HardcoverTestResult & { testing?: boolean }) | null>(null)
  const [systemStatus, setSystemStatus] = useState<SystemStatus | null>(null)
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
    api.status().then(setSystemStatus).catch(console.error)
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

  if (loading) return <div className="text-slate-600 dark:text-zinc-500">{t('common.loading')}</div>

  const hardcoverTokenConfigured = systemStatus?.hardcoverTokenConfigured ?? false
  const enhancedHardcoverAdminEnabled = (settings['hardcover.enhanced_series_enabled'] ?? 'false').toLowerCase() === 'true'
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
    </div>
  )
}
