import { describe, it, expect, vi, beforeEach } from 'vitest'
import { act, render, screen } from '@testing-library/react'
import GeneralTab from './GeneralTab'
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
    },
  }
})

const cfg: AuthConfig = { mode: 'enabled', apiKey: 'k', username: 'admin' }

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(api.listSettings).mockResolvedValue([])
  vi.mocked(api.libraryScanStatus).mockRejectedValue(new Error('no scan'))
  vi.mocked(api.getStorage).mockRejectedValue(new Error('no storage'))
})

// The Settings layout shift on phones (#3052): Security fetched its config
// only after the settings had loaded and the sections below it were already
// on screen, then appeared above them and pushed them all down.
describe('GeneralTab Security section and layout shift', () => {
  it('does not show the sections below Security before Security can render', async () => {
    let resolveCfg: (c: AuthConfig) => void = () => {}
    vi.mocked(api.authConfig).mockReturnValue(new Promise<AuthConfig>(r => { resolveCfg = r }))
    render(<GeneralTab />)

    // The settings have arrived; the auth config has not.
    await act(async () => {})
    expect(screen.queryByText('settings.general.fileNaming')).toBeNull()

    await act(async () => { resolveCfg(cfg) })
    const security = await screen.findByRole('heading', { name: 'Security' })
    const naming = screen.getByRole('heading', { name: 'settings.general.fileNaming' })
    expect(security.compareDocumentPosition(naming) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('asks for the auth config at the same time as the settings, not after', () => {
    vi.mocked(api.listSettings).mockReturnValue(new Promise(() => {}))
    vi.mocked(api.authConfig).mockReturnValue(new Promise(() => {}))
    render(<GeneralTab />)
    expect(api.listSettings).toHaveBeenCalledTimes(1)
    expect(api.authConfig).toHaveBeenCalledTimes(1)
  })

  it('stops waiting for a hung auth config after 2.5s, then adds Security when it arrives', async () => {
    vi.useFakeTimers()
    try {
      let resolveCfg: (c: AuthConfig) => void = () => {}
      vi.mocked(api.authConfig).mockReturnValue(new Promise<AuthConfig>(r => { resolveCfg = r }))
      render(<GeneralTab />)

      await act(async () => { await vi.advanceTimersByTimeAsync(2400) })
      expect(screen.queryByRole('heading', { name: 'settings.general.fileNaming' })).toBeNull()

      await act(async () => { await vi.advanceTimersByTimeAsync(200) })
      expect(screen.getByRole('heading', { name: 'settings.general.fileNaming' })).toBeInTheDocument()
      expect(screen.queryByRole('heading', { name: 'Security' })).toBeNull()

      await act(async () => { resolveCfg(cfg) })
      const security = screen.getByRole('heading', { name: 'Security' })
      const naming = screen.getByRole('heading', { name: 'settings.general.fileNaming' })
      expect(security.compareDocumentPosition(naming) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
      expect(screen.getByDisplayValue('Enabled')).toBeInTheDocument()
    } finally {
      vi.useRealTimers()
    }
  })

  it('still renders the rest of the tab when the auth config fails', async () => {
    vi.mocked(api.authConfig).mockRejectedValue(new Error('forbidden'))
    render(<GeneralTab />)
    expect(await screen.findByRole('heading', { name: 'settings.general.fileNaming' })).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: 'Security' })).toBeNull()
  })
})
