import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor, within } from '@testing-library/react'
import AddToLibraryModal from './AddToLibraryModal'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, options?: Record<string, unknown>) => {
      const strings: Record<string, string> = {
        'addToLibrary.title': 'Add to library',
        'addToLibrary.description': 'Search by author, title, ISBN, or ASIN.',
        'addToLibrary.searchPlaceholder': 'Author, title, ISBN, or ASIN',
        'addToLibrary.searchPlaceholderAuthor': 'Search by author name...',
        'addToLibrary.searchPlaceholderBook': 'Title, ISBN, or ASIN',
        'addToLibrary.searching': 'Searching...',
        'addToLibrary.booksDivider': 'Books',
        'addToLibrary.select': 'Select',
        'addToLibrary.selectAuthor': 'Select {{name}}',
        'addToLibrary.selectBook': 'Select {{title}}',
        'addToLibrary.inLibrary': 'In your library',
        'addToLibrary.open': 'Open',
        'addToLibrary.openAuthor': 'Open {{name}}',
        'addToLibrary.openBook': 'Open {{title}}',
        'addToLibrary.idMissing': 'This result has no book ID and cannot be added',
        'addToLibrary.topWork': 'Top work:',
        'addToLibrary.books': 'up to {{count}} works',
        'addToLibrary.ratings': '{{count}} ratings',
        'addToLibrary.resultProvider': 'from {{provider}}',
        'addToLibrary.noResults': 'No results found',
        'addToLibrary.searchError': 'Could not reach the metadata provider - {{error}}',
        'addToLibrary.partialError': 'Some results could not be loaded: {{error}}',
        'addToLibrary.showHiddenResults': 'Show {{count}} hidden results',
        'addToLibrary.backToResults': 'Back to results',
        'addToLibrary.adding': 'Adding...',
        'addToLibrary.author.confirmAdd': 'Add author',
        'addToLibrary.author.customizeMonitoring': 'Monitoring',
        'addToLibrary.author.monitorMode': 'Monitor mode',
        'addToLibrary.author.monitorNewItems': 'Monitor newly discovered books',
        'addToLibrary.author.monitorLatestCount': 'Latest book count',
        'addToLibrary.author.mediaType': 'Media type',
        'addToLibrary.author.outcome.all': 'Adds up to {{count}} books and searches for all of them.',
        'addToLibrary.author.outcomeNoCount.all': 'Adds the whole catalogue and searches for all of it.',
        'addToLibrary.author.openExisting': 'Open existing author',
        'addToLibrary.author.findMetadata': 'Find metadata',
        'addToLibrary.author.providerMismatchNotice': 'This record comes from {{linked}}, not from your primary metadata provider {{primary}}.',
        'addToLibrary.book.confirmAdd': 'Add book',
        'addToLibrary.book.coverAlt': '{{title}} cover',
        'addToLibrary.book.noCover': 'No cover',
        'addToLibrary.book.searchedIsbn': 'Searched ISBN',
        'addToLibrary.book.isbns': 'ISBNs',
        'addToLibrary.book.formatLabel': 'Format to add',
        'addToLibrary.book.autoSearchLabel': 'Search indexers after adding',
        'addToLibrary.book.alreadyInLibrary': 'Already in your library',
        'addToLibrary.book.openExisting': 'Open existing book',
        'common.search': 'Search',
        'common.cancel': 'Cancel',
      }
      let out = strings[key] ?? key
      for (const [k, v] of Object.entries(options ?? {})) {
        out = out.split(`{{${k}}}`).join(String(v))
      }
      return out
    },
  }),
}))

// Mock the entire api/client module so no real HTTP calls are made.
vi.mock('../api/client', () => ({
  api: {
    listMetadataProfiles: vi.fn().mockResolvedValue([]),
    listRootFolders: vi.fn().mockResolvedValue([]),
    getSetting: vi.fn().mockRejectedValue(new Error('HTTP 404')),
    // useNeedsSetup (auto-grab misconfig warning) probes these on mount;
    // report a configured pipeline so no warning interferes with layout
    // assertions here.
    listIndexers: vi.fn().mockResolvedValue([{ id: 1, enabled: true, type: 'newznab' }]),
    listDownloadClients: vi.fn().mockResolvedValue([{ id: 1, enabled: true, type: 'sabnzbd' }]),
    searchAuthors: vi.fn(),
    searchBooks: vi.fn(),
    lookupISBN: vi.fn(),
    lookupASIN: vi.fn(),
    addAuthor: vi.fn(),
    addBook: vi.fn(),
  },
}))

import { api } from '../api/client'
import type { Author, Book } from '../api/client'

function author(overrides: Partial<Author>): Author {
  return {
    id: 0,
    foreignAuthorId: 'OL_AUTHOR_A',
    authorName: 'Author',
    sortName: 'Author',
    description: '',
    imageUrl: '',
    disambiguation: '',
    ratingsCount: 0,
    averageRating: 0,
    monitored: true,
    ...overrides,
  }
}

