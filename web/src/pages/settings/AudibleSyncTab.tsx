import { useEffect, useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import RichText from '../../components/RichText'
import { api, AudibleSyncConfig, AudibleSyncTestResult } from '../../api/client'
import { inputCls } from './formStyles'
import { secretInputAttrs, urlInputAttrs } from '../../util/inputAttrs'
import { primaryFill } from '../../components/buttons'
import { errorText } from '../../util/errorText'

interface Draft {
  enabled: boolean
  baseUrl: string
  apiToken: string
}

function draftFrom(cfg: AudibleSyncConfig): Draft {
  return {
    enabled: cfg.enabled,
    baseUrl: cfg.baseUrl ?? '',
    apiToken: '',
  }
}

export default function AudibleSyncTab() {
  const { t } = useTranslation()
  const fid = useId()
  const [config, setConfig] = useState<AudibleSyncConfig | null>(null)
  const [draft, setDraft] = useState<Draft>({ enabled: false, baseUrl: '', apiToken: '' })
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [testing, setTesting] = useState(false)
  const [testResult, setTestResult] = useState<AudibleSyncTestResult | null>(null)
  const [testError, setTestError] = useState<string | null>(null)

  useEffect(() => {
    api.audibleSyncConfig()
      .then(cfg => {
        setConfig(cfg)
        setDraft(draftFrom(cfg))
      })
      .catch(console.error)
      .finally(() => setLoading(false))
  }, [])

  const save = async () => {
    setSaving(true)
    setSaveError(null)
    setTestResult(null)
    try {
      const payload: { enabled: boolean; baseUrl: string; apiToken?: string } = {
        enabled: draft.enabled,
        baseUrl: draft.baseUrl,
      }
      if (draft.apiToken) payload.apiToken = draft.apiToken
      const next = await api.audibleSyncSetConfig(payload)
      setConfig(next)
      setDraft(draftFrom(next))
    } catch (err) {
      setSaveError(errorText(err, t('settings.audiblesync.errors.save', 'Save failed')))
    } finally {
      setSaving(false)
    }
  }

  const test = async () => {
    setTesting(true)
    setTestResult(null)
    setTestError(null)
    try {
      const r = await api.audibleSyncTest(draft.apiToken ? { apiToken: draft.apiToken } : {})
      setTestResult(r)
    } catch (err) {
      setTestError(errorText(err, t('settings.audiblesync.errors.test', 'Test failed')))
    } finally {
      setTesting(false)
    }
  }

  if (loading) return <div className="text-fg-muted">{t('common.loading', 'Loading…')}</div>

  // The URL is probed as saved, never as typed, so Test waits for a save.
  const urlUnsaved = draft.baseUrl.trim() !== (config?.baseUrl ?? '')

  return (
    <div className="space-y-6 max-w-lg">
      <div>
        <h3 className="text-lg font-semibold mb-1">{t('settings.audiblesync.heading', 'Audible Sync')}</h3>
        <p className="text-xs text-fg-muted mb-4">
          <RichText
            text={t('settings.audiblesync.intro', 'Connect a self-hosted <site>Audible Sync</site> instance to bring your Audible wishlist, and the library books Audible no longer lets you download, into Bindery. Once it is connected, pick the lists to watch under Settings, Import.')}
            components={{ site: <a href="https://github.com/AlexStansfield/audible-sync" target="_blank" rel="noopener noreferrer" className="underline" /> }}
          />
        </p>
      </div>

      <label className="flex items-center gap-3 cursor-pointer">
        <input
          type="checkbox"
          checked={draft.enabled}
          onChange={e => setDraft(prev => ({ ...prev, enabled: e.target.checked }))}
          className="w-4 h-4"
        />
        <span className="text-sm font-medium">{t('settings.audiblesync.enable', 'Enable Audible Sync integration')}</span>
      </label>

      <div className="space-y-3">
        <div>
          <label htmlFor={`${fid}-url`} className="block text-xs font-medium mb-1">{t('settings.audiblesync.fields.serverUrl', 'Server URL')}</label>
          <input id={`${fid}-url`}
            {...urlInputAttrs}
            type="text"
            value={draft.baseUrl}
            onChange={e => setDraft(prev => ({ ...prev, baseUrl: e.target.value }))}
            placeholder="http://audible-sync:8080"
            className={inputCls}
          />
        </div>
        <div>
          <label htmlFor={`${fid}-token`} className="block text-xs font-medium mb-1">
            {t('settings.audiblesync.fields.apiToken', 'API token')}
            <span className="ml-1 text-fg-muted font-normal">
              {config?.apiTokenConfigured && !draft.apiToken ? t('settings.audiblesync.fields.configured', '(configured — leave blank to keep)') : ''}
            </span>
          </label>
          <input id={`${fid}-token`}
            {...secretInputAttrs}
            type="password"
            value={draft.apiToken}
            onChange={e => setDraft(prev => ({ ...prev, apiToken: e.target.value }))}
            placeholder={config?.apiTokenConfigured ? '••••••••' : ''}
            className={inputCls}
          />
          <p className="mt-1 text-[11px] text-fg-muted">
            {t('settings.audiblesync.fields.apiTokenHint', 'Audible Sync logs a token on its first start, or uses AUDIBLE_SYNC_API_TOKEN when that is set. Its last four characters are shown in its own settings.')}
          </p>
        </div>
      </div>

      <div className="flex gap-2 flex-wrap">
        <button
          onClick={save}
          disabled={saving}
          className={`px-4 py-2 ${primaryFill} disabled:opacity-50 rounded text-sm font-medium`}
        >
          {saving ? t('common.saving', 'Saving…') : t('common.save', 'Save')}
        </button>
        <button
          onClick={test}
          disabled={testing || !config?.baseUrl || urlUnsaved}
          className="px-4 py-2 bg-slate-200 dark:bg-zinc-700 hover:bg-slate-300 dark:hover:bg-zinc-600 disabled:opacity-50 rounded text-sm font-medium"
        >
          {testing ? t('common.testing', 'Testing…') : t('settings.audiblesync.testConnection', 'Test Connection')}
        </button>
      </div>
      {urlUnsaved && draft.baseUrl.trim() !== '' && (
        <p className="text-[11px] text-fg-muted">{t('settings.audiblesync.saveBeforeTest', 'Save the URL to test it.')}</p>
      )}

      {saveError && <p className="text-red-700 dark:text-red-400 text-xs">{saveError}</p>}
      {testError && <p className="text-red-700 dark:text-red-400 text-xs">{testError}</p>}
      {testResult && (
        <p className={`text-xs ${testResult.ok ? 'text-accent-text' : 'text-red-700 dark:text-red-400'}`}>
          {testResult.message}
        </p>
      )}
    </div>
  )
}
