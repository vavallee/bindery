import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import SetupChecklist from './SetupChecklist'
import { api } from '../api/client'
import type { RootFolder, SetupState, StorageHealth } from '../api/client'
import en from '../i18n/locales/en.json'

vi.mock('../api/client', async importOriginal => {
  const actual = await importOriginal<typeof import('../api/client')>()
  return {
    ...actual,
    api: { ...actual.api, setupState: vi.fn(), listRootFolders: vi.fn(), getStorage: vi.fn() },
  }
})

const authState = { isAdmin: true }
vi.mock('../auth/AuthContext', () => ({
  useIsAdmin: () => authState.isAdmin,
}))

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, fallback?: string) => fallback ?? key,
  }),
}))

function state(overrides: Partial<SetupState> = {}): SetupState {
  return {
    hasIndexer: false,
    hasClient: false,
    hasAuthor: false,
    hasGrab: false,
    hasImport: false,
    complete: false,
    ...overrides,
  }
}

function folder(path: string): RootFolder {
  return { id: 1, path, freeSpace: 0, createdAt: '' }
}

function storage(library: { exists: boolean; writable: boolean }): StorageHealth {
  return {
    downloadDir: '/downloads',
    audiobookDownloadDir: '',
    libraryDir: '/books',
    audiobookDir: '',
    dirs: [
      { name: 'download', path: '/downloads', exists: true, writable: true },
      { name: 'library', path: '/books', ...library },
    ],
    hardlinkable: false,
  }
}

function renderIt() {
  return render(
    <MemoryRouter>
      <SetupChecklist />
    </MemoryRouter>,
  )
}

describe('SetupChecklist root folder step', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    localStorage.clear()
    authState.isAdmin = true
    vi.mocked(api.listRootFolders).mockResolvedValue([])
    vi.mocked(api.getStorage).mockResolvedValue(storage({ exists: false, writable: false }))
  })

  // A fresh install with no root folder and no usable BINDERY_LIBRARY_DIR has
  // nowhere to put an import, so that comes before the indexer and client.
  it('asks for a root folder first when there is no library folder', async () => {
    vi.mocked(api.setupState).mockResolvedValue(state())
    renderIt()

    expect(await screen.findByText('Add a root folder')).toBeInTheDocument()
    expect(screen.getByText('0/6')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Set up Root Folders' })).toHaveAttribute('href', '/settings?tab=rootfolders')
    expect(screen.queryByText('Add an indexer')).not.toBeInTheDocument()
  })

  it('orders the steps root folder, indexer, download client', async () => {
    vi.mocked(api.setupState).mockResolvedValue(state({ hasIndexer: true, hasClient: true }))
    renderIt()

    // Root folder is still outstanding, so it wins over everything after it.
    expect(await screen.findByText('Add a root folder')).toBeInTheDocument()
    expect(screen.getByText('2/6')).toBeInTheDocument()
    const segments = screen.getByRole('img').children
    expect(segments).toHaveLength(6)
    expect(segments[0].className).not.toContain('bg-emerald-600')
    expect(segments[1].className).toContain('bg-emerald-600')
    expect(segments[2].className).toContain('bg-emerald-600')
  })

  it('counts the step done once a root folder exists', async () => {
    vi.mocked(api.listRootFolders).mockResolvedValue([folder('/mnt/books')])
    vi.mocked(api.setupState).mockResolvedValue(state())
    renderIt()

    expect(await screen.findByText('Add an indexer')).toBeInTheDocument()
    expect(screen.getByText('1/6')).toBeInTheDocument()
    expect(screen.queryByText('Add a root folder')).not.toBeInTheDocument()
    expect(api.getStorage).not.toHaveBeenCalled()
  })

  // No root folder row is needed when BINDERY_LIBRARY_DIR is mounted: the
  // importer falls back to it, so asking for a root folder would be wrong.
  it('counts the step done when the configured library folder is usable', async () => {
    vi.mocked(api.getStorage).mockResolvedValue(storage({ exists: true, writable: true }))
    vi.mocked(api.setupState).mockResolvedValue(state({ hasIndexer: true }))
    renderIt()

    expect(await screen.findByText('Add a download client')).toBeInTheDocument()
    expect(screen.getByText('2/6')).toBeInTheDocument()
  })

  it('still asks when the library folder exists but cannot be written', async () => {
    vi.mocked(api.getStorage).mockResolvedValue(storage({ exists: true, writable: false }))
    vi.mocked(api.setupState).mockResolvedValue(state())
    renderIt()

    expect(await screen.findByText('Add a root folder')).toBeInTheDocument()
  })

  // Non admins cannot add root folders and cannot read storage health, so the
  // step is left out rather than shown as something they cannot do.
  it('leaves the step out for non admins', async () => {
    authState.isAdmin = false
    vi.mocked(api.setupState).mockResolvedValue(state())
    renderIt()

    expect(await screen.findByText('Add an indexer')).toBeInTheDocument()
    expect(screen.getByText('0/5')).toBeInTheDocument()
    expect(screen.queryByText('Add a root folder')).not.toBeInTheDocument()
    expect(api.listRootFolders).not.toHaveBeenCalled()
    expect(api.getStorage).not.toHaveBeenCalled()
  })

  it('does not nag about root folders when the lookup fails', async () => {
    vi.mocked(api.listRootFolders).mockRejectedValue(new Error('boom'))
    vi.mocked(api.setupState).mockResolvedValue(state())
    renderIt()

    expect(await screen.findByText('Add an indexer')).toBeInTheDocument()
    expect(screen.getByText('1/6')).toBeInTheDocument()
  })

  it('waits for the root folder answer instead of flashing a later step', async () => {
    let resolveFolders: (v: RootFolder[]) => void = () => {}
    vi.mocked(api.listRootFolders).mockReturnValue(new Promise(r => { resolveFolders = r }))
    vi.mocked(api.setupState).mockResolvedValue(state())
    renderIt()

    await waitFor(() => expect(api.setupState).toHaveBeenCalled())
    await Promise.resolve()
    expect(screen.queryByText('Add an indexer')).not.toBeInTheDocument()
    resolveFolders([])
    expect(await screen.findByText('Add a root folder')).toBeInTheDocument()
  })

  it('has its strings in en.json', () => {
    const locale = en as { setupChecklist: Record<string, unknown>; gettingStarted: Record<string, string> }
    expect(locale.setupChecklist.rootFolder).toBeTruthy()
    expect(locale.gettingStarted.rootFolders).toBeTruthy()
  })
})
