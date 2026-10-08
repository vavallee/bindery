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

vi.mock('react-router', async importOriginal => {
  const actual = await importOriginal<typeof import('react-router')>()
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
      if (key === 'login.hostNotAllowed') {
        return `Bindery is in ${String(options?.mode)} mode. ${String(options?.host)} is not local. Add ${String(options?.host)} to BINDERY_ALLOWED_HOSTS.`
      }
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
        'login.contactAdmin': 'Contact your administrator for access',
        'login.hostNotAllowedTitle': 'Sign in is required at this address',
        'login.hostNotAllowedDocs': 'How host names are checked',
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

  it('keeps the phone keyboard from capitalising or correcting the username', () => {
    renderLoginPage()

    const username = document.getElementById('username')
    expect(username).toHaveAttribute('autocomplete', 'username')
    expect(username).toHaveAttribute('autocapitalize', 'none')
    expect(username).toHaveAttribute('autocorrect', 'off')
    expect(username).toHaveAttribute('spellcheck', 'false')
  })

  it('does not render the login form for an authenticated session', () => {
    authState.status = makeAuthStatus({ authenticated: true, username: 'alice', role: 'admin' })

    renderLoginPage()

    expect(screen.queryByRole('button', { name: 'Sign in' })).not.toBeInTheDocument()
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

  it('hides the local login form when localAuthEnabled is false and OIDC providers exist', async () => {
    authState.status = makeAuthStatus({ localAuthEnabled: false })
    useOidcProviders([{ id: 'corp', name: 'Corp SSO', status: { state: 'ok' } }])

    renderLoginPage()

    // OIDC button should be present
    expect(await screen.findByRole('link', { name: 'Sign in with Corp SSO' })).toBeInTheDocument()
    // Local form must not be rendered
    expect(screen.queryByLabelText('Username')).not.toBeInTheDocument()
    // "or" divider must not be rendered when local auth is disabled
    expect(screen.queryByText('or')).not.toBeInTheDocument()
  })

  it('shows contact-admin message when localAuthEnabled is false and no OIDC providers', async () => {
    authState.status = makeAuthStatus({ localAuthEnabled: false })
    useOidcProviders([])

    renderLoginPage()

    // Local form must not be rendered
    expect(screen.queryByLabelText('Username')).not.toBeInTheDocument()
    // Contact admin message should appear
    expect(await screen.findByText('Contact your administrator for access')).toBeInTheDocument()
  })

  it('explains a refused host name and names BINDERY_ALLOWED_HOSTS', () => {
    authState.status = makeAuthStatus({ mode: 'disabled', hostNotAllowed: true, refusedHost: 'books.example.com' })

    renderLoginPage()

    const notice = screen.getByRole('alert')
    expect(notice).toHaveTextContent('Sign in is required at this address')
    expect(notice).toHaveTextContent('books.example.com')
    expect(notice).toHaveTextContent('BINDERY_ALLOWED_HOSTS')
    expect(notice).toHaveTextContent('disabled mode')
    expect(screen.getByRole('link', { name: 'How host names are checked' })).toHaveAttribute(
      'href',
      expect.stringContaining('DEPLOYMENT.md#host-names-in-local-only-and-disabled-mode'),
    )
    // The sign in form is still offered: a session works under any name.
    expect(screen.getByLabelText('Username')).toBeInTheDocument()
  })

  it('falls back to the page host when the backend did not name one', () => {
    authState.status = makeAuthStatus({ mode: 'local-only', hostNotAllowed: true })

    renderLoginPage()

    expect(screen.getByRole('alert')).toHaveTextContent(window.location.host)
  })

  it('shows no host notice when the flag is absent', () => {
    authState.status = makeAuthStatus({ mode: 'local-only' })

    renderLoginPage()

    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })
})
