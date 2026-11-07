import { describe, it, expect, vi, beforeEach } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import GeneralTab from './GeneralTab'
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
      setSetting: vi.fn(),
    },
  }
})

function seedSettings(entries: Record<string, string>) {
  vi.mocked(api.listSettings).mockResolvedValue(
    Object.entries(entries).map(([key, value]) => ({ key, value })) as Awaited<
      ReturnType<typeof api.listSettings>
    >,
  )
}

beforeEach(() => {
  seedSettings({})
  vi.mocked(api.libraryScanStatus).mockRejectedValue(new Error('no scan'))
  vi.mocked(api.getStorage).mockRejectedValue(new Error('no storage'))
  vi.mocked(api.authConfig).mockRejectedValue(new Error('no auth cfg'))
  vi.mocked(api.setSetting).mockReset()
})

// #3692: the library scan interval picker follows the same rules: default 6h,
// presets selected as stored, and a valid-but-unlisted value kept visible.
describe('Library scan interval picker', () => {
  it('falls back to the 6h default when nothing is stored', async () => {
    render(<GeneralTab />)
    const select = (await screen.findByTestId('library-scan-interval')) as HTMLSelectElement
    expect(select.value).toBe('6h')
  })

  it('shows a stored preset as selected', async () => {
    seedSettings({ 'library.scan_interval': '30m' })
    render(<GeneralTab />)
    const select = (await screen.findByTestId('library-scan-interval')) as HTMLSelectElement
    expect(select.value).toBe('30m')
  })

  it('keeps a valid-but-unlisted value selected instead of rendering blank', async () => {
    seedSettings({ 'library.scan_interval': '90m' })
    render(<GeneralTab />)
    const select = (await screen.findByTestId('library-scan-interval')) as HTMLSelectElement
    expect(select.value).toBe('90m')
    expect(within(select).getByRole('option', { name: '90m' })).toBeInTheDocument()
  })

  it('saves the chosen interval under library.scan_interval', async () => {
    vi.mocked(api.setSetting).mockResolvedValue(undefined as never)
    render(<GeneralTab />)
    const select = (await screen.findByTestId('library-scan-interval')) as HTMLSelectElement
    fireEvent.change(select, { target: { value: '1h' } })
    await waitFor(() => expect(api.setSetting).toHaveBeenCalledWith('library.scan_interval', '1h'))
    expect(select.value).toBe('1h')
  })
})

// #1848 — the server accepts any interval in [1h, 168h], so the stored value
// need not be one of the picker's presets. Without an option that matches it
// the select renders blank, which reads as "not configured" while the scheduler
// is honouring the value.
describe('Hardcover sync interval picker', () => {
  it('falls back to the 24h default when nothing is stored', async () => {
    render(<GeneralTab />)
    const select = (await screen.findByTestId('hardcover-sync-interval')) as HTMLSelectElement
    expect(select.value).toBe('24h')
  })

  it('shows a preset value as selected', async () => {
    seedSettings({ 'hardcover.sync_interval': '6h' })
    render(<GeneralTab />)
    const select = (await screen.findByTestId('hardcover-sync-interval')) as HTMLSelectElement
    expect(select.value).toBe('6h')
  })

  it('keeps a valid-but-unlisted value selected instead of rendering blank', async () => {
    seedSettings({ 'hardcover.sync_interval': '36h' })
    render(<GeneralTab />)
    const select = (await screen.findByTestId('hardcover-sync-interval')) as HTMLSelectElement
    expect(select.value).toBe('36h')
    expect(within(select).getByRole('option', { name: '36h' })).toBeInTheDocument()
  })
})
