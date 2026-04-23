import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import BookDetailPage from '../BookDetailPage'

// ---------------------------------------------------------------------------
// Static mocks
// ---------------------------------------------------------------------------

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}))

vi.mock('../../api/client', () => ({
  api: {
    getBook: vi.fn(),
    listHistory: vi.fn(),
    searchBook: vi.fn(),
    listIndexers: vi.fn(),
    updateBook: vi.fn(),
    deleteBook: vi.fn(),
    deleteBookFile: vi.fn(),
    enrichAudiobook: vi.fn(),
    toggleExcluded: vi.fn(),
    grab: vi.fn(),
  },
}))

import { api } from '../../api/client'
import type { SearchResult } from '../../api/client'

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

const baseBook = {
  id: 42,
  foreignBookId: 'OL123W',
  authorId: 1,
  title: 'Test Book',
  description: '',
  imageUrl: '',
  genres: [],
  monitored: true,
  status: 'wanted',
  filePath: '',
  mediaType: 'ebook' as const,
  ebookFilePath: '',
  audiobookFilePath: '',
  excluded: false,
  author: { id: 1, foreignAuthorId: 'OL1A', authorName: 'Author Name', sortName: 'Name, Author', description: '', imageUrl: '', disambiguation: '', ratingsCount: 0, averageRating: 0, monitored: true },
}

const makeResult = (overrides: Partial<SearchResult>) => ({
  guid: 'guid-' + Math.random(),
  indexerName: 'TestIndexer',
  title: 'Test.Release.epub',
  size: 1048576,
  nzbUrl: 'https://example.com/nzb',
  grabs: 5,
  pubDate: '',
  protocol: 'usenet' as const,
  language: undefined,
  mediaType: 'ebook',
  ...overrides,
})

function renderPage() {
  return render(
    <MemoryRouter initialEntries={['/book/42']}>
      <Routes>
        <Route path="/book/:id" element={<BookDetailPage />} />
      </Routes>
    </MemoryRouter>
  )
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe('BookDetailPage — search results section', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(api.getBook).mockResolvedValue(baseBook)
    vi.mocked(api.listHistory).mockResolvedValue([])
    vi.mocked(api.listIndexers).mockResolvedValue([{ id: 1, name: 'TestIndexer', type: 'newznab', url: 'https://x', apiKey: 'key', categories: [7020], enabled: true }])
  })

  it('renders two labelled sections when results contain both ebook and audiobook items', async () => {
    vi.mocked(api.searchBook).mockResolvedValue([
      makeResult({ guid: 'e1', title: 'Book.epub', mediaType: 'ebook' }),
      makeResult({ guid: 'e2', title: 'Book.2.epub', mediaType: 'ebook' }),
      makeResult({ guid: 'a1', title: 'Book.m4b', mediaType: 'audiobook' }),
      makeResult({ guid: 'a2', title: 'Book.mp3', mediaType: 'audiobook' }),
    ])

    renderPage()

    // Wait for the h2 book title heading to load (the page renders the title twice — once in
    // an image placeholder div and once in the h2; use the heading role to disambiguate)
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Test Book' })).toBeInTheDocument())

    // Click the search button (label varies by media type — match on the word "Search")
    fireEvent.click(screen.getByRole('button', { name: /search.*indexers/i }))

    await waitFor(() =>
      expect(screen.getByText(/📖 Ebook results/)).toBeInTheDocument()
    )
    expect(screen.getByText(/🎧 Audiobook results/)).toBeInTheDocument()

    // Count markers in each heading
    expect(screen.getByText(/📖 Ebook results \(2\)/)).toBeInTheDocument()
    expect(screen.getByText(/🎧 Audiobook results \(2\)/)).toBeInTheDocument()

    // All four titles should be rendered
    expect(screen.getByText('Book.epub')).toBeInTheDocument()
    expect(screen.getByText('Book.2.epub')).toBeInTheDocument()
    expect(screen.getByText('Book.m4b')).toBeInTheDocument()
    expect(screen.getByText('Book.mp3')).toBeInTheDocument()
  })

  it('renders a single results section when all results are ebook type', async () => {
    vi.mocked(api.searchBook).mockResolvedValue([
      makeResult({ guid: 'e1', title: 'Book.epub', mediaType: 'ebook' }),
      makeResult({ guid: 'e2', title: 'Book.mobi', mediaType: 'ebook' }),
    ])

    renderPage()

    await waitFor(() => expect(screen.getByRole('heading', { name: 'Test Book' })).toBeInTheDocument())
    fireEvent.click(screen.getByRole('button', { name: /search.*indexers/i }))

    await waitFor(() => expect(screen.getByText('Book.epub')).toBeInTheDocument())

    // Single unified section, no split headings
    expect(screen.queryByText(/📖 Ebook results/)).not.toBeInTheDocument()
    expect(screen.queryByText(/🎧 Audiobook results/)).not.toBeInTheDocument()
    expect(screen.getByText(/Results \(2\)/)).toBeInTheDocument()
  })

  it('renders a single results section when all results are audiobook type', async () => {
    vi.mocked(api.searchBook).mockResolvedValue([
      makeResult({ guid: 'a1', title: 'Book.m4b', mediaType: 'audiobook' }),
    ])

    renderPage()

    await waitFor(() => expect(screen.getByRole('heading', { name: 'Test Book' })).toBeInTheDocument())
    fireEvent.click(screen.getByRole('button', { name: /search.*indexers/i }))

    await waitFor(() => expect(screen.getByText('Book.m4b')).toBeInTheDocument())

    expect(screen.queryByText(/📖 Ebook results/)).not.toBeInTheDocument()
    expect(screen.queryByText(/🎧 Audiobook results/)).not.toBeInTheDocument()
    expect(screen.getByText(/Results \(1\)/)).toBeInTheDocument()
  })
})
