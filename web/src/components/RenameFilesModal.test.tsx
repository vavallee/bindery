import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import RenameFilesModal from './RenameFilesModal'
import { reorganizeApi } from '../api/reorganize'

vi.mock('../api/reorganize', () => ({
  reorganizeApi: {
    preview: vi.fn(),
    apply: vi.fn(),
  },
}))

const previewMock = vi.mocked(reorganizeApi.preview)
const applyMock = vi.mocked(reorganizeApi.apply)

const sampleMoves = [
  {
    bookId: 1, fileId: 10, format: 'ebook', bookTitle: 'My Book', author: 'Jane Doe',
    current: '/lib/old/book.epub', proposed: '/lib/Jane Doe/My Book (2020)/My Book - Jane Doe.epub',
    status: 'move' as const,
  },
  {
    bookId: 1, fileId: 11, format: 'ebook', bookTitle: 'My Book', author: 'Jane Doe',
    current: '/lib/Jane Doe/ok.epub', proposed: '/lib/Jane Doe/ok.epub', status: 'noop' as const,
  },
]

describe('RenameFilesModal', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('shows the preview and only counts movable files', async () => {
    previewMock.mockResolvedValue({
      moves: sampleMoves,
      summary: { total: 2, toMove: 1, noop: 1, collision: 0, missing: 0, errored: 0, moved: 0, failed: 0 },
    })
    render(<RenameFilesModal scope="book" id={1} label="My Book" onClose={() => {}} />)

    await waitFor(() => expect(screen.getByText(/1 of 2 will move/)).toBeInTheDocument())
    expect(previewMock).toHaveBeenCalledWith('book', 1)
    // The apply button reflects the single movable file.
    expect(screen.getByRole('button', { name: /Rename 1 file/ })).toBeInTheDocument()
  })

  it('applies only movable files and reports the result', async () => {
    previewMock.mockResolvedValue({
      moves: sampleMoves,
      summary: { total: 2, toMove: 1, noop: 1, collision: 0, missing: 0, errored: 0, moved: 0, failed: 0 },
    })
    applyMock.mockResolvedValue({
      moves: [{ ...sampleMoves[0], status: 'moved' }],
      summary: { total: 1, toMove: 0, noop: 0, collision: 0, missing: 0, errored: 0, moved: 1, failed: 0 },
    })
    const onApplied = vi.fn()
    render(<RenameFilesModal scope="book" id={1} label="My Book" onClose={() => {}} onApplied={onApplied} />)

    await waitFor(() => screen.getByRole('button', { name: /Rename 1 file/ }))
    fireEvent.click(screen.getByRole('button', { name: /Rename 1 file/ }))

    await waitFor(() => expect(applyMock).toHaveBeenCalledWith([10]))
    await waitFor(() => expect(screen.getByText(/1 moved, 0 failed/)).toBeInTheDocument())
    expect(onApplied).toHaveBeenCalled()
  })

  it('disables apply when there is nothing to move', async () => {
    previewMock.mockResolvedValue({
      moves: [sampleMoves[1]],
      summary: { total: 1, toMove: 0, noop: 1, collision: 0, missing: 0, errored: 0, moved: 0, failed: 0 },
    })
    render(<RenameFilesModal scope="library" label="your library" onClose={() => {}} />)

    await waitFor(() => expect(screen.getByText(/0 of 1 will move/)).toBeInTheDocument())
    expect(previewMock).toHaveBeenCalledWith('library', undefined)
    expect(screen.queryByRole('button', { name: /Rename 0 files/ })).toBeDisabled()
  })
})
