import { useCallback, useEffect, useRef, useState } from 'react'
import { api, GrimmoryConfig, GrimmorySyncStatus, GrimmoryTestResult } from '../../api/client'
import { inputCls } from './formStyles'

export default function GrimmoryTab() {
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
      setSaveError(err instanceof Error ? err.message : 'Save failed')
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
      setTestError(err instanceof Error ? err.message : 'Test failed')
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
      setSyncError(err instanceof Error ? err.message : 'Sync failed to start')
    }
  }

  if (loading) return <div className="text-slate-600 dark:text-zinc-500">Loading…</div>

  const credsConfigured = Boolean(config?.apiKeyConfigured || (config?.username && config?.passwordConfigured))

  return (
    <div className="space-y-6 max-w-lg">
      <div>
        <h3 className="text-lg font-semibold mb-1">Grimmory</h3>
        <p className="text-xs text-slate-600 dark:text-zinc-500 mb-4">
          Push imported ebooks to a{' '}
          <a href="https://grimmory.org" target="_blank" rel="noopener noreferrer" className="underline">Grimmory</a>{' '}
          self-hosted library. Every successful ebook import is uploaded to Grimmory's <strong>BookDrop</strong> inbox,
          where Grimmory's own metadata matching and review flow takes over. Files are pushed at most once.
        </p>
      </div>

      <label className="flex items-center gap-3 cursor-pointer">
        <input
          type="checkbox"
          checked={draft.enabled}
          onChange={e => setDraft(prev => ({ ...prev, enabled: e.target.checked }))}
          className="w-4 h-4 accent-emerald-500"
        />
        <span className="text-sm font-medium">Enable Grimmory integration</span>
      </label>

      <div className="space-y-3">
        <div>
          <label className="block text-xs font-medium mb-1">Server URL</label>
          <input
            type="text"
            value={draft.baseUrl}
            onChange={e => setDraft(prev => ({ ...prev, baseUrl: e.target.value }))}
            placeholder="https://grimmory.example.com"
            className={inputCls}
          />
        </div>
        <div>
          <label className="block text-xs font-medium mb-1">Username</label>
          <input
            type="text"
            value={draft.username}
            onChange={e => setDraft(prev => ({ ...prev, username: e.target.value }))}
            placeholder="Grimmory account with upload permission"
            className={inputCls}
          />
        </div>
        <div>
          <label className="block text-xs font-medium mb-1">
            Password
            <span className="ml-1 text-slate-500 dark:text-zinc-500 font-normal">
              {config?.passwordConfigured && !draft.password ? '(configured — leave blank to keep)' : ''}
            </span>
          </label>
          <input
            type="password"
            value={draft.password}
            onChange={e => setDraft(prev => ({ ...prev, password: e.target.value }))}
            placeholder={config?.passwordConfigured ? '••••••••' : ''}
            className={inputCls}
          />
          <p className="mt-1 text-[11px] text-slate-500 dark:text-zinc-500">
            Bindery signs in to Grimmory with these credentials (current Grimmory has no API tokens).
            Use an account with upload permission — a dedicated one is a good idea.
          </p>
        </div>
        <div>
          <label className="block text-xs font-medium mb-1">
            API token
            <span className="ml-1 text-slate-500 dark:text-zinc-500 font-normal">
              {config?.apiKeyConfigured && !draft.apiKey ? '(configured — leave blank to keep)' : '(optional)'}
            </span>
          </label>
          <input
            type="password"
            value={draft.apiKey}
            onChange={e => setDraft(prev => ({ ...prev, apiKey: e.target.value }))}
            placeholder="Only if a future Grimmory release adds token auth"
            className={inputCls}
          />
          <p className="mt-1 text-[11px] text-slate-500 dark:text-zinc-500">
            If set, the token is sent as a Bearer credential and the username/password login is skipped
            (<a href="https://github.com/orgs/grimmory-tools/discussions/1487" target="_blank" rel="noopener noreferrer" className="underline">grimmory-tools#1487</a>).
          </p>
        </div>
      </div>

      <div className="flex gap-2 flex-wrap">
        <button
          onClick={save}
          disabled={saving}
          className="px-4 py-2 bg-emerald-600 hover:bg-emerald-500 disabled:opacity-50 rounded text-sm font-medium"
        >
          {saving ? 'Saving…' : 'Save'}
        </button>
        <button
          onClick={test}
          disabled={testing || !draft.baseUrl}
          className="px-4 py-2 bg-slate-200 dark:bg-zinc-700 hover:bg-slate-300 dark:hover:bg-zinc-600 disabled:opacity-50 rounded text-sm font-medium"
        >
          {testing ? 'Testing…' : 'Test Connection'}
        </button>
      </div>

      {saveError && <p className="text-red-500 text-xs">{saveError}</p>}
      {testError && <p className="text-red-500 text-xs">{testError}</p>}
      {testResult && (
        <p className={`text-xs ${testResult.ok ? 'text-emerald-600 dark:text-emerald-400' : 'text-red-500'}`}>
          {testResult.message}
        </p>
      )}

      <div className="pt-4 border-t border-slate-200 dark:border-zinc-800">
        <h4 className="text-sm font-semibold mb-1">Push library to Grimmory</h4>
        <p className="text-xs text-slate-600 dark:text-zinc-500 mb-3">
          Upload every imported ebook that hasn't been pushed yet. Already-pushed files are skipped, so
          re-running is safe.
        </p>
        <button
          onClick={startSync}
          disabled={!config?.enabled || !credsConfigured || Boolean(sync?.running)}
          className="px-4 py-2 bg-slate-200 dark:bg-zinc-700 hover:bg-slate-300 dark:hover:bg-zinc-600 disabled:opacity-50 rounded text-sm font-medium"
        >
          {sync?.running ? 'Pushing…' : 'Push all to Grimmory'}
        </button>
        {!config?.enabled && (
          <p className="mt-2 text-[11px] text-slate-500 dark:text-zinc-500">Enable and save the integration first.</p>
        )}
        {syncError && <p className="mt-2 text-red-500 text-xs">{syncError}</p>}
        {sync && (sync.running || sync.finishedAt) && (
          <div className="mt-3 text-xs text-slate-600 dark:text-zinc-400 space-y-1">
            <p>
              {sync.message}
              {' — '}
              {sync.stats.processed}/{sync.stats.total} processed, {sync.stats.pushed} pushed,{' '}
              {sync.stats.alreadyPushed} already pushed, {sync.stats.failed} failed
            </p>
            {sync.error && <p className="text-red-500">{sync.error}</p>}
            {sync.errors.length > 0 && (
              <ul className="list-disc ml-4 text-red-500">
                {sync.errors.slice(0, 5).map(e => (
                  <li key={`${e.bookId}-${e.path}`}>{e.title}: {e.reason}</li>
                ))}
                {sync.errors.length > 5 && <li>…and {sync.errors.length - 5} more</li>}
              </ul>
            )}
          </div>
        )}
        {sync && sync.totalPushedFiles > 0 && (
          <p className="mt-2 text-[11px] text-slate-500 dark:text-zinc-500">
            {sync.totalPushedFiles} file{sync.totalPushedFiles === 1 ? '' : 's'} pushed so far
            {sync.lastPushedAt ? `, last at ${new Date(sync.lastPushedAt).toLocaleString()}` : ''}.
          </p>
        )}
      </div>
    </div>
  )
}
