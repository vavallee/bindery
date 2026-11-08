import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import SetupChecklist from './SetupChecklist'
import { api } from '../api/client'
import type { SetupState } from '../api/client'
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
    hasLibraryFolder: false,
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

// /system/storage probes the media mounts with no cache on every call, so the
// checklist must answer from the setup state alone.
function expectNoStorageCalls() {
  expect(api.getStorage).not.toHaveBeenCalled()
  expect(api.listRootFolders).not.toHaveBeenCalled()
}

describe('SetupChecklist root folder step', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    localStorage.clear()
    authState.isAdmin = true
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
    expectNoStorageCalls()
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

  it('counts the step done when the server says there is a library folder', async () => {
    vi.mocked(api.setupState).mockResolvedValue(state({ hasLibraryFolder: true, hasIndexer: true }))
    renderIt()

    expect(await screen.findByText('Add a download client')).toBeInTheDocument()
    expect(screen.getByText('2/6')).toBeInTheDocument()
    expect(screen.queryByText('Add a root folder')).not.toBeInTheDocument()
    expectNoStorageCalls()
  })

  // The server leaves the field out while its storage probe has no answer
  // (a hung mount). That is not a reason to nag.
  it('counts the step done when the server leaves the answer out', async () => {
    vi.mocked(api.setupState).mockResolvedValue(state({ hasLibraryFolder: undefined }))
    renderIt()

    expect(await screen.findByText('Add an indexer')).toBeInTheDocument()
    expect(screen.getByText('1/6')).toBeInTheDocument()
  })

  // Non admins cannot add root folders, so the step is left out rather than
  // shown as something they cannot do.
  it('leaves the step out for non admins', async () => {
    authState.isAdmin = false
    vi.mocked(api.setupState).mockResolvedValue(state({ hasLibraryFolder: undefined }))
    renderIt()

    expect(await screen.findByText('Add an indexer')).toBeInTheDocument()
    expect(screen.getByText('0/5')).toBeInTheDocument()
    expect(screen.queryByText('Add a root folder')).not.toBeInTheDocument()
    expectNoStorageCalls()
  })

  it('shows nothing until the setup state arrives, then never names the root folder when it is fine', async () => {
    let resolveState: (v: SetupState) => void = () => {}
    vi.mocked(api.setupState).mockReturnValue(new Promise(r => { resolveState = r }))
    const seen: string[] = []
    const { container } = renderIt()
    const observer = new MutationObserver(() => seen.push(container.textContent ?? ''))
    observer.observe(container, { childList: true, subtree: true, characterData: true })

    await waitFor(() => expect(api.setupState).toHaveBeenCalled())
    expect(container).toBeEmptyDOMElement()

    resolveState(state({ hasLibraryFolder: true }))
    expect(await screen.findByText('Add an indexer')).toBeInTheDocument()
    observer.disconnect()
    expect(seen.some(text => text.includes('Add a root folder'))).toBe(false)
  })

  it('makes no storage call once setup is complete', async () => {
    vi.mocked(api.setupState).mockResolvedValue(state({
      hasLibraryFolder: true, hasIndexer: true, hasClient: true, hasAuthor: true, hasGrab: true, hasImport: true, complete: true,
    }))
    const { container } = renderIt()

    await waitFor(() => expect(api.setupState).toHaveBeenCalled())
    await Promise.resolve()
    expect(container).toBeEmptyDOMElement()
    expectNoStorageCalls()
  })

  it('makes no storage call when the checklist was dismissed', async () => {
    localStorage.setItem('bindery.setupChecklistDismissed', '1')
    vi.mocked(api.setupState).mockResolvedValue(state())
    const { container } = renderIt()

    await waitFor(() => expect(api.setupState).toHaveBeenCalled())
    await Promise.resolve()
    expect(container).toBeEmptyDOMElement()
    expectNoStorageCalls()
  })

  it('has its strings in en.json', () => {
    const locale = en as { setupChecklist: Record<string, unknown>; gettingStarted: Record<string, string> }
    expect(locale.setupChecklist.rootFolder).toBeTruthy()
    expect(locale.gettingStarted.rootFolders).toBeTruthy()
  })
})
