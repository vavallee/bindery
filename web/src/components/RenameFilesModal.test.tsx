import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import RenameFilesModal from './RenameFilesModal'
import { reorganizeApi } from '../api/reorganize'
import '../i18n'

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

  // The new filename is the point of the modal; a hover tooltip was the only
  // way to read a long one, and touch screens have no hover.
  it('shows the whole proposed path rather than truncating it', async () => {
    previewMock.mockResolvedValue({
      moves: sampleMoves,
      summary: { total: 2, toMove: 1, noop: 1, collision: 0, missing: 0, errored: 0, moved: 0, failed: 0 },
    })
    render(<RenameFilesModal scope="book" id={1} label="My Book" onClose={() => {}} />)

    const proposed = await screen.findByText('/lib/Jane Doe/My Book (2020)/My Book - Jane Doe.epub')
    expect(proposed.closest('.truncate')).toBeNull()
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

  describe('External import mode (#3946)', () => {
    const externalPreview = {
      moves: sampleMoves,
      summary: { total: 2, toMove: 1, noop: 1, collision: 0, missing: 0, errored: 0, moved: 0, failed: 0 },
      externalMode: true,
      externalFormats: ['ebook'],
      warning: 'Import mode is External for ebook, so another tool owns that layout.',
    }

    it('shows the warning and keeps apply disabled until confirmed', async () => {
      previewMock.mockResolvedValue(externalPreview)
      render(<RenameFilesModal scope="library" label="your library" onClose={() => {}} />)

      expect(await screen.findByText(/another tool owns that layout/)).toBeInTheDocument()
      const button = screen.getByRole('button', { name: /Rename 1 file/ })
      expect(button).toBeDisabled()
      fireEvent.click(screen.getByRole('checkbox'))
      expect(button).toBeEnabled()
    })

    it('falls back to the built in warning text when the server sends none', async () => {
      previewMock.mockResolvedValue({ ...externalPreview, warning: undefined })
      render(<RenameFilesModal scope="library" label="your library" onClose={() => {}} />)

      expect(await screen.findByText(/another tool owns this layout/)).toBeInTheDocument()
    })

    it('sends the confirmation with the apply request', async () => {
      previewMock.mockResolvedValue(externalPreview)
      applyMock.mockResolvedValue({
        moves: [{ ...sampleMoves[0], status: 'moved' }],
        summary: { total: 1, toMove: 0, noop: 0, collision: 0, missing: 0, errored: 0, moved: 1, failed: 0 },
      })
      render(<RenameFilesModal scope="library" label="your library" onClose={() => {}} />)

      await screen.findByText(/another tool owns that layout/)
      fireEvent.click(screen.getByRole('checkbox'))
      fireEvent.click(screen.getByRole('button', { name: /Rename 1 file/ }))
      await waitFor(() => expect(applyMock).toHaveBeenCalledWith([10], true))
    })

    it('shows no warning or checkbox in a managed mode', async () => {
      previewMock.mockResolvedValue({
        moves: sampleMoves,
        summary: { total: 2, toMove: 1, noop: 1, collision: 0, missing: 0, errored: 0, moved: 0, failed: 0 },
        externalMode: false,
      })
      render(<RenameFilesModal scope="library" label="your library" onClose={() => {}} />)

      expect(await screen.findByRole('button', { name: /Rename 1 file/ })).toBeEnabled()
      expect(screen.queryByRole('checkbox')).toBeNull()
      expect(screen.queryByRole('alert')).toBeNull()
    })
  })
})
