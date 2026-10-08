import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor, within } from '@testing-library/react'

// Keys only: the assertions name i18n keys, never English copy.
vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, opts?: unknown) =>
      opts && typeof opts === 'object' ? `${key} ${JSON.stringify(opts)}` : key,
  }),
}))

vi.mock('../../api/client', () => ({
  BINDERY_BASE: '',
  api: {
    uploadMigrate: vi.fn(),
    lookupManualImport: vi.fn(),
    manualImport: vi.fn(),
    listImportLists: vi.fn(),
    hardcoverLists: vi.fn(),
    listUsers: vi.fn(),
    deleteImportList: vi.fn(),
    updateImportList: vi.fn(),
    addImportList: vi.fn(),
    syncImportList: vi.fn(),
    importListSyncStatus: vi.fn(),
    goodreadsPreview: vi.fn(),
  },
}))

import { api, type ImportList, type ImportListSyncProgress } from '../../api/client'
import ImportTab from './ImportTab'

const mocked = api as unknown as Record<string, ReturnType<typeof vi.fn>>

function importList(overrides: Partial<ImportList> = {}): ImportList {
  return {
    id: 1,
    name: 'Want to Read',
    type: 'hardcover',
    url: 'want-to-read',
    apiKey: '',
    apiKeyConfigured: false,
    account: 'reader',
    monitorNew: true,
    autoAdd: true,
    enabled: true,
    mediaType: '',
    ownerUserId: null,
    ...overrides,
  } as ImportList
}

function progress(overrides: Partial<ImportListSyncProgress> = {}): ImportListSyncProgress {
  return {
    running: false,
    listId: 1,
    startedAt: '2026-09-01T10:00:00Z',
    stats: { total: 0, processed: 0, imported: 0, skipped: 0, failed: 0 },
    ...overrides,
  } as ImportListSyncProgress
}

const book = (id: number, title: string) => ({ id, title, author: { authorName: 'Frank Herbert' } })

function fileInput(container: HTMLElement, acceptPrefix: string): HTMLInputElement {
  const input = container.querySelector(`input[type="file"][accept^="${acceptPrefix}"]`)
  if (!input) throw new Error(`file input ${acceptPrefix} not found`)
  return input as HTMLInputElement
}

function manualPathInput(): HTMLInputElement {
  return screen.getByPlaceholderText('settings.import.manualImportPathPlaceholder') as HTMLInputElement
}

async function rowFor(name: string): Promise<HTMLElement> {
  const label = await screen.findByText(name, { selector: 'span.text-sm' })
  return label.closest('div.p-3') as HTMLElement
}

