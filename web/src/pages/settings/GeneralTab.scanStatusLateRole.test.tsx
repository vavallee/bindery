import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import GeneralTab from './GeneralTab'
import { api, ApiError } from '../../api/client'

// AuthProvider starts with status null (isAdmin false, loading true) and only
// flips isAdmin to true once /auth/status resolves. GeneralTab keys the scan
// summary fetch on isAdmin, so an admin whose role arrives after mount must
// still get exactly one fetch, and a user whose role never becomes admin none.
const authState = { isAdmin: false, loading: true }

vi.mock('../../components/ThemeToggle', () => ({ default: () => <button type="button">Theme</button> }))
vi.mock('../../components/LanguageSwitcher', () => ({ default: () => <select aria-label="Language" /> }))
vi.mock('../../auth/AuthContext', () => ({
  useAuth: () => ({
    status: authState.loading
      ? null
      : {
          authenticated: true,
          username: authState.isAdmin ? 'admin' : 'reader',
          role: authState.isAdmin ? 'admin' : 'user',
          mode: 'enabled',
          setupRequired: false,
        },
    loading: authState.loading,
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

beforeEach(() => {
  authState.isAdmin = false
  authState.loading = true
  vi.mocked(api.listSettings).mockResolvedValue([])
  vi.mocked(api.libraryScanStatus).mockReset()
  vi.mocked(api.getStorage).mockRejectedValue(new ApiError(403, { error: 'admin role required' }, 'Forbidden'))
  vi.mocked(api.authConfig).mockRejectedValue(new Error('no auth cfg'))
})

describe('GeneralTab scan status when the role resolves late (#2361)', () => {
  it('fetches once when an admin role arrives after mount', async () => {
    vi.mocked(api.libraryScanStatus).mockResolvedValue({
      ran_at: '2026-09-14T10:00:00Z',
      files_found: 7,
      reconciled: 5,
      unmatched: 2,
      library_dir: '/srv/books',
      scanned_paths: ['/srv/books'],
    })

    const { rerender } = render(<GeneralTab />)
    await waitFor(() => expect(api.listSettings).toHaveBeenCalled())
    expect(api.libraryScanStatus).not.toHaveBeenCalled()

    authState.loading = false
    authState.isAdmin = true
    rerender(<GeneralTab />)

    expect(await screen.findByText('settings.general.lastScan')).toBeInTheDocument()
    expect(api.libraryScanStatus).toHaveBeenCalledTimes(1)
    expect(screen.getByText('7')).toBeInTheDocument()

    // A later rerender with the role unchanged must not fetch again.
    rerender(<GeneralTab />)
    await waitFor(() => expect(api.libraryScanStatus).toHaveBeenCalledTimes(1))
  })

  it('never fetches when the role resolves to user', async () => {
    const { rerender } = render(<GeneralTab />)
    await waitFor(() => expect(api.listSettings).toHaveBeenCalled())

    authState.loading = false
    authState.isAdmin = false
    rerender(<GeneralTab />)
    await waitFor(() => expect(api.getStorage).toHaveBeenCalled())

    expect(api.libraryScanStatus).not.toHaveBeenCalled()
    expect(screen.queryByText('settings.general.lastScan')).toBeNull()
  })
})
