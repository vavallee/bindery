import { describe, it, expect, vi, afterEach } from 'vitest'
import { act, fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter, Route, Routes, useLocation, useNavigate, useNavigationType } from 'react-router'
import type { NavigateFunction } from 'react-router'
import SettingsPage from './SettingsPage'
import { mockMatchMedia } from '../test-utils'

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
    },
  }
})
vi.mock('../components/ProtocolMismatchWarning', () => ({ default: () => null }))

// Stub every tab so the test is about the navigation, not the tabs.
vi.mock('./settings/GeneralTab', () => ({ default: () => <p>general tab</p> }))
vi.mock('./settings/AboutTab', () => ({ default: () => <p>about tab</p> }))
vi.mock('./settings/IndexersTab', () => ({ default: () => <p>indexers tab</p> }))
vi.mock('./settings/ClientsTab', () => ({ default: () => <p>clients tab</p> }))
vi.mock('./settings/NotificationsTab', () => ({ default: () => <p>notifications tab</p> }))
vi.mock('./settings/QualityTab', () => ({ default: () => <p>quality tab</p> }))
vi.mock('./settings/MetadataTab', () => ({ default: () => <p>metadata tab</p> }))
vi.mock('./settings/RootFoldersTab', () => ({ default: () => <p>rootfolders tab</p> }))
vi.mock('./settings/CalibreTab', () => ({ default: () => <p>calibre tab</p> }))
vi.mock('./settings/ABSTab', () => ({ default: () => <p>abs tab</p> }))
vi.mock('./settings/GrimmoryTab', () => ({ default: () => <p>grimmory tab</p> }))
vi.mock('./settings/ApiKeysTab', () => ({ default: () => <p>api keys tab</p> }))
vi.mock('./settings/ImportTab', () => ({ default: () => <p>import tab</p> }))
vi.mock('./settings/BlocklistTab', () => ({ default: () => <p>blocklist tab</p> }))
vi.mock('./settings/LogsTab', () => ({ default: () => <p>logs tab</p> }))
vi.mock('./settings/AdvancedTab', () => ({ default: () => <p>advanced tab</p> }))

let restore: () => void = () => {}

let navigate: NavigateFunction

// Shows where the router is and how it got there, and hands the test a
// navigate function to press back with.
function Probe() {
  const location = useLocation()
  navigate = useNavigate()
  return <output data-testid="where">{`${useNavigationType()} ${location.pathname}${location.search}`}</output>
}

function renderSettings(entry = '/settings') {
  return render(
    <MemoryRouter initialEntries={[entry]}>
      <Routes><Route path="/settings" element={<SettingsPage />} /></Routes>
      <Probe />
    </MemoryRouter>,
  )
}

const where = () => screen.getByTestId('where').textContent
afterEach(() => {
  restore()
  vi.restoreAllMocks()
})

describe('SettingsPage navigation on a phone', () => {
  // Below md the 16 entry sidebar stacked above the content, so tapping a
  // tab changed something a screen further down and the tap looked dead.
  it('replaces the sidebar with a select below md', async () => {
    restore = mockMatchMedia(q => q.includes('48rem'))
    renderSettings()
    const select = screen.getByRole('combobox', { name: 'settings.sectionLabel' })
    expect(screen.queryByRole('button', { name: 'settings.tabs.indexers' })).not.toBeInTheDocument()
    expect(await screen.findByText('general tab')).toBeInTheDocument()

    fireEvent.change(select, { target: { value: 'logs' } })
    expect(await screen.findByText('logs tab')).toBeInTheDocument()
    expect(select).toHaveValue('logs')
  })

  it('groups the select options under translated headings', () => {
    restore = mockMatchMedia(true)
    renderSettings()
    const groups = Array.from(document.querySelectorAll('optgroup')).map(g => g.label)
    expect(groups).toEqual([
      'settings.groups.sources',
      'settings.groups.library',
      'settings.groups.integrations',
      'settings.groups.system',
    ])
  })

  it('keeps the sidebar from md up', async () => {
    restore = mockMatchMedia(false)
    renderSettings()
    expect(screen.queryByRole('combobox', { name: 'settings.sectionLabel' })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'settings.tabs.indexers' })).toBeInTheDocument()
    expect(await screen.findByText('general tab')).toBeInTheDocument()
  })

  it('translates the sidebar group headings and the Preview chip', () => {
    renderSettings()
    for (const key of ['sources', 'library', 'integrations', 'system']) {
      expect(screen.getByText(`settings.groups.${key}`)).toBeInTheDocument()
    }
    expect(screen.getByText('settings.previewBadge')).toBeInTheDocument()
    expect(screen.queryByText('Sources')).not.toBeInTheDocument()
    expect(screen.queryByText('Preview')).not.toBeInTheDocument()
  })
})

describe('SettingsPage tab history', () => {
  // Android back left Settings entirely because tab changes replaced the
  // history entry instead of adding one. Tab changes now go through the
  // router, so each one is its own location with its own key.
  it('pushes a router entry per tab change', async () => {
    renderSettings()
    fireEvent.click(screen.getByRole('button', { name: 'settings.tabs.logs' }))
    expect(await screen.findByText('logs tab')).toBeInTheDocument()
    expect(where()).toBe('PUSH /settings?tab=logs')
  })

  it('replaces rather than pushes when the active tab is picked', async () => {
    renderSettings()
    fireEvent.click(screen.getByRole('button', { name: 'settings.tabs.general' }))
    expect(where()).toBe('REPLACE /settings?tab=general')
  })

  it('goes back to the previous tab', async () => {
    renderSettings()
    fireEvent.click(screen.getByRole('button', { name: 'settings.tabs.logs' }))
    expect(await screen.findByText('logs tab')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'settings.tabs.calibre' }))
    expect(await screen.findByText('calibre tab')).toBeInTheDocument()

    act(() => { void navigate(-1) })
    expect(await screen.findByText('logs tab')).toBeInTheDocument()
    act(() => { void navigate(-1) })
    expect(await screen.findByText('general tab')).toBeInTheDocument()
    expect(where()).toBe('POP /settings')
  })

  // The header Settings link goes to bare /settings. The tab follows the
  // URL, so that shows General instead of leaving the last tab up.
  it('shows General when the URL loses its tab', async () => {
    renderSettings('/settings?tab=logs')
    expect(await screen.findByText('logs tab')).toBeInTheDocument()
    act(() => { void navigate('/settings') })
    expect(await screen.findByText('general tab')).toBeInTheDocument()
  })

  it('still opens a deep linked tab', async () => {
    renderSettings('/settings?tab=calibre')
    expect(await screen.findByText('calibre tab')).toBeInTheDocument()
  })
})
