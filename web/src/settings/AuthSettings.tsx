import { FormEvent, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, OidcProvider, OidcProviderConfig } from '../api/client'

const inputCls = 'w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600'

export default function AuthSettings() {
  const { t } = useTranslation()
  // GET returns public shape only (no client_secret). We track display list
  // separately from the full configs we accumulate during add operations.
  const [displayed, setDisplayed] = useState<OidcProvider[]>([])
  // Full configs built up in-session from add operations. Used as the write
  // source for PUT. Entries not in this map (i.e. loaded from GET) are sent
  // with client_secret:'' — backend preserves the existing secret for those.
  const [fullConfigs, setFullConfigs] = useState<Map<string, OidcProviderConfig>>(new Map())
  const [showAdd, setShowAdd] = useState(false)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    api.oidcProviders().then(setDisplayed).catch(console.error)
  }, [])

  const buildPutPayload = (ids: string[]): OidcProviderConfig[] =>
    ids.map(id => fullConfigs.get(id) ?? { id, name: displayed.find(p => p.id === id)?.name ?? id, issuer: '', client_id: '', client_secret: '', scopes: [] })

  const remove = async (id: string) => {
    if (!confirm(t('settings.oidc.removeConfirm'))) return
    setSaving(true)
    setError('')
    try {
      const nextIds = displayed.filter(p => p.id !== id).map(p => p.id)
      await api.oidcSetProviders(buildPutPayload(nextIds))
      setDisplayed(ps => ps.filter(p => p.id !== id))
      setFullConfigs(m => { const n = new Map(m); n.delete(id); return n })
    } catch (e) {
      setError(e instanceof Error ? e.message : t('settings.oidc.saveFail'))
    } finally {
      setSaving(false)
    }
  }

  const add = async (cfg: OidcProviderConfig) => {
    setSaving(true)
    setError('')
    try {
      const nextIds = [...displayed.map(p => p.id), cfg.id]
      const nextConfigs = new Map(fullConfigs).set(cfg.id, cfg)
      await api.oidcSetProviders(buildPutPayload(nextIds).map(p => nextConfigs.get(p.id) ?? p))
      // Status will populate on next GET; the row briefly renders without a
      // badge until then, which is acceptable. Re-fetch async to refresh it.
      setDisplayed(ps => [...ps, { id: cfg.id, name: cfg.name }])
      api.oidcProviders().then(setDisplayed).catch(() => {})
      setFullConfigs(nextConfigs)
      setShowAdd(false)
    } catch (e) {
      setError(e instanceof Error ? e.message : t('settings.oidc.saveFail'))
    } finally {
      setSaving(false)
    }
  }

  return (
    <section>
      <h3 className="text-base font-semibold mb-3 text-slate-800 dark:text-zinc-200">
        {t('settings.oidc.heading')}
      </h3>
      <div className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900 space-y-4">
        <p className="text-xs text-slate-600 dark:text-zinc-500">
          {t('settings.oidc.description')}
        </p>

        {displayed.length === 0 && !showAdd && (
          <p className="text-sm text-slate-500 dark:text-zinc-500">{t('settings.oidc.empty')}</p>
        )}

        {displayed.map(p => (
          <div key={p.id} className="flex items-start justify-between py-2 border-b border-slate-200 dark:border-zinc-800 last:border-0 gap-3">
            <div className="flex-1 min-w-0">
              <div className="flex items-center gap-2">
                <span className="text-sm font-medium text-slate-800 dark:text-zinc-200">{p.name}</span>
                {p.status?.state === 'failed' ? (
                  <span
                    title={p.status.last_error}
                    className="text-[10px] uppercase tracking-wide px-1.5 py-0.5 rounded bg-red-100 dark:bg-red-900/40 text-red-700 dark:text-red-300"
                  >
                    {t('settings.oidc.statusFailed')}
                  </span>
                ) : p.status?.state === 'ok' ? (
                  <span className="text-[10px] uppercase tracking-wide px-1.5 py-0.5 rounded bg-emerald-100 dark:bg-emerald-900/40 text-emerald-700 dark:text-emerald-300">
                    {t('settings.oidc.statusOk')}
                  </span>
                ) : null}
              </div>
              {p.status?.state === 'failed' && p.status.last_error && (
                <p className="text-xs text-red-600 dark:text-red-400 mt-1 break-all">
                  {p.status.last_error}
                </p>
              )}
            </div>
            <button
              onClick={() => remove(p.id)}
              disabled={saving}
              className="text-xs text-red-600 dark:text-red-400 hover:underline disabled:opacity-50 shrink-0"
            >
              {t('common.remove')}
            </button>
          </div>
        ))}

        {error && <p className="text-xs text-red-600 dark:text-red-400">{error}</p>}

        {showAdd ? (
          <AddProviderForm onAdd={add} onCancel={() => setShowAdd(false)} saving={saving} />
        ) : (
          <button
            onClick={() => setShowAdd(true)}
            className="text-sm text-blue-600 dark:text-blue-400 hover:underline"
          >
            {t('settings.oidc.addButton')}
          </button>
        )}
      </div>
    </section>
  )
}

