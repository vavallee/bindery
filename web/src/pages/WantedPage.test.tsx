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
      if (key === 'wanted.changeFormat') return `Change format for ${String(options?.title)}`
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
        'wanted.colTitleAuthor': 'Title & author',
        'wanted.colFormat': 'Format',
        'wanted.colActions': 'Actions',
        'wanted.noCover': 'No cover',
        'wanted.authorUnknown': 'Author unknown',
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
    addedAt: '2026-05-01T12:00:00Z',
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

  it('renders a row with the book title and its author', async () => {
    const tolkien: Author = { ...author, id: 99, authorName: 'J.R.R. Tolkien' }
    vi.mocked(api.listWanted).mockResolvedValue([
      makeBook({ id: 1, title: 'The Hobbit', authorId: 99, author: tolkien, releaseDate: '1937-09-21' }),
    ])

    renderWantedPage()

    expect(await screen.findByRole('link', { name: 'The Hobbit' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'J.R.R. Tolkien' })).toBeInTheDocument()
    // author line also carries the release year
    expect(screen.getByText(/1937/)).toBeInTheDocument()
  })

  it('renders gracefully and shows a fallback when a book has no author', async () => {
    vi.mocked(api.listWanted).mockResolvedValue([
      makeBook({ id: 1, title: 'Orphan Book', author: undefined }),
    ])

    renderWantedPage()

    expect(await screen.findByRole('link', { name: 'Orphan Book' })).toBeInTheDocument()
    expect(screen.getByText('Author unknown')).toBeInTheDocument()
  })

  it('renders the column header row with a select-all checkbox', async () => {
    vi.mocked(api.listWanted).mockResolvedValue([makeBook({ id: 1, title: 'Dune' })])

    renderWantedPage()

    await screen.findByRole('link', { name: 'Dune' })
    expect(screen.getByText('Title & author')).toBeInTheDocument()
    expect(screen.getByText('Format')).toBeInTheDocument()
    expect(screen.getByText('Actions')).toBeInTheDocument()
    expect(screen.getByRole('checkbox', { name: 'Select all on this page' })).toBeInTheDocument()
  })

  it('filters the list with the search box', async () => {
    vi.mocked(api.listWanted).mockResolvedValue([
      makeBook({ id: 1, title: 'Dune' }),
      makeBook({ id: 2, title: 'Hyperion' }),
    ])

    renderWantedPage()

    await screen.findByRole('link', { name: 'Dune' })
    fireEvent.change(screen.getByPlaceholderText('Search by title or author...'), {
      target: { value: 'hyper' },
    })

    expect(screen.queryByRole('link', { name: 'Dune' })).not.toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Hyperion' })).toBeInTheDocument()
  })

  it('changes a book format with the per-row format control', async () => {
    vi.mocked(api.listWanted).mockResolvedValue([makeBook({ id: 1, title: 'Dune' })])

    renderWantedPage()

    await screen.findByRole('link', { name: 'Dune' })
    const formatSelect = screen.getByRole('combobox', { name: 'Change format for Dune' })
    expect(formatSelect).toHaveValue('ebook')
    fireEvent.change(formatSelect, { target: { value: 'audiobook' } })

    await waitFor(() => expect(api.updateBook).toHaveBeenCalledWith(1, { mediaType: 'audiobook' }))
  })

  it('selecting a row drives the bulk action bar and select-all toggles every row', async () => {
    vi.mocked(api.listWanted).mockResolvedValue([
      makeBook({ id: 1, title: 'Dune' }),
      makeBook({ id: 2, title: 'Hyperion' }),
    ])

    renderWantedPage()

    await screen.findByRole('link', { name: 'Dune' })
    expect(screen.queryByText('1 selected')).not.toBeInTheDocument()

    // per-row selection
    fireEvent.click(screen.getByRole('checkbox', { name: 'Select Dune' }))
    expect(screen.getByText('1 selected')).toBeInTheDocument()

    // select-all picks up every row
    fireEvent.click(screen.getByRole('checkbox', { name: 'Select all on this page' }))
    expect(screen.getByText('2 selected')).toBeInTheDocument()

    // select-all again clears
    fireEvent.click(screen.getByRole('checkbox', { name: 'Select all on this page' }))
    expect(screen.queryByText(/selected/)).not.toBeInTheDocument()
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

    expect(api.updateBook).toHaveBeenCalledWith(1, { monitored: false })
    expect(screen.queryByRole('link', { name: 'Dune' })).not.toBeInTheDocument()

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
    const initialBooks = [
      makeBook({ id: 1, title: 'Dune' }),
      makeBook({ id: 2, title: 'Hyperion' }),
    ]
    const refreshedBooks = action === 'search'
      ? [
          makeBook({ id: 1, title: 'Dune Refreshed' }),
          makeBook({ id: 2, title: 'Hyperion Refreshed' }),
        ]
      : []
    vi.mocked(api.listWanted)
      .mockResolvedValueOnce(initialBooks)
      .mockResolvedValueOnce(refreshedBooks)

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
    if (action === 'search') {
      expect(await screen.findByRole('link', { name: 'Dune Refreshed' })).toBeInTheDocument()
      expect(screen.getByRole('link', { name: 'Hyperion Refreshed' })).toBeInTheDocument()
    } else {
      expect(await screen.findByText('No wanted books. Add an author to start tracking.')).toBeInTheDocument()
    }
    expect(screen.queryByRole('link', { name: 'Dune' })).not.toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'Hyperion' })).not.toBeInTheDocument()
  })
})

describe('WantedPage — live polling (#1161)', () => {
  it('polls the wanted list so background changes appear without a reload', async () => {
    vi.useFakeTimers()
    vi.mocked(api.listWanted)
      .mockResolvedValueOnce([makeBook({ id: 1, title: 'Stays Wanted' }), makeBook({ id: 2, title: 'Grabbed Away' })])
      .mockResolvedValue([makeBook({ id: 1, title: 'Stays Wanted' })])

    renderWantedPage()
    await act(async () => { await vi.advanceTimersByTimeAsync(0) }) // initial load
    expect(screen.getByText('Grabbed Away')).toBeInTheDocument()
    expect(vi.mocked(api.listWanted).mock.calls.length).toBe(1)

    // A background auto-grab removes book 2 from the wanted set; the 5s poll
    // reflects it without a manual reload.
    await act(async () => { await vi.advanceTimersByTimeAsync(5000) })
    expect(vi.mocked(api.listWanted).mock.calls.length).toBeGreaterThan(1)
    expect(screen.queryByText('Grabbed Away')).not.toBeInTheDocument()
    expect(screen.getByText('Stays Wanted')).toBeInTheDocument()
  })
})
