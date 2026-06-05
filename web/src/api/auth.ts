import { request, initCSRF, clearCSRFToken } from './core'

export interface AuthStatus {
  authenticated: boolean
  setupRequired: boolean
  username?: string
  role?: string
  mode: 'enabled' | 'local-only' | 'disabled' | 'proxy'
  localAuthEnabled: boolean
}

export interface ManagedUser {
  id: number
  username: string
  role: string
  email?: string
  displayName?: string
  createdAt: string
}

export interface AuthConfig {
  mode: 'enabled' | 'local-only' | 'disabled' | 'proxy'
  apiKey: string
  username: string
}

export interface OidcProviderStatus {
  state: 'ok' | 'failed'
  last_error?: string
  last_attempt?: string
}

export interface OidcProvider {
  id: string
  name: string
  // Optional runtime status. Present on responses from a backend that
  // tracks failed-discovery state; absent for older backends.
  status?: OidcProviderStatus
}

export interface OidcProviderConfig {
  id: string
  name: string
  issuer: string
  client_id: string
  client_secret: string
  scopes: string[]
}

export const authApi = {
  // Auth
  authStatus: () => request<AuthStatus>('/auth/status'),
  oidcProviders: () => request<OidcProvider[]>('/auth/oidc/providers'),
  oidcSetProviders: (providers: OidcProviderConfig[]) =>
    request<void>('/auth/oidc/providers', { method: 'PUT', body: JSON.stringify(providers) }),
  // Returns the public base URL Bindery will use as the prefix for OIDC
  // callback URLs (resolved from the current request) plus the path template
  // with `{id}` placeholder. The settings UI uses these to live-render the
  // redirect URI as the admin types the provider id.
  oidcRedirectBase: () => request<{ base: string; callback_path: string; configured: boolean }>('/auth/oidc/redirect-base'),
  // Probes <issuer>/.well-known/openid-configuration server-side. ok=false
  // means the IdP is unreachable / wrong / not OIDC; the error string is
  // safe to render directly. issuer_mismatch=true is the silent killer for
  // Authentik per-provider mode and Keycloak realms.
  oidcTestDiscovery: (issuer: string) =>
    request<{
      ok: boolean
      error?: string
      issuer_mismatch?: boolean
      discovered?: {
        issuer: string
        authorization_endpoint: string
        token_endpoint: string
        userinfo_endpoint?: string
        jwks_uri?: string
        scopes_supported?: string[]
      }
    }>('/auth/oidc/test-discovery', { method: 'POST', body: JSON.stringify({ issuer }) }),
  authLogin: async (username: string, password: string, rememberMe: boolean) => {
    const res = await request<{ ok: boolean; username: string }>('/auth/login', {
      method: 'POST',
      body: JSON.stringify({ username, password, rememberMe }),
    })
    await initCSRF()
    return res
  },
  authLogout: async () => {
    const res = await request<{ ok: boolean }>('/auth/logout', { method: 'POST' })
    clearCSRFToken()
    return res
  },
  authSetup: (username: string, password: string) =>
    request<{ ok: boolean }>('/auth/setup', {
      method: 'POST',
      body: JSON.stringify({ username, password }),
    }),
  authConfig: () => request<AuthConfig>('/auth/config'),
  authChangePassword: (currentPassword: string, newPassword: string) =>
    request<{ ok: boolean }>('/auth/password', {
      method: 'POST',
      body: JSON.stringify({ currentPassword, newPassword }),
    }),
  authRegenerateApiKey: () =>
    request<{ apiKey: string }>('/auth/apikey/regenerate', { method: 'POST' }),
  authRotateSessionSecret: () =>
    request<{ ok: boolean }>('/auth/session-secret/rotate', { method: 'POST' }),
  authSetMode: (mode: AuthStatus['mode']) =>
    request<{ mode: string }>('/auth/mode', {
      method: 'PUT',
      body: JSON.stringify({ mode }),
    }),
  listUsers: () => request<ManagedUser[]>('/auth/users'),
  createUser: (username: string, password: string, role: string) =>
    request<ManagedUser>('/auth/users', { method: 'POST', body: JSON.stringify({ username, password, role }) }),
  deleteUser: (id: number) => request<{ ok: boolean }>(`/auth/users/${id}`, { method: 'DELETE' }),
  setUserRole: (id: number, role: string) =>
    request<{ ok: boolean }>(`/auth/users/${id}/role`, { method: 'PUT', body: JSON.stringify({ role }) }),
  resetUserPassword: (id: number, password: string) =>
    request<{ ok: boolean }>(`/auth/users/${id}/reset-password`, { method: 'PUT', body: JSON.stringify({ password }) }),
}
