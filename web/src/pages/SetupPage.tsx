import { FormEvent, useState } from 'react'
import { useNavigate } from 'react-router'
import { useTranslation } from 'react-i18next'
import { api } from '../api/client'
import { useAuth } from '../auth/AuthContext'
import { CardShell } from './LoginPage'
import { literalInputAttrs } from '../util/inputAttrs'

export default function SetupPage() {
  const { t } = useTranslation()
  const [error, setError] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const { refresh } = useAuth()
  const navigate = useNavigate()

  // Read values from the form at submit time. See LoginPage for rationale —
  // controlled inputs break when browser autofill bypasses React onChange.
  const submit = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault()
    const data = new FormData(e.currentTarget)
    const username = String(data.get('username') || '').trim()
    const password = String(data.get('password') || '')
    const confirm = String(data.get('confirm') || '')
    setError('')
    if (!username) {
      setError(t('setup.errorUsernameRequired'))
      return
    }
    if (password !== confirm) {
      setError(t('setup.errorPasswordMatch'))
      return
    }
    if (password.length < 8) {
      setError(t('setup.errorPasswordLength'))
      return
    }
    setSubmitting(true)
    try {
      await api.authSetup(username, password)
      await refresh()
      navigate('/', { replace: true })
    } catch (e: unknown) {
      const msg = e instanceof Error ? e.message : t('setup.errorFailed')
      setError(msg)
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <CardShell title={t('setup.title')} subtitle={t('setup.subtitle')}>
      <p className="text-xs text-slate-500 dark:text-zinc-500 mb-4 leading-relaxed">
        {t('setup.description')}
      </p>
      <form onSubmit={submit} className="space-y-4">
        <label className="block">
          <span className="block text-xs font-medium text-slate-600 dark:text-zinc-400 mb-1">{t('setup.username')}</span>
          <input
            {...literalInputAttrs}
            type="text"
            name="username"
            id="username"
            autoComplete="username"
            defaultValue="admin"
            required
            className="w-full bg-white dark:bg-zinc-900 border border-slate-300 dark:border-zinc-700 rounded-md px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
          />
        </label>
        <label className="block">
          <span className="block text-xs font-medium text-slate-600 dark:text-zinc-400 mb-1">{t('setup.password')}</span>
          <input
            type="password"
            name="password"
            id="password"
            autoComplete="new-password"
            required
            className="w-full bg-white dark:bg-zinc-900 border border-slate-300 dark:border-zinc-700 rounded-md px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
          />
        </label>
        <label className="block">
          <span className="block text-xs font-medium text-slate-600 dark:text-zinc-400 mb-1">{t('setup.confirmPassword')}</span>
          <input
            type="password"
            name="confirm"
            id="confirm-password"
            autoComplete="new-password"
            required
            className="w-full bg-white dark:bg-zinc-900 border border-slate-300 dark:border-zinc-700 rounded-md px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
          />
        </label>
        {error && (
          <div className="text-sm text-red-600 dark:text-red-400 py-1">{error}</div>
        )}
        <button
          type="submit"
          disabled={submitting}
          className="w-full bg-blue-600 hover:bg-blue-700 disabled:opacity-50 disabled:cursor-not-allowed text-white font-medium rounded-md py-2 text-sm transition-colors"
        >
          {submitting ? t('setup.submitting') : t('setup.submit')}
        </button>
      </form>
    </CardShell>
  )
}
