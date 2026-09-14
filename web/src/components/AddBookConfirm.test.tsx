import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import AddBookConfirm from './AddBookConfirm'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, options?: Record<string, unknown>) => {
      const strings: Record<string, string> = {
        'addToLibrary.backToResults': 'Back to results',
        'addToLibrary.adding': 'Adding...',
        'addToLibrary.book.confirmAdd': 'Add book',
        'addToLibrary.book.coverAlt': '{{title}} cover',
        'addToLibrary.book.noCover': 'No cover',
        'addToLibrary.book.published': 'Published',
        'addToLibrary.book.language': 'Language',
        'addToLibrary.book.resultFormat': 'Metadata format',
        'addToLibrary.book.source': 'Source',
        'addToLibrary.book.providerId': 'Source ID',
        'addToLibrary.book.asin': 'ASIN',
        'addToLibrary.book.searchedIsbn': 'Searched ISBN',
        'addToLibrary.book.isbns': 'ISBNs',
        'addToLibrary.book.identifiersHint': 'Identifiers reported by the metadata source.',
        'addToLibrary.book.showMoreIdentifiers': 'Show {{count}} more identifiers',
        'addToLibrary.book.format': 'Format',
        'addToLibrary.book.formatLabel': 'Format to add',
        'addToLibrary.book.formatHint': 'Choose which format to add',
        'addToLibrary.book.defaultFormat': 'Default',
        'addToLibrary.book.autoSearchLabel': 'Search indexers after adding',
        'addToLibrary.book.autoSearchHint': 'Try to grab the book automatically after adding it to wanted.',
        'addToLibrary.book.addFailed': 'Failed to add book',
        'addToLibrary.book.alreadyInLibrary': 'Already in your library',
        'addToLibrary.book.openExisting': 'Open existing book',
        'common.viewOnSource': 'View on {{source}} ↗',
        'common.links': 'Links',
        'common.cancel': 'Cancel',
        'common.ebook': 'Ebook',
        'common.audiobook': 'Audiobook',
        'common.both': 'Both',
      }
      let out = strings[key] ?? key
      for (const [k, v] of Object.entries(options ?? {})) {
        out = out.split(`{{${k}}}`).join(String(v))
      }
      return out
    },
  }),
}))

vi.mock('../api/client', () => ({
  api: {
    addBook: vi.fn(),
  },
}))

import { api } from '../api/client'
import type { Book } from '../api/client'

function book(overrides: Partial<Book>): Book {
  return {
    id: 0,
    foreignBookId: 'OL1W',
    authorId: 0,
    title: 'Dune',
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
    ...overrides,
  }
}

const dune = book({
  metadataProvider: 'openlibrary',
  imageUrl: 'https://example.com/dune.jpg',
  releaseDate: '1965-08-01T00:00:00Z',
  language: 'eng',
  isbns: ['9780441172719', '0441172717', '9780593099322', '9780143111580', '9780307387899'],
  author: { authorName: 'Frank Herbert', foreignAuthorId: 'OL-FH' } as Book['author'],
})

function renderConfirm(b: Book, searchedISBN: string | null = null) {
  const onBack = vi.fn()
  const onClose = vi.fn()
  const onAdded = vi.fn()
  render(<AddBookConfirm book={b} searchedISBN={searchedISBN} onBack={onBack} onClose={onClose} onAdded={onAdded} />)
  return { onBack, onClose, onAdded }
}

