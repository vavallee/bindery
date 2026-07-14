import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import GeneralTab from './GeneralTab'
import { api, type StorageHealth } from '../../api/client'

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
    },
  }
})

beforeEach(() => {
  vi.mocked(api.listSettings).mockResolvedValue([])
  vi.mocked(api.libraryScanStatus).mockRejectedValue(new Error('no scan'))
  vi.mocked(api.authConfig).mockRejectedValue(new Error('no auth cfg'))
})

function storage(overrides: Partial<StorageHealth> = {}): StorageHealth {
  return {
    downloadDir: '/downloads',
    audiobookDownloadDir: '',
    libraryDir: '/books',
    audiobookDir: '',
    dirs: [
      { name: 'download', path: '/downloads', exists: true, writable: true },
      { name: 'library', path: '/books', exists: true, writable: true },
    ],
    hardlinkable: true,
    ...overrides,
  }
}

describe('GeneralTab storage health', () => {
  it('renders per-directory OK status and the hardlink-ok notice', async () => {
    vi.mocked(api.getStorage).mockResolvedValue(storage())
    render(<GeneralTab />)
    await waitFor(() => expect(api.getStorage).toHaveBeenCalled())

    // Two healthy dirs both show the OK badge.
    await waitFor(() => expect(screen.getAllByText('settings.general.storageHealthOk').length).toBeGreaterThanOrEqual(2))
    expect(screen.getByText('settings.general.storageHardlinkOk')).toBeTruthy()
  })

  it('renders a failing reason for a missing dir and the cross-filesystem warning', async () => {
    vi.mocked(api.getStorage).mockResolvedValue(storage({
      hardlinkable: false,
      hardlinkReason: 'the download directory and the library are on different filesystems, so imports copy instead of hardlinking',
      dirs: [
        { name: 'download', path: '/downloads', exists: true, writable: true },
        { name: 'library', path: '/books', exists: false, writable: false, reason: 'directory does not exist' },
      ],
    }))
    render(<GeneralTab />)

    await waitFor(() => expect(screen.getByText('settings.general.storageHealthMissing')).toBeTruthy())
    // Reason text is shown inline with the badge.
    expect(screen.getByText(/directory does not exist/)).toBeTruthy()
    // Different-filesystem notice ("imports will copy, not hardlink").
    expect(screen.getByText('settings.general.storageHardlinkWarning')).toBeTruthy()
    // The specific WHY from the backend is rendered under it (#1427).
    expect(screen.getByText(/different filesystems/)).toBeTruthy()
  })
})
