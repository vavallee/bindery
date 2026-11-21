import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import GeneralTab from './GeneralTab'
import { api } from '../../api/client'
import { expectFieldValuesWired } from '../../test/fieldValue'

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
      if (typeof fallback === 'string') return fallback.replace(/\{\{(\w+)\}\}/g, (_, k) => opts?.[k] ?? '')
      if (fallback && typeof fallback === 'object') return `${key} ${(fallback as Record<string, unknown>).count ?? ''}`.trim()
      return key
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

function seed(entries: Record<string, string>) {
  vi.mocked(api.listSettings).mockResolvedValue(
    Object.entries(entries).map(([key, value]) => ({ key, value })) as Awaited<ReturnType<typeof api.listSettings>>,
  )
}

const sub = (id: string) => document.getElementById(id)!

beforeEach(() => {
  seed({})
  vi.mocked(api.libraryScanStatus).mockRejectedValue(new Error('no scan'))
  vi.mocked(api.getStorage).mockRejectedValue(new Error('no storage'))
  vi.mocked(api.authConfig).mockRejectedValue(new Error('no auth cfg'))
  vi.mocked(api.setSetting).mockReset().mockResolvedValue(undefined as never)
})

describe('GeneralTab current value subtitles (#3989)', () => {
  it('shows a default for an unset Import Mode, then the chosen mode after a save', async () => {
    render(<GeneralTab />)
    await screen.findByRole('button', { name: 'External' })
    expect(sub('import-mode-value')).toHaveTextContent('Current: Default (Auto)')
    fireEvent.click(screen.getByRole('button', { name: 'Hardlink' }))
    await waitFor(() => expect(sub('import-mode-value')).toHaveTextContent('Current: Hardlink'))
    expect(api.setSetting).toHaveBeenCalledWith('import.mode', 'hardlink')
  })

  it('goes back to the last saved mode when the save fails', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    seed({ 'import.mode': 'copy' })
    vi.mocked(api.setSetting).mockRejectedValue(new Error('boom'))
    render(<GeneralTab />)
    await screen.findByRole('button', { name: 'External' })
    expect(sub('import-mode-value')).toHaveTextContent('Current: Copy')
    fireEvent.click(screen.getByRole('button', { name: 'Move' }))
    await waitFor(() => expect(api.setSetting).toHaveBeenCalled())
    await waitFor(() => expect(sub('import-mode-value')).toHaveTextContent('Current: Copy'))
  })

  it('describes the Import Mode group and a select with their subtitles', async () => {
    seed({ 'import.ebook.content_check': 'block' })
    render(<GeneralTab />)
    await screen.findByRole('button', { name: 'External' })
    expect(screen.getByRole('group', { name: 'Import Mode' })).toHaveAccessibleDescription(/Current: Default \(Auto\)/)
    expect(screen.getByTestId('import-ebook-content-check')).toHaveAccessibleDescription(/Current: .*Block/)
  })

  it('shows translated labels, not stored keys, for the audiobook mode and cadences', async () => {
    seed({ 'import.audiobook.mode': 'hardlink', 'authors.discovery.interval': '168h' })
    render(<GeneralTab />)
    await screen.findByRole('button', { name: 'External' })
    expect(screen.getByTestId('import-audiobook-mode')).toHaveAccessibleDescription('Current: Hardlink')
    expect(sub('discovery-interval-value')).toHaveTextContent('Current: settings.general.discoveryWeekly')
    expect(sub('discovery-interval-value').textContent).not.toContain('168h')
  })

  it('shows a default when the audiobook mode is unset', async () => {
    render(<GeneralTab />)
    await screen.findByRole('button', { name: 'External' })
    expect(sub('import-audiobook-mode-value')).toHaveTextContent('Current: Default (settings.general.audiobookImportModeSame)')
  })

  it('wires every subtitle on the tab to a control', async () => {
    render(<GeneralTab />)
    await screen.findByRole('button', { name: 'External' })
    expectFieldValuesWired(document.body)
  })
})
