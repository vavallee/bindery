import { useEffect } from 'react'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { MemoryRouter, useLocation } from 'react-router'
import WantedPage from './WantedPage'
import { api } from '../api/client'
import { coverColorFor } from '../components/coverPalette'
import type { Author, Book, Download, SearchResult } from '../api/client'
import { acceptConfirm, clickBulkAction } from '../test-utils'

vi.mock('../api/client', async importOriginal => {
  const actual = await importOriginal<typeof import('../api/client')>()
  return {
    ...actual,
    api: {
      ...actual.api,
      listWantedPage: vi.fn(),
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
      if (key === 'books.countLabel') return `${String(options?.count)} books`
      if (key === 'wanted.selectBook') return `Select ${String(options?.title)}`
      if (key === 'search.grabs') return `${String(options?.n)} grabs`
      if (key === 'wanted.changeFormat') return `Change format for ${String(options?.title)}`
      if (key === 'bookDetail.filteredHeading') return `Filtered (${String(options?.count)})`
      if (key === 'bookDetail.noResultsFiltered') return `None matched, ${String(options?.count)} filtered.`
      if (key === 'bulkActionBar.selected') return `${String(options?.count)} selected`

      const labels: Record<string, string> = {
        'wanted.title': 'Wanted',
        'search.grab': 'Grab',
        'search.grabbing': 'Grabbing…',
        'wanted.searchPlaceholder': 'Search by title or author...',
        'wanted.empty': 'No wanted books. Add an author to start tracking.',
        'importHints.heading': 'Already have files on disk?',
        'importHints.body': 'Bindery only auto-imports downloads it grabbed itself.',
        'importHints.manualImport': 'Import them',
        'importHints.scanLibrary': 'Scan Library',
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
        'wanted.authorUnknown': 'Author unknown',
        'wanted.authorNotMonitored': 'Author not monitored, so this is not searched automatically',
        'search.autoGrabDisabled': 'No search was run. Automatic grabbing is off.',
      }
      return labels[key] ?? key
    },
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

// pageOf is what GET /wanted/missing?limit= answers: one page and the total
// matching the query. pageOf(books) is the whole result on one page.
function pageOf(items: Book[], total = items.length, offset = 0) {
  return { items, total, limit: 50, offset }
}

// serverLike answers listWantedPage the way the server does: it filters by the
// search (substring of title or author), then slices the page asked for and
// reports how many matched.
function serverLike(all: Book[]) {
  vi.mocked(api.listWantedPage).mockImplementation(async ({ limit, offset, search }) => {
    const q = (search ?? '').trim().toLowerCase()
    const matching = q
      ? all.filter(b => b.title.toLowerCase().includes(q) || (b.author?.authorName ?? '').toLowerCase().includes(q))
      : all
    return { items: matching.slice(offset, offset + limit), total: matching.length, limit, offset }
  })
}

function makeResult({ guid, title, ...rest }: Partial<SearchResult> & { guid: string; title: string }): SearchResult {
  return {
    guid,
    title,
    indexerId: 9,
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

type Located = { pathname: string; state: unknown }

function LocationProbe({ onLocation }: { onLocation: (location: Located) => void }) {
  const location = useLocation()
  useEffect(() => {
    onLocation({ pathname: location.pathname, state: location.state })
  }, [location, onLocation])
  return null
}

function renderWantedPage(onLocation?: (location: Located) => void) {
  return render(
    <MemoryRouter>
      {onLocation && <LocationProbe onLocation={onLocation} />}
      <WantedPage />
    </MemoryRouter>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  document.title = 'Bindery'
  vi.mocked(api.listWantedPage).mockResolvedValue(pageOf([]))
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
    expect(screen.getByText('0 books')).toBeInTheDocument()
    expect(api.listWantedPage).toHaveBeenCalledWith({ limit: 50, offset: 0, search: undefined, includeExcluded: false })
  })

  it('surfaces the Manual Import and Scan Library hints in the empty state', async () => {
    renderWantedPage()

    expect(await screen.findByText('No wanted books. Add an author to start tracking.')).toBeInTheDocument()
    const manualImport = screen.getByRole('link', { name: 'Import them' })
    expect(manualImport).toHaveAttribute('href', '/import?view=folder')
    const scanLibrary = screen.getByRole('link', { name: 'Scan Library' })
    expect(scanLibrary).toHaveAttribute('href', '/import')
  })

  it('renders a row with the book title and its author', async () => {
    const tolkien: Author = { ...author, id: 99, authorName: 'J.R.R. Tolkien' }
    vi.mocked(api.listWantedPage).mockResolvedValue(pageOf([
      makeBook({ id: 1, title: 'The Hobbit', authorId: 99, author: tolkien, releaseDate: '1937-09-21' }),
    ]))

    renderWantedPage()

    expect(await screen.findByRole('link', { name: 'The Hobbit' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'J.R.R. Tolkien' })).toBeInTheDocument()
    // author line also carries the release year
    expect(screen.getByText(/1937/)).toBeInTheDocument()
  })

  it('renders gracefully and shows a fallback when a book has no author', async () => {
    vi.mocked(api.listWantedPage).mockResolvedValue(pageOf([
      makeBook({ id: 1, title: 'Orphan Book', author: undefined }),
    ]))

    renderWantedPage()

    expect(await screen.findByRole('link', { name: 'Orphan Book' })).toBeInTheDocument()
    expect(screen.getByText('Author unknown')).toBeInTheDocument()
  })

  // A cover that fails to load, such as a stored cover the list returned raw
  // before #3146, shows the shared cover placeholder rather than a broken image.
  it('shows the cover placeholder when a cover fails to load', async () => {
    vi.mocked(api.listWantedPage).mockResolvedValue(pageOf([
      makeBook({ id: 1, title: 'Fjellvinden', imageUrl: '/api/v1/images?url=bindery-cover%3Agone.jpg' }),
    ]))

    const { container } = renderWantedPage()

    await screen.findByRole('link', { name: 'Fjellvinden' })
    fireEvent.error(container.querySelector('img')!)
    expect(container.querySelector('img')).toBeNull()
    // The shared placeholder: the book's own palette ground, seeded by its id,
    // where the old fallback was a fixed grey box.
    const tile = container.querySelector('.w-8.h-11[aria-hidden]')!
    expect(tile.className).toContain(coverColorFor(1).className.split(' ')[0])
    expect(tile.className).not.toContain('bg-slate-200')
  })

  it('renders the column header row with a select-all checkbox', async () => {
    vi.mocked(api.listWantedPage).mockResolvedValue(pageOf([makeBook({ id: 1, title: 'Dune' })]))

    renderWantedPage()

    await screen.findByRole('link', { name: 'Dune' })
    expect(screen.getByText('Title & author')).toBeInTheDocument()
    expect(screen.getByText('Format')).toBeInTheDocument()
    expect(screen.getByText('Actions')).toBeInTheDocument()
    expect(screen.getByRole('checkbox', { name: 'Select all on this page' })).toBeInTheDocument()
  })

  it('searches on the server with the search box', async () => {
    serverLike([
      makeBook({ id: 1, title: 'Dune' }),
      makeBook({ id: 2, title: 'Hyperion' }),
    ])

    renderWantedPage()

    await screen.findByRole('link', { name: 'Dune' })
    fireEvent.change(screen.getByPlaceholderText('Search by title or author...'), {
      target: { value: 'hyper' },
    })

    // The box commits after a pause and the server answers for the query.
    await waitFor(() => expect(api.listWantedPage).toHaveBeenLastCalledWith(
      expect.objectContaining({ search: 'hyper', offset: 0 }),
    ))
    await waitFor(() => expect(screen.queryByRole('link', { name: 'Dune' })).not.toBeInTheDocument())
    expect(screen.getByRole('link', { name: 'Hyperion' })).toBeInTheDocument()
    expect(screen.getByText('1 books')).toBeInTheDocument()
  })

  it('shows the no match message when the search finds nothing, not the empty state', async () => {
    serverLike([makeBook({ id: 1, title: 'Dune' })])
    renderWantedPage()
    await screen.findByRole('link', { name: 'Dune' })

    fireEvent.change(screen.getByPlaceholderText('Search by title or author...'), { target: { value: 'zzz' } })

    expect(await screen.findByText('No books match your search.')).toBeInTheDocument()
    expect(screen.queryByText('No wanted books. Add an author to start tracking.')).not.toBeInTheDocument()
  })

  // The page asks the server for one page, not the whole list: 120 wanted
  // books means 50 rows on screen and a total of 120 in the header.
  it('asks the server for one page and shows the total', async () => {
    serverLike(Array.from({ length: 120 }, (_, i) => makeBook({ id: i + 1, title: `Book ${i + 1}` })))
    renderWantedPage()

    await screen.findByRole('link', { name: 'Book 1' })
    expect(screen.getAllByRole('link', { name: /^Book \d+$/ })).toHaveLength(50)
    expect(screen.getByText('120 books')).toBeInTheDocument()
    expect(api.listWantedPage).toHaveBeenCalledWith(expect.objectContaining({ limit: 50, offset: 0 }))
  })

  it('removing a book on screen lowers the total with it', async () => {
    serverLike([makeBook({ id: 1, title: 'Dune' }), makeBook({ id: 2, title: 'Hyperion' })])
    vi.mocked(api.updateBook).mockImplementation(() => new Promise(() => {}))
    renderWantedPage()

    await screen.findByRole('link', { name: 'Dune' })
    expect(screen.getByText('2 books')).toBeInTheDocument()
    fireEvent.click(screen.getAllByRole('button', { name: 'Unmonitor' })[0])
    expect(await screen.findByText('1 books')).toBeInTheDocument()
  })

  it('changes a book format with the per-row format control', async () => {
    vi.mocked(api.listWantedPage).mockResolvedValue(pageOf([makeBook({ id: 1, title: 'Dune' })]))

    renderWantedPage()

    await screen.findByRole('link', { name: 'Dune' })
    const formatSelect = screen.getByRole('combobox', { name: 'Change format for Dune' })
    expect(formatSelect).toHaveValue('ebook')
    fireEvent.change(formatSelect, { target: { value: 'audiobook' } })

    await waitFor(() => expect(api.updateBook).toHaveBeenCalledWith(1, { mediaType: 'audiobook' }))
  })

  it('selecting a row drives the bulk action bar and select-all toggles every row', async () => {
    vi.mocked(api.listWantedPage).mockResolvedValue(pageOf([
      makeBook({ id: 1, title: 'Dune' }),
      makeBook({ id: 2, title: 'Hyperion' }),
    ]))

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
    vi.mocked(api.listWantedPage).mockResolvedValue(pageOf([book]))
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
    vi.mocked(api.listWantedPage).mockResolvedValue(pageOf([makeBook({ id: 1, title: 'Dune' })]))
    vi.mocked(api.searchBook).mockResolvedValue({ results: [], debug: null })

    renderWantedPage()

    await screen.findByRole('link', { name: 'Dune' })
    fireEvent.click(screen.getByRole('button', { name: 'Search' }))

    expect(await screen.findByText('No results found on any indexer.')).toBeInTheDocument()
  })

  it('offers the releases the filters dropped, collapsed, and grabs one by its guid (#3364)', async () => {
    const book = makeBook({ id: 1, title: 'Dune' })
    vi.mocked(api.listWantedPage).mockResolvedValue(pageOf([book]))
    vi.mocked(api.searchBook).mockResolvedValue({
      results: [makeResult({ guid: 'kept', title: 'Dune 2026 EPUB' })],
      filtered: [makeResult({ guid: 'dropped', title: 'Dune Messiah', nzbUrl: '', rejection: 'title mismatch' })],
      filteredTotal: 7,
      debug: null,
    })
    vi.mocked(api.grab).mockResolvedValue({ id: 5 } as Download)

    renderWantedPage()

    await screen.findByRole('link', { name: 'Dune' })
    fireEvent.click(screen.getByRole('button', { name: 'Search' }))

    // Collapsed: the heading carries the full count, the release is hidden.
    const heading = await screen.findByRole('button', { name: /Filtered/ })
    expect(heading).toHaveTextContent('7')
    expect(screen.queryByText('Dune Messiah')).not.toBeInTheDocument()

    fireEvent.click(heading)
    expect(await screen.findByText('Dune Messiah')).toBeInTheDocument()
    expect(screen.getByText(/title mismatch/)).toBeInTheDocument()

    const row = screen.getByText('Dune Messiah').closest('div.flex.items-center.justify-between') as HTMLElement
    fireEvent.click(within(row).getByRole('button', { name: 'Grab' }))
    await waitFor(() => expect(api.grab).toHaveBeenCalled())
    expect(vi.mocked(api.grab).mock.calls[0][0]).toMatchObject({ guid: 'dropped', bookId: 1 })
  })

  it('points at the filtered section when every release was filtered (#3364)', async () => {
    vi.mocked(api.listWantedPage).mockResolvedValue(pageOf([makeBook({ id: 1, title: 'Dune' })]))
    vi.mocked(api.searchBook).mockResolvedValue({
      results: [],
      filtered: [makeResult({ guid: 'dropped', title: 'Dune Messiah' })],
      debug: null,
    })

    renderWantedPage()

    await screen.findByRole('link', { name: 'Dune' })
    fireEvent.click(screen.getByRole('button', { name: 'Search' }))

    expect(await screen.findByText('None matched, 1 filtered.')).toBeInTheDocument()
    expect(screen.queryByText('No results found on any indexer.')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: /Filtered/ })).toBeInTheDocument()
  })

  it('shows no filtered section when the server sends none (older server)', async () => {
    vi.mocked(api.listWantedPage).mockResolvedValue(pageOf([makeBook({ id: 1, title: 'Dune' })]))
    vi.mocked(api.searchBook).mockResolvedValue({ results: [makeResult({ guid: 'k', title: 'Kept' })], debug: null })

    renderWantedPage()

    await screen.findByRole('link', { name: 'Dune' })
    fireEvent.click(screen.getByRole('button', { name: 'Search' }))
    await screen.findByText('Kept')
    expect(screen.queryByRole('button', { name: /Filtered/ })).not.toBeInTheDocument()
  })

  it('grabs a result, shows transient states, and refreshes wanted books', async () => {
    const book = makeBook({ id: 1, title: 'Dune' })
    let resolveGrab: (download: Download) => void = () => {}
    vi.mocked(api.listWantedPage)
      .mockResolvedValueOnce(pageOf([book]))
      .mockResolvedValueOnce(pageOf([]))
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
      indexerId: 9,
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

    expect(api.listWantedPage).toHaveBeenCalledTimes(2)
    expect(screen.queryByText('Dune 2026 EPUB')).not.toBeInTheDocument()
    expect(screen.getByText('No wanted books. Add an author to start tracking.')).toBeInTheDocument()
  })

  // #2933: a dual-format book's search classifies each result, and that, not
  // the book's 'both', is what the grab must carry.
  it('grabs an audiobook result of a dual-format book as an audiobook', async () => {
    vi.mocked(api.listWantedPage).mockResolvedValue(pageOf([makeBook({ id: 1, title: 'Dune', mediaType: 'both' })]))
    vi.mocked(api.searchBook).mockResolvedValue({
      results: [makeResult({ guid: 'dune-audio', title: 'Dune Unabridged', mediaType: 'audiobook' })],
      debug: null,
    })

    renderWantedPage()

    await screen.findByRole('link', { name: 'Dune' })
    fireEvent.click(screen.getByRole('button', { name: 'Search' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Grab' }))

    await waitFor(() => expect(api.grab).toHaveBeenCalledTimes(1))
    expect(api.grab).toHaveBeenCalledWith(expect.objectContaining({ guid: 'dune-audio', mediaType: 'audiobook' }))
  })

  it('unmonitors a single wanted book and removes it from the list', async () => {
    const book = makeBook({ id: 1, title: 'Dune' })
    let resolveUpdate: (book: Book) => void = () => {}
    vi.mocked(api.listWantedPage).mockResolvedValue(pageOf([book]))
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
    vi.mocked(api.listWantedPage)
      .mockResolvedValueOnce(pageOf(initialBooks))
      .mockResolvedValueOnce(pageOf(refreshedBooks))

    renderWantedPage()

    await screen.findByRole('link', { name: 'Dune' })
    fireEvent.click(screen.getByTitle('Select Dune'))
    fireEvent.click(screen.getByTitle('Select Hyperion'))

    clickBulkAction(label, action === 'blocklist' ? 'menu' : undefined)
    // Blocklisting takes each book off Wanted for good, so it asks first and
    // says how many.
    if (action === 'blocklist') {
      expect(within(await screen.findByTestId('confirm-dialog')).getByText(/wanted\.blocklistSelectedConfirm|Blocklist 2 books/)).toBeInTheDocument()
      expect(api.bulkActionWanted).not.toHaveBeenCalled()
      await acceptConfirm()
    }

    await waitFor(() => expect(api.bulkActionWanted).toHaveBeenCalledWith([1, 2], action))
    await waitFor(() => expect(api.listWantedPage).toHaveBeenCalledTimes(2))
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

  // #2669: with automatic grabbing off the server refuses the search instead
  // of queueing it. Before the fix every entry came back ok:true, the page
  // cleared the selection and reloaded, and the user saw a button flash and
  // no other sign that nothing had happened.
  it('tells the user nothing was searched when automatic grabbing is off, and keeps the selection', async () => {
    vi.mocked(api.listWantedPage).mockResolvedValue(pageOf([
      makeBook({ id: 1, title: 'Dune' }),
      makeBook({ id: 2, title: 'Hyperion' }),
    ]))
    vi.mocked(api.bulkActionWanted).mockResolvedValue({
      results: {
        1: { ok: false, code: 'auto_grab_disabled', error: 'automatic grabbing is disabled' },
        2: { ok: false, code: 'auto_grab_disabled', error: 'automatic grabbing is disabled' },
      },
    })

    renderWantedPage()

    await screen.findByRole('link', { name: 'Dune' })
    fireEvent.click(screen.getByTitle('Select Dune'))
    fireEvent.click(screen.getByTitle('Select Hyperion'))

    clickBulkAction('Search')

    expect(await screen.findByText('No search was run. Automatic grabbing is off.')).toBeInTheDocument()
    // The selection survives, so flipping the setting and pressing Search
    // again does not mean re-picking every book.
    expect(screen.getByText('2 selected')).toBeInTheDocument()
    // And the list is not reloaded, because nothing changed.
    expect(api.listWantedPage).toHaveBeenCalledTimes(1)
  })
})

describe('WantedPage — live polling (#1161)', () => {
  it('polls the wanted list so background changes appear without a reload', async () => {
    vi.useFakeTimers()
    vi.mocked(api.listWantedPage)
      .mockResolvedValueOnce(pageOf([makeBook({ id: 1, title: 'Stays Wanted' }), makeBook({ id: 2, title: 'Grabbed Away' })]))
      .mockResolvedValue(pageOf([makeBook({ id: 1, title: 'Stays Wanted' })]))

    renderWantedPage()
    await act(async () => { await vi.advanceTimersByTimeAsync(0) }) // initial load
    expect(screen.getByText('Grabbed Away')).toBeInTheDocument()
    expect(vi.mocked(api.listWantedPage).mock.calls.length).toBe(1)

    // A background auto-grab removes book 2 from the wanted set; the 5s poll
    // reflects it without a manual reload.
    await act(async () => { await vi.advanceTimersByTimeAsync(5000) })
    expect(vi.mocked(api.listWantedPage).mock.calls.length).toBeGreaterThan(1)
    expect(screen.queryByText('Grabbed Away')).not.toBeInTheDocument()
    expect(screen.getByText('Stays Wanted')).toBeInTheDocument()
  })
})

describe('WantedPage — book link nav state (#2548, book side)', () => {
  it('carries {ids, index} scoped to pageItems (this page’s slice), not the unpaginated filtered list', async () => {
    vi.mocked(api.listWantedPage).mockResolvedValue(pageOf([
      makeBook({ id: 1, title: 'Elantris' }),
      makeBook({ id: 2, title: 'Mistborn' }),
    ]))
    let located: Located | undefined
    renderWantedPage(loc => { located = loc })

    await screen.findByText('Mistborn')
    fireEvent.click(screen.getByText('Mistborn'))

    await waitFor(() => expect(located?.pathname).toBe('/book/2'))
    expect(located?.state).toEqual({ ids: [1, 2], index: 1, hopDepth: 1 })
  })

  it('narrows the carried ids to the search-filtered set, not every wanted book', async () => {
    serverLike([
      makeBook({ id: 1, title: 'Elantris' }),
      makeBook({ id: 2, title: 'Mistborn' }),
      makeBook({ id: 3, title: 'Warbreaker' }),
    ])
    let located: Located | undefined
    renderWantedPage(loc => { located = loc })

    await screen.findByText('Mistborn')
    fireEvent.change(screen.getByPlaceholderText('Search by title or author...'), {
      target: { value: 'Mist' },
    })
    // Narrows the visible list to just Mistborn — Elantris and Warbreaker
    // must also be dropped from the carried ids, not just hidden.
    await waitFor(() => expect(screen.queryByText('Elantris')).not.toBeInTheDocument())

    fireEvent.click(screen.getByText('Mistborn'))

    await waitFor(() => expect(located?.pathname).toBe('/book/2'))
    expect(located?.state).toEqual({ ids: [2], index: 0, hopDepth: 1 })
  })
})

// A book whose author is unmonitored is never searched by the sweep (#2742),
// so on this page it is indistinguishable from one whose grab is slow. The row
// says which it is, and only for the rows it applies to.
describe('WantedPage author monitoring hint', () => {
  it('explains a row the sweep will not search, and leaves the others alone', async () => {
    vi.mocked(api.listWantedPage).mockResolvedValue(pageOf([
      makeBook({ id: 1, title: 'Dune', authorUnmonitored: true }),
      makeBook({ id: 2, title: 'Messiah' }),
    ]))

    render(
      <MemoryRouter>
        <WantedPage />
      </MemoryRouter>,
    )

    await screen.findByText('Dune')
    const hints = screen.getAllByText('Author not monitored, so this is not searched automatically')
    expect(hints).toHaveLength(1)
  })
})

describe('WantedPage filter bar', () => {
  it('sends sort and format to the server, and searches the page on screen after confirming', async () => {
    vi.mocked(api.listWantedPage).mockResolvedValue(pageOf([makeBook({ id: 1, title: 'Dune' }), makeBook({ id: 2, title: 'Hyperion' })]))
    renderWantedPage()
    await screen.findByRole('link', { name: 'Dune' })

    fireEvent.click(screen.getByRole('button', { name: 'books.sortLabel' }))
    fireEvent.click(screen.getByRole('radio', { name: 'books.sortNewest' }))
    await waitFor(() => expect(api.listWantedPage).toHaveBeenLastCalledWith(expect.objectContaining({ sort: 'date-new' })))

    fireEvent.click(screen.getByRole('button', { name: /^common\.filters/ }))
    fireEvent.click(screen.getByRole('radio', { name: 'common.audiobook' }))
    await waitFor(() => expect(api.listWantedPage).toHaveBeenLastCalledWith(expect.objectContaining({ mediaType: 'audiobook', sort: 'date-new' })))

    // One page-level search instead of a green Search button per row. While
    // it runs the row buttons are off, so no book is sent twice.
    let finish: (v: unknown) => void = () => {}
    vi.mocked(api.bulkActionWanted).mockImplementationOnce(() => new Promise(r => { finish = r }) as never)
    fireEvent.click(await screen.findByRole('button', { name: 'wanted.searchPage' }))
    await acceptConfirm()
    await waitFor(() => expect(api.bulkActionWanted).toHaveBeenCalledWith([1, 2], 'search'))
    for (const b of screen.getAllByRole('button', { name: 'Search' })) expect(b).toBeDisabled()
    await act(async () => { finish({ results: {} }) })
    await waitFor(() => { for (const b of screen.getAllByRole('button', { name: 'Search' })) expect(b).toBeEnabled() })
  })
})
