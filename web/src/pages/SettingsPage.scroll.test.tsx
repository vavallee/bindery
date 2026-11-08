import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes, useNavigate } from 'react-router'
import type { NavigateFunction } from 'react-router'
import SettingsPage from './SettingsPage'
import ScrollRestoration from '../components/ScrollRestoration'
import { mockMatchMedia } from '../test-utils'

// Switching Settings tabs left the window scrolled where the previous tab was,
// so the new section and the Settings header sat off screen. A tab change now
// starts at the top, on the sidebar and on the phone select alike, while a
// link to a field (#3059) still lands on that field.

vi.mock('../auth/AuthContext', () => ({
  useAuth: () => ({ isAdmin: true, status: { authenticated: true, role: 'admin' } }),
}))
vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string) => key,
    i18n: { changeLanguage: vi.fn() },
  }),
}))
vi.mock('../api/client', async importOriginal => {
  const actual = await importOriginal<typeof import('../api/client')>()
  return {
    ...actual,
    api: {
      ...actual.api,
      listIndexers: vi.fn().mockResolvedValue([]),
      listDownloadClients: vi.fn().mockResolvedValue([]),
      listProwlarr: vi.fn().mockResolvedValue([]),
      listSettingDescriptors: vi.fn().mockResolvedValue([]),
    },
  }
})
vi.mock('../components/ProtocolMismatchWarning', () => ({ default: () => null }))
vi.mock('./settings/GeneralTab', () => ({ default: () => <p>general tab</p> }))
vi.mock('./settings/AboutTab', () => ({ default: () => <p>about tab</p> }))
vi.mock('./settings/IndexersTab', () => ({ default: () => <p>indexers tab</p> }))
vi.mock('./settings/ClientsTab', () => ({ default: () => <p>clients tab</p> }))
vi.mock('./settings/NotificationsTab', () => ({ default: () => <p>notifications tab</p> }))
vi.mock('./settings/QualityTab', () => ({ default: () => <p>quality tab</p> }))
vi.mock('./settings/MetadataTab', () => ({
  default: () => (
    <div>
      <p>metadata tab</p>
      <div id="metadata.primary_provider"><select aria-label="provider"><option>openlibrary</option></select></div>
    </div>
  ),
}))
vi.mock('./settings/RootFoldersTab', () => ({ default: () => <p>rootfolders tab</p> }))
vi.mock('./settings/CalibreTab', () => ({ default: () => <p>calibre tab</p> }))
vi.mock('./settings/ABSTab', () => ({ default: () => <p>abs tab</p> }))
vi.mock('./settings/GrimmoryTab', () => ({ default: () => <p>grimmory tab</p> }))
vi.mock('./settings/ApiKeysTab', () => ({ default: () => <p>api keys tab</p> }))
vi.mock('./settings/ImportTab', () => ({ default: () => <p>import tab</p> }))
vi.mock('./settings/BlocklistTab', () => ({ default: () => <p>blocklist tab</p> }))
vi.mock('./settings/LogsTab', () => ({ default: () => <p>logs tab</p> }))
vi.mock('./settings/AdvancedTab', () => ({ default: () => <p>advanced tab</p> }))

let navigate: NavigateFunction
function NavHandle() {
  navigate = useNavigate()
  return null
}

function renderSettings(entry = '/settings') {
  return render(
    <MemoryRouter initialEntries={[entry]}>
      <ScrollRestoration />
      <NavHandle />
      <Routes><Route path="/settings" element={<SettingsPage />} /></Routes>
    </MemoryRouter>,
  )
}

const scrollTo = vi.fn()
const scrollIntoView = vi.fn()
let restoreMedia: () => void = () => {}
const originalScrollIntoView = Element.prototype.scrollIntoView

function scrolledDown() {
  Object.defineProperty(window, 'scrollY', { configurable: true, value: 293 })
  window.dispatchEvent(new Event('scroll'))
}

beforeEach(() => {
  sessionStorage.clear()
  scrollTo.mockReset()
  scrollIntoView.mockReset()
  vi.stubGlobal('scrollTo', scrollTo)
  Element.prototype.scrollIntoView = scrollIntoView
})

afterEach(() => {
  restoreMedia()
  restoreMedia = () => {}
  vi.unstubAllGlobals()
  Element.prototype.scrollIntoView = originalScrollIntoView
})

describe('SettingsPage scroll on tab change', () => {
  it('does not scroll on the first load', async () => {
    renderSettings('/settings?tab=indexers')
    expect(await screen.findByText('indexers tab')).toBeInTheDocument()
    expect(scrollTo).not.toHaveBeenCalled()
  })

  it('scrolls to the top when a sidebar tab is clicked', async () => {
    restoreMedia = mockMatchMedia(false)
    renderSettings()
    expect(await screen.findByText('general tab')).toBeInTheDocument()
    scrolledDown()
    fireEvent.click(screen.getByRole('button', { name: 'settings.tabs.logs' }))
    expect(await screen.findByText('logs tab')).toBeInTheDocument()
    expect(scrollTo).toHaveBeenLastCalledWith(0, 0)
  })

  it('scrolls to the top when the phone select changes', async () => {
    restoreMedia = mockMatchMedia(q => q.includes('48rem'))
    renderSettings()
    expect(await screen.findByText('general tab')).toBeInTheDocument()
    scrolledDown()
    fireEvent.change(screen.getByRole('combobox', { name: 'settings.sectionLabel' }), { target: { value: 'clients' } })
    expect(await screen.findByText('clients tab')).toBeInTheDocument()
    expect(scrollTo).toHaveBeenLastCalledWith(0, 0)
  })

  it('still lands on the field when the tab change carries a field hash', async () => {
    renderSettings('/settings?tab=general')
    expect(await screen.findByText('general tab')).toBeInTheDocument()
    scrolledDown()
    await act(async () => { await navigate('/settings?tab=metadata#metadata.primary_provider') })
    expect(await screen.findByText('metadata tab')).toBeInTheDocument()
    await waitFor(() => expect(scrollIntoView).toHaveBeenCalledWith({ block: 'center' }))
    expect(scrollIntoView.mock.contexts.at(-1)).toBe(document.getElementById('metadata.primary_provider'))
    expect(scrollTo).not.toHaveBeenCalledWith(0, 0)
  })
})
