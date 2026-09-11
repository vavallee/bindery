import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'

// i18n: render the bare key, appending interpolation values (except defaultValue)
// so count/name assertions stay stable. Mirrors FolderScanSection.test.
vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, options?: unknown) => {
      if (!options || typeof options !== 'object') return key
      let out = key
      for (const [k, v] of Object.entries(options as Record<string, unknown>)) {
        if (k === 'defaultValue') continue
        out += ` ${k}=${String(v)}`
      }
      return out
    },
  }),
}))

vi.mock('../api/client', () => ({
  api: {
    scanFolder: vi.fn(),
    batchImport: vi.fn(),
    listBooks: vi.fn(),
    // Reached through resolveBookQuery / CatalogueAdder (#1719).
    searchBooks: vi.fn(),
    lookupISBN: vi.fn(),
    lookupASIN: vi.fn(),
    addBook: vi.fn(),
  },
}))

import { api } from '../api/client'
import type { Book, FolderScanResponse } from '../api/client'
import ManualImportPage from './ManualImportPage'

const mockScan = api.scanFolder as ReturnType<typeof vi.fn>
const mockBatch = api.batchImport as ReturnType<typeof vi.fn>
const mockListBooks = api.listBooks as ReturnType<typeof vi.fn>
const mockSearchBooks = api.searchBooks as ReturnType<typeof vi.fn>
const mockLookupISBN = api.lookupISBN as ReturnType<typeof vi.fn>
const mockAddBook = api.addBook as ReturnType<typeof vi.fn>

function scanResult(): FolderScanResponse {
  return {
    truncated: false,
    items: [
      {
        path: '/dl/Confident Book', name: 'Confident Book', match: 'confident',
        parsedTitle: 'Confident Book', parsedAuthor: 'A. Writer', detectedFormat: 'ebook',
        book: { id: 11, title: 'Confident Book', author: { authorName: 'A. Writer' } } as never,
        alreadyImported: false,
      },
      {
        path: '/dl/Maybe Book', name: 'Maybe Book', match: 'ambiguous',
        parsedTitle: 'Maybe Book', parsedAuthor: '', detectedFormat: 'ebook',
        candidates: [
          { id: 21, title: 'Maybe Book (1)', author: { authorName: 'X' } } as never,
          { id: 22, title: 'Maybe Book (2)', author: { authorName: 'Y' } } as never,
        ],
        alreadyImported: false,
      },
      {
        path: '/dl/Orphan', name: 'Orphan', match: 'none',
        parsedTitle: 'Orphan', parsedAuthor: '', detectedFormat: 'audiobook',
        alreadyImported: false,
      },
    ],
  }
}

async function scan() {
  mockScan.mockResolvedValue(scanResult())
  render(<ManualImportPage />)
  fireEvent.change(screen.getByPlaceholderText('manualImport.pathPlaceholder'), { target: { value: '/dl' } })
  fireEvent.click(screen.getByText('manualImport.scan'))
  await waitFor(() => expect(screen.getByText('Confident Book')).toBeInTheDocument())
}

// The three per-row Import buttons render in MATCH_ORDER: confident, ambiguous, none.
function rowImportButtons() {
  return screen.getAllByText('manualImport.import') as HTMLButtonElement[]
}

