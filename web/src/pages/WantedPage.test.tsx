import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import WantedPage from './WantedPage'
import { api } from '../api/client'
import type { Author, Book, Download, SearchResult } from '../api/client'

vi.mock('../api/client', async importOriginal => {
  const actual = await importOriginal<typeof import('../api/client')>()
  return {
    ...actual,
    api: {
      ...actual.api,
      listWanted: vi.fn(),
      searchBook: vi.fn(),
      grab: vi.fn(),
      updateBook: vi.fn(),
      bulkActionWanted: vi.fn(),
    },
  }
})

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, options?: Record<string, unknown>) => {
      if (key === 'wanted.countLabel') return `${String(options?.filtered)} of ${String(options?.total)}`
      if (key === 'wanted.selectBook') return `Select ${String(options?.title)}`
      if (key === 'bulkActionBar.selected') return `${String(options?.count)} selected`

      const labels: Record<string, string> = {
        'wanted.title': 'Wanted',
        'wanted.searchPlaceholder': 'Search by title or author...',
        'wanted.empty': 'No wanted books. Add an author to start tracking.',
        'wanted.noMatch': 'No books match your search.',
        'wanted.showExcluded': 'Show excluded',
        'wanted.searching': 'Searching...',
        'wanted.noIndexerResults': 'No results found on any indexer.',
        'wanted.ebookNeeded': '📖 needed',
        'wanted.audiobookNeeded': '🎧 needed',
        'wanted.ebookDone': '📖 ✓',
        'wanted.audiobookDone': '🎧 ✓',
        'wanted.unmonitorHint': 'Stop monitoring this book',
        'wanted.excluded': 'Excluded',
        'wanted.grab': 'Grab',
        'wanted.grabbing': 'Grabbing…',
        'wanted.grabbed': '✓ Grabbed',
        'common.loading': 'Loading...',
        'common.search': 'Search',
        'common.unmonitor': 'Unmonitor',
        'common.blocklist': 'Blocklist',
        'common.selectAllPage': 'Select all on this page',
        'books.mediaEbook': '📖 Ebook',
        'books.mediaAudiobook': '🎧 Audiobook',
        'books.mediaBoth': '📖🎧 Both',
        'bulkActionBar.clear': 'Clear',
      }
      return labels[key] ?? key
    },
  }),
}))

vi.mock('../components/usePagination', () => ({
  usePagination: <T,>(items: T[]) => ({
    pageItems: items,
    paginationProps: {
      page: 1,
      totalPages: 1,
      pageSize: 50,
      totalItems: items.length,
      onPageChange: vi.fn(),
      onPageSizeChange: vi.fn(),
    },
    reset: vi.fn(),
  }),
}))

vi.mock('../components/Pagination', () => ({ default: () => null }))

const author: Author = {
  id: 20,
  foreignAuthorId: 'author-20',
  authorName: 'Frank Herbert',
  sortName: 'Herbert, Frank',
  description: '',
  imageUrl: '',
  disambiguation: '',
  ratingsCount: 0,
  averageRating: 0,
  monitored: true,
}

function makeBook(overrides: Partial<Book> & Pick<Book, 'id' | 'title'>): Book {
  const { id, title, ...rest } = overrides
  return {
    id,
    foreignBookId: `book-${id}`,
    authorId: author.id,
    title,
    description: '',
    imageUrl: '',
    releaseDate: undefined,
    genres: [],
    monitored: true,
    status: 'wanted',
    filePath: '',
    mediaType: 'ebook',
    ebookFilePath: '',
    audiobookFilePath: '',
    excluded: false,
    author,
    ...rest,
  }
}

function makeResult({ guid, title, ...rest }: Partial<SearchResult> & { guid: string; title: string }): SearchResult {
  return {
    guid,
    title,
    indexerName: 'Wanted Indexer',
    size: 1572864,
    nzbUrl: 'https://indexer.example.com/release.nzb',
    grabs: 12,
    pubDate: '2026-05-01',
    protocol: 'usenet',
    ...rest,
  }
}

function makeDownload(overrides: Partial<Download> = {}): Download {
  return {
    id: 44,
    guid: 'download-guid',
    title: 'Queued release',
    status: 'queued',
    size: 1572864,
    protocol: 'usenet',
    errorMessage: '',
    ...overrides,
  }
}

function renderWantedPage() {
  return render(
    <MemoryRouter>
      <WantedPage />
    </MemoryRouter>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  document.title = 'Bindery'
  vi.mocked(api.listWanted).mockResolvedValue([])
  vi.mocked(api.searchBook).mockResolvedValue({ results: [], debug: null })
  vi.mocked(api.grab).mockResolvedValue(makeDownload())
  vi.mocked(api.updateBook).mockImplementation(async (id, patch) => makeBook({ id, title: 'Updated Book', ...patch }))
  vi.mocked(api.bulkActionWanted).mockResolvedValue({ results: {} })
})

afterEach(() => {
  vi.useRealTimers()
})