function AddProviderForm({
  onAdd,
  onCancel,
  saving,
}: {
  onAdd: (cfg: OidcProviderConfig) => void
  onCancel: () => void
  saving: boolean
}) {
  const { t } = useTranslation()
  const [id, setId] = useState('')
  const [name, setName] = useState('')
  const [issuer, setIssuer] = useState('')
  const [clientId, setClientId] = useState('')
  const [clientSecret, setClientSecret] = useState('')
  const [scopes, setScopes] = useState('openid email profile')
  // Live callback URL preview — fetched once from the backend so we get the
  // same base URL Bindery itself will resolve at /login (honors the env var
  // / forwarded-headers precedence). Older backends don't have this endpoint;
  // fall back to window.location.origin which is the right answer for any
  // deploy not using a path prefix.
  const [redirectBase, setRedirectBase] = useState(() =>
    typeof window !== 'undefined' ? window.location.origin : ''
  )
  const [callbackTemplate, setCallbackTemplate] = useState('/api/v1/auth/oidc/{id}/callback')
  const [copied, setCopied] = useState(false)

  useEffect(() => {
    api.oidcRedirectBase()
      .then(rb => {
        if (rb.base) setRedirectBase(rb.base)
        if (rb.callback_path) setCallbackTemplate(rb.callback_path)
      })
      .catch(() => {})
  }, [])

  const trimmedId = id.trim()
  const callbackPreview = trimmedId
    ? redirectBase + callbackTemplate.replace('{id}', encodeURIComponent(trimmedId))
    : ''
  const copyCallback = async () => {
    if (!callbackPreview) return
    try {
      await navigator.clipboard.writeText(callbackPreview)
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    } catch {
      /* clipboard unavailable */
    }
  }

  const submit = (e: FormEvent) => {
    e.preventDefault()
    onAdd({
      id: id.trim(),
      name: name.trim(),
      issuer: issuer.trim(),
      client_id: clientId.trim(),
      client_secret: clientSecret.trim(),
      scopes: scopes.trim().split(/\s+/).filter(Boolean),
    })
  }

  return (
    <form onSubmit={submit} className="space-y-3 pt-2 border-t border-slate-200 dark:border-zinc-800">
      <p className="text-xs font-medium text-slate-700 dark:text-zinc-300">{t('settings.oidc.addHeading')}</p>
      <div className="grid grid-cols-2 gap-3">
        <div>
          <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">{t('settings.oidc.fieldId')}</label>
          <input value={id} onChange={e => setId(e.target.value)} required placeholder="google" className={inputCls} />
        </div>
        <div>
          <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">{t('settings.oidc.fieldName')}</label>
          <input value={name} onChange={e => setName(e.target.value)} required placeholder="Google" className={inputCls} />
        </div>
      </div>
      {/* Live preview of the redirect URI Bindery will register with the IdP.
          Eliminates the most common setup mistake: registering a URL that
          doesn't match what Bindery actually sends. */}
      <div>
        <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">
          {t('settings.oidc.fieldCallbackUrl')}
        </label>
        <div className="flex gap-2 items-center">
          <code className="flex-1 text-xs font-mono px-3 py-2 rounded bg-slate-50 dark:bg-zinc-950 border border-slate-300 dark:border-zinc-700 text-slate-600 dark:text-zinc-400 truncate min-w-0">
            {callbackPreview || (
              <span className="italic text-slate-400 dark:text-zinc-600">
                {t('settings.oidc.callbackUrlEmpty')}
              </span>
            )}
          </code>
          <button
            type="button"
            onClick={copyCallback}
            disabled={!callbackPreview}
            className="px-2 py-1.5 text-xs rounded border border-slate-300 dark:border-zinc-700 hover:bg-slate-100 dark:hover:bg-zinc-800 disabled:opacity-40 disabled:cursor-not-allowed shrink-0"
          >
            {copied ? t('settings.oidc.callbackUrlCopied') : t('settings.oidc.callbackUrlCopy')}
          </button>
        </div>
        <p className="text-[11px] text-slate-500 dark:text-zinc-500 mt-1">
          {t('settings.oidc.callbackUrlHint')}
        </p>
      </div>
      <IssuerField value={issuer} onChange={setIssuer} />

      <div className="grid grid-cols-2 gap-3">
        <div>
          <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">{t('settings.oidc.fieldClientId')}</label>
          <input value={clientId} onChange={e => setClientId(e.target.value)} required className={inputCls} />
        </div>
        <div>
          <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">{t('settings.oidc.fieldClientSecret')}</label>
          <input value={clientSecret} onChange={e => setClientSecret(e.target.value)} required type="password" className={inputCls} />
        </div>
      </div>
      <div>
        <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">{t('settings.oidc.fieldScopes')}</label>
        <input value={scopes} onChange={e => setScopes(e.target.value)} className={inputCls} />
      </div>
      <div className="flex gap-2 justify-end">
        <button type="button" onClick={onCancel} className="px-3 py-1.5 text-sm text-slate-600 dark:text-zinc-400">
          {t('common.cancel')}
        </button>
        <button
          type="submit"
          disabled={saving || !id || !name || !issuer || !clientId || !clientSecret}
          className="px-3 py-1.5 bg-emerald-600 hover:bg-emerald-500 rounded text-sm font-medium disabled:opacity-50"
        >
          {saving ? t('common.saving') : t('settings.oidc.addSave')}
        </button>
      </div>
    </form>
  )
}

