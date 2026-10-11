import { describe, it, expect, vi, beforeEach, onTestFinished } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'

const COMMON: Record<string, string> = {
  'common.save': 'Save',
  'common.saving': 'Saving...',
  'common.cancel': 'Cancel',
}

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, def?: string | Record<string, unknown>) => {
      if (typeof def === 'string') return def
      return COMMON[key] ?? key
    },
  }),
}))

// Series changes are admin only (#468); a test switches to another user.
const authState = { isAdmin: true }
vi.mock('../auth/AuthContext', async importOriginal => ({
  ...await importOriginal<typeof import('../auth/AuthContext')>(),
  useIsAdmin: () => authState.isAdmin,
}))
const asAnotherUser = () => {
  authState.isAdmin = false
  onTestFinished(() => { authState.isAdmin = true })
}

vi.mock('../api/client', () => ({
  api: {
    updateBook: vi.fn(),
    listAuthorSeries: vi.fn(),
    createSeries: vi.fn(),
    linkBookToSeries: vi.fn(),
    removeBookFromSeries: vi.fn(),
  },
}))

import { api } from '../api/client'
import EditBookModal from './EditBookModal'
import type { Book } from '../api/client'

const BOOK = {
  id: 7,
  title: 'Original',
  description: 'Desc',
  genres: ['Old'],
  language: 'en',
  releaseDate: '2020-01-02T00:00:00Z',
  lockedFields: [],
} as unknown as Book

