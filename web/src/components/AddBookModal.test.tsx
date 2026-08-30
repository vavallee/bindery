import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import AddBookModal from './AddBookModal'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, options?: Record<string, unknown>) => {
      if (key === 'addBookModal.selectBook') return `Select ${options?.title ?? ''}`
      if (key === 'addBookModal.coverAlt') return `${options?.title ?? ''} cover`
      if (key === 'addBookModal.showMoreIdentifiers') return `Show ${options?.count ?? 0} more identifiers`
      if (key === 'common.viewOnSource') return `View on ${options?.source ?? ''} ↗`
      return ({
      'addBookModal.title': 'Add Book',
      'addBookModal.description': 'Search by title, ISBN, or ASIN to add a specific book to your wanted list.',
      'addBookModal.autoSearchLabel': 'Search indexers after adding',
      'addBookModal.autoSearchHint': 'Try to grab the book automatically after adding it to wanted.',
      'addBookModal.format': 'Format',
      'addBookModal.formatLabel': 'Format to add',
      'addBookModal.formatHint': 'Choose which format to add',
      'addBookModal.defaultFormat': 'Default',
      'addBookModal.searchPlaceholder': 'Title, ISBN, or ASIN (for example, Dune, 9780441478125, B0DBJBFHGT)',
      'addBookModal.searching': 'Searching...',
      'addBookModal.searchFailed': 'Search failed',
      'addBookModal.idMissing': 'This result has no book ID and cannot be added',
      'addBookModal.select': 'Select',
      'addBookModal.confirmAdd': 'Add book',
      'addBookModal.backToResults': 'Back to results',
      'addBookModal.noCover': 'No cover',
      'addBookModal.published': 'Published',
      'addBookModal.language': 'Language',
      'addBookModal.resultFormat': 'Metadata format',
      'addBookModal.source': 'Source',
      'addBookModal.providerId': 'Source ID',
      'addBookModal.asin': 'ASIN',
      'addBookModal.isbns': 'ISBNs',
      'addBookModal.identifiersHint': 'Identifiers reported by the metadata source.',
      'addBookModal.adding': 'Adding...',
      'addBookModal.addFailed': 'Failed to add book',
      'common.search': 'Search',
      'common.links': 'Links',
      'common.cancel': 'Cancel',
      'common.noResults': 'No results found',
      'common.ebook': 'Ebook',
      'common.audiobook': 'Audiobook',
      'common.both': 'Both',
      }[key] ?? key)
    },
  }),
}))

// Mock the api/client module so no real HTTP calls are made.
vi.mock('../api/client', () => ({
  api: {
    searchBooks: vi.fn(),
    lookupISBN: vi.fn(),
    lookupASIN: vi.fn(),
    addBook: vi.fn(),
  },
}))

import { api } from '../api/client'