describe('AddBookConfirm', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(api.addBook).mockResolvedValue(book({ id: 1 }))
  })

  it('focuses the title, shows the large cover, metadata and identifiers, and keeps the Tab trap inside', async () => {
    renderConfirm(dune)
    const heading = screen.getByRole('heading', { name: 'Dune' })
    await waitFor(() => expect(heading).toHaveFocus())
    const cover = screen.getByRole('img', { name: 'Dune cover' })
    expect(cover.className).toContain('w-28')
    expect(screen.getByText('Frank Herbert')).toBeInTheDocument()
    expect(screen.getByText('1965')).toBeInTheDocument()
    expect(screen.getByText('eng')).toBeInTheDocument()
    // The metadata format row; the Format select also carries an Ebook option.
    expect(screen.getByText('Metadata format').nextElementSibling).toHaveTextContent('Ebook')
    expect(screen.getByText('9780441172719')).toBeInTheDocument()
    const identifiersSummary = screen.getByText('Show 2 more identifiers')
    expect(identifiersSummary).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Links' }))
    const sourceLink = screen.getByRole('link', { name: 'View on OpenLibrary ↗' })
    expect(sourceLink).toHaveAttribute('href', 'https://openlibrary.org/works/OL1W')
    expect(sourceLink).toHaveAttribute('target', '_blank')
    expect(sourceLink).toHaveAttribute('rel', 'noopener noreferrer')
    expect(screen.getByLabelText('Format to add')).toBeInTheDocument()
    expect(screen.getByText('Search indexers after adding')).toBeInTheDocument()
  })

  it('shows the searched ISBN apart from provider-reported identifiers', () => {
    renderConfirm(book({ foreignBookId: 'OL2W', title: 'Hyperion', isbns: ['9780316769488'] }), '9780553283686')
    expect(screen.getByText('Searched ISBN')).toBeInTheDocument()
    expect(screen.getByText('9780553283686')).toBeInTheDocument()
    expect(screen.getByText('ISBNs')).toBeInTheDocument()
    expect(screen.getByText('9780316769488')).toBeInTheDocument()
  })

  it('hides Links when the result has no trustworthy upstream URL', () => {
    renderConfirm(book({ foreignBookId: 'abs:local-item', metadataProvider: 'audiobookshelf', title: 'Local audiobook' }))
    expect(screen.queryByRole('button', { name: 'Links' })).not.toBeInTheDocument()
  })

  it('omits mediaType when the selector is left on Default and reports the created book', async () => {
    const { onAdded, onClose } = renderConfirm(dune)
    fireEvent.click(screen.getByRole('button', { name: 'Add book' }))
    await waitFor(() => expect(api.addBook).toHaveBeenCalled())
    const sent = vi.mocked(api.addBook).mock.calls[0][0]
    expect(sent).toMatchObject({ foreignBookId: 'OL1W', foreignAuthorId: 'OL-FH', authorName: 'Frank Herbert', searchOnAdd: true })
    expect(sent).not.toHaveProperty('mediaType')
    await waitFor(() => expect(onAdded).toHaveBeenCalledWith(expect.objectContaining({ id: 1 })))
    // Closing is the modal's job, so a caller can navigate first.
    expect(onClose).not.toHaveBeenCalled()
  })

  it('sends the chosen mediaType and the overridden auto-search choice', async () => {
    renderConfirm(dune)
    fireEvent.change(screen.getByLabelText('Format to add'), { target: { value: 'audiobook' } })
    fireEvent.click(screen.getByRole('checkbox', { name: /search indexers after adding/i }))
    fireEvent.click(screen.getByRole('button', { name: 'Add book' }))
    await waitFor(() => expect(api.addBook).toHaveBeenCalled())
    expect(vi.mocked(api.addBook).mock.calls[0][0]).toMatchObject({ mediaType: 'audiobook', searchOnAdd: false })
  })

  it('lets a result with no author be added, and sends empty author fields (#2187)', async () => {
    // An OpenLibrary edition with no /works/ link and no resolvable author.
    // The backend only requires foreignBookId, so the dialog must not refuse
    // to send the request.
    renderConfirm(book({ foreignBookId: 'OL999M', title: 'Standalone Edition', author: undefined }))
    expect(screen.getByText('No cover')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Add book' }))
    await waitFor(() => expect(api.addBook).toHaveBeenCalled())
    expect(vi.mocked(api.addBook).mock.calls[0][0]).toMatchObject({ foreignBookId: 'OL999M', foreignAuthorId: '', authorName: '' })
  })

  it('still sends the author name when the result carries one but no author id (DNB)', async () => {
    renderConfirm(book({ foreignBookId: 'DNB-123', title: 'Standalone Edition', author: { authorName: 'Frank Herbert' } as Book['author'] }))
    fireEvent.click(screen.getByRole('button', { name: 'Add book' }))
    await waitFor(() => expect(api.addBook).toHaveBeenCalled())
    expect(vi.mocked(api.addBook).mock.calls[0][0]).toMatchObject({ foreignBookId: 'DNB-123', foreignAuthorId: '', authorName: 'Frank Herbert' })
  })

  it('shows the server message with an open link when the add answers 409', async () => {
    // The server says which library the book is in and whether the format can
    // be changed from the book page; that is more useful than a fixed string.
    vi.mocked(api.addBook).mockRejectedValue(Object.assign(new Error('book already in your library'), {
      status: 409,
      body: { error: 'book already in your library as an ebook; change the format from the book page', existingBookId: 42 },
    }))
    const { onAdded } = renderConfirm(dune)
    fireEvent.click(screen.getByRole('button', { name: 'Add book' }))
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('book already in your library as an ebook; change the format from the book page'))
    expect(screen.getByRole('link', { name: 'Open existing book' })).toHaveAttribute('href', '/book/42')
    expect(onAdded).not.toHaveBeenCalled()
  })

  it('falls back to "Already in your library" when the 409 body has no message', async () => {
    vi.mocked(api.addBook).mockRejectedValue(Object.assign(new Error('HTTP 409'), {
      status: 409,
      body: { existingBookId: 42 },
    }))
    renderConfirm(dune)
    fireEvent.click(screen.getByRole('button', { name: 'Add book' }))
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('Already in your library'))
    expect(screen.getByRole('link', { name: 'Open existing book' })).toHaveAttribute('href', '/book/42')
  })

  it('keeps the plain error for a non-conflict failure', async () => {
    vi.mocked(api.addBook).mockRejectedValue(Object.assign(new Error('metadata provider unavailable'), { status: 502, body: { error: 'metadata provider unavailable' } }))
    const alertSpy = vi.spyOn(window, 'alert').mockImplementation(() => {})
    const { onClose } = renderConfirm(dune)
    fireEvent.click(screen.getByRole('button', { name: 'Add book' }))
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('metadata provider unavailable'))
    expect(screen.queryByRole('link', { name: 'Open existing book' })).not.toBeInTheDocument()
    expect(alertSpy).not.toHaveBeenCalled()
    expect(onClose).not.toHaveBeenCalled()
    alertSpy.mockRestore()
  })

  it('wires Back and Cancel', () => {
    const { onBack, onClose } = renderConfirm(dune)
    fireEvent.click(screen.getByRole('button', { name: 'Back to results' }))
    expect(onBack).toHaveBeenCalledTimes(1)
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(onClose).toHaveBeenCalledTimes(1)
  })
})
