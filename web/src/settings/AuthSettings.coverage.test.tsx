import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor, within } from '@testing-library/react'
import AuthSettings from './AuthSettings'
import { api, BINDERY_BASE } from '../api/client'
import type { OidcProvider } from '../api/client'
import { acceptConfirm, cancelConfirm } from '../test-utils'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, opts?: unknown) => {
      if (opts && typeof opts === 'object' && 'discovered' in opts) {
        const o = opts as { entered: string; discovered: string }
        return `${key}:${o.entered}->${o.discovered}`
      }
      return key
    },
  }),
}))

vi.mock('../api/client', async importOriginal => {
  const actual = await importOriginal<typeof import('../api/client')>()
  return {
    ...actual,
    api: {
      ...actual.api,
      oidcProviders: vi.fn(),
      oidcSetProviders: vi.fn(),
      oidcRedirectBase: vi.fn(),
      oidcTestDiscovery: vi.fn(),
    },
  }
})

const providers: OidcProvider[] = [
  { id: 'google', name: 'Google', status: { state: 'ok' } },
  { id: 'kc', name: 'Keycloak', status: { state: 'failed', last_error: 'discovery 404' } },
  { id: 'plain', name: 'Plain' },
]

const blankFor = (id: string, name: string) => ({ id, name, issuer: '', client_id: '', client_secret: '', scopes: [] })

const originalExec = Object.getOwnPropertyDescriptor(document, 'execCommand')

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(api.oidcProviders).mockResolvedValue([])
  vi.mocked(api.oidcSetProviders).mockResolvedValue(undefined)
  vi.mocked(api.oidcRedirectBase).mockResolvedValue({ base: 'https://books.example', callback_path: '/auth/{id}/cb', configured: true })
})

afterEach(() => {
  if (originalExec) Object.defineProperty(document, 'execCommand', originalExec)
  else Reflect.deleteProperty(document, 'execCommand')
})

function input(placeholder: string) {
  return screen.getByPlaceholderText(placeholder)
}

async function openAddForm() {
  fireEvent.click(await screen.findByRole('button', { name: 'settings.oidc.addButton' }))
  return screen.getByRole('button', { name: 'settings.oidc.addSave' }).closest('form') as HTMLFormElement
}

function fillForm(form: HTMLFormElement) {
  fireEvent.change(input('google'), { target: { value: ' my idp ' } })
  fireEvent.change(input('Google'), { target: { value: ' My IdP ' } })
  fireEvent.change(input('https://accounts.google.com'), { target: { value: ' https://idp.example ' } })
  const passwordInput = form.querySelector('input[type="password"]') as HTMLInputElement
  const clientIdInput = passwordInput.closest('.grid')!.querySelector('input:not([type="password"])') as HTMLInputElement
  fireEvent.change(clientIdInput, { target: { value: ' client ' } })
  fireEvent.change(passwordInput, { target: { value: ' secret ' } })
  fireEvent.change(screen.getByDisplayValue('openid email profile'), { target: { value: ' openid   groups ' } })
}