function book(overrides: Partial<Book>): Book {
  return {
    id: 0,
    foreignBookId: 'OL_BOOK_W',
    authorId: 0,
    title: 'Book',
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

const leGuin = author({ foreignAuthorId: 'OL-LEGUIN', authorName: 'Ursula K. Le Guin', disambiguation: 'A Wizard of Earthsea', statistics: { bookCount: 87, availableBookCount: 0, wantedBookCount: 0 } })
const dispossessed = book({ foreignBookId: 'OL1W', title: 'The Dispossessed', author: author({ foreignAuthorId: 'OL-LEGUIN', authorName: 'Ursula K. Le Guin' }) })
const lathe = book({ foreignBookId: 'OL2W', title: 'The Lathe of Heaven', author: author({ foreignAuthorId: '', authorName: 'Ursula K. Le Guin' }) })
const dune = book({ foreignBookId: 'OL3W', title: 'Dune', author: author({ foreignAuthorId: 'OL-HERBERT', authorName: 'Frank Herbert' }) })

const input = () => screen.getByPlaceholderText('Author, title, ISBN, or ASIN')

function typeAndSearch(value: string) {
  fireEvent.change(input(), { target: { value } })
  fireEvent.click(screen.getByRole('button', { name: /^search$/i }))
}

describe('AddToLibraryModal - dialog contract', () => {
  const onClose = vi.fn()
  const onAdded = vi.fn()

  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(api.getSetting).mockRejectedValue(new Error('HTTP 404'))
  })

  it('exposes dialog semantics, disables Search until there is a query, and closes on Cancel', () => {
    render(<AddToLibraryModal onClose={onClose} onAdded={onAdded} />)
    expect(screen.getByRole('dialog', { name: 'Add to library' })).toBeInTheDocument()
    const btn = screen.getByRole('button', { name: 'Search' })
    expect(btn).toBeDisabled()
    fireEvent.change(input(), { target: { value: 'Sanderson' } })
    expect(btn).toBeEnabled()
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('closes on Escape and keeps Tab focus inside the dialog', () => {
    render(<AddToLibraryModal onClose={onClose} onAdded={onAdded} />)
    const box = input()
    const cancel = screen.getByRole('button', { name: 'Cancel' })
    box.focus()
    fireEvent.keyDown(box, { key: 'Tab', shiftKey: true })
    expect(cancel).toHaveFocus()
    fireEvent.keyDown(cancel, { key: 'Tab' })
    expect(box).toHaveFocus()
    fireEvent.keyDown(box, { key: 'Escape' })
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('Escape closes and Tab lands inside even when focus has left the dialog', () => {
    render(<AddToLibraryModal onClose={onClose} onAdded={onAdded} />)
    const box = input()
    // Focus on the body, as after an overlay click.
    ;(document.activeElement as HTMLElement | null)?.blur()
    expect(document.activeElement).toBe(document.body)
    fireEvent.keyDown(document.body, { key: 'Tab' })
    expect(box).toHaveFocus()
    ;(document.activeElement as HTMLElement | null)?.blur()
    fireEvent.keyDown(document.body, { key: 'Tab', shiftKey: true })
    expect(screen.getByRole('button', { name: 'Cancel' })).toHaveFocus()
    ;(document.activeElement as HTMLElement | null)?.blur()
    fireEvent.keyDown(document.body, { key: 'Escape' })
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('forward Tab from a non-focusable element inside the dialog goes to the first control', () => {
    render(<AddToLibraryModal onClose={onClose} onAdded={onAdded} />)
    const heading = screen.getByRole('heading', { name: 'Add to library' })
    fireEvent.keyDown(heading, { key: 'Tab' })
    expect(input()).toHaveFocus()
  })

  it('closes on an overlay click but not on a click inside the dialog', () => {
    render(<AddToLibraryModal onClose={onClose} onAdded={onAdded} />)
    fireEvent.click(screen.getByRole('dialog'))
    expect(onClose).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('dialog').parentElement as HTMLElement)
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('the mode hint only changes the placeholder', () => {
    const { unmount } = render(<AddToLibraryModal onClose={onClose} onAdded={onAdded} mode="author" />)
    expect(screen.getByPlaceholderText('Search by author name...')).toBeInTheDocument()
    unmount()
    render(<AddToLibraryModal onClose={onClose} onAdded={onAdded} mode="book" />)
    expect(screen.getByPlaceholderText('Title, ISBN, or ASIN')).toBeInTheDocument()
  })
})

describe('AddToLibraryModal - when the search runs', () => {
  const onClose = vi.fn()
  const onAdded = vi.fn()

  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(api.getSetting).mockRejectedValue(new Error('HTTP 404'))
    vi.mocked(api.searchAuthors).mockResolvedValue([leGuin])
    vi.mocked(api.searchBooks).mockResolvedValue([dispossessed])
  })

  it('never searches on a keystroke, only on Enter', async () => {
    render(<AddToLibraryModal onClose={onClose} onAdded={onAdded} />)
    fireEvent.change(input(), { target: { value: 'l' } })
    fireEvent.change(input(), { target: { value: 'le' } })
    fireEvent.change(input(), { target: { value: 'le guin' } })
    expect(api.searchAuthors).not.toHaveBeenCalled()
    expect(api.searchBooks).not.toHaveBeenCalled()

    fireEvent.keyDown(input(), { key: 'Enter' })
    await waitFor(() => expect(screen.getByTestId('add-result-author')).toHaveTextContent('Ursula K. Le Guin'))
    expect(api.searchAuthors).toHaveBeenCalledTimes(1)
    expect(api.searchAuthors).toHaveBeenCalledWith('le guin')
    expect(api.searchBooks).toHaveBeenCalledWith('le guin')
  })

  it('fans out to authors and books in parallel for free text', async () => {
    render(<AddToLibraryModal onClose={onClose} onAdded={onAdded} />)
    typeAndSearch('le guin')
    await waitFor(() => expect(screen.getByText('The Dispossessed')).toBeInTheDocument())
    expect(api.searchAuthors).toHaveBeenCalledWith('le guin')
    expect(api.searchBooks).toHaveBeenCalledWith('le guin')
    expect(api.lookupISBN).not.toHaveBeenCalled()
    expect(api.lookupASIN).not.toHaveBeenCalled()
  })

  it('pre-fills the box and runs the search once on open with initialQuery', async () => {
    render(<AddToLibraryModal onClose={onClose} onAdded={onAdded} initialQuery="The Dispossessed" />)
    expect(input()).toHaveValue('The Dispossessed')
    await waitFor(() => expect(api.searchBooks).toHaveBeenCalledWith('The Dispossessed'))
    expect(api.searchBooks).toHaveBeenCalledTimes(1)
    expect(api.searchAuthors).toHaveBeenCalledTimes(1)
    await waitFor(() => expect(screen.getByText('The Dispossessed')).toBeInTheDocument())
  })

  it('does not search on open without an initialQuery', () => {
    render(<AddToLibraryModal onClose={onClose} onAdded={onAdded} />)
    expect(api.searchBooks).not.toHaveBeenCalled()
    expect(api.searchAuthors).not.toHaveBeenCalled()
  })

  it('does not overlap searches started with Enter', async () => {
    let resolveLookup!: (b: Book) => void
    vi.mocked(api.lookupISBN).mockReturnValue(new Promise(resolve => { resolveLookup = resolve }))

    render(<AddToLibraryModal onClose={onClose} onAdded={onAdded} />)
    fireEvent.change(input(), { target: { value: '9780553283686' } })
    fireEvent.keyDown(input(), { key: 'Enter' })
    fireEvent.change(input(), { target: { value: '9780140328721' } })
    fireEvent.keyDown(input(), { key: 'Enter' })

    expect(api.lookupISBN).toHaveBeenCalledTimes(1)
    resolveLookup(book({ foreignBookId: 'OL2W', title: 'Hyperion' }))
    await screen.findByRole('button', { name: 'Select Hyperion' })
    fireEvent.click(screen.getByRole('button', { name: 'Select Hyperion' }))
    expect(screen.getByText('9780553283686')).toBeInTheDocument()
    expect(screen.queryByText('9780140328721')).not.toBeInTheDocument()
  })
})

describe('AddToLibraryModal - identifier dispatch', () => {
  const onClose = vi.fn()
  const onAdded = vi.fn()

  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(api.getSetting).mockRejectedValue(new Error('HTTP 404'))
  })

  it('routes an ISBN to the ISBN lookup and skips the author search', async () => {
    vi.mocked(api.lookupISBN).mockResolvedValue(book({ foreignBookId: 'OL2W', title: 'Hyperion', isbns: ['9780316769488'] }))
    render(<AddToLibraryModal onClose={onClose} onAdded={onAdded} />)
    typeAndSearch('978-0-553-28368-6')
    await waitFor(() => expect(screen.getByText('Hyperion')).toBeInTheDocument())
    expect(api.lookupISBN).toHaveBeenCalledWith('9780553283686')
    expect(api.searchAuthors).not.toHaveBeenCalled()
    expect(api.searchBooks).not.toHaveBeenCalled()
    // The result is a book row and lands on the book confirm step, which
    // shows the searched ISBN apart from the identifiers the provider reports.
    fireEvent.click(screen.getByRole('button', { name: 'Select Hyperion' }))
    expect(screen.getByText('Searched ISBN')).toBeInTheDocument()
    expect(screen.getByText('9780553283686')).toBeInTheDocument()
    expect(screen.getByText('ISBNs')).toBeInTheDocument()
    expect(screen.getByText('9780316769488')).toBeInTheDocument()
  })

  it('routes an ASIN-shaped query to the ASIN lookup', async () => {
    vi.mocked(api.lookupASIN).mockResolvedValue(book({ foreignBookId: 'OL-IRON', title: 'Iron Flame', asin: 'B0DBJBFHGT', mediaType: 'audiobook' }))
    render(<AddToLibraryModal onClose={onClose} onAdded={onAdded} />)
    typeAndSearch('b0dbjbfhgt')
    await waitFor(() => expect(screen.getByText('Iron Flame')).toBeInTheDocument())
    expect(api.lookupASIN).toHaveBeenCalledWith('B0DBJBFHGT')
    expect(api.searchAuthors).not.toHaveBeenCalled()
    expect(api.searchBooks).not.toHaveBeenCalled()
    expect(screen.getByRole('button', { name: 'Select Iron Flame' })).toBeEnabled()
  })

  it('shows the error when the ASIN does not resolve', async () => {
    vi.mocked(api.lookupASIN).mockRejectedValue(new Error('not found'))
    render(<AddToLibraryModal onClose={onClose} onAdded={onAdded} />)
    typeAndSearch('B0NONEXIST')
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('not found'))
    expect(api.searchBooks).not.toHaveBeenCalled()
  })
})

describe('AddToLibraryModal - mixed result list', () => {
  const onClose = vi.fn()
  const onAdded = vi.fn()

  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(api.getSetting).mockRejectedValue(new Error('HTTP 404'))
  })

  it('lists each author row followed by its books, then the rest under a Books divider', async () => {
    vi.mocked(api.searchAuthors).mockResolvedValue([leGuin])
    // The book search returns Dune first; it still lands after the divider
    // because no author row claims it. Lathe of Heaven has no author id and
    // is matched by folded name.
    vi.mocked(api.searchBooks).mockResolvedValue([dune, dispossessed, lathe])

    render(<AddToLibraryModal onClose={onClose} onAdded={onAdded} />)
    typeAndSearch('le guin')
    await waitFor(() => expect(screen.getByText('Dune')).toBeInTheDocument())

    const rows = screen.getAllByTestId(/^add-result-(author|book)$/)
    expect(rows.map(r => r.getAttribute('data-testid'))).toEqual(['add-result-author', 'add-result-book', 'add-result-book', 'add-result-book'])
    expect(within(rows[0]).getByText('Ursula K. Le Guin')).toBeInTheDocument()
    expect(within(rows[1]).getByText('The Dispossessed')).toBeInTheDocument()
    expect(within(rows[2]).getByText('The Lathe of Heaven')).toBeInTheDocument()
    expect(within(rows[3]).getByText('Dune')).toBeInTheDocument()
    const divider = screen.getByRole('separator')
    expect(divider).toHaveTextContent('Books')
    // The divider sits between the last grouped book and Dune.
    expect(rows[2].compareDocumentPosition(divider) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(divider.compareDocumentPosition(rows[3]) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('groups a book whose author id comes from another provider under the row by name', async () => {
    // With the default setup every non primary provider is an enricher: a DNB
    // book carries a dnb: author id while the author row was collapsed to the
    // OpenLibrary record, so the ids can never agree even for the same person.
    vi.mocked(api.searchAuthors).mockResolvedValue([author({ foreignAuthorId: 'OL123A', authorName: 'Juli Zeh' })])
    vi.mocked(api.searchBooks).mockResolvedValue([
      book({ foreignBookId: 'OL1W', title: 'Unterleuten', author: author({ foreignAuthorId: 'OL123A', authorName: 'Juli Zeh' }) }),
      book({ foreignBookId: 'dnb:456', title: 'Corpus Delicti', author: author({ foreignAuthorId: 'dnb:789', authorName: 'Juli Zeh' }) }),
    ])
    render(<AddToLibraryModal onClose={onClose} onAdded={onAdded} />)
    typeAndSearch('juli zeh')
    await waitFor(() => expect(screen.getByText('Corpus Delicti')).toBeInTheDocument())
    const rows = screen.getAllByTestId(/^add-result-(author|book)$/)
    expect(rows.map(r => r.getAttribute('data-testid'))).toEqual(['add-result-author', 'add-result-book', 'add-result-book'])
    expect(within(rows[2]).getByText('Corpus Delicti')).toBeInTheDocument()
    expect(screen.queryByRole('separator')).not.toBeInTheDocument()
  })

  it('keeps a same-provider id mismatch decisive even when the names agree', async () => {
    vi.mocked(api.searchAuthors).mockResolvedValue([author({ foreignAuthorId: 'OL123A', authorName: 'John Smith' })])
    vi.mocked(api.searchBooks).mockResolvedValue([
      book({ foreignBookId: 'OL9W', title: 'Another Smith', author: author({ foreignAuthorId: 'OL999A', authorName: 'John Smith' }) }),
    ])
    render(<AddToLibraryModal onClose={onClose} onAdded={onAdded} />)
    typeAndSearch('john smith')
    await waitFor(() => expect(screen.getByText('Another Smith')).toBeInTheDocument())
    const rows = screen.getAllByTestId(/^add-result-(author|book)$/)
    const divider = screen.getByRole('separator')
    // Author row, divider, then the book: a different OpenLibrary author.
    expect(rows[0].compareDocumentPosition(divider) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(divider.compareDocumentPosition(rows[1]) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('leaves a book under Books when its name matches two author rows from different providers', async () => {
    // Two different people named John Smith, one per provider. A third
    // provider's book carries only the name, so there is no way to tell whose
    // it is: guessing the first row would hand a stranger's book to him.
    vi.mocked(api.searchAuthors).mockResolvedValue([
      author({ foreignAuthorId: 'OL123A', authorName: 'John Smith' }),
      author({ foreignAuthorId: 'dnb:555', authorName: 'John Smith' }),
    ])
    vi.mocked(api.searchBooks).mockResolvedValue([
      book({ foreignBookId: 'gb:1', title: 'Whose Book', author: author({ foreignAuthorId: 'gb:777', authorName: 'John Smith' }) }),
    ])
    render(<AddToLibraryModal onClose={onClose} onAdded={onAdded} />)
    typeAndSearch('john smith')
    await waitFor(() => expect(screen.getByText('Whose Book')).toBeInTheDocument())
    const rows = screen.getAllByTestId(/^add-result-(author|book)$/)
    expect(rows.map(r => r.getAttribute('data-testid'))).toEqual(['add-result-author', 'add-result-author', 'add-result-book'])
    const divider = screen.getByRole('separator')
    expect(rows[1].compareDocumentPosition(divider) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(divider.compareDocumentPosition(rows[2]) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('groups a book under the row its author id names even when another row shares the name', async () => {
    vi.mocked(api.searchAuthors).mockResolvedValue([
      author({ foreignAuthorId: 'OL123A', authorName: 'John Smith' }),
      author({ foreignAuthorId: 'dnb:555', authorName: 'John Smith' }),
    ])
    vi.mocked(api.searchBooks).mockResolvedValue([
      book({ foreignBookId: 'dnb:1', title: 'German Smith', author: author({ foreignAuthorId: 'dnb:555', authorName: 'John Smith' }) }),
    ])
    render(<AddToLibraryModal onClose={onClose} onAdded={onAdded} />)
    typeAndSearch('john smith')
    await waitFor(() => expect(screen.getByText('German Smith')).toBeInTheDocument())
    const rows = screen.getAllByTestId(/^add-result-(author|book)$/)
    // Author OL, author DNB, then the book under the DNB row with no divider.
    expect(rows.map(r => r.getAttribute('data-testid'))).toEqual(['add-result-author', 'add-result-author', 'add-result-book'])
    expect(screen.queryByRole('separator')).not.toBeInTheDocument()
  })

  it('leaves an id less book under Books when its name matches two author rows', async () => {
    vi.mocked(api.searchAuthors).mockResolvedValue([
      author({ foreignAuthorId: 'OL123A', authorName: 'John Smith' }),
      author({ foreignAuthorId: 'dnb:555', authorName: 'John Smith' }),
    ])
    vi.mocked(api.searchBooks).mockResolvedValue([
      book({ foreignBookId: 'hc:1', title: 'Nameonly Smith', author: author({ foreignAuthorId: '', authorName: 'John Smith' }) }),
    ])
    render(<AddToLibraryModal onClose={onClose} onAdded={onAdded} />)
    typeAndSearch('john smith')
    await waitFor(() => expect(screen.getByText('Nameonly Smith')).toBeInTheDocument())
    expect(screen.getByRole('separator')).toHaveTextContent('Books')
  })

  it('counts the same author record returned twice as one person', async () => {
    vi.mocked(api.searchAuthors).mockResolvedValue([
      author({ foreignAuthorId: 'OL1A', authorName: 'Ursula K. Le Guin' }),
      author({ foreignAuthorId: 'OL1A', authorName: 'Ursula K. Le Guin' }),
    ])
    vi.mocked(api.searchBooks).mockResolvedValue([
      book({ foreignBookId: 'dnb:9', title: 'Die Enteigneten', author: author({ foreignAuthorId: 'dnb:77', authorName: 'Ursula K. Le Guin' }) }),
    ])
    render(<AddToLibraryModal onClose={onClose} onAdded={onAdded} />)
    typeAndSearch('le guin')
    await waitFor(() => expect(screen.getByText('Die Enteigneten')).toBeInTheDocument())
    const rows = screen.getAllByTestId(/^add-result-(author|book)$/)
    expect(rows.map(r => r.getAttribute('data-testid'))).toEqual(['add-result-author', 'add-result-book', 'add-result-author'])
    expect(screen.queryByRole('separator')).not.toBeInTheDocument()
  })

  it('shows no divider when every book belongs to an author row', async () => {
    vi.mocked(api.searchAuthors).mockResolvedValue([leGuin])
    vi.mocked(api.searchBooks).mockResolvedValue([dispossessed])
    render(<AddToLibraryModal onClose={onClose} onAdded={onAdded} />)
    typeAndSearch('le guin')
    await waitFor(() => expect(screen.getByText('The Dispossessed')).toBeInTheDocument())
    expect(screen.queryByRole('separator')).not.toBeInTheDocument()
  })

  it('renders author rows with top work, work count and ratings, and book rows with the larger cover', async () => {
    vi.mocked(api.searchAuthors).mockResolvedValue([author({ ...leGuin, ratingsCount: 5000 })])
    vi.mocked(api.searchBooks).mockResolvedValue([book({ ...dispossessed, imageUrl: 'https://example.com/d.jpg', releaseDate: '1974-05-01T00:00:00Z' })])
    render(<AddToLibraryModal onClose={onClose} onAdded={onAdded} />)
    typeAndSearch('le guin')
    await waitFor(() => expect(screen.getByText('The Dispossessed')).toBeInTheDocument())
    expect(screen.getByText('Top work: A Wizard of Earthsea')).toBeInTheDocument()
    expect(screen.getByText('up to 87 works')).toBeInTheDocument()
    expect(screen.getByText('5000 ratings')).toBeInTheDocument()
    expect(screen.getByText('1974')).toBeInTheDocument()
    const cover = within(screen.getByTestId('add-result-book')).getByRole('presentation')
    expect(cover.className).toContain('w-14')
    expect(cover.className).toContain('h-20')
  })

  it('renders "No results found" when both searches come back empty or null', async () => {
    vi.mocked(api.searchAuthors).mockResolvedValue(null as unknown as Author[])
    vi.mocked(api.searchBooks).mockResolvedValue(null as unknown as Book[])
    render(<AddToLibraryModal onClose={onClose} onAdded={onAdded} />)
    typeAndSearch('qzznomatch')
    await waitFor(() => expect(screen.getByText('No results found')).toBeInTheDocument())
  })

  it('shows an error banner when both provider searches fail', async () => {
    vi.mocked(api.searchAuthors).mockRejectedValue(new Error('search authors: HTTP 503: Service Unavailable'))
    vi.mocked(api.searchBooks).mockRejectedValue(new Error('search books: HTTP 503'))
    render(<AddToLibraryModal onClose={onClose} onAdded={onAdded} />)
    typeAndSearch('tolkien')
    await waitFor(() => expect(screen.getByText(/HTTP 503/i)).toBeInTheDocument())
    expect(screen.queryByText(/no results found/i)).not.toBeInTheDocument()
  })

  it('shows the error banner when the author search fails and nothing else came back', async () => {
    vi.mocked(api.searchAuthors).mockRejectedValue(new Error('search authors: HTTP 503: Service Unavailable'))
    vi.mocked(api.searchBooks).mockResolvedValue([])
    render(<AddToLibraryModal onClose={onClose} onAdded={onAdded} />)
    typeAndSearch('tolkien')
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('HTTP 503'))
    expect(screen.queryByText(/no results found/i)).not.toBeInTheDocument()
  })

  it('keeps author results visible when the book search fails, with a partial notice', async () => {
    vi.mocked(api.searchAuthors).mockResolvedValue([author({ foreignAuthorId: 'OL_BAD_TITLE_A', authorName: 'Romeo and Juliet', disambiguation: 'William Shakespeare' })])
    vi.mocked(api.searchBooks).mockRejectedValue(new Error('book search down'))
    render(<AddToLibraryModal onClose={onClose} onAdded={onAdded} />)
    typeAndSearch('Romeo and Juliet')
    await waitFor(() => expect(screen.getByText('Romeo and Juliet')).toBeInTheDocument())
    expect(screen.queryByRole('button', { name: /hidden result/i })).not.toBeInTheDocument()
    expect(screen.queryByText(/could not reach/i)).not.toBeInTheDocument()
    expect(screen.getByText(/Some results could not be loaded: book search down/)).toBeInTheDocument()
  })

  it('clears the error banner when a subsequent search succeeds', async () => {
    vi.mocked(api.searchAuthors)
      .mockRejectedValueOnce(new Error('search authors: HTTP 503: Service Unavailable'))
      .mockResolvedValueOnce([author({ foreignAuthorId: 'OL26320A', authorName: 'J.R.R. Tolkien' })])
    vi.mocked(api.searchBooks).mockResolvedValue([])
    render(<AddToLibraryModal onClose={onClose} onAdded={onAdded} />)
    typeAndSearch('tolkien')
    await waitFor(() => expect(screen.getByText(/HTTP 503/i)).toBeInTheDocument())
    fireEvent.click(screen.getByRole('button', { name: /^search$/i }))
    await waitFor(() => expect(screen.queryByText(/HTTP 503/i)).not.toBeInTheDocument())
    expect(screen.getByText('J.R.R. Tolkien')).toBeInTheDocument()
  })

  it('renders both rows when a provider returns the same id twice', async () => {
    vi.mocked(api.searchAuthors).mockResolvedValue([author({ foreignAuthorId: 'OL-DUP', authorName: 'Twice' }), author({ foreignAuthorId: 'OL-DUP', authorName: 'Twice' })])
    vi.mocked(api.searchBooks).mockResolvedValue([book({ foreignBookId: 'OL-DUPW', title: 'Same Work' }), book({ foreignBookId: 'OL-DUPW', title: 'Same Work' })])
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {})
    render(<AddToLibraryModal onClose={onClose} onAdded={onAdded} />)
    typeAndSearch('twice')
    await waitFor(() => expect(screen.getAllByTestId('add-result-book')).toHaveLength(2))
    expect(screen.getAllByTestId('add-result-author')).toHaveLength(2)
    expect(consoleError).not.toHaveBeenCalled()
    consoleError.mockRestore()
  })

  it('disables Select on a book row with no foreign id', async () => {
    vi.mocked(api.searchAuthors).mockResolvedValue([])
    vi.mocked(api.searchBooks).mockResolvedValue([book({ foreignBookId: '', title: 'Orphan' })])
    render(<AddToLibraryModal onClose={onClose} onAdded={onAdded} />)
    typeAndSearch('orphan')
    await waitFor(() => expect(screen.getByText('Orphan')).toBeInTheDocument())
    const row = screen.getByTestId('add-result-book')
    expect(within(row).getByRole('button', { name: 'Select' })).toBeDisabled()
  })
})

describe('AddToLibraryModal - title guard', () => {
  const onClose = vi.fn()
  const onAdded = vi.fn()
  const hiddenAuthor = author({ foreignAuthorId: 'OL_BAD_TITLE_A', authorName: 'Romeo and Juliet', disambiguation: 'William Shakespeare' })
  const shakespeare = author({ foreignAuthorId: 'OL_SHAKESPEARE_A', authorName: 'William Shakespeare', disambiguation: 'Romeo and Juliet' })
  const play = book({ foreignBookId: 'OL_RJ', title: 'Romeo and Juliet', author: shakespeare })

  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(api.getSetting).mockRejectedValue(new Error('HTTP 404'))
    vi.mocked(api.addAuthor).mockResolvedValue(author({ id: 5 }))
  })

  it('hides a likely book-title author by default, keeps the book row, and reveals the author on request', async () => {
    vi.mocked(api.searchAuthors).mockResolvedValue([hiddenAuthor])
    vi.mocked(api.searchBooks).mockResolvedValue([play])
    render(<AddToLibraryModal onClose={onClose} onAdded={onAdded} />)
    typeAndSearch('Romeo and Juliet')

    const revealButton = await screen.findByRole('button', { name: /show 1 hidden result/i })
    expect(screen.queryByTestId('add-result-author')).not.toBeInTheDocument()
    // The play itself is still offered, as a book.
    expect(screen.getByRole('button', { name: 'Select Romeo and Juliet' })).toBeInTheDocument()

    fireEvent.click(revealButton)
    expect(screen.getByTestId('add-result-author')).toHaveTextContent('Romeo and Juliet')
    fireEvent.click(within(screen.getByTestId('add-result-author')).getByRole('button', { name: 'Select Romeo and Juliet' }))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Add author' })).toBeEnabled())
    fireEvent.click(screen.getByRole('button', { name: 'Add author' }))
    await waitFor(() => expect(api.addAuthor).toHaveBeenCalledWith(expect.objectContaining({ foreignAuthorId: 'OL_BAD_TITLE_A' })))
    expect(onAdded).toHaveBeenCalledWith({ kind: 'author', author: expect.objectContaining({ id: 5 }) })
    expect(onClose).toHaveBeenCalled()
  })

  it('keeps regular author results visible while hiding only the title-shaped one', async () => {
    vi.mocked(api.searchAuthors).mockResolvedValue([shakespeare, hiddenAuthor])
    vi.mocked(api.searchBooks).mockResolvedValue([play])
    render(<AddToLibraryModal onClose={onClose} onAdded={onAdded} />)
    typeAndSearch('Romeo and Juliet')
    await waitFor(() => expect(screen.getAllByTestId('add-result-author')).toHaveLength(1))
    expect(screen.getByTestId('add-result-author')).toHaveTextContent('William Shakespeare')
    expect(screen.getByRole('button', { name: /show 1 hidden result/i })).toBeInTheDocument()
  })
})

describe('AddToLibraryModal - already in the library (#1227)', () => {
  const onClose = vi.fn()
  const onAdded = vi.fn()

  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(api.getSetting).mockRejectedValue(new Error('HTTP 404'))
  })

  it('renders "In your library" and Open instead of Select on stamped author and book rows', async () => {
    vi.mocked(api.searchAuthors).mockResolvedValue([
      author({ foreignAuthorId: 'OL-OWNED-A', authorName: 'Owned Author', libraryAuthorId: 7 }),
      author({ foreignAuthorId: 'OL-NEW-A', authorName: 'New Author' }),
    ])
    vi.mocked(api.searchBooks).mockResolvedValue([
      book({ foreignBookId: 'OL-OWNED-B', title: 'Owned Book', libraryBookId: 42, author: author({ foreignAuthorId: 'OL-OWNED-A', authorName: 'Owned Author' }) }),
      book({ foreignBookId: 'OL-NEW-B', title: 'New Book', author: author({ foreignAuthorId: 'OL-NEW-A', authorName: 'New Author' }) }),
    ])
    render(<AddToLibraryModal onClose={onClose} onAdded={onAdded} />)
    typeAndSearch('owned')
    await waitFor(() => expect(screen.getByText('Owned Book')).toBeInTheDocument())

    expect(screen.getAllByText('In your library')).toHaveLength(2)
    expect(screen.getByRole('link', { name: 'Open Owned Author' })).toHaveAttribute('href', '/author/7')
    expect(screen.getByRole('link', { name: 'Open Owned Book' })).toHaveAttribute('href', '/book/42')
    expect(screen.queryByRole('button', { name: 'Select Owned Author' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Select Owned Book' })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Select New Author' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Select New Book' })).toBeInTheDocument()
  })

  it('shows the author 409 inline with existing-author actions', async () => {
    vi.mocked(api.searchAuthors).mockResolvedValue([author({ foreignAuthorId: 'OL13200512A', authorName: 'Emilia Jae' })])
    vi.mocked(api.searchBooks).mockResolvedValue([])
    vi.mocked(api.addAuthor).mockRejectedValue(Object.assign(new Error('author already exists'), {
      status: 409,
      body: { error: 'author already exists', canonicalAuthorId: 60 },
    }))
    render(<AddToLibraryModal onClose={onClose} onAdded={onAdded} />)
    typeAndSearch('emilia jae')
    await waitFor(() => expect(screen.getByText('Emilia Jae')).toBeInTheDocument())
    fireEvent.click(screen.getByRole('button', { name: 'Select Emilia Jae' }))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Add author' })).toBeEnabled())
    fireEvent.click(screen.getByRole('button', { name: 'Add author' }))

    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('author already exists'))
    expect(screen.getByRole('link', { name: 'Open existing author' })).toHaveAttribute('href', '/author/60')
    expect(screen.getByRole('link', { name: 'Find metadata' })).toHaveAttribute('href', '/author/60?linkMetadata=1')
    expect(onAdded).not.toHaveBeenCalled()
    expect(onClose).not.toHaveBeenCalled()
  })

  it('shows the book 409 message with an open link', async () => {
    vi.mocked(api.searchAuthors).mockResolvedValue([])
    vi.mocked(api.searchBooks).mockResolvedValue([book({ foreignBookId: 'OL-OWNED', title: 'Owned Book', author: author({ foreignAuthorId: 'OL-A', authorName: 'Someone' }) })])
    vi.mocked(api.addBook).mockRejectedValue(Object.assign(new Error('book already in your library'), {
      status: 409,
      body: { error: 'book already in your library', existingBookId: 42 },
    }))
    render(<AddToLibraryModal onClose={onClose} onAdded={onAdded} />)
    typeAndSearch('owned')
    await waitFor(() => expect(screen.getByText('Owned Book')).toBeInTheDocument())
    fireEvent.click(screen.getByRole('button', { name: 'Select Owned Book' }))
    fireEvent.click(screen.getByRole('button', { name: 'Add book' }))

    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('Already in your library'))
    expect(screen.getByRole('link', { name: 'Open existing book' })).toHaveAttribute('href', '/book/42')
    expect(onAdded).not.toHaveBeenCalled()
    expect(onClose).not.toHaveBeenCalled()
  })
})

describe('AddToLibraryModal - selection and confirm steps', () => {
  const onClose = vi.fn()
  const onAdded = vi.fn()

  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(api.getSetting).mockRejectedValue(new Error('HTTP 404'))
    vi.mocked(api.searchAuthors).mockResolvedValue([leGuin])
    vi.mocked(api.searchBooks).mockResolvedValue([dispossessed])
  })

  it('selecting an author opens the author confirm step, adds, and reports the author', async () => {
    vi.mocked(api.addAuthor).mockResolvedValue(author({ id: 37, foreignAuthorId: 'OL-LEGUIN', authorName: 'Ursula K. Le Guin' }))
    render(<AddToLibraryModal onClose={onClose} onAdded={onAdded} />)
    typeAndSearch('le guin')
    await waitFor(() => expect(screen.getByText('The Dispossessed')).toBeInTheDocument())
    fireEvent.click(screen.getByRole('button', { name: 'Select Ursula K. Le Guin' }))

    expect(screen.getByLabelText('Monitor mode')).toBeInTheDocument()
    expect(screen.queryByPlaceholderText('Author, title, ISBN, or ASIN')).not.toBeInTheDocument()
    expect(screen.getByTestId('add-author-outcome')).toHaveTextContent('Adds up to 87 books and searches for all of them.')

    await waitFor(() => expect(screen.getByRole('button', { name: 'Add author' })).toBeEnabled())
    fireEvent.click(screen.getByRole('button', { name: 'Add author' }))
    await waitFor(() => expect(onAdded).toHaveBeenCalledWith({ kind: 'author', author: expect.objectContaining({ id: 37 }) }))
    expect(onClose).toHaveBeenCalledTimes(1)
    expect(api.addAuthor).toHaveBeenCalledWith(expect.objectContaining({ foreignAuthorId: 'OL-LEGUIN', authorName: 'Ursula K. Le Guin' }))
  })

  it('selecting a book opens the book confirm step, adds, and reports the book', async () => {
    vi.mocked(api.addBook).mockResolvedValue(book({ id: 9, title: 'The Dispossessed' }))
    render(<AddToLibraryModal onClose={onClose} onAdded={onAdded} />)
    typeAndSearch('le guin')
    await waitFor(() => expect(screen.getByText('The Dispossessed')).toBeInTheDocument())
    fireEvent.click(screen.getByRole('button', { name: 'Select The Dispossessed' }))

    const heading = screen.getByRole('heading', { name: 'The Dispossessed' })
    await waitFor(() => expect(heading).toHaveFocus())
    expect(screen.getByLabelText('Format to add')).toBeInTheDocument()
    expect(screen.getByText('Search indexers after adding')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Add book' }))
    await waitFor(() => expect(onAdded).toHaveBeenCalledWith({ kind: 'book', book: expect.objectContaining({ id: 9 }) }))
    expect(onClose).toHaveBeenCalledTimes(1)
    expect(api.addBook).toHaveBeenCalledWith(expect.objectContaining({ foreignBookId: 'OL1W', foreignAuthorId: 'OL-LEGUIN', authorName: 'Ursula K. Le Guin' }))
  })

  it('Back to results restores the query and the mixed list without searching again', async () => {
    render(<AddToLibraryModal onClose={onClose} onAdded={onAdded} />)
    typeAndSearch('le guin')
    await waitFor(() => expect(screen.getByText('The Dispossessed')).toBeInTheDocument())
    fireEvent.click(screen.getByRole('button', { name: 'Select The Dispossessed' }))
    fireEvent.click(screen.getByRole('button', { name: 'Back to results' }))

    expect(input()).toHaveValue('le guin')
    expect(screen.getByRole('button', { name: 'Select Ursula K. Le Guin' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Select The Dispossessed' })).toBeInTheDocument()
    expect(screen.queryByLabelText('Format to add')).not.toBeInTheDocument()
    expect(api.searchBooks).toHaveBeenCalledTimes(1)
  })

  it('the Tab trap follows into the confirm step', async () => {
    render(<AddToLibraryModal onClose={onClose} onAdded={onAdded} />)
    typeAndSearch('le guin')
    await waitFor(() => expect(screen.getByText('The Dispossessed')).toBeInTheDocument())
    fireEvent.click(screen.getByRole('button', { name: 'Select The Dispossessed' }))
    const heading = screen.getByRole('heading', { name: 'The Dispossessed' })
    await waitFor(() => expect(heading).toHaveFocus())
    fireEvent.keyDown(heading, { key: 'Tab', shiftKey: true })
    expect(screen.getByRole('button', { name: 'Add book' })).toHaveFocus()
  })

  // #2237: with a primary metadata provider configured, a result that would
  // sync from another provider is flagged in the list and on the confirm step.
  it('flags an author result from another provider and warns on the confirm step', async () => {
    vi.mocked(api.getSetting).mockImplementation(async (key: string) => {
      if (key === 'metadata.primary_provider') return { key, value: 'hardcover' }
      throw new Error('HTTP 404')
    })
    vi.mocked(api.searchAuthors).mockResolvedValue([author({ foreignAuthorId: 'OL3101279A', authorName: 'Matt Dinniman', metadataProvider: 'openlibrary' })])
    vi.mocked(api.searchBooks).mockResolvedValue([])
    render(<AddToLibraryModal onClose={onClose} onAdded={onAdded} />)
    typeAndSearch('Matt Dinniman')
    await waitFor(() => expect(screen.getByText('Matt Dinniman')).toBeInTheDocument())
    expect(await screen.findByText('from OpenLibrary')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Select Matt Dinniman' }))
    const notice = await screen.findByRole('alert')
    expect(notice.textContent).toContain('OpenLibrary')
    expect(notice.textContent).toContain('Hardcover')
  })

  it('does not flag a result from the configured primary provider', async () => {
    vi.mocked(api.getSetting).mockImplementation(async (key: string) => {
      if (key === 'metadata.primary_provider') return { key, value: 'openlibrary' }
      throw new Error('HTTP 404')
    })
    vi.mocked(api.searchAuthors).mockResolvedValue([author({ foreignAuthorId: 'OL3101279A', authorName: 'Matt Dinniman', metadataProvider: 'openlibrary' })])
    vi.mocked(api.searchBooks).mockResolvedValue([])
    render(<AddToLibraryModal onClose={onClose} onAdded={onAdded} />)
    typeAndSearch('Matt Dinniman')
    await waitFor(() => expect(screen.getByText('Matt Dinniman')).toBeInTheDocument())
    expect(screen.queryByText(/^from /)).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Select Matt Dinniman' }))
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('loads the author defaults once on open, not on each selection', async () => {
    render(<AddToLibraryModal onClose={onClose} onAdded={onAdded} />)
    await waitFor(() => expect(api.listMetadataProfiles).toHaveBeenCalledTimes(1))
    typeAndSearch('le guin')
    await waitFor(() => expect(screen.getByText('The Dispossessed')).toBeInTheDocument())
    fireEvent.click(screen.getByRole('button', { name: 'Select Ursula K. Le Guin' }))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Add author' })).toBeEnabled())
    fireEvent.click(screen.getByRole('button', { name: 'Back to results' }))
    fireEvent.click(screen.getByRole('button', { name: 'Select Ursula K. Le Guin' }))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Add author' })).toBeEnabled())
    expect(api.listMetadataProfiles).toHaveBeenCalledTimes(1)
    expect(api.listRootFolders).toHaveBeenCalledTimes(1)
  })

  it('shows no provider notice when no primary provider is configured', async () => {
    vi.mocked(api.searchAuthors).mockResolvedValue([author({ foreignAuthorId: 'OL3101279A', authorName: 'Matt Dinniman', metadataProvider: 'openlibrary' })])
    vi.mocked(api.searchBooks).mockResolvedValue([])
    render(<AddToLibraryModal onClose={onClose} onAdded={onAdded} />)
    typeAndSearch('Matt Dinniman')
    await waitFor(() => expect(screen.getByText('Matt Dinniman')).toBeInTheDocument())
    expect(screen.queryByText(/^from /)).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Select Matt Dinniman' }))
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })
})
