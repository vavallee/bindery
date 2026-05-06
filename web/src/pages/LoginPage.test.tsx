import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, screen, waitFor } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import LoginPage from './LoginPage'
import { apiUrl, server } from '../test/msw'
import { makeAuthStatus, renderWithRouter } from '../test-utils'

const { navigateMock, refreshMock, authState } = vi.hoisted(() => ({
  navigateMock: vi.fn(),
  refreshMock: vi.fn(),
  authState: { status: null as unknown },
}))

vi.mock('react-router-dom', async importOriginal => {
  const actual = await importOriginal<typeof import('react-router-dom')>()
  return {
    ...actual,
    useNavigate: () => navigateMock,
  }
})

vi.mock('../auth/AuthContext', () => ({
  useAuth: () => ({
    status: authState.status,
    refresh: refreshMock,
  }),
}))

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, options?: Record<string, unknown>) => {
      if (key === 'login.signInWith') return `Sign in with ${String(options?.name ?? '')}`
      const strings: Record<string, string> = {
        'login.title': 'Sign in',
        'login.username': 'Username',
        'login.password': 'Password',
        'login.rememberMe': 'Remember me on this device for 30 days',
        'login.submit': 'Sign in',
        'login.submitting': 'Signing in...',
        'login.errorRequired': 'Username and password are required',
        'login.errorFailed': 'Login failed',
        'login.proxyHint': 'Sign in via your SSO provider',
        'login.orLocal': 'or',
      }
      return strings[key] ?? key
    },
  }),
}))

function useOidcProviders(providers: unknown[] = []) {
  server.use(
    http.get(apiUrl('/auth/oidc/providers'), () => HttpResponse.json(providers)),
  )
}

function renderLoginPage() {
  return renderWithRouter(<LoginPage />, { initialEntries: ['/login'] })
}

describe('LoginPage', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    window.history.pushState(null, '', '/login')
    authState.status = null
    refreshMock.mockResolvedValue(undefined)
    useOidcProviders()
  })

  it('requires username and password before submitting', () => {
    let loginHit = false
    server.use(
      http.post(apiUrl('/auth/login'), () => {
        loginHit = true
        return HttpResponse.json({ ok: true, username: 'alice' })
      }),
    )

    renderLoginPage()

    const form = screen.getByRole('button', { name: 'Sign in' }).closest('form')
    if (!form) throw new Error('Login form was not rendered')
    fireEvent.submit(form)

    expect(screen.getByText('Username and password are required')).toBeInTheDocument()
    expect(loginHit).toBe(false)
  })

  it('submits DOM form values, refreshes auth state, and navigates home', async () => {
    let loginBody: unknown

    server.use(
      http.post(apiUrl('/auth/login'), async ({ request }) => {
        loginBody = await request.json()
        return HttpResponse.json({ ok: true, username: 'alice' })
      }),
      http.get(apiUrl('/auth/csrf'), () => HttpResponse.json({ csrfToken: 'fresh-token' })),
    )

    renderLoginPage()

    fireEvent.change(screen.getByLabelText('Username'), { target: { value: ' alice ' } })
    fireEvent.change(screen.getByLabelText('Password'), { target: { value: 'secret' } })
    fireEvent.click(screen.getByRole('button', { name: 'Sign in' }))

    await waitFor(() => {
      expect(loginBody).toEqual({ username: 'alice', password: 'secret', rememberMe: true })
    })
    expect(refreshMock).toHaveBeenCalledTimes(1)
    expect(navigateMock).toHaveBeenCalledWith('/', { replace: true })
  })

  it('shows an API error when login fails', async () => {
    server.use(
      http.post(apiUrl('/auth/login'), () => HttpResponse.json({ error: 'bad credentials' }, { status: 401 })),
    )

    renderLoginPage()

    fireEvent.change(screen.getByLabelText('Username'), { target: { value: 'alice' } })
    fireEvent.change(screen.getByLabelText('Password'), { target: { value: 'wrong' } })
    fireEvent.click(screen.getByRole('button', { name: 'Sign in' }))

    expect(await screen.findByText('bad credentials')).toBeInTheDocument()
    expect(refreshMock).not.toHaveBeenCalled()
    expect(navigateMock).not.toHaveBeenCalled()
  })

  it('renders only usable OIDC providers with encoded login links', async () => {
    useOidcProviders([
      { id: 'ok/provider', name: 'Company SSO', status: { state: 'ok' } },
      { id: 'failed', name: 'Broken SSO', status: { state: 'failed' } },
      { id: 'legacy', name: 'Legacy SSO' },
    ])

    renderLoginPage()

    const company = await screen.findByRole('link', { name: 'Sign in with Company SSO' })
    expect(company).toHaveAttribute('href', '/api/v1/auth/oidc/ok%2Fprovider/login')
    expect(screen.getByRole('link', { name: 'Sign in with Legacy SSO' })).toHaveAttribute(
      'href',
      '/api/v1/auth/oidc/legacy/login',
    )
    expect(screen.queryByText('Sign in with Broken SSO')).not.toBeInTheDocument()
  })

  it('renders the proxy sign-in hint instead of the local form', () => {
    authState.status = makeAuthStatus({ mode: 'proxy' })

    renderLoginPage()

    expect(screen.getByText('Sign in via your SSO provider')).toBeInTheDocument()
    expect(screen.queryByLabelText('Username')).not.toBeInTheDocument()
  })
})
