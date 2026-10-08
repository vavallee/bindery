import { ReactNode, useCallback, useEffect, useState } from 'react'
import { Navigate, useLocation } from 'react-router'
import { useTranslation } from 'react-i18next'
import { useAuth } from './AuthContext'

// AuthGuard wraps the main app. Decision tree:
//
//   loading → render a quiet placeholder
//   no status because the check failed → offer a retry, not the login page
//   setup required → force /setup
//   not authenticated → force /login
//   authenticated → render children
//
// /login and /setup render outside of the guard (they're routed above it),
// so they never get bounced by their own redirects.
export default function AuthGuard({ children }: { children: ReactNode }) {
  const { status, loading, statusError, refresh } = useAuth()
  const location = useLocation()

  if (loading) {
    return (
      <div className="min-h-screen flex items-center justify-center text-slate-500 dark:text-zinc-500 text-sm">
        Loading…
      </div>
    )
  }

  // The status check failed before any status loaded. That says nothing
  // about the session, so do not send the user to sign in again.
  if (!status && statusError) {
    return <ServerUnavailable refresh={refresh} />
  }
  if (status?.setupRequired && location.pathname !== '/setup') {
    return <Navigate to="/setup" replace />
  }
  if (!status?.authenticated && !status?.setupRequired && location.pathname !== '/login') {
    return <Navigate to="/login" replace />
  }

  return <>{children}</>
}

// Retry delays grow from 2s and level off at 30s.
const FIRST_RETRY_MS = 2000
const MAX_RETRY_MS = 30000

function retryDelayMs(attempt: number): number {
  return Math.min(FIRST_RETRY_MS * 2 ** attempt, MAX_RETRY_MS)
}

// ServerUnavailable keeps asking for the auth status, backing off while the
// server stays unreachable. It unmounts once a status loads, which stops the
// timer. The button retries at once and shows that a check is in flight.
function ServerUnavailable({ refresh }: { refresh: () => Promise<void> }) {
  const { t } = useTranslation()
  const [attempt, setAttempt] = useState(0)
  const [busy, setBusy] = useState(false)

  const retry = useCallback(async () => {
    setBusy(true)
    try {
      await refresh()
    } finally {
      setBusy(false)
      setAttempt((a) => a + 1)
    }
  }, [refresh])

  useEffect(() => {
    if (busy) return
    const id = window.setTimeout(() => { void retry() }, retryDelayMs(attempt))
    return () => window.clearTimeout(id)
  }, [attempt, busy, retry])

  return (
    <div className="min-h-screen flex flex-col items-center justify-center gap-3 px-4 text-center text-slate-500 dark:text-zinc-500 text-sm">
      <p>{t('common.serverUnavailable', 'Bindery is not answering right now.')}</p>
      <button
        type="button"
        onClick={() => { void retry() }}
        disabled={busy}
        aria-busy={busy}
        className="rounded px-3 py-1.5 border border-slate-300 dark:border-zinc-700 text-slate-700 dark:text-zinc-300 disabled:opacity-60"
      >
        {busy ? t('common.retrying', 'Retrying…') : t('common.retry', 'Retry')}
      </button>
    </div>
  )
}
