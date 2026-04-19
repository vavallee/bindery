import { FormEvent, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { api } from '../api/client'
import { useAuth } from '../auth/AuthContext'
import Logo from '../components/Logo'

export default function LoginPage() {
  const { t } = useTranslation()
  const [error, setError] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const { status, refresh } = useAuth()
  const navigate = useNavigate()

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