describe('AuthSettings provider list', () => {
  it('shows the empty state', async () => {
    render(<AuthSettings />)
    expect(await screen.findByText('settings.oidc.empty')).toBeInTheDocument()
  })

  it('renders status badges and the last error', async () => {
    vi.mocked(api.oidcProviders).mockResolvedValue(providers)
    render(<AuthSettings />)
    expect(await screen.findByText('Google')).toBeInTheDocument()
    expect(screen.getByText('settings.oidc.statusOk')).toBeInTheDocument()
    expect(screen.getByText('settings.oidc.statusFailed')).toHaveAttribute('title', 'discovery 404')
    expect(screen.getByText('discovery 404')).toBeInTheDocument()
    expect(screen.queryByText('settings.oidc.empty')).not.toBeInTheDocument()
  })

  it('logs and stays empty when the provider list fails to load', async () => {
    const err = vi.spyOn(console, 'error').mockImplementation(() => {})
    vi.mocked(api.oidcProviders).mockRejectedValue(new Error('forbidden'))
    render(<AuthSettings />)
    await waitFor(() => expect(err).toHaveBeenCalled())
    expect(screen.getByText('settings.oidc.empty')).toBeInTheDocument()
    err.mockRestore()
  })

  it('removes a provider after confirmation, sending the rest with blank secrets', async () => {
    vi.mocked(api.oidcProviders).mockResolvedValue(providers)
    render(<AuthSettings />)
    await screen.findByText('Google')
    fireEvent.click(screen.getAllByRole('button', { name: 'common.remove' })[1])
    await cancelConfirm()
    expect(api.oidcSetProviders).not.toHaveBeenCalled()

    fireEvent.click(screen.getAllByRole('button', { name: 'common.remove' })[1])
    await acceptConfirm()
    await waitFor(() => expect(api.oidcSetProviders).toHaveBeenCalledWith([
      blankFor('google', 'Google'),
      blankFor('plain', 'Plain'),
    ]))
    await waitFor(() => expect(screen.queryByText('Keycloak')).not.toBeInTheDocument())
  })

  it('shows an error alert when removal fails', async () => {
    vi.mocked(api.oidcProviders).mockResolvedValue([providers[0]])
    vi.mocked(api.oidcSetProviders).mockRejectedValueOnce(new Error('write failed'))
    vi.mocked(api.oidcSetProviders).mockRejectedValueOnce('opaque')
    render(<AuthSettings />)
    await screen.findByText('Google')
    fireEvent.click(screen.getByRole('button', { name: 'common.remove' }))
    await acceptConfirm()
    expect(await screen.findByText('write failed')).toBeInTheDocument()
    expect(screen.getByText('Google')).toBeInTheDocument()

    await waitFor(() => expect(screen.getByRole('button', { name: 'common.remove' })).toBeEnabled())
    fireEvent.click(screen.getByRole('button', { name: 'common.remove' }))
    await acceptConfirm()
    expect(await screen.findByText('settings.oidc.saveFail')).toBeInTheDocument()
  })
})

