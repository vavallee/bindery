import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import BookDetailPage, { MapMetadataModal, SearchResultsSection } from './BookDetailPage'
import { api } from '../api/client'
import type { Book, SearchResult } from '../api/client'

vi.mock('../components/MediaBadge', () => ({
  default: ({ type }: { type?: string }) => <span data-testid={`badge-${type}`}>{type}</span>,
}))

vi.mock('../api/client', async importOriginal => {
  const actual = await importOriginal<typeof import('../api/client')>()
  return {
    ...actual,
    api: {
      ...actual.api,
      getBook: vi.fn(),
      listHistory: vi.fn(),
      updateBook: vi.fn(),
      searchBooks: vi.fn(),
      mapBookMetadata: vi.fn(),
    },
  }
})

function makeBook(overrides: Partial<Book> = {}): Book {
  return {
    id: 7,
    foreignBookId: 'fb-7',
    authorId: 42,
    title: 'Mistborn',
    description: '',
    imageUrl: '',
    releaseDate: undefined,
    genres: [],
    monitored: true,
    status: 'imported',
    filePath: '',
    mediaType: 'ebook',
    ebookFilePath: '',
    audiobookFilePath: '',
    excluded: false,
    ...overrides,
  }
}

function renderBookDetailPage() {
  return render(
    <MemoryRouter initialEntries={['/book/7']}>
      <Routes>
        <Route path="/book/:id" element={<BookDetailPage />} />
      </Routes>
    </MemoryRouter>,
  )
}

function makeResult({ guid, title, ...rest }: Partial<SearchResult> & { guid: string }): SearchResult {
  return {
    guid,
    indexerName: 'TestIndexer',
    title: title ?? guid,
    size: 1048576,
    nzbUrl: 'http://example.com/nzb',
    grabs: 0,
    pubDate: '2024-01-01',
    protocol: 'usenet',
    ...rest,
  }
}

const noop = () => {}

beforeEach(() => {
  vi.clearAllMocks()
})

describe('SearchResultsSection — dual-format book', () => {
  it('renders separate Ebooks and Audiobooks sections', () => {
    const results = [
      makeResult({ guid: 'eb1', title: 'Book epub', mediaType: 'ebook' }),
      makeResult({ guid: 'au1', title: 'Book mp3', mediaType: 'audiobook' }),
    ]
    render(
      <SearchResultsSection results={results} bookMediaType="both" grabbing={null} onGrab={noop} />,
    )
    expect(screen.getByText(/^Ebooks/)).toBeInTheDocument()
    expect(screen.getByText(/^Audiobooks/)).toBeInTheDocument()
    expect(screen.getByText('Book epub')).toBeInTheDocument()
    expect(screen.getByText('Book mp3')).toBeInTheDocument()
  })

  it('renders ebook badges for ebook results', () => {
    const results = [makeResult({ guid: 'eb1', title: 'Ebook title', mediaType: 'ebook' })]
    render(
      <SearchResultsSection results={results} bookMediaType="both" grabbing={null} onGrab={noop} />,
    )
    expect(screen.getByTestId('badge-ebook')).toBeInTheDocument()
  })

  it('renders audiobook badges for audiobook results', () => {
    const results = [makeResult({ guid: 'au1', title: 'Audio title', mediaType: 'audiobook' })]
    render(
      <SearchResultsSection results={results} bookMediaType="both" grabbing={null} onGrab={noop} />,
    )
    expect(screen.getByTestId('badge-audiobook')).toBeInTheDocument()
  })

  it('caps each section at 20 results', () => {
    const ebooks = Array.from({ length: 25 }, (_, i) =>
      makeResult({ guid: `eb${i}`, title: `Ebook ${i}`, mediaType: 'ebook' }),
    )
    const audiobooks = Array.from({ length: 25 }, (_, i) =>
      makeResult({ guid: `au${i}`, title: `Audio ${i}`, mediaType: 'audiobook' }),
    )
    const { container } = render(
      <SearchResultsSection results={[...ebooks, ...audiobooks]} bookMediaType="both" grabbing={null} onGrab={noop} />,
    )
    const grabBtns = container.querySelectorAll('button')
    expect(grabBtns.length).toBe(40) // 20 per section
  })

  it('omits a section when it has no results', () => {
    const results = [makeResult({ guid: 'eb1', title: 'Only ebook', mediaType: 'ebook' })]
    render(
      <SearchResultsSection results={results} bookMediaType="both" grabbing={null} onGrab={noop} />,
    )
    expect(screen.queryByText(/^Audiobooks/)).toBeNull()
  })
})

