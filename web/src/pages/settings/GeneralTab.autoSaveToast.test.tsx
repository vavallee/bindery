import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import GeneralTab from '../../test/allGeneralSections'
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
    expect(await screen.findByText(/set to External/)).toBeInTheDocument()
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

  // The handlers update the control before the request, so a failed save has
  // to put the old value back: otherwise it shows a choice the server never
  // stored.
  it('puts the old value back when a select save fails', async () => {
    const spy = vi.spyOn(console, 'error').mockImplementation(() => {})
    vi.mocked(api.setSetting).mockRejectedValue(new Error('nope'))
    render(<><SettingsToastHost /><GeneralTab /></>)
    const select = await screen.findByTestId('import-audiobook-mode') as HTMLSelectElement
    expect(select.value).toBe('')
    fireEvent.change(select, { target: { value: 'copy' } })
    await screen.findByTestId('settings-toast')
    expect(select.value).toBe('')
    spy.mockRestore()
  })

  it('puts Import Mode back on the previous choice when its save fails', async () => {
    const spy = vi.spyOn(console, 'error').mockImplementation(() => {})
    vi.mocked(api.setSetting).mockRejectedValue(new Error('nope'))
    render(<><SettingsToastHost /><GeneralTab /></>)
    const copy = await screen.findByRole('button', { name: 'Copy' })
    const auto = screen.getByRole('button', { name: 'Auto' })
    expect(auto.className).toContain('bg-emerald-700')
    fireEvent.click(copy)
    await screen.findByTestId('settings-toast')
    expect(auto.className).toContain('bg-emerald-700')
    expect(copy.className).not.toContain('bg-emerald-700')
    spy.mockRestore()
  })
})
