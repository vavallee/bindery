import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import AuthSettings from './AuthSettings'
import { api } from '../api/client'
import { authApi } from '../api/auth'
import { apiUrl, server } from '../test/msw'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, opts?: { name?: string }) => (opts?.name ? `${key}:${opts.name}` : key),
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
    },
  }
})

const envProvider = {
  id: 'keycloak', name: 'Keycloak', issuer: 'https://kc.example/realms/home', client_id: 'bindery',
  scopes: ['openid'], client_secret_from_env: true, client_secret_env_var: 'BINDERY_OIDC_KEYCLOAK_CLIENT_SECRET',
}
const plainProvider = {
  id: 'plain', name: 'Plain', issuer: 'https://p.example', client_id: 'p', scopes: ['openid'],
  client_secret_from_env: false, client_secret_env_var: 'BINDERY_OIDC_PLAIN_CLIENT_SECRET',
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(api.oidcSetProviders).mockResolvedValue(undefined)
  vi.mocked(api.oidcRedirectBase).mockResolvedValue({ base: 'https://b.example', callback_path: '/auth/{id}/cb', configured: true })
})

describe('OIDC secret set via environment', () => {
  it('renders the secret disabled with the variable name and does not require it', async () => {
    vi.mocked(api.oidcProviders).mockResolvedValue([envProvider])
    render(<AuthSettings />)
    fireEvent.click(await screen.findByRole('button', { name: 'common.edit' }))

    const secret = document.querySelector('input[type="password"]') as HTMLInputElement
    expect(secret).toBeDisabled()
    expect(secret).not.toBeRequired()
    expect(secret.value).toBe('')
    expect(secret).toHaveAttribute('placeholder', 'settings.oidc.clientSecretFromEnv')
    expect(screen.getByText('settings.oidc.clientSecretFromEnv')).toBeInTheDocument()
    expect(screen.getByText(/BINDERY_OIDC_KEYCLOAK_CLIENT_SECRET/)).toBeInTheDocument()
  })

  it('fixes the issuer and says why when the secret is bound to it', async () => {
    vi.mocked(api.oidcProviders).mockResolvedValue([{
      ...envProvider, client_secret_issuer_bound: true, client_secret_issuer_env_var: 'BINDERY_OIDC_KEYCLOAK_ISSUER',
    }])
    render(<AuthSettings />)
    fireEvent.click(await screen.findByRole('button', { name: 'common.edit' }))

    const issuer = screen.getByDisplayValue('https://kc.example/realms/home')
    expect(issuer).toHaveAttribute('readonly')
    expect(screen.getAllByText(/settings\.oidc\.issuerFixedByEnv:BINDERY_OIDC_KEYCLOAK_ISSUER/).length).toBe(2)
  })

  it('leaves the issuer editable when it is not bound', async () => {
    vi.mocked(api.oidcProviders).mockResolvedValue([envProvider])
    render(<AuthSettings />)
    fireEvent.click(await screen.findByRole('button', { name: 'common.edit' }))
    expect(screen.getByDisplayValue('https://kc.example/realms/home')).not.toHaveAttribute('readonly')
    expect(screen.queryByText(/issuerFixedByEnv/)).not.toBeInTheDocument()
  })

  it('keeps the normal editable field when the secret is not from the environment', async () => {
    vi.mocked(api.oidcProviders).mockResolvedValue([plainProvider])
    render(<AuthSettings />)
    fireEvent.click(await screen.findByRole('button', { name: 'common.edit' }))

    const secret = document.querySelector('input[type="password"]') as HTMLInputElement
    expect(secret).not.toBeDisabled()
    expect(screen.queryByText('settings.oidc.clientSecretFromEnv')).not.toBeInTheDocument()
  })
})

describe('oidcSetProviders wire body', () => {
  it('drops the response only env fields before the PUT', async () => {
    let body: unknown
    server.use(
      http.put(apiUrl('/auth/oidc/providers'), async ({ request }) => {
        body = await request.json()
        return HttpResponse.json({ ok: true })
      }),
    )
    await authApi.oidcSetProviders([{
      id: 'keycloak', name: 'K', issuer: 'https://i', client_id: 'c', client_secret: '', scopes: [],
      client_secret_from_env: true, client_secret_env_var: 'BINDERY_OIDC_KEYCLOAK_CLIENT_SECRET',
      client_secret_issuer_bound: true, client_secret_issuer_env_var: 'BINDERY_OIDC_KEYCLOAK_ISSUER',
    }])
    const sent = (body as Record<string, unknown>[])[0]
    expect(sent).not.toHaveProperty('client_secret_from_env')
    expect(sent).not.toHaveProperty('client_secret_env_var')
    expect(sent).not.toHaveProperty('client_secret_issuer_bound')
    expect(sent).not.toHaveProperty('client_secret_issuer_env_var')
    expect(sent.client_secret).toBe('')
  })
})
