import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import BookDetailPage, { SearchResultsSection } from './BookDetailPage'
import { api } from '../api/client'
import type { Author, Book, Download, HistoryEvent, Indexer, SearchResult } from '../api/client'

vi.mock('../api/client', async importOriginal => {
  const actual = await importOriginal<typeof import('../api/client')>()
  return {
    ...actual,
    api: {
      ...actual.api,
      getBook: vi.fn(),
      listHistory: vi.fn(),
      searchBook: vi.fn(),
      listIndexers: vi.fn(),
      grab: vi.fn(),
      updateBook: vi.fn(),
    },
  }
})

vi.mock('../components/MediaBadge', () => ({
  default: ({ type }: { type?: string }) => <span data-testid={`badge-${type}`}>{type}</span>,
}))

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

const author: Author = {
  id: 7,
  foreignAuthorId: 'author-7',
  authorName: 'Brandon Sanderson',
  sortName: 'Sanderson, Brandon',
  description: '',
  imageUrl: '',
  disambiguation: '',
  ratingsCount: 0,
  averageRating: 0,
  monitored: true,
}

function makeBook(overrides: Partial<Book> = {}): Book {
  return {
    id: 42,
    foreignBookId: 'book-42',
    authorId: author.id,
    title: 'The Final Empire',
    description: 'A skaa thief joins a rebellion.',
    imageUrl: '',
    releaseDate: '2006-07-17T00:00:00Z',
    genres: [],
    monitored: true,
    status: 'wanted',
    filePath: '',
    mediaType: 'ebook',
    ebookFilePath: '',
    audiobookFilePath: '',
    excluded: false,
    language: 'en',
    author,
    ...overrides,
  }
}

function makeHistory(overrides: Partial<HistoryEvent> = {}): HistoryEvent {
  return {
    id: 99,
    bookId: 42,
    eventType: 'grabbed',
    sourceTitle: 'The Final Empire release',
    data: '{}',
    createdAt: '2026-05-01T12:00:00Z',
    ...overrides,
  }
}

function makeIndexer(overrides: Partial<Indexer> = {}): Indexer {
  return {
    id: 1,
    name: 'Indexer One',
    type: 'newznab',
    url: 'https://indexer.example.com',
    apiKey: 'test-key',
    categories: [7020],
    enabled: true,
    ...overrides,
  }
}

function makeDownload(overrides: Partial<Download> = {}): Download {
  return {
    id: 5,
    guid: 'download-guid',
    title: 'Downloaded release',
    status: 'queued',
    size: 1048576,
    protocol: 'usenet',
    errorMessage: '',
    ...overrides,
  }
}

function renderBookDetailPage() {
  return render(
    <MemoryRouter initialEntries={['/book/42']}>
      <Routes>
        <Route path="/book/:id" element={<BookDetailPage />} />
        <Route path="/settings" element={<div>Settings Page</div>} />
      </Routes>
    </MemoryRouter>,
  )
}

const noop = () => {}

