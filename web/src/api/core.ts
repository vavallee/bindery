// Shared API core: base-url logic, CSRF state, the request()/uploadFile()
// helpers, and the ApiError class. This is the private foundation the domain
// modules (books.ts, authors.ts, …) build on. client.ts re-exports the public
// pieces of this module (ApiError, BINDERY_BASE, isNoDownloadClientError,
// initCSRF) so the historical import surface is unchanged.

// Path prefix injected by the backend at serve time (empty for root-mounted
// deploys). Read once at module load so all API calls and redirects use a
// consistent value throughout the session.
export const BINDERY_BASE: string = (window as unknown as { __BINDERY_BASE__?: string }).__BINDERY_BASE__ ?? ''
const BASE = `${BINDERY_BASE}/api/v1`

// Pages that render before the user is authenticated — reaching /auth/status
// will 401 in enabled mode before setup, which is expected, and we must not
// try to redirect to /login from the login/setup pages themselves.
const PUBLIC_PATHS = new Set([`${BINDERY_BASE}/login`, `${BINDERY_BASE}/setup`])

// CSRF double-submit token, fetched once on init and refreshed after login.
// Held in this single module so request()/uploadFile() and the auth methods
// (which mutate it via setCSRFToken/clearCSRFToken) all share one source of
// truth — preserving the original module-closure behaviour after the split.
let csrfToken = ''

// Read from the bindery_csrf cookie (set by GET /auth/csrf).
function readCSRFCookie(): string {
  const m = document.cookie.match(/(?:^|;\s*)bindery_csrf=([^;]+)/)
  return m ? decodeURIComponent(m[1]) : ''
}

// CSRF mutator used by the auth domain module after logout.
export function clearCSRFToken(): void {
  csrfToken = ''
}

export async function initCSRF(): Promise<void> {
  try {
    const res = await fetch(`${BASE}/auth/csrf`, {
      credentials: 'include',
      headers: { 'X-Requested-With': 'bindery-ui' },
    })
    if (res.ok) {
      const data = await res.json()
      csrfToken = data.csrfToken || readCSRFCookie()
    }
  } catch {
    // Non-fatal — mutations will fail with 403 if still missing.
  }
}

const SAFE_METHODS = new Set(['GET', 'HEAD', 'OPTIONS'])

// Error thrown for any non-OK API response. Carries the HTTP status and the
// parsed JSON body so callers can branch on structured fields (e.g. a 409
// rebind conflict exposing `force_required`). Stays an `Error` subclass with a
// human-readable `.message`, so callers that only read `.message` are
// unaffected.
export class ApiError extends Error {
  status: number
  body: { error?: string; [key: string]: unknown }

  constructor(status: number, body: { error?: string; [key: string]: unknown }, fallback: string) {
    super(body.error || fallback)
    this.name = 'ApiError'
    this.status = status
    this.body = body
  }
}

// True when an error is the backend's "no enabled download client configured"
// failure from a grab attempt (#959 / internal/api/queue.go). Detected by the
// same substring pair the backend uses to classify it as a 400, so a missing
// download client surfaces a contextual setup nudge instead of a raw error —
// independent of whether the library is empty (#968). Tolerant of the protocol
// variants noProtocolClientError produces ("no enabled usenet download client
// configured …", etc).
export function isNoDownloadClientError(err: unknown): boolean {
  const msg = err instanceof Error ? err.message : ''
  return msg.includes('no enabled') && msg.includes('download client configured')
}

export async function request<T>(path: string, options?: RequestInit): Promise<T> {
  // Merge caller-supplied headers on top of the defaults so we can't lose
  // the CSRF header if a caller passes their own `headers`.
  const headers = new Headers({
    'Content-Type': 'application/json',
    'X-Requested-With': 'bindery-ui',
  })
  const method = (options?.method ?? 'GET').toUpperCase()
  if (!SAFE_METHODS.has(method) && csrfToken) {
    headers.set('X-CSRF-Token', csrfToken)
  }
  if (options?.headers) {
    new Headers(options.headers).forEach((v, k) => headers.set(k, v))
  }
  const res = await fetch(`${BASE}${path}`, {
    credentials: 'include', // send + accept the session cookie
    ...options,
    headers,
  })
  if (res.status === 401 && !PUBLIC_PATHS.has(window.location.pathname)) {
    // Session expired or missing — punt to login. The router there will
    // bounce to /setup if no user exists yet.
    window.location.href = `${BINDERY_BASE}/login`
    throw new Error('unauthorized')
  }
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: res.statusText }))
    throw new ApiError(res.status, err, res.statusText)
  }
  if (res.status === 204) return undefined as unknown as T
  return res.json()
}

// Upload a file (multipart/form-data). Omits Content-Type so the browser
// sets the boundary automatically, but still injects the CSRF token.
export async function uploadFile<T>(path: string, body: FormData): Promise<T> {
  const headers = new Headers({
    'X-Requested-With': 'bindery-ui',
  })
  if (csrfToken) {
    headers.set('X-CSRF-Token', csrfToken)
  }
  const res = await fetch(`${BASE}${path}`, {
    method: 'POST',
    credentials: 'include',
    headers,
    body,
  })
  if (res.status === 401 && !PUBLIC_PATHS.has(window.location.pathname)) {
    window.location.href = `${BINDERY_BASE}/login`
    return Promise.reject(new Error('unauthenticated'))
  }
  if (!res.ok) {
    const data = await res.json().catch(() => ({ error: res.statusText }))
    throw new ApiError(res.status, data, `HTTP ${res.status}`)
  }
  return res.json() as Promise<T>
}
