import { ReactNode } from 'react'
import { Navigate } from 'react-router-dom'
import { useAuth } from './AuthContext'

// PublicOnlyRoute is the inverse of AuthGuard: it wraps routes that should
// only ever render for users who aren't yet signed in (e.g. /login, /setup).
// Decision tree mirrors AuthGuard:
//
//   loading → render the same quiet placeholder (avoids a login-page flash on
//             refresh while /api/v1/auth/status is in flight)
//   setup required & route is not /setup → force /setup
//   authenticated & setup not required → redirect home
//   otherwise → render children
//
// `mode`:
//   'login'  — redirect home if already authenticated
//   'setup'  — redirect home once setup is no longer required (i.e. an admin
//              account exists), or to /setup if we're on /login but setup is
//              still pending. The latter is already handled by AuthGuard for
//              authed routes; here we keep /setup itself stable.
export default function PublicOnlyRoute({
  children,
  mode,
}: {
  children: ReactNode
  mode: 'login' | 'setup'
}) {
  const { status, loading } = useAuth()

  if (loading) {
    return (
      <div className="min-h-screen flex items-center justify-center text-slate-500 dark:text-zinc-500 text-sm">
        Loading…
      </div>
    )
  }

  if (mode === 'login') {
    if (status?.setupRequired) {
      return <Navigate to="/setup" replace />
    }
    if (status?.authenticated) {
      return <Navigate to="/" replace />
    }
  }

  if (mode === 'setup') {
    // Setup page should disappear once setup is complete. If the user is
    // already authenticated or setup is no longer required, bounce home.
    if (!status?.setupRequired) {
      return <Navigate to="/" replace />
    }
  }

  return <>{children}</>
}
