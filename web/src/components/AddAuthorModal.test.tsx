import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import AddAuthorModal from './AddAuthorModal'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, options?: Record<string, unknown>) => {
      const strings: Record<string, string> = {
        'addAuthorModal.searchPlaceholder': 'Search by author name...',
        'addAuthorModal.search': 'Search',
        'addAuthorModal.add': 'Add',
        'addAuthorModal.noResults': 'No results found',
      }
      if (key === 'addAuthorModal.searchError') {
        return `Could not reach the metadata provider - ${String(options?.error ?? '')}`
      }
      if (key === 'addAuthorModal.showHiddenResults') {
        const count = Number(options?.count ?? 0)
        return `Show ${count} hidden result${count === 1 ? '' : 's'}`
      }
      return strings[key] ?? key
    },
  }),
}))

// Mock the entire api/client module so no real HTTP calls are made.
vi.mock('../api/client', () => ({
  api: {
    listMetadataProfiles: vi.fn().mockResolvedValue([]),
    listRootFolders: vi.fn().mockResolvedValue([]),
    getSetting: vi.fn().mockResolvedValue({ key: 'default.media_type', value: 'ebook' }),
    searchAuthors: vi.fn(),
    searchBooks: vi.fn(),
    addAuthor: vi.fn(),
  },
}))

// Import after the mock is set up so we get the mocked version.
import { api } from '../api/client'
import type { Author, Book } from '../api/client'

