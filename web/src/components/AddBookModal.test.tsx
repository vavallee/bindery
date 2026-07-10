import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import AddBookModal from './AddBookModal'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string) => ({
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
      'addBookModal.authorMissing': 'Author name missing on this result',
      'addBookModal.added': 'Added',
      'addBookModal.adding': 'Adding...',
      'addBookModal.addFailed': 'Failed to add book',
      'common.search': 'Search',
      'common.add': 'Add',
      'common.cancel': 'Cancel',
      'common.noResults': 'No results found',
      'common.ebook': 'Ebook',
      'common.audiobook': 'Audiobook',
      'common.both': 'Both',
    }[key] ?? key),
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
    // The result is addable.
    expect(screen.getByRole('button', { name: /^add$/i })).toBeEnabled()
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

  const searchAndFind = async () => {
    fireEvent.change(screen.getByPlaceholderText(/Title, ISBN, or ASIN/i), {
      target: { value: 'Dune' },
    })
    fireEvent.click(screen.getByRole('button', { name: /^search$/i }))
    await waitFor(() => expect(screen.getByText('Dune')).toBeInTheDocument())
  }

  it('omits mediaType when the selector is left on Default', async () => {
    render(<AddBookModal onClose={onClose} onAdded={onAdded} />)
    await searchAndFind()
    fireEvent.click(screen.getByRole('button', { name: /^add$/i }))
    await waitFor(() => expect(api.addBook).toHaveBeenCalled())
    expect(vi.mocked(api.addBook).mock.calls[0][0]).not.toHaveProperty('mediaType')
  })

  it('sends the chosen mediaType with the add request', async () => {
    render(<AddBookModal onClose={onClose} onAdded={onAdded} />)
    await searchAndFind()
    fireEvent.change(screen.getByLabelText('Format to add'), { target: { value: 'audiobook' } })
    fireEvent.click(screen.getByRole('button', { name: /^add$/i }))
    await waitFor(() => expect(api.addBook).toHaveBeenCalled())
    expect(vi.mocked(api.addBook).mock.calls[0][0]).toMatchObject({
      foreignBookId: 'OL-DUNE',
      mediaType: 'audiobook',
    })
  })

  it('shows add failures inline without closing the dialog', async () => {
    vi.mocked(api.addBook).mockRejectedValue(new Error('book already exists'))
    const alertSpy = vi.spyOn(window, 'alert').mockImplementation(() => {})
    render(<AddBookModal onClose={onClose} onAdded={onAdded} />)
    await searchAndFind()

    fireEvent.click(screen.getByRole('button', { name: /^add$/i }))

    expect(await screen.findByRole('alert')).toHaveTextContent('book already exists')
    expect(alertSpy).not.toHaveBeenCalled()
    expect(onClose).not.toHaveBeenCalled()
    alertSpy.mockRestore()
  })
})
