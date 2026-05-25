import { useEffect, useState } from 'react'
import { api, GrimmoryConfig, GrimmoryTestResult } from '../../api/client'
import { inputCls } from './formStyles'

export default function GrimmoryTab() {
  const [config, setConfig] = useState<GrimmoryConfig | null>(null)
  const [draft, setDraft] = useState({ baseUrl: '', apiKey: '', enabled: false })
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [testing, setTesting] = useState(false)
  const [testResult, setTestResult] = useState<GrimmoryTestResult | null>(null)
  const [testError, setTestError] = useState<string | null>(null)

  useEffect(() => {
    api.grimmoryConfig()
      .then(cfg => {
        setConfig(cfg)
        setDraft({ baseUrl: cfg.baseUrl ?? '', apiKey: '', enabled: cfg.enabled })
      })
      .catch(console.error)
      .finally(() => setLoading(false))
  }, [])

  const save = async () => {
    setSaving(true)
    setSaveError(null)
    setTestResult(null)
    try {
      const payload: { enabled: boolean; baseUrl: string; apiKey?: string } = {
        enabled: draft.enabled,
        baseUrl: draft.baseUrl,
      }
      if (draft.apiKey) payload.apiKey = draft.apiKey
      const next = await api.grimmorySetConfig(payload)
      setConfig(next)
      setDraft(prev => ({ ...prev, apiKey: '' }))
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
      const payload: { baseUrl?: string; apiKey?: string } = { baseUrl: draft.baseUrl }
      if (draft.apiKey) payload.apiKey = draft.apiKey
      const r = await api.grimmoryTest(payload)
      setTestResult(r)
    } catch (err) {
      setTestError(err instanceof Error ? err.message : 'Test failed')
    } finally {
      setTesting(false)
    }
  }

  if (loading) return <div className="text-slate-600 dark:text-zinc-500">Loading…</div>

  return (
    <div className="space-y-6 max-w-lg">
      <div>
        <h3 className="text-lg font-semibold mb-1">Grimmory</h3>
        <p className="text-xs text-slate-600 dark:text-zinc-500 mb-4">
          Push newly imported books to a{' '}
          <a href="https://grimmory.org" target="_blank" rel="noopener noreferrer" className="underline">Grimmory</a>{' '}
          self-hosted library. Enable the Grimmory REST API on your Grimmory server with{' '}
          <code>API_DOCS_ENABLED=true</code> and enter its URL below.
        </p>
        <p className="text-xs text-amber-700 dark:text-amber-400 mb-4">
          Grimmory v3.x does not have API keys
          (<a href="https://github.com/orgs/grimmory-tools/discussions/1487" target="_blank" rel="noopener noreferrer" className="underline">grimmory-tools/grimmory#1487</a>)
          — leave the field blank unless a future Grimmory release adds token auth.
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
          <label className="block text-xs font-medium mb-1">
            API Key
            <span className="ml-1 text-slate-500 dark:text-zinc-500 font-normal">
              {config?.apiKeyConfigured && !draft.apiKey ? '(configured — leave blank to keep)' : '(optional, see above)'}
            </span>
          </label>
          <input
            type="password"
            value={draft.apiKey}
            onChange={e => setDraft(prev => ({ ...prev, apiKey: e.target.value }))}
            placeholder={config?.apiKeyConfigured ? '••••••••' : 'Leave blank for current Grimmory'}
            className={inputCls}
          />
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
    </div>
  )
}
