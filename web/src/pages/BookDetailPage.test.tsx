import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import BookDetailPage, { SearchResultsSection } from './BookDetailPage'
import { api } from '../api/client'
import type { Author, Book, Download, HistoryEvent, Indexer, SearchResult } from '../api/client'
import en from '../i18n/locales/en.json'

// Resolve a dotted i18n key against the real English locale, applying the
// {{var}} interpolation and the second-arg default-value fallback so tests
// assert against the real strings the page renders.
function resolveKey(key: string): string | undefined {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  let node: any = en
  for (const part of key.split('.')) {
    if (node && typeof node === 'object' && part in node) node = node[part]
    else return undefined
  }
  return typeof node === 'string' ? node : undefined
}

// A stable t() — a fresh reference each render would re-trigger effects with
// `t` in their dependency array (mirrors react-i18next, whose t is stable).
function translate(key: string, arg?: unknown): string {
  const resolved = resolveKey(key)
  const fallback = typeof arg === 'string' ? arg : key
  let str = resolved ?? fallback
  if (arg && typeof arg === 'object') {
    for (const [k, v] of Object.entries(arg as Record<string, unknown>)) {
      str = str.replace(new RegExp(`{{\\s*${k}\\s*}}`, 'g'), String(v))
    }
  }
  return str
}

const translation = { t: translate }

vi.mock('react-i18next', () => ({
  useTranslation: () => translation,
}))

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
      deleteBook: vi.fn(),
      deleteBookFile: vi.fn(),
      toggleExcluded: vi.fn(),
      enrichAudiobook: vi.fn(),
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
    addedAt: '2026-05-01T12:00:00Z',
    ...overrides,
  }
}

function renderBookDetailPage() {
  return render(
    <MemoryRouter initialEntries={['/book/42']}>
      <Routes>
        <Route path="/book/:id" element={<BookDetailPage />} />
        <Route path="/settings" element={<div>Settings Page</div>} />
        <Route path="/author/:id" element={<div>Author Page</div>} />
      </Routes>
    </MemoryRouter>,
  )
}

const noop = () => {}

