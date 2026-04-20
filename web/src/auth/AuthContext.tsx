import { createContext, useContext, useEffect, useState, ReactNode, useCallback } from 'react'
import { api, AuthStatus, initCSRF } from '../api/client'

interface AuthContextValue {
  status: AuthStatus | null
  loading: boolean
  isAdmin: boolean
  refresh: () => Promise<void>
  logout: () => Promise<void>
}

const AuthContext = createContext<AuthContextValue | null>(null)

export function AuthProvider({ children }: { children: ReactNode }) {
  const [status, setStatus] = useState<AuthStatus | null>(null)
  const [loading, setLoading] = useState(true)

  const refresh = useCallback(async () => {
    try {
      const s = await api.authStatus()
      setStatus(s)
      // Re-hydrate CSRF token after page reload: authLogin() calls initCSRF,
      // but a subsequent reload keeps the session cookie without the token
      // in JS memory, so mutating requests would 403 until the next login.
      if (s.authenticated) {
        await initCSRF()
      }
    } catch {
      setStatus(null)
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => { refresh() }, [refresh])

  useEffect(() => {
    const onVisible = () => { if (document.visibilityState === 'visible') refresh() }
    document.addEventListener('visibilitychange', onVisible)
    return () => document.removeEventListener('visibilitychange', onVisible)
  }, [refresh])

  const logout = useCallback(async () => {
    try { await api.authLogout() } catch { /* ignore — we're clearing state anyway */ }
    await refresh()
    window.location.href = '/login'
  }, [refresh])

  const isAdmin = status?.role === 'admin'

  return (
    <AuthContext.Provider value={{ status, loading, isAdmin, refresh, logout }}>
      {children}
    </AuthContext.Provider>
  )
}

export function useAuth() {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error('useAuth must be used inside <AuthProvider>')
  return ctx
}
