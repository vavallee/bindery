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

describe('ApiError message extraction', () => {
  it('surfaces a `message`-shaped failure body instead of the HTTP status text (#1431)', async () => {
    server.use(
      http.post(apiUrl('/grimmory/test'), () =>
        HttpResponse.json(
          { ok: false, message: 'grimmory login: dial tcp 192.168.1.5:6060: connection refused' },
          { status: 502 },
        ),
      ),
    )

    const { api } = await loadClient()

    await expect(api.grimmoryTest({ baseUrl: 'http://192.168.1.5:6060' })).rejects.toThrow(
      /connection refused/,
    )
  })

  it('prefers `error` over `message` when both are present', async () => {
    server.use(
      http.post(apiUrl('/grimmory/test'), () =>
        HttpResponse.json({ error: 'primary', message: 'secondary' }, { status: 502 }),
      ),
    )

    const { api } = await loadClient()

    await expect(api.grimmoryTest()).rejects.toThrow('primary')
  })
})

describe('listAllBooks', () => {
  it('forwards authorId/includeExcluded and pages until the full catalogue is collected (#1467)', async () => {
    const requests: Array<{ authorId: string | null; includeExcluded: string | null; limit: number; offset: number }> = []
    const total = 230

    server.use(
      http.get(apiUrl('/book'), ({ request }) => {
        const url = new URL(request.url)
        const limit = Number(url.searchParams.get('limit'))
        const offset = Number(url.searchParams.get('offset'))
        requests.push({
          authorId: url.searchParams.get('authorId'),
          includeExcluded: url.searchParams.get('includeExcluded'),
          limit,
          offset,
        })
        const count = Math.max(0, Math.min(limit, total - offset))
        const items = Array.from({ length: count }, (_, i) => ({ id: offset + i + 1, title: `Book ${offset + i + 1}` }))
        return HttpResponse.json({ items, total, limit, offset })
      }),
    )

    const { api } = await loadClient()

    const books = await api.listAllBooks({ authorId: 42, includeExcluded: true })

    // 230 books at a 500 page size: one request would suffice, but the loop
    // must have asked for the author's rows with the filters intact.
    expect(books).toHaveLength(total)
    expect(books[0].id).toBe(1)
    expect(books[total - 1].id).toBe(total)
    expect(requests.every(r => r.authorId === '42' && r.includeExcluded === 'true')).toBe(true)
    expect(requests[0].offset).toBe(0)
  })

  it('keeps fetching pages while the server returns partial pages', async () => {
    // Server caps the page size below the client's 500 request — the loop must
    // keep advancing by the *returned* count until total is reached.
    const total = 250
    const serverCap = 100
    const offsets: number[] = []

    server.use(
      http.get(apiUrl('/book'), ({ request }) => {
        const url = new URL(request.url)
        const offset = Number(url.searchParams.get('offset'))
        offsets.push(offset)
        const count = Math.max(0, Math.min(serverCap, total - offset))
        const items = Array.from({ length: count }, (_, i) => ({ id: offset + i + 1, title: `Book ${offset + i + 1}` }))
        return HttpResponse.json({ items, total, limit: serverCap, offset })
      }),
    )

    const { api } = await loadClient()

    const books = await api.listAllBooks({ authorId: 7 })

    expect(books).toHaveLength(total)
    expect(offsets).toEqual([0, 100, 200])
  })
})
