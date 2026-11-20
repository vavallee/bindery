import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, within, fireEvent, waitFor } from '@testing-library/react'
import GeneralTab from '../../test/allGeneralSections'
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
  vi.mocked(api.setSetting).mockResolvedValue(undefined as never)
})

async function findPicker() {
  return (await screen.findByLabelText('settings.general.discoveryIntervalLabel')) as HTMLSelectElement
}

// #2236: discovery ships off. The picker offers Off, Daily, Weekly and
// Monthly, shows Off until an interval is stored, saves on change, and never
// claims a restart is needed, because the scheduler reads the setting on
// every tick.
describe('Release discovery interval picker', () => {
  it('shows Off when nothing is stored', async () => {
    render(<GeneralTab />)
    const select = await findPicker()
    expect(select.value).toBe('off')
    const options = within(select).getAllByRole('option').map(o => (o as HTMLOptionElement).value)
    expect(options).toEqual(['off', '24h', '168h', '720h'])
  })

  it('keeps showing a stored interval', async () => {
    seedSettings({ 'authors.discovery.interval': '168h' })
    render(<GeneralTab />)
    const select = await findPicker()
    expect(select.value).toBe('168h')
  })

  it('saves Weekly when chosen', async () => {
    render(<GeneralTab />)
    const select = await findPicker()
    fireEvent.change(select, { target: { value: '168h' } })
    await waitFor(() => expect(api.setSetting).toHaveBeenCalledWith('authors.discovery.interval', '168h'))
    expect(select.value).toBe('168h')
  })

  it('saves Off when chosen', async () => {
    seedSettings({ 'authors.discovery.interval': '24h' })
    render(<GeneralTab />)
    const select = await findPicker()
    fireEvent.change(select, { target: { value: 'off' } })
    await waitFor(() => expect(api.setSetting).toHaveBeenCalledWith('authors.discovery.interval', 'off'))
    expect(select.value).toBe('off')
  })

  it('keeps a valid but unlisted value selected', async () => {
    seedSettings({ 'authors.discovery.interval': '336h' })
    render(<GeneralTab />)
    const select = await findPicker()
    expect(select.value).toBe('336h')
    expect(within(select).getByRole('option', { name: '336h' })).toBeInTheDocument()
  })

  it('does not say a restart is required', async () => {
    render(<GeneralTab />)
    const select = await findPicker()
    const section = select.closest('section') as HTMLElement
    expect(within(section).queryByText('settings.general.searchIntervalRestart')).not.toBeInTheDocument()
  })
})
