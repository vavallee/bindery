import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import SetupChecklist from './SetupChecklist'
import { api } from '../api/client'
import type { SetupState } from '../api/client'
import en from '../i18n/locales/en.json'

vi.mock('../api/client', async importOriginal => {
  const actual = await importOriginal<typeof import('../api/client')>()
  return { ...actual, api: { ...actual.api, setupState: vi.fn() } }
})

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, fallback?: string) => fallback ?? key,
  }),
}))

function state(overrides: Partial<SetupState> = {}): SetupState {
  return {
    hasIndexer: true,
    hasClient: true,
    hasAuthor: true,
    hasGrab: true,
    hasImport: true,
    complete: true,
    ...overrides,
  }
}

function renderIt() {
  return render(
    <MemoryRouter>
      <SetupChecklist />
    </MemoryRouter>,
  )
}

describe('SetupChecklist blocked state', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    localStorage.clear()
  })

  it('speaks after setup is complete when the pipeline is blocked', async () => {
    vi.mocked(api.setupState).mockResolvedValue(
      state({ blocked: true, blockedArea: 'storage', blockedReason: 'A library or download folder is missing' }),
    )
    renderIt()

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('Imports have a problem right now')
    expect(alert).toHaveTextContent('not available')
    expect(screen.getByRole('link', { name: 'Check storage' })).toHaveAttribute('href', '/settings?tab=general')
  })

  it('points a failing download client at the clients tab', async () => {
    vi.mocked(api.setupState).mockResolvedValue(state({ blocked: true, blockedArea: 'download-client' }))
    renderIt()

    expect(await screen.findByRole('alert')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Set up Download Clients' })).toHaveAttribute('href', '/settings?tab=clients')
  })

  it('points a missing indexer at the indexers tab', async () => {
    vi.mocked(api.setupState).mockResolvedValue(state({ blocked: true, blockedArea: 'indexer' }))
    renderIt()

    expect(await screen.findByRole('alert')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Set up Indexers' })).toHaveAttribute('href', '/settings?tab=indexers')
  })

  it('still shows when the checklist itself was dismissed earlier', async () => {
    localStorage.setItem('bindery.setupChecklistDismissed', '1')
    vi.mocked(api.setupState).mockResolvedValue(state({ blocked: true, blockedArea: 'storage' }))
    renderIt()

    expect(await screen.findByRole('alert')).toBeInTheDocument()
  })

  it('renders nothing for a complete, unblocked install', async () => {
    vi.mocked(api.setupState).mockResolvedValue(state())
    const { container } = renderIt()

    await waitFor(() => expect(api.setupState).toHaveBeenCalled())
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(container).toBeEmptyDOMElement()
  })

  it('refetches on window focus after a minute, not before', async () => {
    const now = vi.spyOn(Date, 'now')
    now.mockReturnValue(1_000_000)
    vi.mocked(api.setupState).mockResolvedValue(state())
    renderIt()
    await waitFor(() => expect(api.setupState).toHaveBeenCalledTimes(1))

    now.mockReturnValue(1_000_000 + 30_000)
    window.dispatchEvent(new Event('focus'))
    expect(api.setupState).toHaveBeenCalledTimes(1)

    vi.mocked(api.setupState).mockResolvedValue(state({ blocked: true, blockedArea: 'storage' }))
    now.mockReturnValue(1_000_000 + 61_000)
    window.dispatchEvent(new Event('focus'))
    expect(await screen.findByRole('alert')).toBeInTheDocument()
    expect(api.setupState).toHaveBeenCalledTimes(2)
    now.mockRestore()
  })

  it('clears the notice when a later fetch says it is fixed', async () => {
    const now = vi.spyOn(Date, 'now')
    now.mockReturnValue(1_000_000)
    vi.mocked(api.setupState).mockResolvedValue(state({ blocked: true, blockedArea: 'storage' }))
    renderIt()
    expect(await screen.findByRole('alert')).toBeInTheDocument()

    vi.mocked(api.setupState).mockResolvedValue(state())
    now.mockReturnValue(1_000_000 + 61_000)
    window.dispatchEvent(new Event('focus'))
    await waitFor(() => expect(screen.queryByRole('alert')).not.toBeInTheDocument())
    now.mockRestore()
  })

  // role=alert is announced when inserted, so an unchanged response must keep
  // the very same element rather than replace it.
  it('keeps the same alert element when the refetch is identical', async () => {
    const now = vi.spyOn(Date, 'now')
    now.mockReturnValue(1_000_000)
    vi.mocked(api.setupState).mockImplementation(() => Promise.resolve(state({ blocked: true, blockedArea: 'storage' })))
    renderIt()
    const first = await screen.findByRole('alert')

    now.mockReturnValue(1_000_000 + 61_000)
    window.dispatchEvent(new Event('focus'))
    await waitFor(() => expect(api.setupState).toHaveBeenCalledTimes(2))
    await Promise.resolve()
    expect(screen.getByRole('alert')).toBe(first)
    now.mockRestore()
  })

  it('has its strings in en.json', () => {
    const blocked = (en as { setupChecklist: { blocked: Record<string, string> } }).setupChecklist.blocked
    for (const k of ['title', 'storage', 'storageAction', 'indexer', 'downloadClient']) {
      expect(blocked[k], k).toBeTruthy()
    }
  })
})
