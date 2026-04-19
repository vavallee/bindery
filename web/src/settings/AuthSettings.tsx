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
      setDisplayed(ps => [...ps, { id: cfg.id, name: cfg.name }])
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
          <div key={p.id} className="flex items-center justify-between py-2 border-b border-slate-200 dark:border-zinc-800 last:border-0">
            <div>
              <span className="text-sm font-medium text-slate-800 dark:text-zinc-200">{p.name}</span>
            </div>
            <button
              onClick={() => remove(p.id)}
              disabled={saving}
              className="text-xs text-red-600 dark:text-red-400 hover:underline disabled:opacity-50"
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
      <div>
        <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">{t('settings.oidc.fieldIssuer')}</label>
        <input value={issuer} onChange={e => setIssuer(e.target.value)} required placeholder="https://accounts.google.com" className={inputCls} />
      </div>
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
