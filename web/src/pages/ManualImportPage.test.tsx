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
  },
}))

import { api } from '../api/client'
import type { Book, FolderScanResponse } from '../api/client'
import ManualImportPage from './ManualImportPage'

const mockScan = api.scanFolder as ReturnType<typeof vi.fn>
const mockBatch = api.batchImport as ReturnType<typeof vi.fn>
const mockListBooks = api.listBooks as ReturnType<typeof vi.fn>

function scanResult(): FolderScanResponse {
  return {
    truncated: false,
    items: [
      {
        path: '/dl/Confident Book', name: 'Confident Book', match: 'confident',
        parsedTitle: 'Confident Book', parsedAuthor: 'A. Writer', detectedFormat: 'ebook',
        book: { id: 11, title: 'Confident Book', author: { authorName: 'A. Writer' } } as never,
      },
      {
        path: '/dl/Maybe Book', name: 'Maybe Book', match: 'ambiguous',
        parsedTitle: 'Maybe Book', parsedAuthor: '', detectedFormat: 'ebook',
        candidates: [
          { id: 21, title: 'Maybe Book (1)', author: { authorName: 'X' } } as never,
          { id: 22, title: 'Maybe Book (2)', author: { authorName: 'Y' } } as never,
        ],
      },
      {
        path: '/dl/Orphan', name: 'Orphan', match: 'none',
        parsedTitle: 'Orphan', parsedAuthor: '', detectedFormat: 'audiobook',
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
    expect(mockScan).toHaveBeenCalledWith('/dl')

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
})
