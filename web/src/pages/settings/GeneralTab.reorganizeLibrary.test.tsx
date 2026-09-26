import { describe, it, expect, vi, beforeEach } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import GeneralTab from './GeneralTab'
import { api } from '../../api/client'
import { reorganizeApi } from '../../api/reorganize'

// #2296: reorganize has had a library scope on the server since #1181
// (internal/api/reorganize.go case "library" -> PreviewReorganizeLibrary) and
// RenameFilesModal already accepts scope="library" with no id, but the modal
// was only ever mounted per author and per book. Settings, General is the
// place the issue asks for, next to the library scan, and the order has to be
// scan first then reorganize because reorganize only moves files a scan has
// already attached to a book.
const authState = { isAdmin: true }

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
vi.mock('../../api/reorganize', () => ({
  reorganizeApi: { preview: vi.fn(), apply: vi.fn() },
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
      triggerLibraryScan: vi.fn(),
    },
  }
})

beforeEach(() => {
  authState.isAdmin = true
  vi.mocked(api.listSettings).mockResolvedValue([])
  vi.mocked(api.libraryScanStatus).mockRejectedValue(new Error('no prior scan'))
  vi.mocked(api.getStorage).mockRejectedValue(new Error('no storage'))
  vi.mocked(api.authConfig).mockRejectedValue(new Error('no auth cfg'))
  vi.mocked(reorganizeApi.preview).mockResolvedValue({
    moves: [],
    summary: { total: 0, toMove: 0, noop: 0, collision: 0, missing: 0, errored: 0, moved: 0, failed: 0 },
  })
})

describe('GeneralTab library reorganize (#2296)', () => {
  it('offers a reorganize control after the scan control', async () => {
    render(<GeneralTab />)
    await waitFor(() => expect(api.listSettings).toHaveBeenCalled())

    const scan = screen.getByRole('button', { name: 'settings.general.scanLibraryButton' })
    const reorganize = screen.getByRole('button', { name: 'settings.general.reorganizeLibraryButton' })

    // Scan first, then reorganize: DOCUMENT_POSITION_FOLLOWING is 4.
    expect(scan.compareDocumentPosition(reorganize) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('previews the whole library, with no id, when the control is pressed', async () => {
    render(<GeneralTab />)
    await waitFor(() => expect(api.listSettings).toHaveBeenCalled())

    fireEvent.click(screen.getByRole('button', { name: 'settings.general.reorganizeLibraryButton' }))

    await waitFor(() => expect(reorganizeApi.preview).toHaveBeenCalledWith('library', undefined))
    expect(screen.getByText('Rename files')).toBeInTheDocument()
  })

  it('hides the reorganize control from a non admin', async () => {
    authState.isAdmin = false
    render(<GeneralTab />)
    await waitFor(() => expect(api.listSettings).toHaveBeenCalled())

    expect(screen.queryByRole('button', { name: 'settings.general.reorganizeLibraryButton' })).toBeNull()
  })
})