describe('ManualImportPage', () => {
  beforeEach(() => vi.clearAllMocks())

  it('renders scan results grouped by match status', async () => {
    await scan()
    expect(mockScan).toHaveBeenCalledWith('/dl', { includeImported: false })

    // One heading per non-empty group (plus a badge per row using the same key).
    expect(screen.getAllByText(/manualImport\.group\.confident/).length).toBeGreaterThan(0)
    expect(screen.getAllByText(/manualImport\.group\.ambiguous/).length).toBeGreaterThan(0)
    expect(screen.getAllByText(/manualImport\.group\.none/).length).toBeGreaterThan(0)

    // Every discovered unit is listed.
    expect(screen.getByText('Confident Book')).toBeInTheDocument()
    expect(screen.getByText('Maybe Book')).toBeInTheDocument()
    expect(screen.getByText('Orphan')).toBeInTheDocument()

    // Confident is preselected; its candidate radios don't exist, the none row
    // shows a library search box.
    expect(screen.getByText(/manualImport\.importSelected count=1/)).toBeInTheDocument()
    expect(screen.getByPlaceholderText('manualImport.searchPlaceholder')).toBeInTheDocument()
  })

  it('enables import for an ambiguous unit once a candidate is picked', async () => {
    await scan()
    // Ambiguous row's per-row Import button starts disabled (no book chosen).
    expect(rowImportButtons()[1].disabled).toBe(true)
    // Bulk count reflects only the preselected confident unit.
    expect(screen.getByText(/manualImport\.importSelected count=1/)).toBeInTheDocument()

    // Pick the second candidate radio for the ambiguous unit.
    const radios = screen.getAllByRole('radio') as HTMLInputElement[]
    fireEvent.click(radios[1])

    expect(rowImportButtons()[1].disabled).toBe(false)
    expect(screen.getByText(/manualImport\.importSelected count=2/)).toBeInTheDocument()
  })

  it('submits the chosen items to the batch API', async () => {
    await scan()
    // Resolve the ambiguous unit too.
    fireEvent.click((screen.getAllByRole('radio') as HTMLInputElement[])[1])

    mockBatch.mockResolvedValue({
      accepted: 2, failed: 0,
      results: [
        { path: '/dl/Confident Book', accepted: true, downloadId: 5 },
        { path: '/dl/Maybe Book', accepted: true, downloadId: 6 },
      ],
    })
    fireEvent.click(screen.getByText(/manualImport\.importSelected count=2/))

    await waitFor(() => expect(mockBatch).toHaveBeenCalledTimes(1))
    expect(mockBatch).toHaveBeenCalledWith([
      { path: '/dl/Confident Book', bookId: 11, format: 'ebook' },
      { path: '/dl/Maybe Book', bookId: 22, format: 'ebook' },
    ])
    // Per-item success surfaced.
    await waitFor(() => expect(screen.getAllByText('manualImport.queued').length).toBe(2))
  })

  it('does not submit a none unit that has no selection', async () => {
    await scan()
    mockBatch.mockResolvedValue({
      accepted: 1, failed: 0,
      results: [{ path: '/dl/Confident Book', accepted: true, downloadId: 5 }],
    })
    // Only the confident unit is selected; the unresolved none unit is excluded.
    fireEvent.click(screen.getByText(/manualImport\.importSelected count=1/))

    await waitFor(() => expect(mockBatch).toHaveBeenCalledTimes(1))
    const submitted = mockBatch.mock.calls[0][0] as Array<{ path: string }>
    expect(submitted.map(i => i.path)).toEqual(['/dl/Confident Book'])
    expect(submitted.some(i => i.path === '/dl/Orphan')).toBe(false)
  })

  it('resolves a none unit against an existing catalogue book via search', async () => {
    await scan()
    const picked: Book = { id: 99, title: 'Found In Library', author: { authorName: 'Z' } } as never
    mockListBooks.mockResolvedValue({ items: [picked], total: 1 })

    const search = screen.getByPlaceholderText('manualImport.searchPlaceholder')
    fireEvent.change(search, { target: { value: 'found' } })

    // Debounced search resolves and offers the existing book; pick it.
    const option = await screen.findByText('Found In Library')
    fireEvent.click(option)

    // The none unit now counts toward the import selection.
    expect(screen.getByText(/manualImport\.importSelected count=2/)).toBeInTheDocument()

    mockBatch.mockResolvedValue({
      accepted: 2, failed: 0,
      results: [
        { path: '/dl/Confident Book', accepted: true, downloadId: 5 },
        { path: '/dl/Orphan', accepted: true, downloadId: 7 },
      ],
    })
    fireEvent.click(screen.getByText(/manualImport\.importSelected count=2/))
    await waitFor(() => expect(mockBatch).toHaveBeenCalledTimes(1))
    expect(mockBatch).toHaveBeenCalledWith(
      expect.arrayContaining([{ path: '/dl/Orphan', bookId: 99, format: 'audiobook' }]),
    )
  })

  it('creates an unmatched unit\'s book from a metadata search and imports against it', async () => {
    await scan()

    // The metadata search is offered only on the unresolved `none` unit.
    fireEvent.click(screen.getByText('manualImport.metadataOpen'))

    // Prefilled from what the scan parsed out of the file, so the user edits a
    // query instead of retyping one.
    const term = screen.getByPlaceholderText('manualImport.metadataPlaceholder') as HTMLInputElement
    expect(term.value).toBe('Orphan')

    mockSearchBooks.mockResolvedValue([
      { foreignBookId: 'OL1W', title: 'Orphan Works', author: { authorName: 'A. Writer', foreignAuthorId: 'OL9A' } },
    ])
    fireEvent.click(screen.getByText('manualImport.metadataSearch'))
    await screen.findByText('Orphan Works')

    mockAddBook.mockResolvedValue({ id: 77, title: 'Orphan Works', author: { authorName: 'A. Writer' } })
    fireEvent.click(screen.getByText('manualImport.metadataAdd'))
    await waitFor(() => expect(mockAddBook).toHaveBeenCalledTimes(1))

    // searchOnAdd MUST be false: the file is already on disk, so searching
    // indexers for it would grab a second copy.
    expect(mockAddBook).toHaveBeenCalledWith({
      foreignBookId: 'OL1W',
      foreignAuthorId: 'OL9A',
      authorName: 'A. Writer',
      searchOnAdd: false,
    })

    // The created book resolves the unit, which now imports like any other.
    expect(screen.getByText(/manualImport\.importSelected count=2/)).toBeInTheDocument()
    mockBatch.mockResolvedValue({
      accepted: 2, failed: 0,
      results: [
        { path: '/dl/Confident Book', accepted: true, downloadId: 5 },
        { path: '/dl/Orphan', accepted: true, downloadId: 7 },
      ],
    })
    fireEvent.click(screen.getByText(/manualImport\.importSelected count=2/))
    await waitFor(() => expect(mockBatch).toHaveBeenCalledTimes(1))
    expect(mockBatch).toHaveBeenCalledWith(
      expect.arrayContaining([{ path: '/dl/Orphan', bookId: 77, format: 'audiobook' }]),
    )
  })

  it('routes an ISBN query to the lookup endpoint rather than the term search', async () => {
    await scan()
    fireEvent.click(screen.getByText('manualImport.metadataOpen'))
    fireEvent.change(screen.getByPlaceholderText('manualImport.metadataPlaceholder'), {
      target: { value: '978-0-441-47812-5' },
    })
    mockLookupISBN.mockResolvedValue({ foreignBookId: 'OL2W', title: 'Dune', author: { authorName: 'Frank Herbert' } })
    fireEvent.click(screen.getByText('manualImport.metadataSearch'))

    await screen.findByText('Dune')
    expect(mockLookupISBN).toHaveBeenCalledWith('9780441478125')
    expect(mockSearchBooks).not.toHaveBeenCalled()
  })

  it('refuses to add a metadata result that carries no author name', async () => {
    await scan()
    fireEvent.click(screen.getByText('manualImport.metadataOpen'))
    mockSearchBooks.mockResolvedValue([{ foreignBookId: 'OL3W', title: 'Authorless', author: undefined }])
    fireEvent.click(screen.getByText('manualImport.metadataSearch'))

    await screen.findByText('Authorless')
    const addBtn = screen.getByText('manualImport.metadataAdd') as HTMLButtonElement
    expect(addBtn.disabled).toBe(true)
    fireEvent.click(addBtn)
    expect(mockAddBook).not.toHaveBeenCalled()
  })

  it('re-scans with includeImported when "show already imported" is toggled on', async () => {
    await scan()
    mockScan.mockClear()
    mockScan.mockResolvedValue({
      truncated: false,
      items: [{
        path: '/dl/Already Imported', name: 'Already Imported', match: 'confident',
        parsedTitle: 'Already Imported', parsedAuthor: '', detectedFormat: 'ebook',
        book: { id: 33, title: 'Already Imported' } as never, alreadyImported: true,
      }],
    })

    fireEvent.click(screen.getByLabelText('manualImport.showImported'))

    expect(mockScan).toHaveBeenCalledWith('/dl', { includeImported: true })
    await waitFor(() => expect(screen.getByText('manualImport.alreadyImported')).toBeInTheDocument())
  })

  it('shows the truncation banner even when filtering leaves no items (#2480)', async () => {
    mockScan.mockResolvedValue({ truncated: true, items: [] })
    render(<ManualImportPage />)
    fireEvent.change(screen.getByPlaceholderText('manualImport.pathPlaceholder'), { target: { value: '/dl' } })
    fireEvent.click(screen.getByText('manualImport.scan'))

    await waitFor(() => expect(screen.getByText('manualImport.empty')).toBeInTheDocument())
    expect(screen.getByText('manualImport.truncated')).toBeInTheDocument()
  })

  it('clears a stale truncation banner once a later scan reports untruncated', async () => {
    mockScan.mockResolvedValue({ truncated: true, items: [] })
    render(<ManualImportPage />)
    fireEvent.change(screen.getByPlaceholderText('manualImport.pathPlaceholder'), { target: { value: '/dl' } })
    fireEvent.click(screen.getByText('manualImport.scan'))
    await waitFor(() => expect(screen.getByText('manualImport.truncated')).toBeInTheDocument())

    // A second scan (e.g. after narrowing the folder) that is NOT truncated
    // must not leave the earlier banner showing.
    mockScan.mockResolvedValue({ truncated: false, items: [] })
    fireEvent.click(screen.getByText('manualImport.scan'))
    await waitFor(() => expect(screen.queryByText('manualImport.truncated')).not.toBeInTheDocument())
  })

  it('does not let "select all" re-select an already-imported unit', async () => {
    await scan()
    mockScan.mockClear()
    mockScan.mockResolvedValue({
      truncated: false,
      items: [{
        path: '/dl/Already Imported', name: 'Already Imported', match: 'confident',
        parsedTitle: 'Already Imported', parsedAuthor: '', detectedFormat: 'ebook',
        book: { id: 33, title: 'Already Imported' } as never, alreadyImported: true,
      }],
    })
    fireEvent.click(screen.getByLabelText('manualImport.showImported'))
    await screen.findByText('Already Imported')

    // Starts unchecked and the per-row Import button starts disabled — the
    // whole point of not preselecting an already-imported match.
    const rowCheckbox = screen.getByLabelText('manualImport.selectUnit name=Already Imported') as HTMLInputElement
    expect(rowCheckbox.checked).toBe(false)
    const importButtons = screen.getAllByText('manualImport.import') as HTMLButtonElement[]
    expect(importButtons[importButtons.length - 1].disabled).toBe(true)

    // "Select all matched" must not silently sweep it in.
    fireEvent.click(screen.getByLabelText('manualImport.selectAll'))
    expect(rowCheckbox.checked).toBe(false)

    // Checking it by hand is still how you'd deliberately re-import it.
    fireEvent.click(rowCheckbox)
    expect(rowCheckbox.checked).toBe(true)
    expect(importButtons[importButtons.length - 1].disabled).toBe(false)
  })
})
