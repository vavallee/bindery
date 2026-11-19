import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import GeneralTab from './GeneralTab'
import { SettingsToastHost } from './autoSave'
import { api } from '../../api/client'

vi.mock('../../components/ThemeToggle', () => ({ default: () => <button type="button">Theme</button> }))
vi.mock('../../components/LanguageSwitcher', () => ({ default: () => <select aria-label="Language" /> }))
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
    t: (key: string, fallback?: unknown, opts?: Record<string, string>) => {
      if (typeof fallback !== 'string') return key
      return fallback.replace(/\{\{(\w+)\}\}/g, (_, k) => opts?.[k] ?? '')
    },
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
      setSetting: vi.fn(),
    },
  }
})

beforeEach(() => {
  vi.mocked(api.listSettings).mockResolvedValue([] as unknown as Awaited<ReturnType<typeof api.listSettings>>)
  vi.mocked(api.libraryScanStatus).mockRejectedValue(new Error('no scan'))
  vi.mocked(api.getStorage).mockRejectedValue(new Error('no storage'))
  vi.mocked(api.authConfig).mockRejectedValue(new Error('no auth cfg'))
  vi.mocked(api.setSetting).mockReset()
})

// #3991: Import Mode saved silently on click, so a person reloaded the page to
// check it had stuck.
describe('GeneralTab auto-save toasts', () => {
  it('confirms an Import Mode change with a toast naming the mode', async () => {
    vi.mocked(api.setSetting).mockResolvedValue(undefined as never)
    render(<><SettingsToastHost /><GeneralTab /></>)
    fireEvent.click(await screen.findByRole('button', { name: 'External' }))
    expect(await screen.findByRole('status')).toHaveTextContent('set to External')
    expect(api.setSetting).toHaveBeenCalledWith('import.mode', 'external')
  })

  it('shows an error toast when the Import Mode save fails', async () => {
    const spy = vi.spyOn(console, 'error').mockImplementation(() => {})
    vi.mocked(api.setSetting).mockRejectedValue(new Error('nope'))
    render(<><SettingsToastHost /><GeneralTab /></>)
    fireEvent.click(await screen.findByRole('button', { name: 'Copy' }))
    expect(await screen.findByTestId('settings-toast')).toHaveTextContent("Couldn't save")
    spy.mockRestore()
  })
})
