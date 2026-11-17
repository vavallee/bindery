import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router'
import SettingsPage from './SettingsPage'
import { api, ApiError } from '../api/client'

// A non admin's Settings page showed only General and About but still asked
// for the indexer, download client and Prowlarr lists (page mount) and the
// storage health (General tab mount). All four are admin only on the server,
// so each answered 403 and was logged as "ApiError: admin role required".
// The real General and About tabs render here so their mount fetches count.
const state = vi.hoisted(() => ({ isAdmin: false }))

vi.mock('../auth/AuthContext', () => ({
  useAuth: () => ({
    status: {
      authenticated: true,
      setupRequired: false,
      username: state.isAdmin ? 'admin' : 'reader',
      role: state.isAdmin ? 'admin' : 'user',
      mode: 'enabled',
    },
    loading: false,
    isAdmin: state.isAdmin,
    role: state.isAdmin ? 'admin' : 'user',
    isRequester: false,
    refresh: vi.fn(),
    logout: vi.fn(),
  }),
}))
vi.mock('../components/ThemeToggle', () => ({ default: () => <button type="button">Theme</button> }))
vi.mock('../components/LanguageSwitcher', () => ({ default: () => <select aria-label="Language" /> }))
vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, fallback?: unknown) => (typeof fallback === 'string' ? fallback : key),
    i18n: { changeLanguage: vi.fn() },
  }),
}))
vi.mock('../api/client', async importOriginal => {
  const actual = await importOriginal<typeof import('../api/client')>()
  return {
    ...actual,
    api: {
      ...actual.api,
      // Open to every role.
      listSettings: vi.fn(),
      authConfig: vi.fn(),
      status: vi.fn(),
      // Admin only on the server.
      listIndexers: vi.fn(),
      listDownloadClients: vi.fn(),
      listProwlarr: vi.fn(),
      getStorage: vi.fn(),
      libraryScanStatus: vi.fn(),
      listSettingDescriptors: vi.fn(),
      bookOrbitConfig: vi.fn(),
      bookOrbitImportStatus: vi.fn(),
      bookOrbitImportRuns: vi.fn(),
    },
  }
})

const ADMIN_ONLY = [
  'listIndexers',
  'listDownloadClients',
  'listProwlarr',
  'getStorage',
  'libraryScanStatus',
  'listSettingDescriptors',
  'bookOrbitConfig',
  'bookOrbitImportStatus',
  'bookOrbitImportRuns',
] as const

const forbidden = () => new ApiError(403, { error: 'admin role required' }, 'Forbidden')

function Where() {
  const loc = useLocation()
  return <p data-testid="where">{loc.search}</p>
}

function renderAt(entry: string) {
  return render(
    <MemoryRouter initialEntries={[entry]}>
      <Routes>
        <Route path="/settings" element={<><SettingsPage /><Where /></>} />
      </Routes>
    </MemoryRouter>,
  )
}

let errorSpy: ReturnType<typeof vi.spyOn>

beforeEach(() => {
  vi.mocked(api.listSettings).mockResolvedValue([])
  vi.mocked(api.authConfig).mockResolvedValue({ mode: 'enabled', apiKey: '', username: 'reader' })
  vi.mocked(api.status).mockResolvedValue({
    version: 'dev',
    commit: 'unknown',
    buildDate: '',
    enhancedHardcoverApi: false,
    hardcoverTokenConfigured: false,
    enhancedHardcoverDisabledReason: 'env_disabled',
  })
  for (const name of ADMIN_ONLY) {
    const fn = api[name] as unknown as ReturnType<typeof vi.fn>
    fn.mockReset()
    fn.mockImplementation(() => (state.isAdmin ? Promise.resolve([]) : Promise.reject(forbidden())))
  }
  // Storage is an object, not a list, for the admin case.
  vi.mocked(api.getStorage).mockImplementation(() => state.isAdmin
    ? Promise.resolve({ downloadDir: '/downloads', audiobookDownloadDir: '', libraryDir: '/books', audiobookDir: '', dirs: [], hardlinkable: true })
    : Promise.reject(forbidden()))
  errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
})

afterEach(() => {
  errorSpy.mockRestore()
})

// Wait for both visible tabs' open fetches, then one more turn so any
// rejected admin fetch has had its chance to reach console.error.
async function settle() {
  await waitFor(() => expect(api.listSettings).toHaveBeenCalled())
  await waitFor(() => expect(api.authConfig).toHaveBeenCalled())
  await new Promise(r => setTimeout(r, 20))
}

describe('Settings page requests by role', () => {
  it('makes no admin only request for a non admin and logs nothing', async () => {
    state.isAdmin = false
    renderAt('/settings')
    await settle()

    for (const name of ADMIN_ONLY) {
      expect(api[name], name).not.toHaveBeenCalled()
    }
    expect(errorSpy).not.toHaveBeenCalled()
  })

  it('makes no admin only request for a non admin landing on an admin tab link', async () => {
    state.isAdmin = false
    renderAt('/settings?tab=bookorbit')
    await waitFor(() => expect(screen.getByTestId('where').textContent).toBe('?tab=general'))
    await settle()

    for (const name of ADMIN_ONLY) {
      expect(api[name], name).not.toHaveBeenCalled()
    }
    expect(errorSpy).not.toHaveBeenCalled()
  })

  it('still loads the cross tab lists and storage for an admin', async () => {
    state.isAdmin = true
    renderAt('/settings')
    await settle()

    await waitFor(() => expect(api.listIndexers).toHaveBeenCalled())
    expect(api.listDownloadClients).toHaveBeenCalled()
    expect(api.listProwlarr).toHaveBeenCalled()
    await waitFor(() => expect(api.getStorage).toHaveBeenCalled())
    await waitFor(() => expect(api.libraryScanStatus).toHaveBeenCalled())
  })
})