describe('EditBookModal (#1237, #1446)', () => {
  const onClose = vi.fn()
  const onSaved = vi.fn()

  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(api.updateBook).mockResolvedValue({ ...BOOK, title: 'Changed' } as never)
    vi.mocked(api.listAuthorSeries).mockResolvedValue([])
  })

  it('sends only the fields the user changed', async () => {
    render(<EditBookModal book={BOOK} onClose={onClose} onSaved={onSaved} />)
    fireEvent.change(screen.getByLabelText('Title'), { target: { value: 'Changed' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(api.updateBook).toHaveBeenCalled())
    expect(vi.mocked(api.updateBook).mock.calls[0]).toEqual([7, { title: 'Changed' }])
    expect(onSaved).toHaveBeenCalled()
    expect(onClose).toHaveBeenCalled()
  })

  it('parses comma-separated genres', async () => {
    render(<EditBookModal book={BOOK} onClose={onClose} onSaved={onSaved} />)
    fireEvent.change(screen.getByLabelText('Genres (comma-separated)'), {
      target: { value: ' Fantasy , Epic ,, ' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(api.updateBook).toHaveBeenCalled())
    expect(vi.mocked(api.updateBook).mock.calls[0][1]).toEqual({ genres: ['Fantasy', 'Epic'] })
  })

  it('closes without a request when nothing changed', async () => {
    render(<EditBookModal book={BOOK} onClose={onClose} onSaved={onSaved} />)
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(onClose).toHaveBeenCalled())
    expect(api.updateBook).not.toHaveBeenCalled()
  })

  it('shows the unlock action only when fields are locked, and it clears the set', async () => {
    render(<EditBookModal book={BOOK} onClose={onClose} onSaved={onSaved} />)
    expect(screen.queryByRole('button', { name: 'Unlock all fields' })).toBeNull()

    render(
      <EditBookModal
        book={{ ...BOOK, lockedFields: ['title', 'genres'] } as Book}
        onClose={onClose}
        onSaved={onSaved}
      />,
    )
    fireEvent.click(screen.getByRole('button', { name: 'Unlock all fields' }))
    await waitFor(() => expect(api.updateBook).toHaveBeenCalled())
    expect(vi.mocked(api.updateBook).mock.calls[0]).toEqual([7, { lockedFields: [] }])
  })

  describe('series (#2554)', () => {
    const IN_SERIES = { ...BOOK, authorId: 3 } as unknown as Book
    const A = { id: 1, title: 'Fjellserien', books: [{ seriesId: 1, bookId: 7, positionInSeries: '1', primarySeries: true }] }
    const B = { id: 2, title: 'Havserien', books: [] }
    const onSeriesSaved = vi.fn()

    beforeEach(() => {
      vi.mocked(api.listAuthorSeries).mockResolvedValue([A, B] as never)
      vi.mocked(api.linkBookToSeries).mockResolvedValue({} as never)
      vi.mocked(api.removeBookFromSeries).mockResolvedValue(undefined as never)
      vi.mocked(api.createSeries).mockResolvedValue({ id: 9, title: 'Ny serie' } as never)
    })

    const open = async () => {
      render(<EditBookModal book={IN_SERIES} onClose={onClose} onSaved={onSaved} onSeriesSaved={onSeriesSaved} />)
      const select = await screen.findByLabelText('Series') as HTMLSelectElement
      await waitFor(() => expect(select.value).toBe('1'))
      return select
    }
    const save = () => fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    it('offers only the author\'s series', async () => {
      const select = await open()
      expect(api.listAuthorSeries).toHaveBeenCalledWith(3)
      expect([...select.options].map(o => o.textContent)).toEqual(['No series', 'Fjellserien', 'Havserien', 'New series…'])
    })

    it('shows the primary series and position, and saving untouched makes no series call', async () => {
      await open()
      expect((screen.getByLabelText('Position') as HTMLInputElement).value).toBe('1')
      save()
      await waitFor(() => expect(onClose).toHaveBeenCalled())
      expect(api.linkBookToSeries).not.toHaveBeenCalled()
      expect(api.removeBookFromSeries).not.toHaveBeenCalled()
      expect(onSeriesSaved).not.toHaveBeenCalled()
    })

    it('moves the book: files it under the new series and takes it out of the old one', async () => {
      const select = await open()
      fireEvent.change(select, { target: { value: '2' } })
      fireEvent.change(screen.getByLabelText('Position'), { target: { value: ' 3 ' } })
      save()
      await waitFor(() => expect(api.removeBookFromSeries).toHaveBeenCalledWith(1, 7))
      expect(api.linkBookToSeries).toHaveBeenCalledWith(2, { bookId: 7, positionInSeries: '3', primarySeries: true })
      expect(api.updateBook).not.toHaveBeenCalled()
      expect(onSeriesSaved).toHaveBeenCalled()
    })

    it('changes only the position in the same series', async () => {
      await open()
      fireEvent.change(screen.getByLabelText('Position'), { target: { value: '2' } })
      save()
      await waitFor(() => expect(api.linkBookToSeries).toHaveBeenCalledWith(1, { bookId: 7, positionInSeries: '2', primarySeries: true }))
      expect(api.removeBookFromSeries).not.toHaveBeenCalled()
    })

    it('creates a new series and files the book there', async () => {
      const select = await open()
      fireEvent.change(select, { target: { value: 'new' } })
      expect(screen.getByLabelText('New series name')).toHaveFocus()
      expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled()
      // Enter in the empty name field does nothing, as the disabled Save would.
      fireEvent.keyDown(screen.getByLabelText('New series name'), { key: 'Enter' })
      expect(api.createSeries).not.toHaveBeenCalled()
      fireEvent.change(screen.getByLabelText('New series name'), { target: { value: ' Ny serie ' } })
      fireEvent.keyDown(screen.getByLabelText('New series name'), { key: 'Enter' })
      await waitFor(() => expect(api.createSeries).toHaveBeenCalledWith({ title: 'Ny serie' }))
      expect(api.linkBookToSeries).toHaveBeenCalledWith(9, { bookId: 7, positionInSeries: '1', primarySeries: true })
      expect(api.removeBookFromSeries).toHaveBeenCalledWith(1, 7)
    })

    it('takes the book out of its series when cleared', async () => {
      const select = await open()
      fireEvent.change(select, { target: { value: '' } })
      expect(screen.getByLabelText('Position')).toBeDisabled()
      save()
      await waitFor(() => expect(api.removeBookFromSeries).toHaveBeenCalledWith(1, 7))
      expect(api.linkBookToSeries).not.toHaveBeenCalled()
    })

    it('takes the book out of every series it is in with No series', async () => {
      vi.mocked(api.listAuthorSeries).mockResolvedValue([
        A, { ...B, books: [{ seriesId: 2, bookId: 7, positionInSeries: '2', primarySeries: false }] },
      ] as never)
      const select = await open()
      fireEvent.change(select, { target: { value: '' } })
      save()
      await waitFor(() => expect(api.removeBookFromSeries).toHaveBeenCalledTimes(2))
      expect(api.removeBookFromSeries).toHaveBeenCalledWith(1, 7)
      expect(api.removeBookFromSeries).toHaveBeenCalledWith(2, 7)
    })

    it('shows the series the book is named under when none is primary, and saving untouched changes nothing', async () => {
      vi.mocked(api.listAuthorSeries).mockResolvedValue([
        { ...A, books: [{ seriesId: 1, bookId: 7, positionInSeries: '', primarySeries: false }] },
        { ...B, books: [{ seriesId: 2, bookId: 7, positionInSeries: '2', primarySeries: false }] },
      ] as never)
      render(<EditBookModal book={IN_SERIES} onClose={onClose} onSaved={onSaved} />)
      const select = await screen.findByLabelText('Series') as HTMLSelectElement
      // The server names it by the one with a position.
      await waitFor(() => expect(select.value).toBe('2'))
      save()
      await waitFor(() => expect(onClose).toHaveBeenCalled())
      expect(api.removeBookFromSeries).not.toHaveBeenCalled()
      expect(api.linkBookToSeries).not.toHaveBeenCalled()
    })

    it('files a book that is in no series under the one picked', async () => {
      vi.mocked(api.listAuthorSeries).mockResolvedValue([{ ...A, books: [] }, B] as never)
      render(<EditBookModal book={IN_SERIES} onClose={onClose} onSaved={onSaved} onSeriesSaved={onSeriesSaved} />)
      const select = await screen.findByLabelText('Series') as HTMLSelectElement
      await waitFor(() => expect(select.options.length).toBe(4))
      expect(select.value).toBe('')
      fireEvent.change(select, { target: { value: '2' } })
      fireEvent.change(screen.getByLabelText('Position'), { target: { value: '5' } })
      save()
      await waitFor(() => expect(api.linkBookToSeries).toHaveBeenCalledWith(2, { bookId: 7, positionInSeries: '5', primarySeries: true }))
      expect(api.removeBookFromSeries).not.toHaveBeenCalled()
    })

    it('preselects the only series a book is in, even when it is not flagged primary', async () => {
      vi.mocked(api.listAuthorSeries).mockResolvedValue([
        { ...A, books: [{ seriesId: 1, bookId: 7, positionInSeries: '4', primarySeries: false }] }, B,
      ] as never)
      render(<EditBookModal book={IN_SERIES} onClose={onClose} onSaved={onSaved} />)
      const select = await screen.findByLabelText('Series') as HTMLSelectElement
      await waitFor(() => expect(select.value).toBe('1'))
      expect((screen.getByLabelText('Position') as HTMLInputElement).value).toBe('4')
    })

    it('offers another user no series row, and Unlock all only for locked fields', async () => {
      asAnotherUser()
      render(<EditBookModal book={IN_SERIES} onClose={onClose} onSaved={onSaved} seriesExclusions={1} />)
      expect(await screen.findByLabelText('Title')).toBeInTheDocument()
      expect(api.listAuthorSeries).not.toHaveBeenCalled()
      expect(screen.queryByLabelText('Series')).toBeNull()
      expect(screen.queryByRole('button', { name: 'Unlock all fields' })).toBeNull()
    })

    it('hides the series row when the series cannot be loaded, and other edits still save', async () => {
      vi.mocked(api.listAuthorSeries).mockRejectedValue(new Error('down'))
      render(<EditBookModal book={IN_SERIES} onClose={onClose} onSaved={onSaved} />)
      fireEvent.change(screen.getByLabelText('Title'), { target: { value: 'Changed' } })
      save()
      await waitFor(() => expect(api.updateBook).toHaveBeenCalledWith(7, { title: 'Changed' }))
      expect(screen.queryByLabelText('Series')).toBeNull()
    })
  })

  it('offers Unlock all when the book was only taken out of series, and reloads its series after (#2554)', async () => {
    const onSeriesSaved = vi.fn()
    render(<EditBookModal book={BOOK} onClose={onClose} onSaved={onSaved} onSeriesSaved={onSeriesSaved} seriesExclusions={1} />)
    fireEvent.click(screen.getByRole('button', { name: 'Unlock all fields' }))
    await waitFor(() => expect(api.updateBook).toHaveBeenCalledWith(7, { lockedFields: [] }))
    expect(onSeriesSaved).toHaveBeenCalled()
  })
})
