import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import GeneralTab from './GeneralTab'
import { api, ApiError } from '../../api/client'

// GET /library/scan/status is admin only since #2361: the scan summary names
// the library roots and the absolute path of every unmatched file. The panel
// that renders it already sat inside GeneralTab's isAdmin block, so a non
// admin must not request it at all, and must see no error for its absence.
const authState = { isAdmin: false }

vi.mock('../../components/ThemeToggle', () => ({ default: () => <button type="button">Theme</button> }))
vi.mock('../../components/LanguageSwitcher', () => ({ default: () => <select aria-label="Language" /> }))
vi.mock('../../auth/AuthContext', () => ({
  useAuth: () => ({
    status: {
      authenticated: true,
      username: authState.isAdmin ? 'admin' : 'reader',
      role: authState.isAdmin ? 'admin' : 'user',
      mode: 'enabled',
      setupRequired: false,
    },
    loading: false,
    isAdmin: authState.isAdmin,
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

const forbidden = () => new ApiError(403, { error: 'admin role required' }, 'Forbidden')

beforeEach(() => {
  vi.mocked(api.listSettings).mockResolvedValue([])
  vi.mocked(api.libraryScanStatus).mockReset()
  vi.mocked(api.getStorage).mockRejectedValue(forbidden())
  vi.mocked(api.authConfig).mockRejectedValue(new Error('no auth cfg'))
})

describe('GeneralTab scan status by role (#2361)', () => {
  it('does not request the scan summary for a non admin and shows no error', async () => {
    authState.isAdmin = false
    vi.mocked(api.libraryScanStatus).mockRejectedValue(forbidden())

    render(<GeneralTab />)
    await waitFor(() => expect(api.listSettings).toHaveBeenCalled())
    // Let the other mount effects settle before asserting the absence.
    await waitFor(() => expect(api.getStorage).toHaveBeenCalled())

    expect(api.libraryScanStatus).not.toHaveBeenCalled()
    expect(screen.queryByText('settings.general.lastScan')).toBeNull()
    expect(screen.queryByRole('alert')).toBeNull()
    expect(screen.queryByText(/admin role required/i)).toBeNull()
  })

  it('still loads and renders the scan summary for an admin', async () => {
    authState.isAdmin = true
    vi.mocked(api.libraryScanStatus).mockResolvedValue({
      ran_at: '2026-09-14T10:00:00Z',
      files_found: 12,
      reconciled: 9,
      unmatched: 3,
      library_dir: '/srv/books',
      scanned_paths: ['/srv/books'],
    })

    render(<GeneralTab />)

    expect(await screen.findByText('settings.general.lastScan')).toBeInTheDocument()
    expect(api.libraryScanStatus).toHaveBeenCalledTimes(1)
    expect(screen.getByText('12')).toBeInTheDocument()
  })
})