beforeEach(() => {
  vi.clearAllMocks()
  document.title = 'Bindery'
  vi.mocked(api.getBook).mockResolvedValue(makeBook())
  vi.mocked(api.listHistory).mockResolvedValue({ items: [], total: 0, limit: 100, offset: 0 })
  vi.mocked(api.searchBook).mockResolvedValue({ results: [], debug: null })
  vi.mocked(api.listIndexers).mockResolvedValue([])
  vi.mocked(api.grab).mockResolvedValue(makeDownload())
  vi.mocked(api.updateBook).mockImplementation(async (_id, patch) => makeBook(patch))
  vi.mocked(api.deleteBook).mockResolvedValue(undefined)
  vi.mocked(api.deleteBookFile).mockImplementation(async () => makeBook())
  vi.mocked(api.toggleExcluded).mockImplementation(async () => makeBook({ excluded: true }))
  vi.mocked(api.enrichAudiobook).mockImplementation(async () => makeBook())
  // jsdom has no clipboard by default.
  Object.defineProperty(navigator, 'clipboard', {
    value: { writeText: vi.fn().mockResolvedValue(undefined) },
    configurable: true,
  })
  Object.defineProperty(window, 'isSecureContext', {
    value: true,
    configurable: true,
  })
  Object.defineProperty(document, 'execCommand', {
    value: vi.fn().mockReturnValue(false),
    configurable: true,
  })
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

describe('BookDetailPage — header & metadata', () => {
  it('loads book details and history', async () => {
    vi.mocked(api.listHistory).mockResolvedValue({ items: [makeHistory()], total: 1, limit: 100, offset: 0 })

    renderBookDetailPage()

    expect(await screen.findByRole('heading', { name: 'The Final Empire' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Brandon Sanderson' })).toBeInTheDocument()
    expect(screen.getByText('A skaa thief joins a rebellion.')).toBeInTheDocument()
    expect(api.getBook).toHaveBeenCalledWith(42)
    expect(api.listHistory).toHaveBeenCalledWith({ bookId: 42 })
  })

  it('maps the ISO-639 language code to a full word', async () => {
    vi.mocked(api.getBook).mockResolvedValue(makeBook({ language: 'eng' }))
    renderBookDetailPage()
    expect(await screen.findByText('English')).toBeInTheDocument()
  })

  it('labels the published date', async () => {
    renderBookDetailPage()
    expect(await screen.findByText(/^Published /)).toBeInTheDocument()
  })

  it('links the author byline to the author page', async () => {
    renderBookDetailPage()
    const link = await screen.findByRole('link', { name: 'Brandon Sanderson' })
    expect(link).toHaveAttribute('href', '/author/7')
  })
})

describe('BookDetailPage — file section actions', () => {
  it('renders a download link for a single-format book with a file', async () => {
    vi.mocked(api.getBook).mockResolvedValue(makeBook({ filePath: '/library/book.epub' }))
    renderBookDetailPage()
    const download = await screen.findByRole('link', { name: 'Download' })
    expect(download).toHaveAttribute('href', '/api/v1/book/42/file')
  })

  it('shows the format badge for a single-format book', async () => {
    vi.mocked(api.getBook).mockResolvedValue(makeBook({ mediaType: 'ebook' }))
    renderBookDetailPage()
    expect(await screen.findByTestId('badge-ebook')).toBeInTheDocument()
  })

  it('opens the re-bind modal', async () => {
    renderBookDetailPage()
    fireEvent.click(await screen.findByRole('button', { name: 'Re-bind' }))
    expect(await screen.findByText('Re-bind metadata')).toBeInTheDocument()
  })

  it('toggles exclude via api.toggleExcluded', async () => {
    renderBookDetailPage()
    fireEvent.click(await screen.findByRole('button', { name: 'Exclude' }))
    await waitFor(() => expect(api.toggleExcluded).toHaveBeenCalledWith(42))
  })

  it('deletes a file via api.deleteBookFile after confirmation', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    vi.mocked(api.getBook).mockResolvedValue(makeBook({ filePath: '/library/book.epub' }))
    renderBookDetailPage()
    fireEvent.click(await screen.findByRole('button', { name: /Delete file/ }))
    await waitFor(() => expect(api.deleteBookFile).toHaveBeenCalledWith(42, ''))
  })

  it('copies the file path to the clipboard', async () => {
    vi.mocked(api.getBook).mockResolvedValue(makeBook({ filePath: '/library/book.epub' }))
    renderBookDetailPage()
    fireEvent.click(await screen.findByRole('button', { name: /Copy file path/ }))
    await waitFor(() =>
      expect(navigator.clipboard.writeText).toHaveBeenCalledWith('/library/book.epub'),
    )
  })
})

describe('BookDetailPage — search', () => {
  it('searches indexers and renders results', async () => {
    vi.mocked(api.listIndexers).mockResolvedValue([makeIndexer()])
    vi.mocked(api.searchBook).mockResolvedValue({
      results: [makeResult({ guid: 'r1', title: 'A Result' })],
      debug: null,
    })

    renderBookDetailPage()

    fireEvent.click(await screen.findByRole('button', { name: /Search ebook indexers/ }))
    await waitFor(() => expect(api.searchBook).toHaveBeenCalledWith(42))
    expect(await screen.findByText('A Result')).toBeInTheDocument()
    expect(api.listIndexers).toHaveBeenCalled()
  })

  it('shows an empty search state when no indexers are configured', async () => {
    vi.mocked(api.listIndexers).mockResolvedValue([])
    vi.mocked(api.searchBook).mockResolvedValue({ results: [], debug: null })

    renderBookDetailPage()

    fireEvent.click(await screen.findByRole('button', { name: /Search ebook indexers/ }))

    expect(await screen.findByText(/No indexers configured/)).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Settings' })).toHaveAttribute('href', '/settings')
  })

  it('grabs a result, refreshes book and history, and clears results', async () => {
    let resolveGrab: (download: Download) => void = () => {}
    vi.mocked(api.getBook)
      .mockResolvedValueOnce(makeBook())
      .mockResolvedValueOnce(makeBook({ status: 'downloading' }))
    vi.mocked(api.listHistory)
      .mockResolvedValueOnce({ items: [], total: 0, limit: 100, offset: 0 })
      .mockResolvedValueOnce({ items: [makeHistory({ sourceTitle: 'Grab refreshed history' })], total: 1, limit: 100, offset: 0 })
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

    fireEvent.click(await screen.findByRole('button', { name: /Search ebook indexers/ }))
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
})

describe('BookDetailPage — media type selector', () => {
  it('renders the selector with the current mediaType selected', async () => {
    vi.mocked(api.getBook).mockResolvedValue(makeBook({ mediaType: 'ebook' }))
    renderBookDetailPage()
    const select = (await screen.findByLabelText('Media type')) as HTMLSelectElement
    expect(select.value).toBe('ebook')
  })

  it('calls api.updateBook with the new mediaType on change', async () => {
    vi.mocked(api.getBook).mockResolvedValue(makeBook({ mediaType: 'ebook' }))
    vi.mocked(api.updateBook).mockResolvedValue(makeBook({ mediaType: 'both' }))

    renderBookDetailPage()

    const select = await screen.findByLabelText('Media type')
    fireEvent.change(select, { target: { value: 'both' } })

    await waitFor(() => expect(api.updateBook).toHaveBeenCalledWith(42, { mediaType: 'both' }))
  })
})

describe('BookDetailPage — dual-format book', () => {
  it('renders a format switcher for a two-format book', async () => {
    vi.mocked(api.getBook).mockResolvedValue(
      makeBook({
        mediaType: 'both',
        ebookFilePath: '/library/book.epub',
        audiobookFilePath: '/library/book-audio',
      }),
    )
    renderBookDetailPage()

    const ebookBtn = await screen.findByRole('button', { name: /Ebook/ })
    const audioBtn = screen.getByRole('button', { name: /Audiobook/ })
    expect(ebookBtn).toHaveAttribute('aria-pressed', 'true')
    expect(audioBtn).toHaveAttribute('aria-pressed', 'false')

    // The path shown follows the active format.
    expect(screen.getByText('/library/book.epub')).toBeInTheDocument()
    fireEvent.click(audioBtn)
    expect(await screen.findByText('/library/book-audio')).toBeInTheDocument()
  })

  it('marks which formats are on disk in the switcher', async () => {
    vi.mocked(api.getBook).mockResolvedValue(
      makeBook({
        mediaType: 'both',
        ebookFilePath: '/library/book.epub',
        audiobookFilePath: '',
      }),
    )
    renderBookDetailPage()

    const ebookBtn = await screen.findByRole('button', { name: /Ebook/ })
    const audioBtn = screen.getByRole('button', { name: /Audiobook/ })
    expect(ebookBtn).toHaveAttribute('title', 'On disk')
    expect(ebookBtn).toHaveTextContent('✓')
    expect(audioBtn).toHaveAttribute('title', 'Not downloaded')
    expect(audioBtn).not.toHaveTextContent('✓')
  })

  it('deletes the active format file for a dual-format book', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    vi.mocked(api.getBook).mockResolvedValue(
      makeBook({
        mediaType: 'both',
        ebookFilePath: '/library/book.epub',
        audiobookFilePath: '/library/book-audio',
      }),
    )
    renderBookDetailPage()

    fireEvent.click(await screen.findByRole('button', { name: /Delete file/ }))
    await waitFor(() => expect(api.deleteBookFile).toHaveBeenCalledWith(42, '?format=ebook'))
  })
})

describe('BookDetailPage — history section', () => {
  it('renders the humanised event label', async () => {
    vi.mocked(api.listHistory).mockResolvedValue({
      items: [makeHistory({ eventType: 'bookImported', sourceTitle: 'A.Desolation.Called.Peace' })],
      total: 1,
      limit: 100,
      offset: 0,
    })
    renderBookDetailPage()
    expect(await screen.findByText('Book imported')).toBeInTheDocument()
    expect(screen.getByText('A.Desolation.Called.Peace')).toBeInTheDocument()
  })
})

describe('BookDetailPage — danger zone', () => {
  it('opens the confirm modal and keeps confirm disabled until acknowledged', async () => {
    renderBookDetailPage()

    fireEvent.click(await screen.findByRole('button', { name: 'Delete book + files…' }))

    const confirm = await screen.findByRole('button', { name: 'Delete book + files' })
    expect(confirm).toBeDisabled()

    fireEvent.click(screen.getByRole('checkbox', { name: /I understand/ }))
    expect(confirm).toBeEnabled()
  })

  it('calls api.deleteBook only after acknowledging and confirming', async () => {
    renderBookDetailPage()

    fireEvent.click(await screen.findByRole('button', { name: 'Delete book + files…' }))
    fireEvent.click(screen.getByRole('checkbox', { name: /I understand/ }))
    fireEvent.click(screen.getByRole('button', { name: 'Delete book + files' }))

    await waitFor(() => expect(api.deleteBook).toHaveBeenCalledWith(42, false))
  })

  it('does not call api.deleteBook when the modal is cancelled', async () => {
    renderBookDetailPage()

    fireEvent.click(await screen.findByRole('button', { name: 'Delete book + files…' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Cancel' }))

    await waitFor(() =>
      expect(screen.queryByRole('checkbox', { name: /I understand/ })).not.toBeInTheDocument(),
    )
    expect(api.deleteBook).not.toHaveBeenCalled()
  })

  it('passes deleteFiles=true to api.deleteBook when the book has files', async () => {
    vi.mocked(api.getBook).mockResolvedValue(makeBook({ filePath: '/library/book.epub' }))
    renderBookDetailPage()

    fireEvent.click(await screen.findByRole('button', { name: 'Delete book + files…' }))
    fireEvent.click(screen.getByRole('checkbox', { name: /I understand/ }))
    fireEvent.click(screen.getByRole('button', { name: 'Delete book + files' }))

    await waitFor(() => expect(api.deleteBook).toHaveBeenCalledWith(42, true))
  })
})