describe('AddBookModal — null search results (#1188)', () => {
  const onClose = vi.fn()
  const onAdded = vi.fn()

  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('exposes dialog semantics and uses Cancel to close', () => {
    render(<AddBookModal onClose={onClose} onAdded={onAdded} />)

    expect(screen.getByRole('dialog', { name: 'Add Book' })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('renders "No results found." when the search returns null instead of crashing', async () => {
    // The backend can encode an empty success as `null`; the modal must treat
    // that as an empty list rather than calling `.map()` on null.
    vi.mocked(api.searchBooks).mockResolvedValue(null as unknown as never)

    render(<AddBookModal onClose={onClose} onAdded={onAdded} />)

    fireEvent.change(screen.getByPlaceholderText(/Title, ISBN, or ASIN/i), {
      target: { value: 'qzznomatch' },
    })
    fireEvent.click(screen.getByRole('button', { name: /^search$/i }))

    await waitFor(() =>
      expect(screen.getByText(/no results found/i)).toBeInTheDocument()
    )
  })
})

describe('AddBookModal — ASIN lookup (#1189)', () => {
  const onClose = vi.fn()
  const onAdded = vi.fn()

  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('routes an ASIN-shaped query to the ASIN lookup and renders the result', async () => {
    vi.mocked(api.lookupASIN).mockResolvedValue({
      foreignBookId: 'OL-IRON',
      title: 'Iron Flame',
      asin: 'B0DBJBFHGT',
      mediaType: 'audiobook',
      author: { authorName: 'Rebecca Yarros' },
    } as never)

    render(<AddBookModal onClose={onClose} onAdded={onAdded} />)

    fireEvent.change(screen.getByPlaceholderText(/Title, ISBN, or ASIN/i), {
      target: { value: 'b0dbjbfhgt' },
    })
    fireEvent.click(screen.getByRole('button', { name: /^search$/i }))

    await waitFor(() =>
      expect(screen.getByText('Iron Flame')).toBeInTheDocument()
    )
    // ASIN lookup is called with the upper-cased token; not the title search.
    expect(api.lookupASIN).toHaveBeenCalledWith('B0DBJBFHGT')
    expect(api.searchBooks).not.toHaveBeenCalled()
    // The result can be selected for confirmation.
    expect(screen.getByRole('button', { name: 'Select Iron Flame' })).toBeEnabled()
  })

  it('shows the normal empty state when the ASIN does not resolve', async () => {
    vi.mocked(api.lookupASIN).mockRejectedValue(new Error('not found'))

    render(<AddBookModal onClose={onClose} onAdded={onAdded} />)

    fireEvent.change(screen.getByPlaceholderText(/Title, ISBN, or ASIN/i), {
      target: { value: 'B0NONEXIST' },
    })
    fireEvent.click(screen.getByRole('button', { name: /^search$/i }))

    await waitFor(() =>
      expect(screen.getByText(/not found/i)).toBeInTheDocument()
    )
    expect(api.searchBooks).not.toHaveBeenCalled()
  })

  it('still routes a plain title query to searchBooks', async () => {
    vi.mocked(api.searchBooks).mockResolvedValue([
      { foreignBookId: 'OL-DUNE', title: 'Dune', author: { authorName: 'Frank Herbert' } },
    ] as never)

    render(<AddBookModal onClose={onClose} onAdded={onAdded} />)

    fireEvent.change(screen.getByPlaceholderText(/Title, ISBN, or ASIN/i), {
      target: { value: 'Dune' },
    })
    fireEvent.click(screen.getByRole('button', { name: /^search$/i }))

    await waitFor(() =>
      expect(screen.getByText('Dune')).toBeInTheDocument()
    )
    expect(api.searchBooks).toHaveBeenCalledWith('Dune')
    expect(api.lookupASIN).not.toHaveBeenCalled()
    expect(api.lookupISBN).not.toHaveBeenCalled()
  })
})

describe('AddBookModal — authorless ISBN result (#2187)', () => {
  const onClose = vi.fn()
  const onAdded = vi.fn()

  beforeEach(() => {
    vi.clearAllMocks()
  })

  const lookupAndFind = async () => {
    fireEvent.change(screen.getByPlaceholderText(/Title, ISBN, or ASIN/i), {
      target: { value: '9780441013593' },
    })
    fireEvent.click(screen.getByRole('button', { name: /^search$/i }))
    await waitFor(() => expect(screen.getByText('Standalone Edition')).toBeInTheDocument())
    fireEvent.click(screen.getByRole('button', { name: 'Select Standalone Edition' }))
  }

  it('lets a result with no author be added, and sends empty author fields', async () => {
    // An OpenLibrary edition with no /works/ link and no resolvable author.
    // The backend only requires foreignBookId — it resolves the author from
    // the book id — so the modal must not refuse to send the request.
    vi.mocked(api.lookupISBN).mockResolvedValue({
      foreignBookId: 'OL999M',
      title: 'Standalone Edition',
    } as never)
    vi.mocked(api.addBook).mockResolvedValue({ id: 7, title: 'Standalone Edition' } as never)

    render(<AddBookModal onClose={onClose} onAdded={onAdded} />)
    await lookupAndFind()

    const addButton = screen.getByRole('button', { name: /^add book$/i })
    expect(addButton).toBeEnabled()
    expect(screen.getByText('No cover')).toBeInTheDocument()

    fireEvent.click(addButton)
    await waitFor(() => expect(api.addBook).toHaveBeenCalled())
    expect(vi.mocked(api.addBook).mock.calls[0][0]).toMatchObject({
      foreignBookId: 'OL999M',
      foreignAuthorId: '',
      authorName: '',
      searchOnAdd: true,
    })
    expect(onAdded).toHaveBeenCalledTimes(1)
  })

  it('still sends the author name when the result carries one but no author id (DNB)', async () => {
    vi.mocked(api.lookupISBN).mockResolvedValue({
      foreignBookId: 'DNB-123',
      title: 'Standalone Edition',
      author: { authorName: 'Frank Herbert' },
    } as never)
    vi.mocked(api.addBook).mockResolvedValue({ id: 8, title: 'Standalone Edition' } as never)

    render(<AddBookModal onClose={onClose} onAdded={onAdded} />)
    await lookupAndFind()

    fireEvent.click(screen.getByRole('button', { name: /^add book$/i }))
    await waitFor(() => expect(api.addBook).toHaveBeenCalled())
    expect(vi.mocked(api.addBook).mock.calls[0][0]).toMatchObject({
      foreignBookId: 'DNB-123',
      foreignAuthorId: '',
      authorName: 'Frank Herbert',
    })
  })
})

describe('AddBookModal — confirmation step (#1227)', () => {
  const onClose = vi.fn()
  const onAdded = vi.fn()

  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(api.searchBooks).mockResolvedValue([{
      foreignBookId: 'OL1W',
      metadataProvider: 'openlibrary',
      title: 'Dune',
      imageUrl: 'https://example.com/dune.jpg',
      releaseDate: '1965-08-01T00:00:00Z',
      language: 'eng',
      mediaType: 'ebook',
      isbns: ['9780441172719', '0441172717', '9780593099322', '9780143111580', '9780307387899'],
      author: { authorName: 'Frank Herbert' },
    }] as never)
  })

  it('moves configuration behind selection and restores the search state on Back', async () => {
    render(<AddBookModal onClose={onClose} onAdded={onAdded} />)

    expect(screen.queryByLabelText('Format to add')).not.toBeInTheDocument()
    expect(screen.queryByText('Search indexers after adding')).not.toBeInTheDocument()

    const input = screen.getByPlaceholderText(/Title, ISBN, or ASIN/i)
    fireEvent.change(input, { target: { value: 'Dune' } })
    fireEvent.click(screen.getByRole('button', { name: /^search$/i }))
    await screen.findByText('Dune')
    fireEvent.click(screen.getByRole('button', { name: 'Select Dune' }))

    const cover = screen.getByRole('img', { name: 'Dune cover' })
    expect(cover.className).toContain('w-28')
    expect(screen.getByText('9780441172719')).toBeInTheDocument()
    expect(screen.getByText('Show 2 more identifiers')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Links' })).toBeInTheDocument()
    const sourceLink = screen.getByRole('link', { name: 'View on OpenLibrary ↗' })
    expect(sourceLink).toHaveAttribute('href', 'https://openlibrary.org/works/OL1W')
    expect(sourceLink).toHaveAttribute('target', '_blank')
    expect(sourceLink).toHaveAttribute('rel', 'noopener noreferrer')
    expect(screen.getByLabelText('Format to add')).toBeInTheDocument()
    expect(screen.getByText('Search indexers after adding')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Back to results' }))

    expect(screen.getByPlaceholderText(/Title, ISBN, or ASIN/i)).toHaveValue('Dune')
    expect(screen.getByRole('button', { name: 'Select Dune' })).toBeInTheDocument()
    expect(screen.queryByLabelText('Format to add')).not.toBeInTheDocument()
  })

  it('hides Links when the result has no trustworthy upstream URL', async () => {
    vi.mocked(api.searchBooks).mockResolvedValue([{
      foreignBookId: 'abs:local-item',
      metadataProvider: 'audiobookshelf',
      title: 'Local audiobook',
    }] as never)

    render(<AddBookModal onClose={onClose} onAdded={onAdded} />)
    fireEvent.change(screen.getByPlaceholderText(/Title, ISBN, or ASIN/i), {
      target: { value: 'Local audiobook' },
    })
    fireEvent.click(screen.getByRole('button', { name: /^search$/i }))
    await screen.findByText('Local audiobook')
    fireEvent.click(screen.getByRole('button', { name: 'Select Local audiobook' }))

    expect(screen.queryByRole('button', { name: 'Links' })).not.toBeInTheDocument()
  })
})

describe('AddBookModal — media-type selector (#1397)', () => {
  const onClose = vi.fn()
  const onAdded = vi.fn()

  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(api.searchBooks).mockResolvedValue([
      { foreignBookId: 'OL-DUNE', title: 'Dune', author: { authorName: 'Frank Herbert', foreignAuthorId: 'OL-FH' } },
    ] as never)
    vi.mocked(api.addBook).mockResolvedValue({ id: 1, title: 'Dune' } as never)
  })

  const searchAndSelect = async () => {
    fireEvent.change(screen.getByPlaceholderText(/Title, ISBN, or ASIN/i), {
      target: { value: 'Dune' },
    })
    fireEvent.click(screen.getByRole('button', { name: /^search$/i }))
    await waitFor(() => expect(screen.getByText('Dune')).toBeInTheDocument())
    fireEvent.click(screen.getByRole('button', { name: 'Select Dune' }))
  }

  it('omits mediaType when the selector is left on Default', async () => {
    render(<AddBookModal onClose={onClose} onAdded={onAdded} />)
    await searchAndSelect()
    fireEvent.click(screen.getByRole('button', { name: /^add book$/i }))
    await waitFor(() => expect(api.addBook).toHaveBeenCalled())
    expect(vi.mocked(api.addBook).mock.calls[0][0]).not.toHaveProperty('mediaType')
  })

  it('sends the chosen mediaType with the add request', async () => {
    render(<AddBookModal onClose={onClose} onAdded={onAdded} />)
    await searchAndSelect()
    fireEvent.change(screen.getByLabelText('Format to add'), { target: { value: 'audiobook' } })
    fireEvent.click(screen.getByRole('button', { name: /^add book$/i }))
    await waitFor(() => expect(api.addBook).toHaveBeenCalled())
    expect(vi.mocked(api.addBook).mock.calls[0][0]).toMatchObject({
      foreignBookId: 'OL-DUNE',
      mediaType: 'audiobook',
    })
  })

  it('sends the overridden auto-search choice with the add request', async () => {
    render(<AddBookModal onClose={onClose} onAdded={onAdded} />)
    await searchAndSelect()
    fireEvent.click(screen.getByRole('checkbox', { name: /search indexers after adding/i }))
    fireEvent.click(screen.getByRole('button', { name: /^add book$/i }))
    await waitFor(() => expect(api.addBook).toHaveBeenCalled())
    expect(vi.mocked(api.addBook).mock.calls[0][0]).toMatchObject({
      foreignBookId: 'OL-DUNE',
      searchOnAdd: false,
    })
  })

  it('shows add failures inline without closing the dialog', async () => {
    vi.mocked(api.addBook).mockRejectedValue(new Error('book already exists'))
    const alertSpy = vi.spyOn(window, 'alert').mockImplementation(() => {})
    render(<AddBookModal onClose={onClose} onAdded={onAdded} />)
    await searchAndSelect()

    fireEvent.click(screen.getByRole('button', { name: /^add book$/i }))

    expect(await screen.findByRole('alert')).toHaveTextContent('book already exists')
    expect(alertSpy).not.toHaveBeenCalled()
    expect(onClose).not.toHaveBeenCalled()
    alertSpy.mockRestore()
  })
})
