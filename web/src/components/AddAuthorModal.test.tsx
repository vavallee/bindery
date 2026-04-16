import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import AddAuthorModal from './AddAuthorModal'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, options?: Record<string, unknown>) => {
      const strings: Record<string, string> = {
        'addAuthorModal.searchPlaceholder': 'Search by author name...',
        'addAuthorModal.search': 'Search',
        'addAuthorModal.noResults': 'No results found',
      }
      if (key === 'addAuthorModal.searchError') {
        return `Could not reach the metadata provider - ${String(options?.error ?? '')}`
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
    searchAuthors: vi.fn(),
    addAuthor: vi.fn(),
  },
}))

// Import after the mock is set up so we get the mocked version.
import { api } from '../api/client'

describe('AddAuthorModal — search error handling', () => {
  const onClose = vi.fn()
  const onAdded = vi.fn()

  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(api.listMetadataProfiles).mockResolvedValue([])
    vi.mocked(api.listRootFolders).mockResolvedValue([])
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
})
