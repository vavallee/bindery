import { beforeEach, describe, expect, it, vi } from 'vitest'
import { http, HttpResponse } from 'msw'
import { apiUrl, server } from '../test/msw'

async function loadClient() {
  vi.resetModules()
  return import('./client')
}

function clearCSRFCookie() {
  document.cookie = 'bindery_csrf=; Max-Age=0; path=/'
}

describe('api/client CSRF handling', () => {
  beforeEach(() => {
    clearCSRFCookie()
    window.history.pushState(null, '', '/')
  })

  it('fetches and stores the JSON CSRF token for mutating requests', async () => {
    const csrfRequests: Array<{ credentials: RequestCredentials; requestedWith: string | null }> = []
    let logoutCSRF: string | null = null

    server.use(
      http.get(apiUrl('/auth/csrf'), ({ request }) => {
        csrfRequests.push({
          credentials: request.credentials,
          requestedWith: request.headers.get('x-requested-with'),
        })
        return HttpResponse.json({ csrfToken: 'json-token' })
      }),
      http.post(apiUrl('/auth/logout'), ({ request }) => {
        logoutCSRF = request.headers.get('x-csrf-token')
        return HttpResponse.json({ ok: true })
      }),
    )

    const { api, initCSRF } = await loadClient()

    await initCSRF()
    await api.authLogout()

    expect(csrfRequests).toEqual([{ credentials: 'include', requestedWith: 'bindery-ui' }])
    expect(logoutCSRF).toBe('json-token')
  })

  it('does not send a CSRF token on safe requests', async () => {
    let authStatusCSRF: string | null = null

    server.use(
      http.get(apiUrl('/auth/csrf'), () => HttpResponse.json({ csrfToken: 'safe-token' })),
      http.get(apiUrl('/auth/status'), ({ request }) => {
        authStatusCSRF = request.headers.get('x-csrf-token')
        return HttpResponse.json({
          authenticated: true,
          setupRequired: false,
          username: 'admin',
          role: 'admin',
          mode: 'enabled',
        })
      }),
    )

    const { api, initCSRF } = await loadClient()

    await initCSRF()
    await api.authStatus()

    expect(authStatusCSRF).toBeNull()
  })

  it('refreshes CSRF after login for the next mutation', async () => {
    let loginBody: unknown
    let logoutCSRF: string | null = null

    server.use(
      http.post(apiUrl('/auth/login'), async ({ request }) => {
        loginBody = await request.json()
        return HttpResponse.json({ ok: true, username: 'alice' })
      }),
      http.get(apiUrl('/auth/csrf'), () => HttpResponse.json({ csrfToken: 'post-login-token' })),
      http.post(apiUrl('/auth/logout'), ({ request }) => {
        logoutCSRF = request.headers.get('x-csrf-token')
        return HttpResponse.json({ ok: true })
      }),
    )

    const { api } = await loadClient()

    await api.authLogin('alice', 'secret', true)
    await api.authLogout()

    expect(loginBody).toEqual({ username: 'alice', password: 'secret', rememberMe: true })
    expect(logoutCSRF).toBe('post-login-token')
  })

  it('falls back to the CSRF cookie when the endpoint omits a token', async () => {
    let logoutCSRF: string | null = null
    document.cookie = 'bindery_csrf=cookie-token; path=/'

    server.use(
      http.get(apiUrl('/auth/csrf'), () => HttpResponse.json({})),
      http.post(apiUrl('/auth/logout'), ({ request }) => {
        logoutCSRF = request.headers.get('x-csrf-token')
        return HttpResponse.json({ ok: true })
      }),
    )

    const { api, initCSRF } = await loadClient()

    await initCSRF()
    await api.authLogout()

    expect(logoutCSRF).toBe('cookie-token')
  })
})