describe('AuthSettings add provider', () => {
  it('previews the callback URL from the server base and adds the trimmed config', async () => {
    vi.mocked(api.oidcProviders).mockResolvedValueOnce([providers[0]])
    vi.mocked(api.oidcProviders).mockResolvedValueOnce([providers[0], { id: 'my idp', name: 'My IdP', status: { state: 'ok' } }])
    render(<AuthSettings />)
    await screen.findByText('Google')
    const form = await openAddForm()
    expect(within(form).getByText('settings.oidc.callbackUrlEmpty')).toBeInTheDocument()
    const save = within(form).getByRole('button', { name: 'settings.oidc.addSave' })
    expect(save).toBeDisabled()

    fillForm(form)
    await waitFor(() => expect(within(form).getByText('https://books.example/auth/my%20idp/cb')).toBeInTheDocument())
    expect(save).toBeEnabled()
    fireEvent.submit(form)

    await waitFor(() => expect(api.oidcSetProviders).toHaveBeenCalledWith([
      blankFor('google', 'Google'),
      { id: 'my idp', name: 'My IdP', issuer: 'https://idp.example', client_id: 'client', client_secret: 'secret', scopes: ['openid', 'groups'] },
    ]))
    expect(await screen.findByText('My IdP')).toBeInTheDocument()
    await waitFor(() => expect(api.oidcProviders).toHaveBeenCalledTimes(2))
    expect(screen.queryByRole('button', { name: 'settings.oidc.addSave' })).not.toBeInTheDocument()
  })

  it('reuses a config added this session when another provider is added', async () => {
    render(<AuthSettings />)
    let form = await openAddForm()
    fillForm(form)
    // The list reloads from the server after a save.
    vi.mocked(api.oidcProviders).mockResolvedValue([{ id: 'my idp', name: 'My IdP' }])
    fireEvent.submit(form)
    await screen.findByText('My IdP')

    form = await openAddForm()
    fireEvent.change(input('google'), { target: { value: 'second' } })
    fireEvent.change(input('Google'), { target: { value: 'Second' } })
    fireEvent.change(input('https://accounts.google.com'), { target: { value: 'https://two.example' } })
    const pw = form.querySelector('input[type="password"]') as HTMLInputElement
    fireEvent.change(pw.closest('.grid')!.querySelector('input:not([type="password"])')!, { target: { value: 'c2' } })
    fireEvent.change(pw, { target: { value: 's2' } })
    fireEvent.submit(form)

    await waitFor(() => expect(api.oidcSetProviders).toHaveBeenCalledTimes(2))
    expect(vi.mocked(api.oidcSetProviders).mock.calls[1][0]).toEqual([
      { id: 'my idp', name: 'My IdP', issuer: 'https://idp.example', client_id: 'client', client_secret: 'secret', scopes: ['openid', 'groups'] },
      { id: 'second', name: 'Second', issuer: 'https://two.example', client_id: 'c2', client_secret: 's2', scopes: ['openid', 'email', 'profile'] },
    ])
  })

  it('shows the save error and keeps the form open when adding fails', async () => {
    vi.mocked(api.oidcSetProviders).mockRejectedValueOnce(new Error('issuer unreachable'))
    vi.mocked(api.oidcSetProviders).mockRejectedValueOnce({})
    render(<AuthSettings />)
    const form = await openAddForm()
    fillForm(form)
    fireEvent.submit(form)
    expect(await screen.findByText('issuer unreachable')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'settings.oidc.addSave' })).toBeInTheDocument()
    fireEvent.submit(form)
    expect(await screen.findByText('settings.oidc.saveFail')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'common.cancel' }))
    expect(screen.queryByRole('button', { name: 'settings.oidc.addSave' })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'settings.oidc.addButton' })).toBeInTheDocument()
  })

  it('warns when the public base URL is not configured', async () => {
    vi.mocked(api.oidcRedirectBase).mockResolvedValue({ base: '', callback_path: '', configured: false })
    render(<AuthSettings />)
    await openAddForm()
    expect(await screen.findByText('settings.oidc.callbackUrlNotConfiguredWarning')).toBeInTheDocument()
    fireEvent.change(input('google'), { target: { value: 'x' } })
    // Falls back to the page origin and the default callback template.
    expect(screen.getByText(`${window.location.origin}${BINDERY_BASE}/api/v1/auth/oidc/x/callback`)).toBeInTheDocument()
  })

  it('falls back silently when the redirect base endpoint is missing', async () => {
    vi.mocked(api.oidcRedirectBase).mockRejectedValue(new Error('404'))
    render(<AuthSettings />)
    await openAddForm()
    await waitFor(() => expect(api.oidcRedirectBase).toHaveBeenCalled())
    expect(screen.queryByText('settings.oidc.callbackUrlNotConfiguredWarning')).not.toBeInTheDocument()
  })

  it('copies the callback URL, or shows it for manual copy when the clipboard is unavailable', async () => {
    const exec = vi.fn().mockReturnValueOnce(true).mockReturnValue(false)
    Object.defineProperty(document, 'execCommand', { configurable: true, value: exec })
    render(<AuthSettings />)
    await openAddForm()
    const copy = screen.getByRole('button', { name: 'settings.oidc.callbackUrlCopy' })
    expect(copy).toBeDisabled()
    fireEvent.change(input('google'), { target: { value: 'abc' } })
    await screen.findByText('https://books.example/auth/abc/cb')
    fireEvent.click(copy)
    expect(await screen.findByRole('button', { name: 'settings.oidc.callbackUrlCopied' })).toBeInTheDocument()
    expect(exec).toHaveBeenCalledWith('copy')

    fireEvent.click(screen.getByRole('button', { name: 'settings.oidc.callbackUrlCopied' }))
    await waitFor(() => expect(screen.getByDisplayValue('https://books.example/auth/abc/cb')).toBeInTheDocument())
  })
})

