import { useCallback, useEffect, useRef, useState, useId } from 'react'
import { useTranslation } from 'react-i18next'
import RichText from '../../components/RichText'
import { api, GrimmoryConfig, GrimmorySyncStatus, GrimmoryTestResult } from '../../api/client'
import { inputCls } from './formStyles'
import { literalInputAttrs, secretInputAttrs, urlInputAttrs } from '../../util/inputAttrs'
import { primaryFill } from '../../components/buttons'
import { errorText } from '../../util/errorText'

export default function GrimmoryTab() {
  const { t } = useTranslation()
  const fid = useId()
  const [config, setConfig] = useState<GrimmoryConfig | null>(null)
  const [draft, setDraft] = useState({ baseUrl: '', apiKey: '', username: '', password: '', enabled: false })
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [testing, setTesting] = useState(false)
  const [testResult, setTestResult] = useState<GrimmoryTestResult | null>(null)
  const [testError, setTestError] = useState<string | null>(null)
  const [sync, setSync] = useState<GrimmorySyncStatus | null>(null)
  const [syncError, setSyncError] = useState<string | null>(null)
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null)

  useEffect(() => {
    api.grimmoryConfig()
      .then(cfg => {
        setConfig(cfg)
        setDraft({ baseUrl: cfg.baseUrl ?? '', apiKey: '', username: cfg.username ?? '', password: '', enabled: cfg.enabled })
      })
      .catch(console.error)
      .finally(() => setLoading(false))
    api.grimmorySyncStatus().then(setSync).catch(() => {})
  }, [])

  const stopPolling = useCallback(() => {
    if (pollRef.current) {
      clearInterval(pollRef.current)
      pollRef.current = null
    }
  }, [])

  useEffect(() => stopPolling, [stopPolling])

  const pollSync = useCallback(() => {
    stopPolling()
    pollRef.current = setInterval(async () => {
      try {
        const s = await api.grimmorySyncStatus()
        setSync(s)
        if (!s.running) stopPolling()
      } catch {
        stopPolling()
      }
    }, 2000)
  }, [stopPolling])

  const save = async () => {
    setSaving(true)
    setSaveError(null)
    setTestResult(null)
    try {
      const payload: { enabled: boolean; baseUrl: string; apiKey?: string; username?: string; password?: string } = {
        enabled: draft.enabled,
        baseUrl: draft.baseUrl,
        username: draft.username,
      }
      if (draft.apiKey) payload.apiKey = draft.apiKey
      if (draft.password) payload.password = draft.password
      const next = await api.grimmorySetConfig(payload)
      setConfig(next)
      setDraft(prev => ({ ...prev, apiKey: '', password: '' }))
    } catch (err) {
      setSaveError(errorText(err, t('settings.grimmory.errors.save', 'Save failed')))
    } finally {
      setSaving(false)
    }
  }

  const test = async () => {
    setTesting(true)
    setTestResult(null)
    setTestError(null)
    try {
      const payload: { baseUrl?: string; apiKey?: string; username?: string; password?: string } = {
        baseUrl: draft.baseUrl,
        username: draft.username,
      }
      if (draft.apiKey) payload.apiKey = draft.apiKey
      if (draft.password) payload.password = draft.password
      const r = await api.grimmoryTest(payload)
      setTestResult(r)
    } catch (err) {
      setTestError(errorText(err, t('settings.grimmory.errors.test', 'Test failed')))
    } finally {
      setTesting(false)
    }
  }

  const startSync = async () => {
    setSyncError(null)
    try {
      const s = await api.grimmorySync()
      setSync(s)
      pollSync()
    } catch (err) {
      setSyncError(errorText(err, t('settings.grimmory.errors.syncStart', 'Sync failed to start')))
    }
  }

  if (loading) return <div className="text-fg-muted">{t('common.loading', 'Loading…')}</div>

  const credsConfigured = Boolean(config?.apiKeyConfigured || (config?.username && config?.passwordConfigured))

  return (
    <div className="space-y-6 max-w-lg">
      <div>
        <h3 className="text-lg font-semibold mb-1">Grimmory</h3>
        <p className="text-xs text-fg-muted mb-4">
          <RichText text={t('settings.grimmory.intro', 'Push imported ebooks to a <site>Grimmory</site> self-hosted library. Every successful ebook import is uploaded to Grimmory\'s <b>BookDrop</b> inbox, where Grimmory\'s own metadata matching and review flow takes over. Files are pushed at most once.')} components={{ site: <a href="https://grimmory.org" target="_blank" rel="noopener noreferrer" className="underline" />, b: <strong /> }} />
        </p>
      </div>

      <label className="flex items-center gap-3 cursor-pointer">
        <input
          type="checkbox"
          checked={draft.enabled}
          onChange={e => setDraft(prev => ({ ...prev, enabled: e.target.checked }))}
          className="w-4 h-4"
        />
        <span className="text-sm font-medium">{t('settings.grimmory.enable', 'Enable Grimmory integration')}</span>
      </label>

      <div className="space-y-3">
        <div>
          <label htmlFor={`${fid}-1`} className="block text-xs font-medium mb-1">{t('settings.grimmory.fields.serverUrl', 'Server URL')}</label>
          <input id={`${fid}-1`}
            {...urlInputAttrs}
            type="text"
            value={draft.baseUrl}
            onChange={e => setDraft(prev => ({ ...prev, baseUrl: e.target.value }))}
            placeholder="https://grimmory.example.com"
            className={inputCls}
          />
        </div>
        <div>
          <label htmlFor={`${fid}-2`} className="block text-xs font-medium mb-1">{t('settings.grimmory.fields.username', 'Username')}</label>
          <input id={`${fid}-2`}
            {...literalInputAttrs}
            type="text"
            value={draft.username}
            onChange={e => setDraft(prev => ({ ...prev, username: e.target.value }))}
            placeholder={t('settings.grimmory.fields.usernamePlaceholder', 'Grimmory account with upload permission')}
            className={inputCls}
          />
        </div>
        <div>
          <label htmlFor={`${fid}-3`} className="block text-xs font-medium mb-1">
            {t('settings.grimmory.fields.password', 'Password')}
            <span className="ml-1 text-fg-muted font-normal">
              {config?.passwordConfigured && !draft.password ? t('settings.grimmory.fields.configured', '(configured — leave blank to keep)') : ''}
            </span>
          </label>
          <input id={`${fid}-3`}
            {...secretInputAttrs}
            type="password"
            value={draft.password}
            onChange={e => setDraft(prev => ({ ...prev, password: e.target.value }))}
            placeholder={config?.passwordConfigured ? '••••••••' : ''}
            className={inputCls}
          />
          <p className="mt-1 text-[11px] text-fg-muted">
            {t('settings.grimmory.fields.passwordHint', 'Bindery signs in to Grimmory with these credentials (current Grimmory has no API tokens). Use an account with upload permission — a dedicated one is a good idea.')}
          </p>
        </div>
        <div>
          <label htmlFor={`${fid}-4`} className="block text-xs font-medium mb-1">
            {t('settings.grimmory.fields.apiToken', 'API token')}
            <span className="ml-1 text-fg-muted font-normal">
              {config?.apiKeyConfigured && !draft.apiKey ? t('settings.grimmory.fields.configured', '(configured — leave blank to keep)') : t('settings.grimmory.fields.optional', '(optional)')}
            </span>
          </label>
          <input id={`${fid}-4`}
            {...secretInputAttrs}
            type="password"
            value={draft.apiKey}
            onChange={e => setDraft(prev => ({ ...prev, apiKey: e.target.value }))}
            placeholder={t('settings.grimmory.fields.apiTokenPlaceholder', 'Only if a future Grimmory release adds token auth')}
            className={inputCls}
          />
          <p className="mt-1 text-[11px] text-fg-muted">
            <RichText text={t('settings.grimmory.fields.apiTokenHint', 'If set, the token is sent as a Bearer credential and the username/password login is skipped (<link>grimmory-tools#1487</link>).')} components={{ link: <a href="https://github.com/orgs/grimmory-tools/discussions/1487" target="_blank" rel="noopener noreferrer" className="underline" /> }} />
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
          disabled={testing || !draft.baseUrl}
          className="px-4 py-2 bg-slate-200 dark:bg-zinc-700 hover:bg-slate-300 dark:hover:bg-zinc-600 disabled:opacity-50 rounded text-sm font-medium"
        >
          {testing ? t('common.testing', 'Testing…') : t('settings.grimmory.testConnection', 'Test Connection')}
        </button>
      </div>

      {saveError && <p className="text-red-700 dark:text-red-400 text-xs">{saveError}</p>}
      {testError && <p className="text-red-700 dark:text-red-400 text-xs">{testError}</p>}
      {testResult && (
        <p className={`text-xs ${testResult.ok ? 'text-accent-text' : 'text-red-700 dark:text-red-400'}`}>
          {testResult.message}
        </p>
      )}

      <div className="pt-4 border-t border-slate-200 dark:border-zinc-800">
        <h4 className="text-sm font-semibold mb-1">{t('settings.grimmory.push.heading', 'Push library to Grimmory')}</h4>
        <p className="text-xs text-fg-muted mb-3">
          {t('settings.grimmory.push.hint', 'Upload every imported ebook that hasn\'t been pushed yet. Already-pushed files are skipped, so re-running is safe.')}
        </p>
        <button
          onClick={startSync}
          disabled={!config?.enabled || !credsConfigured || Boolean(sync?.running)}
          className="px-4 py-2 bg-slate-200 dark:bg-zinc-700 hover:bg-slate-300 dark:hover:bg-zinc-600 disabled:opacity-50 rounded text-sm font-medium"
        >
          {sync?.running ? t('settings.grimmory.push.running', 'Pushing…') : t('settings.grimmory.push.start', 'Push all to Grimmory')}
        </button>
        {!config?.enabled && (
          <p className="mt-2 text-[11px] text-fg-muted">{t('settings.grimmory.push.enableFirst', 'Enable and save the integration first.')}</p>
        )}
        {syncError && <p className="mt-2 text-red-700 dark:text-red-400 text-xs">{syncError}</p>}
        {sync && (sync.running || sync.finishedAt) && (
          <div className="mt-3 text-xs text-fg-muted space-y-1">
            <p>
              {sync.message}
              {' — '}
              {t('settings.grimmory.push.progress', { processed: sync.stats.processed, total: sync.stats.total, pushed: sync.stats.pushed, already: sync.stats.alreadyPushed, failed: sync.stats.failed, defaultValue: '{{processed}}/{{total}} processed, {{pushed}} pushed, {{already}} already pushed, {{failed}} failed' })}
            </p>
            {sync.error && <p className="text-red-700 dark:text-red-400">{sync.error}</p>}
            {sync.errors.length > 0 && (
              <ul className="list-disc ml-4 text-red-700 dark:text-red-400">
                {sync.errors.slice(0, 5).map(e => (
                  <li key={`${e.bookId}-${e.path}`}>{e.title}: {e.reason}</li>
                ))}
                {sync.errors.length > 5 && <li>{t('settings.grimmory.push.moreErrors', { count: sync.errors.length - 5, defaultValue: '…and {{count}} more' })}</li>}
              </ul>
            )}
          </div>
        )}
        {sync && sync.totalPushedFiles > 0 && (
          <p className="mt-2 text-[11px] text-fg-muted">
            {sync.lastPushedAt
              ? t('settings.grimmory.push.totalLast', { count: sync.totalPushedFiles, last: new Date(sync.lastPushedAt).toLocaleString() })
              : t('settings.grimmory.push.total', { count: sync.totalPushedFiles })}
          </p>
        )}
      </div>
    </div>
  )
}
