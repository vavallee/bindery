import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { AuthProvider, useAuth } from './AuthContext'
import { apiUrl, server } from '../test/msw'
import { makeAuthStatus } from '../test-utils'

function AuthProbe() {
  const { status, loading, isAdmin, refresh, logout } = useAuth()

  return (
    <div>
      <div data-testid="loading">{String(loading)}</div>
      <div data-testid="status">
        {status ? `${status.authenticated ? 'authenticated' : 'anonymous'}:${status.mode}:${status.role ?? ''}` : 'none'}
      </div>
      <div data-testid="admin">{String(isAdmin)}</div>
      <button type="button" onClick={() => { void refresh() }}>Refresh</button>
      <button type="button" onClick={() => { void logout() }}>Logout</button>
    </div>
  )
}

function renderAuthProvider() {
  return render(
    <AuthProvider>
      <AuthProbe />
    </AuthProvider>,
  )
}

describe('AuthProvider', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    window.history.pushState(null, '', '/')
  })

  it('loads auth status on mount', async () => {
    let statusHits = 0
    server.use(
      http.get(apiUrl('/auth/status'), () => {
        statusHits += 1
        return HttpResponse.json(makeAuthStatus())
      }),
    )

    renderAuthProvider()

    expect(screen.getByTestId('loading')).toHaveTextContent('true')
    await waitFor(() => expect(screen.getByTestId('loading')).toHaveTextContent('false'))
    expect(screen.getByTestId('status')).toHaveTextContent('anonymous:enabled:')
    expect(statusHits).toBe(1)
  })

  it('hydrates CSRF when the loaded status is authenticated', async () => {
    let csrfHits = 0
    server.use(
      http.get(apiUrl('/auth/status'), () => HttpResponse.json(makeAuthStatus({
        authenticated: true,
        username: 'admin',
        role: 'admin',
      }))),
      http.get(apiUrl('/auth/csrf'), () => {
        csrfHits += 1
        return HttpResponse.json({ csrfToken: 'auth-token' })
      }),
    )

    renderAuthProvider()

    await screen.findByText('authenticated:enabled:admin')
    expect(screen.getByTestId('admin')).toHaveTextContent('true')
    expect(csrfHits).toBe(1)
  })

  it('does not hydrate CSRF for an unauthenticated status', async () => {
    let csrfHits = 0
    server.use(
      http.get(apiUrl('/auth/status'), () => HttpResponse.json(makeAuthStatus())),
      http.get(apiUrl('/auth/csrf'), () => {
        csrfHits += 1
        return HttpResponse.json({ csrfToken: 'unused' })
      }),
    )

    renderAuthProvider()

    await screen.findByText('anonymous:enabled:')
    expect(csrfHits).toBe(0)
  })

  it('clears status and ends loading when status loading fails', async () => {
    server.use(
      http.get(apiUrl('/auth/status'), () => HttpResponse.json({ error: 'offline' }, { status: 503 })),
    )

    renderAuthProvider()

    await waitFor(() => expect(screen.getByTestId('loading')).toHaveTextContent('false'))
    expect(screen.getByTestId('status')).toHaveTextContent('none')
  })

  it('refreshes auth status when the document becomes visible', async () => {
    let statusHits = 0
    server.use(
      http.get(apiUrl('/auth/status'), () => {
        statusHits += 1
        return HttpResponse.json(makeAuthStatus())
      }),
    )

    renderAuthProvider()

    await waitFor(() => expect(statusHits).toBe(1))
    document.dispatchEvent(new Event('visibilitychange'))

    await waitFor(() => expect(statusHits).toBe(2))
  })

  it('logs out through the API and refreshes local status', async () => {
    let statusHits = 0
    let logoutHits = 0
    server.use(
      http.get(apiUrl('/auth/status'), () => {
        statusHits += 1
        return HttpResponse.json(
          statusHits === 1
            ? makeAuthStatus({ authenticated: true, username: 'admin', role: 'admin' })
            : makeAuthStatus(),
        )
      }),
      http.get(apiUrl('/auth/csrf'), () => HttpResponse.json({ csrfToken: 'logout-token' })),
      http.post(apiUrl('/auth/logout'), () => {
        logoutHits += 1
        return HttpResponse.json({ ok: true })
      }),
    )
    renderAuthProvider()

    await screen.findByText('authenticated:enabled:admin')
    window.history.pushState(null, '', '/login')
    fireEvent.click(screen.getByRole('button', { name: 'Logout' }))

    await waitFor(() => expect(logoutHits).toBe(1))
    await waitFor(() => expect(statusHits).toBe(2))
    expect(screen.getByTestId('status')).toHaveTextContent('anonymous:enabled:')
  })
})