beforeEach(() => {
  vi.clearAllMocks()
  document.title = 'Bindery'
  vi.mocked(api.getBook).mockResolvedValue(makeBook())
  vi.mocked(api.listHistory).mockResolvedValue([])
  vi.mocked(api.searchBook).mockResolvedValue({ results: [], debug: null })
  vi.mocked(api.listIndexers).mockResolvedValue([])
  vi.mocked(api.grab).mockResolvedValue(makeDownload())
  vi.mocked(api.updateBook).mockImplementation(async (_id, patch) => makeBook(patch))
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

describe('BookDetailPage', () => {
  it('loads book details and history', async () => {
    vi.mocked(api.listHistory).mockResolvedValue([makeHistory()])

    renderBookDetailPage()

    expect(await screen.findByRole('heading', { name: 'The Final Empire' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Brandon Sanderson' })).toBeInTheDocument()
    expect(screen.getByText('A skaa thief joins a rebellion.')).toBeInTheDocument()
    expect(screen.getByText('The Final Empire release')).toBeInTheDocument()
    expect(api.getBook).toHaveBeenCalledWith(42)
    expect(api.listHistory).toHaveBeenCalledWith({ bookId: 42 })
  })

  it('searches and renders grouped result metadata', async () => {
    vi.mocked(api.getBook).mockResolvedValue(makeBook({ mediaType: 'both' }))
    vi.mocked(api.listIndexers).mockResolvedValue([makeIndexer()])
    vi.mocked(api.searchBook).mockResolvedValue({
      results: [
        makeResult({
          guid: 'ebook-guid',
          title: 'The Final Empire EPUB',
          indexerName: 'Ebook Indexer',
          size: 1048576,
          grabs: 2,
          mediaType: 'ebook',
          rejection: 'Wrong language',
          approved: false,
        }),
        makeResult({
          guid: 'audio-guid',
          title: 'The Final Empire MP3',
          indexerName: 'Audio Indexer',
          size: 2147483648,
          grabs: 8,
          mediaType: 'audiobook',
        }),
      ],
      debug: null,
    })

    renderBookDetailPage()

    fireEvent.click(await screen.findByRole('button', { name: 'Search ebook + audiobook indexers' }))

    await waitFor(() => expect(api.searchBook).toHaveBeenCalledWith(42))
    expect(screen.getByText('Ebooks (1)')).toBeInTheDocument()
    expect(screen.getByText('Audiobooks (1)')).toBeInTheDocument()
    expect(screen.getByText('The Final Empire EPUB')).toBeInTheDocument()
    expect(screen.getByText('The Final Empire MP3')).toBeInTheDocument()
    expect(screen.getByText(/Ebook Indexer/)).toBeInTheDocument()
    expect(screen.getByText(/1 MB/)).toBeInTheDocument()
    expect(screen.getByText(/2 grabs/)).toBeInTheDocument()
    expect(screen.getByText(/Wrong language/)).toBeInTheDocument()
    expect(screen.getByText(/Audio Indexer/)).toBeInTheDocument()
    expect(screen.getByText(/2.0 GB/)).toBeInTheDocument()
    expect(screen.getByText(/8 grabs/)).toBeInTheDocument()
    expect(api.listIndexers).toHaveBeenCalled()
  })

  it('shows an empty search state when no indexers are configured', async () => {
    vi.mocked(api.listIndexers).mockResolvedValue([])
    vi.mocked(api.searchBook).mockResolvedValue({ results: [], debug: null })

    renderBookDetailPage()

    fireEvent.click(await screen.findByRole('button', { name: 'Search ebook indexers' }))

    expect(await screen.findByText(/No indexers configured/)).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Settings' })).toHaveAttribute('href', '/settings')
  })

  it('shows an empty search state when configured indexers return no results', async () => {
    vi.mocked(api.listIndexers).mockResolvedValue([makeIndexer()])
    vi.mocked(api.searchBook).mockResolvedValue({ results: [], debug: null })

    renderBookDetailPage()

    fireEvent.click(await screen.findByRole('button', { name: 'Search ebook indexers' }))

    expect(await screen.findByText(/No results on any indexer/)).toBeInTheDocument()
  })

  it('grabs a result, refreshes book and history, and clears results', async () => {
    let resolveGrab: (download: Download) => void = () => {}
    vi.mocked(api.getBook)
      .mockResolvedValueOnce(makeBook())
      .mockResolvedValueOnce(makeBook({ status: 'downloading' }))
    vi.mocked(api.listHistory)
      .mockResolvedValueOnce([])
      .mockResolvedValueOnce([makeHistory({ sourceTitle: 'Grab refreshed history' })])
    vi.mocked(api.listIndexers).mockResolvedValue([makeIndexer()])
    vi.mocked(api.searchBook).mockResolvedValue({
      results: [
        makeResult({
          guid: 'grab-guid',
          title: 'Grab Me',
          nzbUrl: 'https://indexer.example.com/grab-guid.nzb',
          size: 2147483648,
          grabs: 4,
          protocol: 'torrent',
        }),
      ],
      debug: null,
    })
    vi.mocked(api.grab).mockImplementation(() => new Promise(resolve => { resolveGrab = resolve }))

    renderBookDetailPage()

    fireEvent.click(await screen.findByRole('button', { name: 'Search ebook indexers' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Grab' }))

    expect(screen.getByRole('button', { name: 'Grabbing…' })).toBeDisabled()
    expect(api.grab).toHaveBeenCalledWith({
      guid: 'grab-guid',
      title: 'Grab Me',
      nzbUrl: 'https://indexer.example.com/grab-guid.nzb',
      size: 2147483648,
      bookId: 42,
      protocol: 'torrent',
      mediaType: 'ebook',
    })

    resolveGrab(makeDownload({ guid: 'grab-guid', title: 'Grab Me', protocol: 'torrent' }))

    await waitFor(() => expect(api.getBook).toHaveBeenCalledTimes(2))
    expect(api.listHistory).toHaveBeenLastCalledWith({ bookId: 42 })
    expect(await screen.findByText('Grab refreshed history')).toBeInTheDocument()
    await waitFor(() => expect(screen.queryByText('Grab Me')).not.toBeInTheDocument())
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

    await waitFor(() => expect(api.updateBook).toHaveBeenCalledWith(42, { mediaType: 'both' }))
  })

  it('updates local state on success so the dual-format panels appear', async () => {
    vi.mocked(api.getBook).mockResolvedValue(makeBook({ mediaType: 'ebook' }))
    vi.mocked(api.updateBook).mockResolvedValue(makeBook({ mediaType: 'both' }))

    renderBookDetailPage()

    const select = (await screen.findByLabelText('Format:')) as HTMLSelectElement
    expect(screen.queryByText(/✓ on disk|needed/)).toBeNull()

    fireEvent.change(select, { target: { value: 'both' } })

    await waitFor(() => expect(select.value).toBe('both'))
    const needed = await screen.findAllByText('needed')
    expect(needed.length).toBe(2)
  })
})