describe('AuthSettings issuer discovery test', () => {
  async function typeIssuer(value: string) {
    render(<AuthSettings />)
    await openAddForm()
    fireEvent.change(input('https://accounts.google.com'), { target: { value } })
    return screen.getByRole('button', { name: 'settings.oidc.testDiscovery' })
  }

  it('is disabled until an issuer is typed', async () => {
    render(<AuthSettings />)
    await openAddForm()
    expect(screen.getByRole('button', { name: 'settings.oidc.testDiscovery' })).toBeDisabled()
  })

  it('shows discovered endpoints when the issuer matches', async () => {
    vi.mocked(api.oidcTestDiscovery).mockResolvedValue({
      ok: true,
      discovered: {
        issuer: 'https://idp.example',
        authorization_endpoint: 'https://idp.example/auth',
        token_endpoint: 'https://idp.example/token',
        scopes_supported: ['openid', 'email'],
      },
    })
    fireEvent.click(await typeIssuer(' https://idp.example '))
    expect(await screen.findByText('settings.oidc.testDiscoveryOk')).toBeInTheDocument()
    expect(api.oidcTestDiscovery).toHaveBeenCalledWith('https://idp.example')
    expect(screen.getByText('https://idp.example/auth')).toBeInTheDocument()
    expect(screen.getByText('https://idp.example/token')).toBeInTheDocument()
    expect(screen.getByText('openid email')).toBeInTheDocument()

    // Editing the issuer clears the stale result.
    fireEvent.change(input('https://accounts.google.com'), { target: { value: 'https://other.example' } })
    expect(screen.queryByText('settings.oidc.testDiscoveryOk')).not.toBeInTheDocument()
    fireEvent.change(input('https://accounts.google.com'), { target: { value: 'https://other2.example' } })
  })

  it('warns about an issuer mismatch', async () => {
    vi.mocked(api.oidcTestDiscovery).mockResolvedValue({
      ok: true,
      issuer_mismatch: true,
      discovered: { issuer: 'https://real.example/', authorization_endpoint: 'a', token_endpoint: 'b' },
    })
    fireEvent.click(await typeIssuer('https://idp.example'))
    expect(await screen.findByText('settings.oidc.testDiscoveryMismatch:https://idp.example->https://real.example/')).toBeInTheDocument()
    expect(screen.queryByText(/scopes:/)).not.toBeInTheDocument()
  })

  it('shows the server-reported failure, a generic one, and a thrown error', async () => {
    vi.mocked(api.oidcTestDiscovery)
      .mockResolvedValueOnce({ ok: false, error: 'connection refused' })
      .mockResolvedValueOnce({ ok: false })
      .mockRejectedValueOnce(new Error('network'))
      .mockRejectedValueOnce('raw string')
    const btn = await typeIssuer('https://idp.example')
    fireEvent.click(btn)
    expect(await screen.findByText(/connection refused/)).toBeInTheDocument()
    fireEvent.click(btn)
    expect(await screen.findByText(/settings\.oidc\.testDiscoveryUnknown/)).toBeInTheDocument()
    fireEvent.click(btn)
    expect(await screen.findByText(/: network/)).toBeInTheDocument()
    fireEvent.click(btn)
    expect(await screen.findByText(/raw string/)).toBeInTheDocument()
  })

  it('shows the probing label while the request is in flight', async () => {
    let resolve: (v: { ok: boolean; error?: string }) => void = () => {}
    vi.mocked(api.oidcTestDiscovery).mockReturnValue(new Promise(r => { resolve = r }) as never)
    fireEvent.click(await typeIssuer('https://idp.example'))
    const probing = await screen.findByRole('button', { name: 'settings.oidc.testDiscoveryProbing' })
    expect(probing).toBeDisabled()
    resolve({ ok: false, error: 'late' })
    expect(await screen.findByText(/late/)).toBeInTheDocument()
  })
})
