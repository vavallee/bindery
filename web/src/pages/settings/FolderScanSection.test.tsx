import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'

// i18n: return the key (with crude interpolation) so assertions are stable.
vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, options?: unknown) => {
      // t(key) and t(key, 'default string') both render the bare key for
      // stable assertions; t(key, {opts}) appends the interpolation values.
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

vi.mock('../../api/client', () => ({
  api: {
    scanFolder: vi.fn(),
    batchImport: vi.fn(),
  },
}))

import { api, FolderScanResponse } from '../../api/client'
import { FolderScanSection } from './ImportTab'

const mockScan = api.scanFolder as ReturnType<typeof vi.fn>
const mockBatch = api.batchImport as ReturnType<typeof vi.fn>

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

describe('FolderScanSection', () => {
  beforeEach(() => vi.clearAllMocks())

  async function scan() {
    mockScan.mockResolvedValue(scanResult())
    render(<FolderScanSection />)
    fireEvent.change(screen.getByPlaceholderText('settings.import.bulkPathPlaceholder'), {
      target: { value: '/dl' },
    })
    fireEvent.click(screen.getByText('settings.import.bulkScan'))
    await waitFor(() => expect(screen.getByText('Confident Book')).toBeInTheDocument())
  }

  it('pre-selects only confident matches and imports them', async () => {
    await scan()
    expect(mockScan).toHaveBeenCalledWith('/dl')

    const checkboxes = screen.getAllByRole('checkbox') as HTMLInputElement[]
    expect(checkboxes[0].checked).toBe(true)  // confident
    expect(checkboxes[1].checked).toBe(false) // ambiguous, no pick yet
    expect(checkboxes[2].disabled).toBe(true) // none, not selectable

    // Import button reflects 1 selected (the confident one).
    expect(screen.getByText(/settings\.import\.bulkImport count=1/)).toBeInTheDocument()

    mockBatch.mockResolvedValue({ accepted: 1, failed: 0, results: [{ path: '/dl/Confident Book', accepted: true, downloadId: 5 }] })
    fireEvent.click(screen.getByText(/settings\.import\.bulkImport count=1/))

    await waitFor(() => expect(mockBatch).toHaveBeenCalledTimes(1))
    expect(mockBatch).toHaveBeenCalledWith([{ path: '/dl/Confident Book', bookId: 11, format: 'ebook' }])
    await waitFor(() => expect(screen.getByText(/bulkSummary accepted=1 failed=0/)).toBeInTheDocument())
  })

  it('lets an ambiguous match be resolved via the picker and included', async () => {
    await scan()
    // Pick candidate 22 for the ambiguous row.
    fireEvent.change(screen.getByRole('combobox'), { target: { value: '22' } })
    expect(screen.getByText(/settings\.import\.bulkImport count=2/)).toBeInTheDocument()

    mockBatch.mockResolvedValue({ accepted: 2, failed: 0, results: [] })
    fireEvent.click(screen.getByText(/settings\.import\.bulkImport count=2/))
    await waitFor(() => expect(mockBatch).toHaveBeenCalledTimes(1))
    expect(mockBatch).toHaveBeenCalledWith([
      { path: '/dl/Confident Book', bookId: 11, format: 'ebook' },
      { path: '/dl/Maybe Book', bookId: 22, format: 'ebook' },
    ])
  })
})
