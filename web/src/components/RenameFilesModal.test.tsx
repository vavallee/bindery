import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import RenameFilesModal from './RenameFilesModal'
import { reorganizeApi } from '../api/reorganize'
import { ApiError } from '../api/core'
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

      expect(await screen.findByText(/another tool owns this layout/)).toBeInTheDocument()
      const button = screen.getByRole('button', { name: /Rename 1 file/ })
      expect(button).toBeDisabled()
      fireEvent.click(screen.getByRole('checkbox'))
      expect(button).toBeEnabled()
    })

    it('ignores the server English warning and shows the translated one', async () => {
      previewMock.mockResolvedValue(externalPreview)
      render(<RenameFilesModal scope="library" label="your library" onClose={() => {}} />)

      await screen.findByText(/another tool owns this layout/)
      expect(screen.queryByText(/another tool owns that layout/)).toBeNull()
    })

    it('asks for confirmation when apply answers 409 after the mode flipped', async () => {
      previewMock.mockResolvedValue({
        moves: sampleMoves,
        summary: { total: 2, toMove: 1, noop: 1, collision: 0, missing: 0, errored: 0, moved: 0, failed: 0 },
      })
      applyMock.mockRejectedValueOnce(
        new ApiError(409, { error: 'Import mode is External for ebook, raw english', code: 'reorganize_external_mode' }, 'Conflict'),
      )
      applyMock.mockResolvedValueOnce({
        moves: [{ ...sampleMoves[0], status: 'moved' }],
        summary: { total: 1, toMove: 0, noop: 0, collision: 0, missing: 0, errored: 0, moved: 1, failed: 0 },
      })
      render(<RenameFilesModal scope="library" label="your library" onClose={() => {}} />)

      fireEvent.click(await screen.findByRole('button', { name: /Rename 1 file/ }))
      expect(await screen.findByText(/another tool owns this layout/)).toBeInTheDocument()
      expect(screen.queryByText(/raw english/)).toBeNull()
      const button = screen.getByRole('button', { name: /Rename 1 file/ })
      expect(button).toBeDisabled()
      fireEvent.click(screen.getByRole('checkbox'))
      fireEvent.click(button)
      await waitFor(() => expect(applyMock).toHaveBeenLastCalledWith([10], true))
    })

    it('sends the confirmation with the apply request', async () => {
      previewMock.mockResolvedValue(externalPreview)
      applyMock.mockResolvedValue({
        moves: [{ ...sampleMoves[0], status: 'moved' }],
        summary: { total: 1, toMove: 0, noop: 0, collision: 0, missing: 0, errored: 0, moved: 1, failed: 0 },
      })
      render(<RenameFilesModal scope="library" label="your library" onClose={() => {}} />)

      await screen.findByText(/another tool owns this layout/)
      fireEvent.click(screen.getByRole('checkbox'))
      fireEvent.click(screen.getByRole('button', { name: /Rename 1 file/ }))
      await waitFor(() => expect(applyMock).toHaveBeenCalledWith([10], true))
    })

    describe('names the formats the API reports', () => {
      const withFormats = (externalFormats: string[], externalMode = true) => ({
        moves: [
          ...sampleMoves,
          { ...sampleMoves[0], fileId: 12, format: 'audiobook', current: '/lib/a', proposed: '/lib/b' },
        ],
        summary: { total: 3, toMove: 2, noop: 1, collision: 0, missing: 0, errored: 0, moved: 0, failed: 0 },
        externalMode,
        externalFormats,
      })

      it.each([
        [['ebook'], 'Import mode is External for ebook files, so another tool owns this layout.'],
        [['audiobook'], 'Import mode is External for audiobook files, so another tool owns this layout.'],
        [['ebook', 'audiobook'], 'Import mode is External for ebook files and audiobook files, so another tool owns this layout.'],
      ])('%j', async (formats, text) => {
        previewMock.mockResolvedValue(withFormats(formats))
        render(<RenameFilesModal scope="library" label="your library" onClose={() => {}} />)
        expect(await screen.findByText(new RegExp(text.replace(/[.]/g, '\\.')))).toBeInTheDocument()
      })

      it('does not name the audiobook rows when only ebooks are External', async () => {
        previewMock.mockResolvedValue(withFormats(['ebook']))
        render(<RenameFilesModal scope="library" label="your library" onClose={() => {}} />)
        const alert = await screen.findByRole('alert')
        expect(alert.textContent).toContain('ebook files')
        expect(alert.textContent).not.toContain('audiobook')
      })

      it('shows no warning or checkbox when the formats list is empty', async () => {
        previewMock.mockResolvedValue(withFormats([]))
        render(<RenameFilesModal scope="library" label="your library" onClose={() => {}} />)
        expect(await screen.findByRole('button', { name: /Rename 2 files/ })).toBeEnabled()
        expect(screen.queryByRole('checkbox')).toBeNull()
        expect(screen.queryByRole('alert')).toBeNull()
      })

      it('shows no warning when External is off even if formats are present', async () => {
        previewMock.mockResolvedValue(withFormats(['ebook'], false))
        render(<RenameFilesModal scope="library" label="your library" onClose={() => {}} />)
        expect(await screen.findByRole('button', { name: /Rename 2 files/ })).toBeEnabled()
        expect(screen.queryByRole('alert')).toBeNull()
      })

      it('names the formats from a 409 when the preview did not know', async () => {
        previewMock.mockResolvedValue({ ...withFormats([], false) })
        applyMock.mockRejectedValueOnce(
          new ApiError(409, { error: 'raw', code: 'reorganize_external_mode', formats: ['audiobook'] }, 'Conflict'),
        )
        render(<RenameFilesModal scope="library" label="your library" onClose={() => {}} />)
        fireEvent.click(await screen.findByRole('button', { name: /Rename 2 files/ }))
        expect(await screen.findByText(/External for audiobook files/)).toBeInTheDocument()
      })
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