describe('ImportTab coverage', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocked.listImportLists.mockResolvedValue([])
    mocked.hardcoverLists.mockResolvedValue({ account: 'reader', lists: [] })
    mocked.listUsers.mockResolvedValue([{ id: 2, username: 'alice', role: 'user' }])
    mocked.importListSyncStatus.mockResolvedValue(progress())
  })


  it('uploads a CSV and shows the per-author failures', async () => {
    mocked.uploadMigrate.mockResolvedValue({
      requested: 3, added: 1, skipped: 1, errors: 1, failures: { 'Bad Author': 'not found' },
    })
    const { container } = render(<ImportTab />)
    const file = new File(['name\nFrank Herbert'], 'authors.csv', { type: 'text/csv' })
    fireEvent.change(fileInput(container, '.csv'), { target: { files: [file] } })

    expect(await screen.findByText('3 requested · 1 added · 1 skipped (already exist) · 1 failed')).toBeInTheDocument()
    expect(mocked.uploadMigrate).toHaveBeenCalledWith('csv', expect.any(FormData))
    const fd = mocked.uploadMigrate.mock.calls[0][1] as FormData
    expect((fd.get('file') as File).name).toBe('authors.csv')
    expect(screen.getByText('Show 1 failures')).toBeInTheDocument()
    expect(screen.getByText('Bad Author')).toBeInTheDocument()
  })

  it('uploads a Readarr database and shows each section', async () => {
    mocked.uploadMigrate.mockResolvedValue({
      authors: { requested: 2, added: 2 },
      indexers: { requested: 1, added: 1 },
      downloadClients: {},
    })
    const { container } = render(<ImportTab />)
    fireEvent.change(fileInput(container, '.db'), { target: { files: [new File(['x'], 'readarr.db')] } })

    expect(await screen.findByText('Indexers')).toBeInTheDocument()
    expect(screen.getByText('Download clients')).toBeInTheDocument()
    expect(screen.queryByText('Blocklist')).not.toBeInTheDocument()
    expect(mocked.uploadMigrate).toHaveBeenCalledWith('readarr', expect.any(FormData))
    expect(screen.getByText('0 requested · 0 added · 0 skipped (already exist) · 0 failed')).toBeInTheDocument()
  })

  it('shows an upload failure and ignores an empty file selection', async () => {
    mocked.uploadMigrate.mockRejectedValue(new Error('not a sqlite database'))
    const { container } = render(<ImportTab />)
    fireEvent.change(fileInput(container, '.db'), { target: { files: [] } })
    expect(mocked.uploadMigrate).not.toHaveBeenCalled()

    fireEvent.change(fileInput(container, '.db'), { target: { files: [new File(['x'], 'bad.db')] } })
    expect(await screen.findByText('not a sqlite database')).toBeInTheDocument()
  })

  it('imports a confident manual match with the detected format', async () => {
    mocked.lookupManualImport.mockResolvedValue({
      match: 'confident', book: book(9, 'Dune'), detectedFormat: 'ebook', parsedTitle: 'Dune', parsedAuthor: 'Frank Herbert',
    })
    mocked.manualImport.mockResolvedValue({})
    render(<ImportTab />)
    const lookup = screen.getByRole('button', { name: 'settings.import.manualImportLookup' })
    expect(lookup).toBeDisabled()

    fireEvent.change(manualPathInput(), { target: { value: '  /downloads/Dune.epub  ' } })
    fireEvent.keyDown(manualPathInput(), { key: 'Enter' })
    expect(await screen.findByText(/settings\.import\.manualImportConfident .*"title":"Dune"/)).toBeInTheDocument()
    expect(mocked.lookupManualImport).toHaveBeenCalledWith('/downloads/Dune.epub')

    fireEvent.click(screen.getByRole('button', { name: 'settings.import.manualImportConfirm' }))
    expect(await screen.findByText('settings.import.manualImportSuccess')).toBeInTheDocument()
    expect(mocked.manualImport).toHaveBeenCalledWith({ path: '/downloads/Dune.epub', bookId: 9, format: 'ebook' })
  })

  it('makes the user pick an ambiguous match and reports an import failure', async () => {
    mocked.lookupManualImport.mockResolvedValue({
      match: 'ambiguous',
      candidates: [book(1, 'Dune'), { id: 2, title: 'Dune Messiah' }],
      detectedFormat: '',
      parsedTitle: 'Dune',
      parsedAuthor: '',
    })
    mocked.manualImport.mockRejectedValue(new Error('file vanished'))
    render(<ImportTab />)
    fireEvent.change(manualPathInput(), { target: { value: '/downloads/Dune' } })
    fireEvent.click(screen.getByRole('button', { name: 'settings.import.manualImportLookup' }))

    expect(await screen.findByText('settings.import.manualImportAmbiguous')).toBeInTheDocument()
    const confirm = screen.getByRole('button', { name: 'settings.import.manualImportConfirm' })
    expect(confirm).toBeDisabled()

    const [candidateSelect, formatSelect] = screen.getAllByRole('combobox')
    fireEvent.change(candidateSelect, { target: { value: '2' } })
    fireEvent.change(formatSelect, { target: { value: 'audiobook' } })
    expect(confirm).toBeEnabled()
    fireEvent.click(confirm)

    expect(await screen.findByText('file vanished')).toBeInTheDocument()
    expect(mocked.manualImport).toHaveBeenCalledWith({ path: '/downloads/Dune', bookId: 2, format: 'audiobook' })
  })

  it('says when nothing matched, and when the lookup fails', async () => {
    mocked.lookupManualImport.mockResolvedValueOnce({ match: 'none', detectedFormat: '', parsedTitle: '', parsedAuthor: '' })
    render(<ImportTab />)
    fireEvent.change(manualPathInput(), { target: { value: '/downloads/x' } })
    fireEvent.click(screen.getByRole('button', { name: 'settings.import.manualImportLookup' }))
    expect(await screen.findByText('settings.import.manualImportNone')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'settings.import.manualImportConfirm' })).not.toBeInTheDocument()

    mocked.lookupManualImport.mockRejectedValueOnce(new Error('path outside library'))
    fireEvent.click(screen.getByRole('button', { name: 'settings.import.manualImportLookup' }))
    expect(await screen.findByText('path outside library')).toBeInTheDocument()

    // Typing clears the previous outcome.
    fireEvent.change(manualPathInput(), { target: { value: '/downloads/y' } })
    expect(screen.queryByText('path outside library')).not.toBeInTheDocument()
  })

  it('links to the folder import on the Import page', () => {
    render(<ImportTab />)
    expect(screen.getByRole('link', { name: 'settings.import.folderMovedLink' })).toHaveAttribute('href', '/import?view=folder')
  })

  it('shows the empty state when there are no Hardcover lists', async () => {
    render(<ImportTab />)
    expect(await screen.findByText('settings.import.hardcoverEmpty')).toBeInTheDocument()
    expect(mocked.hardcoverLists).toHaveBeenCalledWith(undefined)
  })

  it('renders saved, remote only and stale rows and edits a saved list', async () => {
    const saved = importList()
    const stale = importList({ id: 3, name: 'Gone', url: 'gone', lastSyncAt: '2026-09-01T10:00:00Z', apiKeyConfigured: true } as Partial<ImportList>)
    const otherAccount = importList({ id: 4, name: 'Elsewhere', url: 'elsewhere', account: 'someone-else' })
    mocked.listImportLists.mockResolvedValue([
      saved, stale, otherAccount, importList({ id: 99, type: 'goodreads', name: 'Not hardcover' }),
    ])
    mocked.hardcoverLists.mockResolvedValue({
      account: 'reader',
      lists: [
        { id: 10, name: 'Want to Read', slug: 'want-to-read', booksCount: 12 },
        { id: 11, name: 'Favourites', slug: 'favourites', booksCount: 4 },
      ],
    })
    mocked.updateImportList.mockImplementation((id: number, patch: Partial<ImportList>) =>
      Promise.resolve(importList({ ...saved, id, ...patch })))
    mocked.deleteImportList.mockResolvedValue(undefined)
    render(<ImportTab />)

    const savedRow = await rowFor('Want to Read')
    expect(within(savedRow).getByText(/12 books/)).toBeInTheDocument()
    expect(within(savedRow).getByText(/settings\.import\.hardcoverNeverSynced/)).toBeInTheDocument()
    expect(within(savedRow).getByText('settings.import.hardcoverGlobalToken')).toBeInTheDocument()
    expect(screen.queryByText('Not hardcover')).not.toBeInTheDocument()

    const favRow = await rowFor('Favourites')
    expect(within(favRow).getByText(/settings\.import\.hardcoverNotSelected/)).toBeInTheDocument()
    expect(within(favRow).getByText('@reader')).toBeInTheDocument()

    const staleRow = await rowFor('Gone')
    expect(within(staleRow).getByText('settings.import.hardcoverSavedOnly')).toBeInTheDocument()
    expect(within(staleRow).getByText('settings.import.hardcoverOverrideConfigured')).toBeInTheDocument()
    expect(within(staleRow).getByText(/settings\.import\.hardcoverLastSync/)).toBeInTheDocument()

    const elsewhereRow = await rowFor('Elsewhere')
    expect(within(elsewhereRow).queryByText('settings.import.hardcoverSavedOnly')).not.toBeInTheDocument()

    // Media type, owner and the download toggle each patch only their field.
    fireEvent.change(within(savedRow).getByRole('combobox', { name: 'settings.import.hardcoverMediaType' }), { target: { value: 'audiobook' } })
    await waitFor(() => expect(mocked.updateImportList).toHaveBeenCalledWith(1, { mediaType: 'audiobook' }))
    const owner = within(savedRow).getByRole('combobox', { name: 'settings.import.ownerLabel' })
    expect(within(owner).getByRole('option', { name: 'alice' })).toBeInTheDocument()
    fireEvent.change(owner, { target: { value: '2' } })
    await waitFor(() => expect(mocked.updateImportList).toHaveBeenCalledWith(1, { ownerUserId: 2 }))
    fireEvent.change(within(await rowFor('Want to Read')).getByRole('combobox', { name: 'settings.import.ownerLabel' }), { target: { value: '' } })
    await waitFor(() => expect(mocked.updateImportList).toHaveBeenCalledWith(1, { ownerUserId: null }))
    fireEvent.click(within(await rowFor('Want to Read')).getByRole('checkbox', { name: 'settings.import.downloadBooks' }))
    await waitFor(() => expect(mocked.updateImportList).toHaveBeenCalledWith(1, { monitorNew: false }))

    // Unticking a saved remote list toggles it rather than creating another.
    fireEvent.click(within(await rowFor('Want to Read')).getByRole('checkbox', { name: /hardcoverImportList/ }))
    await waitFor(() => expect(mocked.updateImportList).toHaveBeenCalledWith(1, { enabled: false }))
    // The stale row has no remote list: its checkbox toggles the saved one.
    fireEvent.click(within(staleRow).getByRole('checkbox', { name: /hardcoverImportList/ }))
    await waitFor(() => expect(mocked.updateImportList).toHaveBeenCalledWith(3, { enabled: false }))

    fireEvent.click(within(await rowFor('Elsewhere')).getByRole('button', { name: 'common.delete' }))
    await waitFor(() => expect(screen.queryByText('Elsewhere', { selector: 'span.text-sm' })).not.toBeInTheDocument())
    expect(mocked.deleteImportList).toHaveBeenCalledWith(4)
  })

  it('shows row update failures', async () => {
    mocked.listImportLists.mockResolvedValue([importList()])
    mocked.updateImportList.mockRejectedValue(new Error('server said no'))
    render(<ImportTab />)
    const row = await rowFor('Want to Read')

    fireEvent.change(within(row).getByRole('combobox', { name: 'settings.import.hardcoverMediaType' }), { target: { value: 'ebook' } })
    expect(await screen.findByText('server said no')).toBeInTheDocument()
    fireEvent.change(within(row).getByRole('combobox', { name: 'settings.import.ownerLabel' }), { target: { value: '2' } })
    await waitFor(() => expect(mocked.updateImportList).toHaveBeenCalledWith(1, { ownerUserId: 2 }))
    fireEvent.click(within(row).getByRole('checkbox', { name: 'settings.import.downloadBooks' }))
    await waitFor(() => expect(mocked.updateImportList).toHaveBeenCalledWith(1, { monitorNew: false }))
    expect(screen.getByText('server said no')).toBeInTheDocument()
  })

  it('adds a remote list with a picker token and switches back to the saved token', async () => {
    mocked.hardcoverLists.mockImplementation((token?: string) => Promise.resolve(token
      ? { account: 'second', lists: [{ id: 20, name: 'Second Shelf', slug: 'shelf', booksCount: 2 }] }
      : { account: 'reader', lists: [{ id: 10, name: 'Want to Read', slug: 'want-to-read', booksCount: 1 }] }))
    mocked.addImportList.mockImplementation((body: Partial<ImportList>) =>
      Promise.resolve(importList({ ...body, id: 50 })))
    render(<ImportTab />)
    await rowFor('Want to Read')

    const tokenInput = screen.getByPlaceholderText('settings.import.hardcoverTokenPlaceholder')
    fireEvent.change(tokenInput, { target: { value: '  tok-2  ' } })
    fireEvent.click(screen.getByRole('button', { name: 'settings.import.hardcoverLoadOverride' }))
    await waitFor(() => expect(mocked.hardcoverLists).toHaveBeenCalledWith('tok-2'))

    const row = await rowFor('Second Shelf')
    fireEvent.click(within(row).getByRole('checkbox', { name: /hardcoverImportList/ }))
    await waitFor(() => expect(mocked.addImportList).toHaveBeenCalledWith({
      name: 'Second Shelf',
      type: 'hardcover',
      url: 'shelf',
      apiKey: 'tok-2',
      account: 'second',
      enabled: true,
      monitorNew: true,
      autoAdd: true,
      ownerUserId: null,
    }))
    expect(await within(await rowFor('Second Shelf')).findByText('settings.import.hardcoverGlobalToken')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'settings.import.hardcoverUseSavedToken' }))
    await waitFor(() => expect(mocked.hardcoverLists).toHaveBeenLastCalledWith(undefined))
    expect(await screen.findByText('Want to Read', { selector: 'span.text-sm' })).toBeInTheDocument()
  })

  it('adds a saved-token list disabled and reports a failed add', async () => {
    mocked.hardcoverLists.mockResolvedValue({ account: 'reader', lists: [{ id: 10, name: 'Favourites', slug: 'favourites', booksCount: 1 }] })
    mocked.addImportList.mockRejectedValueOnce(new Error('duplicate list'))
    render(<ImportTab />)
    const row = await rowFor('Favourites')
    fireEvent.click(within(row).getByRole('checkbox', { name: /hardcoverImportList/ }))
    expect(await screen.findByText('duplicate list')).toBeInTheDocument()
    expect(mocked.addImportList).toHaveBeenCalledWith(expect.objectContaining({ apiKey: '', enabled: false, url: 'favourites' }))
  })

  it('offers the API keys link when the Hardcover token is missing', async () => {
    mocked.hardcoverLists.mockRejectedValue(new Error('Hardcover token not configured'))
    mocked.listImportLists.mockRejectedValue(new Error('boom'))
    const onNavigate = vi.fn()
    render(<ImportTab onNavigate={onNavigate} />)
    expect(await screen.findByText('Hardcover token not configured')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'settings.import.configureHardcoverToken' }))
    expect(onNavigate).toHaveBeenCalledWith('api-keys')
  })

  it('saves and clears a per-list token override', async () => {
    const withKey = importList({ apiKeyConfigured: true })
    mocked.listImportLists.mockResolvedValue([withKey])
    mocked.updateImportList.mockResolvedValueOnce(withKey).mockResolvedValueOnce(importList({ apiKeyConfigured: false }))
    render(<ImportTab />)
    const row = await rowFor('Want to Read')

    fireEvent.click(within(row).getByRole('button', { name: 'settings.import.hardcoverTokenOverride' }))
    const input = within(row).getByPlaceholderText('settings.import.hardcoverTokenOverridePlaceholderConfigured')
    const save = within(row).getByRole('button', { name: 'common.save' })
    expect(save).toBeDisabled()
    fireEvent.change(input, { target: { value: ' new-token ' } })
    fireEvent.click(save)
    await waitFor(() => expect(mocked.updateImportList).toHaveBeenCalledWith(1, { apiKey: 'new-token' }))
    await waitFor(() => expect(input).toHaveValue(''))

    fireEvent.click(within(row).getByRole('button', { name: 'settings.import.hardcoverClearOverride' }))
    await waitFor(() => expect(mocked.updateImportList).toHaveBeenCalledWith(1, { clearApiKey: true }))
    expect(await within(row).findByPlaceholderText('settings.import.hardcoverTokenOverridePlaceholder')).toBeInTheDocument()
  })

  it('reports override save and clear failures', async () => {
    mocked.listImportLists.mockResolvedValue([importList({ apiKeyConfigured: true })])
    mocked.updateImportList.mockRejectedValueOnce(new Error('bad token')).mockRejectedValueOnce(new Error('clear refused'))
    render(<ImportTab />)
    const row = await rowFor('Want to Read')
    fireEvent.click(within(row).getByRole('button', { name: 'settings.import.hardcoverTokenOverride' }))
    fireEvent.change(within(row).getByPlaceholderText('settings.import.hardcoverTokenOverridePlaceholderConfigured'), { target: { value: 'x' } })
    fireEvent.click(within(row).getByRole('button', { name: 'common.save' }))
    // "token" in the message also offers the API keys shortcut.
    expect(await screen.findByText('bad token')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'settings.import.configureHardcoverToken' })).toBeInTheDocument()

    fireEvent.click(within(row).getByRole('button', { name: 'settings.import.hardcoverClearOverride' }))
    expect(await screen.findByText('clear refused')).toBeInTheDocument()
  })

  it('syncs a list that finishes immediately and shows the outcome', async () => {
    mocked.listImportLists.mockResolvedValue([importList()])
    mocked.syncImportList.mockResolvedValue(progress({ stats: { total: 3, processed: 3, imported: 2, skipped: 1, failed: 0 } }))
    render(<ImportTab />)
    const row = await rowFor('Want to Read')
    const callsBefore = mocked.listImportLists.mock.calls.length
    fireEvent.click(within(row).getByRole('button', { name: 'settings.import.hardcoverSyncNow' }))
    expect(await within(row).findByText(/settings\.import\.hardcoverSyncDone .*"imported":2/)).toBeInTheDocument()
    expect(mocked.syncImportList).toHaveBeenCalledWith(1)
    await waitFor(() => expect(mocked.listImportLists.mock.calls.length).toBe(callsBefore + 1))
  })

  it('shows a failed sync and a sync that could not start', async () => {
    mocked.listImportLists.mockResolvedValue([importList(), importList({ id: 2, name: 'Disabled', url: 'disabled', enabled: false })])
    mocked.syncImportList
      .mockResolvedValueOnce(progress({ error: 'rate limited' }))
      .mockRejectedValueOnce(new Error('sync already running'))
    render(<ImportTab />)
    const row = await rowFor('Want to Read')
    expect(within(await rowFor('Disabled')).getByRole('button', { name: 'settings.import.hardcoverSyncNow' })).toBeDisabled()

    fireEvent.click(within(row).getByRole('button', { name: 'settings.import.hardcoverSyncNow' }))
    expect(await within(row).findByText('settings.import.hardcoverSyncFailed: rate limited')).toBeInTheDocument()
    fireEvent.click(within(row).getByRole('button', { name: 'settings.import.hardcoverSyncNow' }))
    expect(await screen.findByText('sync already running')).toBeInTheDocument()
  })

  it('adopts a running sync on mount and polls until it finishes', async () => {
    mocked.listImportLists.mockResolvedValue([importList()])
    mocked.importListSyncStatus
      .mockResolvedValueOnce(progress({ running: true, message: 'Fetching shelf', stats: { total: 4, processed: 1, imported: 0, skipped: 0, failed: 0 } }))
      .mockRejectedValueOnce(new Error('blip'))
      .mockResolvedValue(progress({ stats: { total: 4, processed: 4, imported: 4, skipped: 0, failed: 0 } }))
    render(<ImportTab />)
    const row = await rowFor('Want to Read')
    expect(await within(row).findByText('Fetching shelf (1/4)')).toBeInTheDocument()
    expect(within(row).getByRole('button', { name: 'settings.import.hardcoverSyncing' })).toBeDisabled()

    // Two poll ticks: one transient failure, then the finished snapshot.
    expect(await within(row).findByText(/settings\.import\.hardcoverSyncDone .*"imported":4/, {}, { timeout: 15000 })).toBeInTheDocument()
    await waitFor(() => expect(mocked.listImportLists.mock.calls.length).toBeGreaterThanOrEqual(2))
  }, 30000)

  it('reloads the lists on Refresh', async () => {
    render(<ImportTab />)
    await screen.findByText('settings.import.hardcoverEmpty')
    fireEvent.click(screen.getByRole('button', { name: 'common.refresh' }))
    await waitFor(() => expect(mocked.hardcoverLists).toHaveBeenCalledTimes(2))
  })
})
