import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import GeneralTab from '../../test/allGeneralSections'
import { api, type AuthConfig } from '../../api/client'

vi.mock('../../components/ThemeToggle', () => ({ default: () => <button type="button">Theme</button> }))
vi.mock('../../components/LanguageSwitcher', () => ({ default: () => <select aria-label="Language" /> }))
vi.mock('../../settings/AuthSettings', () => ({ default: () => <div data-testid="auth-settings" /> }))
vi.mock('../../auth/AuthContext', () => ({
  useAuth: () => ({
    status: { authenticated: true, username: 'admin', role: 'admin', mode: 'enabled', setupRequired: false },
    loading: false,
    isAdmin: true,
    refresh: vi.fn(),
    logout: vi.fn(),
  }),
}))
vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, fallback?: unknown) => (typeof fallback === 'string' ? fallback : key),
    i18n: { changeLanguage: vi.fn() },
  }),
}))
vi.mock('../../api/client', async importOriginal => {
  const actual = await importOriginal<typeof import('../../api/client')>()
  return {
    ...actual,
    api: {
      ...actual.api,
      listSettings: vi.fn(),
      libraryScanStatus: vi.fn(),
      getStorage: vi.fn(),
      authConfig: vi.fn(),
      authSetMode: vi.fn(),
    },
  }
})

const cfg: AuthConfig = { mode: 'enabled', apiKey: 'k', username: 'admin' }

// A slice of the exact text the backend sends. Asserting on the server's own
// string proves the component renders what it was given instead of keeping a
// second copy that could drift.
const serverWarning =
  'local-only auth mode is active but BINDERY_TRUSTED_PROXY is empty: if Bindery is behind a reverse proxy ' +
  'it cannot tell a proxied public request from a genuine LAN client, so every proxied request is treated ' +
  'as a trusted local admin.'

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(api.listSettings).mockResolvedValue([])
  vi.mocked(api.libraryScanStatus).mockRejectedValue(new Error('no scan'))
  vi.mocked(api.getStorage).mockRejectedValue(new Error('no storage'))
  vi.mocked(api.authConfig).mockResolvedValue(cfg)
})

async function pickMode(select: HTMLElement, mode: string) {
  fireEvent.change(select, { target: { value: mode } })
  await waitFor(() => expect(api.authSetMode).toHaveBeenCalledWith(mode))
}

describe('GeneralTab auth mode warning', () => {
  it('surfaces the server warning inline when local-only is picked without a trusted proxy', async () => {
    vi.mocked(api.authSetMode).mockResolvedValue({ mode: 'local-only', warning: serverWarning })
    render(<GeneralTab />)

    await pickMode(await screen.findByDisplayValue('Enabled'), 'local-only')

    const warning = await screen.findByTestId('auth-mode-warning')
    expect(warning.textContent).toBe(serverWarning)
    // Persistent rather than a toast: a real element in the document, and one
    // that assistive tech announces.
    expect(warning.getAttribute('role')).toBe('alert')
  })

  it('stays quiet when the response carries no warning', async () => {
    vi.mocked(api.authSetMode).mockResolvedValue({ mode: 'local-only' })
    render(<GeneralTab />)

    await pickMode(await screen.findByDisplayValue('Enabled'), 'local-only')

    // The config reload after the mode change tells us the handler ran to the
    // end, so an absent warning is a real absence and not a race.
    await waitFor(() => expect(api.authConfig).toHaveBeenCalledTimes(2))
    expect(screen.queryByTestId('auth-mode-warning')).toBeNull()
  })

  it('clears a standing warning once a mode without one is chosen', async () => {
    vi.mocked(api.authSetMode).mockResolvedValueOnce({ mode: 'local-only', warning: serverWarning })
    render(<GeneralTab />)
    const select = await screen.findByDisplayValue('Enabled')

    await pickMode(select, 'local-only')
    await screen.findByTestId('auth-mode-warning')

    vi.mocked(api.authSetMode).mockResolvedValueOnce({ mode: 'disabled' })
    await pickMode(select, 'disabled')

    await waitFor(() => expect(screen.queryByTestId('auth-mode-warning')).toBeNull())
  })
})
