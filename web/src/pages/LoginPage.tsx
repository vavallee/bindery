import { FormEvent, useEffect, useState } from 'react'
import { Navigate, useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { api, BINDERY_BASE, OidcProvider } from '../api/client'
import { useAuth } from '../auth/AuthContext'
import Logo from '../components/Logo'

export default function LoginPage() {
  const { t } = useTranslation()
  const [error, setError] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [oidcProviders, setOidcProviders] = useState<OidcProvider[]>([])
  const { status, refresh } = useAuth()
  const navigate = useNavigate()

  useEffect(() => {
    api.oidcProviders()
      // Hide providers that failed startup discovery so users don't click
      // into a flow that's guaranteed to error. Older backends omit `status`
      // entirely — fall through and show those buttons unfiltered.
      .then(ps => setOidcProviders(ps.filter(p => !p.status || p.status.state === 'ok')))
      .catch(() => setOidcProviders([]))
  }, [])

  // Read values from the form at submit time instead of React state.
  // Browser autofill sets input.value directly without firing onChange, which
  // would leave a controlled-component state empty and silently block submit.
  const submit = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault()
    const data = new FormData(e.currentTarget)
    const username = String(data.get('username') || '').trim()
    const password = String(data.get('password') || '')
    const rememberMe = data.get('rememberMe') === 'on'
    if (!username || !password) {
      setError(t('login.errorRequired'))
      return
    }
    setError('')
    setSubmitting(true)
    try {
      await api.authLogin(username, password, rememberMe)
      await refresh()
      navigate('/', { replace: true })
    } catch (e: unknown) {
      const msg = e instanceof Error ? e.message : t('login.errorFailed')
      setError(msg)
    } finally {
      setSubmitting(false)
    }
  }

  if (status?.setupRequired) {
    return <Navigate to="/setup" replace />
  }
  if (status?.authenticated) {
    return <Navigate to="/" replace />
  }

  const localAuthEnabled = status?.localAuthEnabled !== false

  if (status?.mode === 'proxy') {
    return (
      <CardShell title={t('login.title')} subtitle="">
        <p className="text-sm text-slate-600 dark:text-zinc-400 text-center py-2">
          {t('login.proxyHint')}
        </p>
      </CardShell>
    )
  }

  return (
    <CardShell title={t('login.title')} subtitle="">
      {oidcProviders.length > 0 && (
        <div className="space-y-2 mb-4">
          {oidcProviders.map(p => (
            <a
              key={p.id}
              href={`${BINDERY_BASE}/api/v1/auth/oidc/${encodeURIComponent(p.id)}/login`}
              className="flex items-center justify-center w-full border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-800 hover:bg-slate-50 dark:hover:bg-zinc-700 rounded-md py-2 text-sm font-medium transition-colors"
            >
              {t('login.signInWith', { name: p.name })}
            </a>
          ))}
          {localAuthEnabled && (
            <div className="relative flex items-center gap-3 py-1">
              <div className="flex-1 border-t border-slate-200 dark:border-zinc-800" />
              <span className="text-xs text-slate-500 dark:text-zinc-500">{t('login.orLocal')}</span>
              <div className="flex-1 border-t border-slate-200 dark:border-zinc-800" />
            </div>
          )}
        </div>
      )}
      {localAuthEnabled ? (
      <form onSubmit={submit} method="post" className="space-y-4">
        <Field label={t('login.username')}>
          <input
            type="text"
            name="username"
            id="username"
            autoComplete="username"
            autoFocus
            required
            className="w-full bg-white dark:bg-zinc-900 border border-slate-300 dark:border-zinc-700 rounded-md px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
          />
        </Field>
        <Field label={t('login.password')}>
          <input
            type="password"
            name="password"
            id="password"
            autoComplete="current-password"
            required
            className="w-full bg-white dark:bg-zinc-900 border border-slate-300 dark:border-zinc-700 rounded-md px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
          />
        </Field>
        <label className="flex items-center gap-2 text-sm text-slate-600 dark:text-zinc-400">
          <input
            type="checkbox"
            name="rememberMe"
            defaultChecked
            className="rounded"
          />
          {t('login.rememberMe')}
        </label>
        {error && (
          <div className="text-sm text-red-600 dark:text-red-400 py-1">{error}</div>
        )}
        <button
          type="submit"
          disabled={submitting}
          className="w-full bg-blue-600 hover:bg-blue-700 disabled:opacity-50 disabled:cursor-not-allowed text-white font-medium rounded-md py-2 text-sm transition-colors"
        >
          {submitting ? t('login.submitting') : t('login.submit')}
        </button>
      </form>
      ) : oidcProviders.length === 0 ? (
        <p className="text-sm text-slate-600 dark:text-zinc-400 text-center py-2">
          {t('login.contactAdmin')}
        </p>
      ) : null}
    </CardShell>
  )
}

export function CardShell({ title, subtitle, children }: { title: string; subtitle: string; children: React.ReactNode }) {
  return (
    <div className="min-h-screen flex items-center justify-center px-4 bg-slate-50 dark:bg-zinc-950 text-slate-900 dark:text-zinc-100">
      <div className="w-full max-w-sm">
        <div className="mb-6 text-center">
          <div className="flex items-center justify-center gap-2">
            <Logo className="w-12 h-12 rounded-full" />
            <h1 className="text-2xl font-bold tracking-tight">Bindery</h1>
          </div>
          {subtitle && <div className="text-xs text-slate-500 dark:text-zinc-500 mt-2">{subtitle}</div>}
        </div>
        <div className="border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 rounded-lg p-6 shadow-sm">
          <h2 className="text-lg font-semibold mb-4">{title}</h2>
          {children}
        </div>
      </div>
    </div>
  )
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="block">
      <span className="block text-xs font-medium text-slate-600 dark:text-zinc-400 mb-1">{label}</span>
      {children}
    </label>
  )
}