describe('WantedPage', () => {
  it('renders the empty wanted list state', async () => {
    renderWantedPage()

    expect(await screen.findByText('No wanted books. Add an author to start tracking.')).toBeInTheDocument()
    expect(screen.getByText('0 of 0')).toBeInTheDocument()
    expect(api.listWanted).toHaveBeenCalledWith({ includeExcluded: false })
  })

  it('searches a wanted book and renders result metadata', async () => {
    const book = makeBook({ id: 1, title: 'Dune' })
    vi.mocked(api.listWanted).mockResolvedValue([book])
    vi.mocked(api.searchBook).mockResolvedValue({
      results: [
        makeResult({
          guid: 'dune-release',
          title: 'Dune 2026 EPUB',
          indexerName: 'Arrakis Indexer',
          language: 'en',
        }),
      ],
      debug: null,
    })

    renderWantedPage()

    await screen.findByRole('link', { name: 'Dune' })
    fireEvent.click(screen.getByRole('button', { name: 'Search' }))

    await waitFor(() => expect(api.searchBook).toHaveBeenCalledWith(1))
    expect(await screen.findByText('Dune 2026 EPUB')).toBeInTheDocument()
    expect(screen.getByText(/Arrakis Indexer/)).toBeInTheDocument()
    expect(screen.getByText(/1.5 MB/)).toBeInTheDocument()
    expect(screen.getByText(/12 grabs/)).toBeInTheDocument()
    expect(screen.getByText('en')).toBeInTheDocument()
  })

  it('renders the no-results message after an empty search', async () => {
    vi.mocked(api.listWanted).mockResolvedValue([makeBook({ id: 1, title: 'Dune' })])
    vi.mocked(api.searchBook).mockResolvedValue({ results: [], debug: null })

    renderWantedPage()

    await screen.findByRole('link', { name: 'Dune' })
    fireEvent.click(screen.getByRole('button', { name: 'Search' }))

    expect(await screen.findByText('No results found on any indexer.')).toBeInTheDocument()
  })

  it('grabs a result, shows transient states, and refreshes wanted books', async () => {
    const book = makeBook({ id: 1, title: 'Dune' })
    let resolveGrab: (download: Download) => void = () => {}
    vi.mocked(api.listWanted)
      .mockResolvedValueOnce([book])
      .mockResolvedValueOnce([])
    vi.mocked(api.searchBook).mockResolvedValue({
      results: [
        makeResult({
          guid: 'dune-release',
          title: 'Dune 2026 EPUB',
          nzbUrl: 'https://indexer.example.com/dune.nzb',
        }),
      ],
      debug: null,
    })
    vi.mocked(api.grab).mockImplementation(() => new Promise(resolve => { resolveGrab = resolve }))

    renderWantedPage()

    await screen.findByRole('link', { name: 'Dune' })
    fireEvent.click(screen.getByRole('button', { name: 'Search' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Grab' }))

    expect(screen.getByRole('button', { name: 'Grabbing…' })).toBeDisabled()
    expect(api.grab).toHaveBeenCalledWith({
      guid: 'dune-release',
      title: 'Dune 2026 EPUB',
      nzbUrl: 'https://indexer.example.com/dune.nzb',
      size: 1572864,
      bookId: 1,
      protocol: 'usenet',
      mediaType: 'ebook',
    })

    vi.useFakeTimers()
    await act(async () => {
      resolveGrab(makeDownload({ guid: 'dune-release', title: 'Dune 2026 EPUB' }))
    })

    expect(screen.getByRole('button', { name: '✓ Grabbed' })).toBeDisabled()

    await act(async () => {
      vi.advanceTimersByTime(1200)
      await Promise.resolve()
    })

    expect(api.listWanted).toHaveBeenCalledTimes(2)
    expect(screen.queryByText('Dune 2026 EPUB')).not.toBeInTheDocument()
    expect(screen.getByText('No wanted books. Add an author to start tracking.')).toBeInTheDocument()
  })

  it('unmonitors a single wanted book and removes it from the list', async () => {
    const book = makeBook({ id: 1, title: 'Dune' })
    let resolveUpdate: (book: Book) => void = () => {}
    vi.mocked(api.listWanted).mockResolvedValue([book])
    vi.mocked(api.updateBook).mockImplementation(() => new Promise(resolve => { resolveUpdate = resolve }))

    renderWantedPage()

    await screen.findByRole('link', { name: 'Dune' })
    fireEvent.click(screen.getByRole('button', { name: 'Unmonitor' }))

    expect(screen.getByRole('button', { name: '…' })).toBeDisabled()
    expect(api.updateBook).toHaveBeenCalledWith(1, { monitored: false })

    await act(async () => {
      resolveUpdate({ ...book, monitored: false })
    })

    await waitFor(() => expect(screen.queryByRole('link', { name: 'Dune' })).not.toBeInTheDocument())
    expect(screen.getByText('No wanted books. Add an author to start tracking.')).toBeInTheDocument()
  })

  it.each([
    ['Search', 'search'],
    ['Unmonitor', 'unmonitor'],
    ['Blocklist', 'blocklist'],
  ] as const)('runs the bulk %s action and refreshes the list', async (label, action) => {
    vi.mocked(api.listWanted).mockResolvedValue([
      makeBook({ id: 1, title: 'Dune' }),
      makeBook({ id: 2, title: 'Hyperion' }),
    ])

    renderWantedPage()

    await screen.findByRole('link', { name: 'Dune' })
    fireEvent.click(screen.getByTitle('Select Dune'))
    fireEvent.click(screen.getByTitle('Select Hyperion'))

    const bulkBar = screen.getByText('2 selected').closest('div')
    if (!bulkBar) throw new Error('Bulk action bar was not rendered')
    fireEvent.click(within(bulkBar).getByRole('button', { name: label }))

    await waitFor(() => expect(api.bulkActionWanted).toHaveBeenCalledWith([1, 2], action))
    await waitFor(() => expect(api.listWanted).toHaveBeenCalledTimes(2))
    expect(screen.queryByText('2 selected')).not.toBeInTheDocument()
  })
})