function author(overrides: Partial<Author>): Author {
  return {
    id: 1,
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
    id: 1,
    foreignBookId: 'OL_BOOK_W',
    authorId: 1,
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

describe('AddAuthorModal — search error handling', () => {
  const onClose = vi.fn()
  const onAdded = vi.fn()

  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(api.listMetadataProfiles).mockResolvedValue([])
    vi.mocked(api.listRootFolders).mockResolvedValue([])
    vi.mocked(api.getSetting).mockResolvedValue({ key: 'default.media_type', value: 'ebook' })
    vi.mocked(api.searchBooks).mockResolvedValue([])
    vi.mocked(api.addAuthor).mockResolvedValue(author({}))
  })

  it('shows an error banner when the metadata provider is unreachable', async () => {
    vi.mocked(api.searchAuthors).mockRejectedValue(
      new Error('search authors: HTTP 503: Service Unavailable')
    )

    render(<AddAuthorModal onClose={onClose} onAdded={onAdded} />)

    fireEvent.change(screen.getByPlaceholderText('Search by author name...'), {
      target: { value: 'tolkien' },
    })
    fireEvent.click(screen.getByRole('button', { name: /^search$/i }))

    await waitFor(() =>
      expect(screen.getByText(/HTTP 503/i)).toBeInTheDocument()
    )
    // "No results found" should NOT appear alongside the error
    expect(screen.queryByText(/no results found/i)).not.toBeInTheDocument()
  })

  it('displays results when the search succeeds', async () => {
    vi.mocked(api.searchAuthors).mockResolvedValue([
      {
        id: 1,
        foreignAuthorId: 'OL26320A',
        authorName: 'J.R.R. Tolkien',
        sortName: 'Tolkien, J.R.R.',
        description: '',
        imageUrl: '',
        disambiguation: 'The Hobbit',
        ratingsCount: 5000,
        averageRating: 4.8,
        monitored: true,
        statistics: { bookCount: 12, availableBookCount: 0, wantedBookCount: 0 },
      },
    ])

    render(<AddAuthorModal onClose={onClose} onAdded={onAdded} />)

    fireEvent.change(screen.getByPlaceholderText('Search by author name...'), {
      target: { value: 'tolkien' },
    })
    fireEvent.click(screen.getByRole('button', { name: /^search$/i }))

    await waitFor(() =>
      expect(screen.getByText('J.R.R. Tolkien')).toBeInTheDocument()
    )
    expect(screen.queryByText(/no results found/i)).not.toBeInTheDocument()
    expect(screen.queryByText(/could not reach/i)).not.toBeInTheDocument()
  })

  it('clears the error banner when a subsequent search succeeds', async () => {
    vi.mocked(api.searchAuthors)
      .mockRejectedValueOnce(new Error('search authors: HTTP 503: Service Unavailable'))
      .mockResolvedValueOnce([
        {
          id: 1,
          foreignAuthorId: 'OL26320A',
          authorName: 'J.R.R. Tolkien',
          sortName: 'Tolkien, J.R.R.',
          description: '',
          imageUrl: '',
          disambiguation: '',
          ratingsCount: 0,
          averageRating: 0,
          monitored: true,
        },
      ])

    render(<AddAuthorModal onClose={onClose} onAdded={onAdded} />)

    const input = screen.getByPlaceholderText('Search by author name...')
    const btn = screen.getByRole('button', { name: /^search$/i })

    fireEvent.change(input, { target: { value: 'tolkien' } })
    fireEvent.click(btn)
    await waitFor(() => expect(screen.getByText(/HTTP 503/i)).toBeInTheDocument())

    fireEvent.click(btn)
    await waitFor(() =>
      expect(screen.queryByText(/HTTP 503/i)).not.toBeInTheDocument()
    )
    expect(screen.getByText('J.R.R. Tolkien')).toBeInTheDocument()
  })

  it('treats relink add responses as success', async () => {
    vi.mocked(api.searchAuthors).mockResolvedValue([
      {
        id: 0,
        foreignAuthorId: 'OL26320A',
        authorName: 'J.R.R. Tolkien',
        sortName: 'Tolkien, J.R.R.',
        description: '',
        imageUrl: '',
        disambiguation: 'The Hobbit',
        ratingsCount: 1619,
        averageRating: 4.6,
        monitored: true,
      },
    ])
    vi.mocked(api.addAuthor).mockResolvedValue({
      id: 37,
      foreignAuthorId: 'OL26320A',
      authorName: 'J.R.R. Tolkien',
      sortName: 'Tolkien, J.R.R.',
      description: 'Author of The Hobbit.',
      imageUrl: '',
      disambiguation: '',
      ratingsCount: 0,
      averageRating: 0,
      monitored: true,
    })

    render(<AddAuthorModal onClose={onClose} onAdded={onAdded} />)

    fireEvent.change(screen.getByPlaceholderText('Search by author name...'), {
      target: { value: 'tolkien' },
    })
    fireEvent.click(screen.getByRole('button', { name: /^search$/i }))
    await waitFor(() => expect(screen.getByText('J.R.R. Tolkien')).toBeInTheDocument())

    fireEvent.click(screen.getByRole('button', { name: /^add$/i }))

    await waitFor(() => expect(onAdded).toHaveBeenCalledTimes(1))
    expect(onClose).toHaveBeenCalledTimes(1)
    expect(api.addAuthor).toHaveBeenCalledWith(expect.objectContaining({
      foreignAuthorId: 'OL26320A',
      authorName: 'J.R.R. Tolkien',
    }))
  })

  it('loads global monitor defaults and sends them when adding an author', async () => {
    vi.mocked(api.getSetting).mockImplementation(async (key: string) => {
      if (key === 'default.media_type') return { key, value: 'ebook' }
      if (key === 'author.default_monitor_mode') return { key, value: 'latest' }
      if (key === 'author.default_monitor_latest_count') return { key, value: '5' }
      throw new Error('setting not found')
    })
    vi.mocked(api.searchAuthors).mockResolvedValue([
      {
        id: 0,
        foreignAuthorId: 'OL26320A',
        authorName: 'J.R.R. Tolkien',
        sortName: 'Tolkien, J.R.R.',
        description: '',
        imageUrl: '',
        disambiguation: '',
        ratingsCount: 0,
        averageRating: 0,
        monitored: true,
      },
    ])

    render(<AddAuthorModal onClose={onClose} onAdded={onAdded} />)

    await waitFor(() => {
      const selects = screen.getAllByRole('combobox') as HTMLSelectElement[]
      expect(selects[1].value).toBe('latest')
    })
    expect(screen.getByRole('spinbutton')).toHaveValue(5)

    fireEvent.change(screen.getByPlaceholderText('Search by author name...'), {
      target: { value: 'tolkien' },
    })
    fireEvent.click(screen.getByRole('button', { name: /^search$/i }))
    await waitFor(() => expect(screen.getByText('J.R.R. Tolkien')).toBeInTheDocument())

    fireEvent.click(screen.getByRole('button', { name: /^add$/i }))

    await waitFor(() => expect(api.addAuthor).toHaveBeenCalledTimes(1))
    expect(api.addAuthor).toHaveBeenCalledWith(expect.objectContaining({
      foreignAuthorId: 'OL26320A',
      authorName: 'J.R.R. Tolkien',
      monitorMode: 'latest',
      monitorLatestCount: 5,
    }))
  })

  it('hides likely book-title results by default and reveals them on request', async () => {
    const hiddenAuthor = author({
      foreignAuthorId: 'OL_BAD_TITLE_A',
      authorName: 'Romeo and Juliet',
      disambiguation: 'William Shakespeare',
    })
    vi.mocked(api.searchAuthors).mockResolvedValue([hiddenAuthor])
    vi.mocked(api.searchBooks).mockResolvedValue([
      book({
        title: 'Romeo and Juliet',
        author: author({
          foreignAuthorId: 'OL_SHAKESPEARE_A',
          authorName: 'William Shakespeare',
        }),
      }),
    ])

    render(<AddAuthorModal onClose={onClose} onAdded={onAdded} />)

    fireEvent.change(screen.getByPlaceholderText('Search by author name...'), {
      target: { value: 'Romeo and Juliet' },
    })
    fireEvent.click(screen.getByRole('button', { name: /^search$/i }))

    const revealButton = await screen.findByRole('button', { name: /show 1 hidden result/i })
    expect(screen.queryByText('Romeo and Juliet')).not.toBeInTheDocument()

    fireEvent.click(revealButton)
    expect(screen.getByText('Romeo and Juliet')).toBeInTheDocument()
  })

  it('allows revealed hidden results to be added', async () => {
    const hiddenAuthor = author({
      foreignAuthorId: 'OL_BAD_TITLE_A',
      authorName: 'Romeo and Juliet',
      disambiguation: 'William Shakespeare',
    })
    vi.mocked(api.searchAuthors).mockResolvedValue([hiddenAuthor])
    vi.mocked(api.searchBooks).mockResolvedValue([
      book({
        title: 'Romeo and Juliet',
        author: author({
          foreignAuthorId: 'OL_SHAKESPEARE_A',
          authorName: 'William Shakespeare',
        }),
      }),
    ])

    render(<AddAuthorModal onClose={onClose} onAdded={onAdded} />)

    fireEvent.change(screen.getByPlaceholderText('Search by author name...'), {
      target: { value: 'Romeo and Juliet' },
    })
    fireEvent.click(screen.getByRole('button', { name: /^search$/i }))
    fireEvent.click(await screen.findByRole('button', { name: /show 1 hidden result/i }))
    fireEvent.click(screen.getByRole('button', { name: /^add$/i }))

    await waitFor(() =>
      expect(api.addAuthor).toHaveBeenCalledWith(expect.objectContaining({
        foreignAuthorId: 'OL_BAD_TITLE_A',
        authorName: 'Romeo and Juliet',
      }))
    )
    expect(onAdded).toHaveBeenCalled()
    expect(onClose).toHaveBeenCalled()
  })

  it('keeps regular author results visible while hiding only the title-shaped result', async () => {
    const visibleAuthor = author({
      foreignAuthorId: 'OL_SHAKESPEARE_A',
      authorName: 'William Shakespeare',
      disambiguation: 'Romeo and Juliet',
    })
    const hiddenAuthor = author({
      foreignAuthorId: 'OL_BAD_TITLE_A',
      authorName: 'Romeo and Juliet',
      disambiguation: 'William Shakespeare',
    })
    vi.mocked(api.searchAuthors).mockResolvedValue([visibleAuthor, hiddenAuthor])
    vi.mocked(api.searchBooks).mockResolvedValue([
      book({
        title: 'Romeo and Juliet',
        author: visibleAuthor,
      }),
    ])

    render(<AddAuthorModal onClose={onClose} onAdded={onAdded} />)

    fireEvent.change(screen.getByPlaceholderText('Search by author name...'), {
      target: { value: 'Romeo and Juliet' },
    })
    fireEvent.click(screen.getByRole('button', { name: /^search$/i }))

    await waitFor(() => expect(screen.getByText('William Shakespeare')).toBeInTheDocument())
    expect(screen.queryByText('Romeo and Juliet')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: /show 1 hidden result/i })).toBeInTheDocument()
  })

  it('keeps author results visible when the book guard lookup fails', async () => {
    vi.mocked(api.searchAuthors).mockResolvedValue([
      author({
        foreignAuthorId: 'OL_BAD_TITLE_A',
        authorName: 'Romeo and Juliet',
        disambiguation: 'William Shakespeare',
      }),
    ])
    vi.mocked(api.searchBooks).mockRejectedValue(new Error('book search down'))

    render(<AddAuthorModal onClose={onClose} onAdded={onAdded} />)

    fireEvent.change(screen.getByPlaceholderText('Search by author name...'), {
      target: { value: 'Romeo and Juliet' },
    })
    fireEvent.click(screen.getByRole('button', { name: /^search$/i }))

    await waitFor(() => expect(screen.getByText('Romeo and Juliet')).toBeInTheDocument())
    expect(screen.queryByRole('button', { name: /hidden result/i })).not.toBeInTheDocument()
    expect(screen.queryByText(/could not reach/i)).not.toBeInTheDocument()
  })
})
