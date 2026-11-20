import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import AddManualBook from './AddManualBook'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string) => {
      const strings: Record<string, string> = {
        'addToLibrary.backToResults': 'Back to results',
        'addToLibrary.adding': 'Adding...',
        'addToLibrary.book.confirmAdd': 'Add book',
        'addToLibrary.book.format': 'Format',
        'addToLibrary.book.formatLabel': 'Format to add',
        'addToLibrary.book.defaultFormat': 'Default',
        'addToLibrary.book.autoSearchLabel': 'Search indexers after adding',
        'addToLibrary.book.addFailed': 'Failed to add book',
        'addToLibrary.book.openExisting': 'Open existing book',
        'addToLibrary.manual.heading': 'Add a book by hand',
        'addToLibrary.manual.titleLabel': 'Title',
        'addToLibrary.manual.authorLabel': 'Author',
        'addToLibrary.manual.seriesLabel': 'Series (optional)',
        'addToLibrary.manual.positionLabel': 'Position',
        'addToLibrary.manual.alreadyInLibrary': 'This author already has a book with this title in your library.',
        'common.cancel': 'Cancel',
        'common.ebook': 'Ebook',
        'common.audiobook': 'Audiobook',
        'common.both': 'Both',
      }
      return strings[key] ?? key
    },
  }),
}))

vi.mock('../api/client', () => ({
  api: {
    addManualBook: vi.fn(),
  },
}))

import { api } from '../api/client'
import type { Book } from '../api/client'

const created: Book = {
  id: 9,
  foreignBookId: 'manual:book:1',
  authorId: 3,
  title: 'The Last Church',
  description: '',
  imageUrl: '',
  genres: [],
  monitored: true,
  status: 'wanted',
  filePath: '',
  mediaType: 'ebook',
  ebookFilePath: '',
  audiobookFilePath: '',
  excluded: false,
}

function renderForm(initialTitle?: string) {
  const onAdded = vi.fn()
  render(<AddManualBook initialTitle={initialTitle} onBack={vi.fn()} onClose={vi.fn()} onAdded={onAdded} />)
  return { onAdded }
}

const addButton = () => screen.getByRole('button', { name: 'Add book' })

describe('AddManualBook', () => {
  beforeEach(() => {
    vi.mocked(api.addManualBook).mockReset()
  })

  it('prefills the title and needs an author before it can add', () => {
    renderForm('The Last Church')
    expect(screen.getByLabelText('Title')).toHaveValue('The Last Church')
    expect(addButton()).toBeDisabled()
    fireEvent.change(screen.getByLabelText('Author'), { target: { value: 'Graham McNeill' } })
    expect(addButton()).toBeEnabled()
  })

  it('sends the typed fields, trimmed, and reports the created book', async () => {
    vi.mocked(api.addManualBook).mockResolvedValue(created)
    const { onAdded } = renderForm()
    fireEvent.change(screen.getByLabelText('Title'), { target: { value: '  The Last Church ' } })
    fireEvent.change(screen.getByLabelText('Author'), { target: { value: ' Graham McNeill ' } })
    fireEvent.change(screen.getByLabelText('Series (optional)'), { target: { value: 'The Horus Heresy' } })
    fireEvent.change(screen.getByLabelText('Position'), { target: { value: '9.5' } })
    fireEvent.change(screen.getByLabelText('Format to add'), { target: { value: 'audiobook' } })
    fireEvent.click(addButton())

    await waitFor(() => expect(onAdded).toHaveBeenCalledWith(created))
    expect(api.addManualBook).toHaveBeenCalledWith({
      title: 'The Last Church',
      authorName: 'Graham McNeill',
      seriesTitle: 'The Horus Heresy',
      seriesPosition: '9.5',
      mediaType: 'audiobook',
      searchOnAdd: false,
    })
  })

  it('leaves out the optional fields it was not given, and searches only when asked', async () => {
    vi.mocked(api.addManualBook).mockResolvedValue(created)
    renderForm('Fulgrim')
    fireEvent.change(screen.getByLabelText('Author'), { target: { value: 'Graham McNeill' } })
    expect(screen.getByLabelText('Position')).toBeDisabled()
    fireEvent.click(screen.getByRole('checkbox'))
    fireEvent.click(addButton())

    await waitFor(() => expect(api.addManualBook).toHaveBeenCalled())
    expect(api.addManualBook).toHaveBeenCalledWith({ title: 'Fulgrim', authorName: 'Graham McNeill', searchOnAdd: true })
  })

  it('links to the existing book when the add answers 409', async () => {
    vi.mocked(api.addManualBook).mockRejectedValue(Object.assign(new Error('book already in your library'), {
      status: 409,
      body: { error: 'book already in your library', existingBookId: 42 },
    }))
    const { onAdded } = renderForm('Fulgrim')
    fireEvent.change(screen.getByLabelText('Author'), { target: { value: 'Graham McNeill' } })
    fireEvent.click(addButton())

    expect(await screen.findByRole('alert')).toHaveTextContent('This author already has a book with this title in your library.')
    expect(screen.getByRole('link', { name: 'Open existing book' })).toHaveAttribute('href', '/book/42')
    // The title field points at the message that explains it.
    expect(screen.getByLabelText('Title')).toHaveAttribute('aria-invalid', 'true')
    expect(screen.getByLabelText('Title').getAttribute('aria-describedby')).toBe(screen.getByRole('alert').id)
    expect(onAdded).not.toHaveBeenCalled()
  })

  it('shows the server message for any other failure', async () => {
    vi.mocked(api.addManualBook).mockRejectedValue(new Error('authorName must contain a letter or a digit'))
    renderForm('Fulgrim')
    fireEvent.change(screen.getByLabelText('Author'), { target: { value: '?!' } })
    fireEvent.click(addButton())

    expect(await screen.findByRole('alert')).toHaveTextContent('authorName must contain a letter or a digit')
  })
})