// IssuerField wraps the issuer input with a "Test" button that probes the
// IdP's /.well-known/openid-configuration server-side and renders the result
// inline. Surfaces three classes of error before the user attempts a real
// login: unreachable IdP, malformed discovery doc, and (most importantly)
// issuer mismatch — where the discovered issuer differs from what the user
// entered. The mismatch case is the silent killer for Authentik per-provider
// mode and Keycloak realm paths.
type DiscoveryResult =
  | { state: 'idle' }
  | { state: 'probing' }
  | {
      state: 'ok'
      mismatch: boolean
      discovered: NonNullable<Awaited<ReturnType<typeof api.oidcTestDiscovery>>['discovered']>
    }
  | { state: 'error'; message: string }

function IssuerField({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  const { t } = useTranslation()
  const [result, setResult] = useState<DiscoveryResult>({ state: 'idle' })
  // Reset result whenever the user edits the issuer — a stale "ok" badge
  // attached to a different URL would be actively misleading.
  const handleChange = (v: string) => {
    onChange(v)
    if (result.state !== 'idle') setResult({ state: 'idle' })
  }
  const test = async () => {
    if (!value.trim()) return
    setResult({ state: 'probing' })
    try {
      const r = await api.oidcTestDiscovery(value.trim())
      if (!r.ok) {
        setResult({ state: 'error', message: r.error ?? t('settings.oidc.testDiscoveryUnknown') })
        return
      }
      setResult({
        state: 'ok',
        mismatch: r.issuer_mismatch === true,
        discovered: r.discovered!,
      })
    } catch (e) {
      setResult({ state: 'error', message: e instanceof Error ? e.message : String(e) })
    }
  }
  return (
    <div>
      <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">
        {t('settings.oidc.fieldIssuer')}
      </label>
      <div className="flex gap-2">
        <input
          value={value}
          onChange={e => handleChange(e.target.value)}
          required
          placeholder="https://accounts.google.com"
          className={inputCls}
        />
        <button
          type="button"
          onClick={test}
          disabled={!value.trim() || result.state === 'probing'}
          className="px-3 py-1.5 text-xs rounded border border-slate-300 dark:border-zinc-700 hover:bg-slate-100 dark:hover:bg-zinc-800 disabled:opacity-40 disabled:cursor-not-allowed shrink-0"
        >
          {result.state === 'probing' ? t('settings.oidc.testDiscoveryProbing') : t('settings.oidc.testDiscovery')}
        </button>
      </div>
      <DiscoveryResultView result={result} entered={value.trim()} />
    </div>
  )
}

function DiscoveryResultView({ result, entered }: { result: DiscoveryResult; entered: string }) {
  const { t } = useTranslation()
  if (result.state === 'idle' || result.state === 'probing') return null
  if (result.state === 'error') {
    return (
      <p className="text-xs text-red-600 dark:text-red-400 mt-1.5 break-all">
        {t('settings.oidc.testDiscoveryFail')}: {result.message}
      </p>
    )
  }
  const { discovered, mismatch } = result
  return (
    <div className="text-[11px] mt-1.5 space-y-0.5 text-slate-600 dark:text-zinc-400">
      {mismatch ? (
        <p className="text-amber-700 dark:text-amber-400 break-all">
          {t('settings.oidc.testDiscoveryMismatch', { entered, discovered: discovered.issuer })}
        </p>
      ) : (
        <p className="text-emerald-700 dark:text-emerald-400">{t('settings.oidc.testDiscoveryOk')}</p>
      )}
      <p className="break-all">
        <span className="text-slate-500 dark:text-zinc-500">authorize:</span>{' '}
        {discovered.authorization_endpoint}
      </p>
      <p className="break-all">
        <span className="text-slate-500 dark:text-zinc-500">token:</span> {discovered.token_endpoint}
      </p>
      {discovered.scopes_supported && discovered.scopes_supported.length > 0 && (
        <p className="break-all">
          <span className="text-slate-500 dark:text-zinc-500">scopes:</span>{' '}
          {discovered.scopes_supported.join(' ')}
        </p>
      )}
    </div>
  )
}