describe('MapMetadataModal', () => {
  it('maps a manually entered foreign ID', async () => {
    const source = makeBook({ id: 42, foreignBookId: 'OL-OLDW', title: 'Cien años de soledad' })
    const mapped = makeBook({ id: 42, foreignBookId: 'OL274505W', title: 'One Hundred Years of Solitude' })
    const onClose = vi.fn()
    const onMapped = vi.fn()
    vi.mocked(api.mapBookMetadata).mockResolvedValue(mapped)

    render(<MapMetadataModal book={source} onClose={onClose} onMapped={onMapped} />)

    fireEvent.change(screen.getByPlaceholderText('OpenLibrary or provider ID'), {
      target: { value: 'OL274505W' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Map ID' }))

    await waitFor(() => expect(api.mapBookMetadata).toHaveBeenCalledWith(42, 'OL274505W'))
    expect(onMapped).toHaveBeenCalledWith(mapped)
    expect(onClose).toHaveBeenCalled()
  })

  it('maps a metadata search result', async () => {
    const source = makeBook({ id: 42, title: 'Cien años de soledad' })
    const result = makeBook({
      id: 0,
      foreignBookId: 'OL274505W',
      title: 'One Hundred Years of Solitude',
      author: {
        id: 0,
        foreignAuthorId: 'OL45804A',
        authorName: 'Gabriel García Márquez',
        sortName: 'Márquez, Gabriel García',
        description: '',
        imageUrl: '',
        disambiguation: '',
        ratingsCount: 0,
        averageRating: 0,
        monitored: true,
      },
    })
    const mapped = makeBook({ id: 42, foreignBookId: 'OL274505W', title: 'One Hundred Years of Solitude' })
    const onClose = vi.fn()
    const onMapped = vi.fn()
    vi.mocked(api.searchBooks).mockResolvedValue([result])
    vi.mocked(api.mapBookMetadata).mockResolvedValue(mapped)

    render(<MapMetadataModal book={source} onClose={onClose} onMapped={onMapped} />)

    fireEvent.click(screen.getByRole('button', { name: 'Search' }))
    await waitFor(() => expect(screen.getByText('One Hundred Years of Solitude')).toBeInTheDocument())
    fireEvent.click(screen.getByRole('button', { name: 'Map' }))

    await waitFor(() => expect(api.mapBookMetadata).toHaveBeenCalledWith(42, 'OL274505W'))
    expect(onMapped).toHaveBeenCalledWith(mapped)
  })
})

describe('SearchResultsSection — single-format book', () => {
  it('renders a flat list without section labels', () => {
    const results = [
      makeResult({ guid: 'r1', title: 'Result 1' }),
      makeResult({ guid: 'r2', title: 'Result 2' }),
    ]
    render(
      <SearchResultsSection results={results} bookMediaType="ebook" grabbing={null} onGrab={noop} />,
    )
    expect(screen.getByText(/^Results/)).toBeInTheDocument()
    expect(screen.queryByText(/^Ebooks/)).toBeNull()
    expect(screen.queryByText(/^Audiobooks/)).toBeNull()
  })

  it('caps flat list at 20 results', () => {
    const results = Array.from({ length: 25 }, (_, i) =>
      makeResult({ guid: `r${i}`, title: `Result ${i}` }),
    )
    const { container } = render(
      <SearchResultsSection results={results} bookMediaType="ebook" grabbing={null} onGrab={noop} />,
    )
    expect(container.querySelectorAll('button').length).toBe(20)
  })
})

describe('BookDetailPage — media-type selector', () => {
  beforeEach(() => {
    vi.mocked(api.listHistory).mockResolvedValue([])
  })

  it('renders the selector with the current mediaType selected', async () => {
    vi.mocked(api.getBook).mockResolvedValue(makeBook({ mediaType: 'ebook' }))
    renderBookDetailPage()

    const select = (await screen.findByLabelText('Format:')) as HTMLSelectElement
    expect(select.value).toBe('ebook')
  })

  it('calls api.updateBook with the new mediaType on change', async () => {
    vi.mocked(api.getBook).mockResolvedValue(makeBook({ mediaType: 'ebook' }))
    vi.mocked(api.updateBook).mockResolvedValue(makeBook({ mediaType: 'both' }))
    renderBookDetailPage()

    const select = await screen.findByLabelText('Format:')
    fireEvent.change(select, { target: { value: 'both' } })

    await waitFor(() => expect(api.updateBook).toHaveBeenCalledWith(7, { mediaType: 'both' }))
  })

  it('updates local state on success so the dual-format panels appear', async () => {
    vi.mocked(api.getBook).mockResolvedValue(makeBook({ mediaType: 'ebook' }))
    vi.mocked(api.updateBook).mockResolvedValue(makeBook({ mediaType: 'both' }))
    renderBookDetailPage()

    const select = (await screen.findByLabelText('Format:')) as HTMLSelectElement
    expect(screen.queryByText(/✓ on disk|needed/)).toBeNull()

    fireEvent.change(select, { target: { value: 'both' } })

    await waitFor(() => expect((select as HTMLSelectElement).value).toBe('both'))
    // The dual-format panels (gated on mediaType === 'both') now render the
    // "📖 Ebook ... needed" / "🎧 Audiobook ... needed" status pills.
    const needed = await screen.findAllByText('needed')
    expect(needed.length).toBe(2)
  })
})
