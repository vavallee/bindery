import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import SetupBanner from './SetupBanner'
import { api } from '../api/client'
import type { Indexer, DownloadClient, SetupState } from '../api/client'
import en from '../i18n/locales/en.json'

vi.mock('../api/client', async importOriginal => {
  const actual = await importOriginal<typeof import('../api/client')>()
  return {
    ...actual,
    api: {
      ...actual.api,
      listIndexers: vi.fn(),
      listDownloadClients: vi.fn(),
      setupState: vi.fn(),
      listRootFolders: vi.fn(),
      getStorage: vi.fn(),
    },
  }
})

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, fallback?: string) => fallback ?? key,
  }),
}))

function setupState(hasLibraryFolder: boolean | undefined): SetupState {
  return {
    hasIndexer: false, hasClient: false, hasAuthor: false, hasGrab: false, hasImport: false, complete: false,
    hasLibraryFolder,
  }
}

function setup({ indexer, client, library }: { indexer: boolean; client: boolean; library: boolean }) {
  vi.mocked(api.listIndexers).mockResolvedValue(indexer ? [{ id: 1 } as Indexer] : [])
  vi.mocked(api.listDownloadClients).mockResolvedValue(client ? [{ id: 1 } as DownloadClient] : [])
  vi.mocked(api.setupState).mockResolvedValue(setupState(library))
}

function renderIt() {
  return render(
    <MemoryRouter>
      <SetupBanner />
    </MemoryRouter>,
  )
}

describe('SetupBanner root folder', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    localStorage.clear()
  })

  it('asks for a root folder when there is no library folder', async () => {
    setup({ indexer: true, client: true, library: false })
    renderIt()

    expect(await screen.findByText(/add a root folder so imported books/)).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Set up Root Folders' })).toHaveAttribute('href', '/settings?tab=rootfolders')
  })

  it('names all three and links the root folder first on a fresh install', async () => {
    setup({ indexer: false, client: false, library: false })
    renderIt()

    expect(await screen.findByText(/add a root folder, an indexer and a download client/)).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Set up Root Folders' })).toHaveAttribute('href', '/settings?tab=rootfolders')
  })

  it('names the root folder and the one other missing piece', async () => {
    setup({ indexer: true, client: false, library: false })
    renderIt()

    expect(await screen.findByText(/add a root folder and a download client/)).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Set up Root Folders' })).toBeInTheDocument()
  })

  it('falls back to the indexer and client message once the library folder is usable', async () => {
    setup({ indexer: false, client: false, library: true })
    renderIt()

    expect(await screen.findByText(/add an indexer and a download client/)).toBeInTheDocument()
    expect(screen.queryByText(/root folder/)).not.toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Set up Indexers' })).toHaveAttribute('href', '/settings?tab=indexers')
  })

  // The library folder answer arrives separately from the indexer and client
  // one. Showing the two part message and then swapping it for the three part
  // one would announce the banner twice.
  it('waits for the library folder answer before saying anything', async () => {
    setup({ indexer: false, client: false, library: false })
    let resolveState: (v: SetupState) => void = () => {}
    vi.mocked(api.setupState).mockReturnValue(new Promise(r => { resolveState = r }))
    const { container } = renderIt()

    await waitFor(() => expect(api.listDownloadClients).toHaveBeenCalled())
    await Promise.resolve()
    await Promise.resolve()
    expect(container).toBeEmptyDOMElement()

    resolveState(setupState(false))
    expect(await screen.findByText(/add a root folder, an indexer and a download client/)).toBeInTheDocument()
  })

  // A dismissal stored before the root folder step existed has no root part.
  // It must still match its state once the library folder is fine.
  it('keeps honouring an older dismissal when the library folder is fine', async () => {
    localStorage.setItem('bindery.setupBannerDismissed', 'indexer+')
    setup({ indexer: false, client: true, library: true })
    const { container } = renderIt()

    await waitFor(() => expect(api.setupState).toHaveBeenCalled())
    await waitFor(() => expect(api.listDownloadClients).toHaveBeenCalled())
    await Promise.resolve()
    await Promise.resolve()
    expect(container).toBeEmptyDOMElement()
  })

  // The server leaves hasLibraryFolder out while its probe has no answer (a
  // hung mount). Unknown is not a reason to ask for a root folder.
  it('treats a missing library folder answer as done', async () => {
    vi.mocked(api.listIndexers).mockResolvedValue([])
    vi.mocked(api.listDownloadClients).mockResolvedValue([])
    vi.mocked(api.setupState).mockResolvedValue(setupState(undefined))
    renderIt()

    expect(await screen.findByText(/add an indexer and a download client/)).toBeInTheDocument()
    expect(screen.queryByText(/root folder/)).not.toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Set up Indexers' })).toBeInTheDocument()
  })

  it('never asks /system/storage, which probes the mounts uncached', async () => {
    setup({ indexer: false, client: false, library: false })
    renderIt()

    await screen.findByText(/add a root folder/)
    expect(api.getStorage).not.toHaveBeenCalled()
    expect(api.listRootFolders).not.toHaveBeenCalled()
  })

  it('renders nothing when everything is in place', async () => {
    setup({ indexer: true, client: true, library: true })
    const { container } = renderIt()

    await waitFor(() => expect(api.setupState).toHaveBeenCalled())
    await Promise.resolve()
    expect(container).toBeEmptyDOMElement()
  })

  // A dismissal is remembered per missing state, so dismissing the root folder
  // prompt does not also hide a later, different gap.
  it('remembers a dismissal for the root folder state only', async () => {
    setup({ indexer: true, client: true, library: false })
    const first = renderIt()
    await screen.findByText(/add a root folder/)
    fireEvent.click(screen.getByRole('button', { name: /dismiss/i }))
    expect(screen.queryByText(/add a root folder/)).not.toBeInTheDocument()
    first.unmount()

    setup({ indexer: false, client: true, library: false })
    renderIt()
    expect(await screen.findByText(/add a root folder and an indexer/)).toBeInTheDocument()
  })

  it('has its strings in en.json', () => {
    const banner = (en as { setupBanner: Record<string, string> }).setupBanner
    for (const k of ['rootFolder', 'rootFolderIndexer', 'rootFolderClient', 'all']) {
      expect(banner[k], k).toBeTruthy()
    }
  })
})
