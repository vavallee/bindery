import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import BookDetailPage, { SearchResultsSection } from './BookDetailPage'
import { api } from '../api/client'
import type { Author, Book, BookFile, Download, HistoryEvent, Indexer, SearchResult } from '../api/client'
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
      listAuthorSeries: vi.fn(),
    },
  }
})

vi.mock('../components/MediaBadge', () => ({
  default: ({ type }: { type?: string }) => <span data-testid={`badge-${type}`}>{type}</span>,
}))

function makeResult({ guid, title, ...rest }: Partial<SearchResult> & { guid: string }): SearchResult {
  return {
    guid,
    indexerId: 3,
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

function makeFile(overrides: Partial<BookFile> & { id: number }): BookFile {
  return {
    bookId: 42,
    format: 'ebook',
    path: '/library/book.epub',
    sizeBytes: 0,
    createdAt: '2026-05-01T12:00:00Z',
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
    priority: 0,
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
  vi.mocked(api.listAuthorSeries).mockResolvedValue([])
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

  it('renders an indexer detail link when infoUrl is present', () => {
    const results = [
      makeResult({ guid: 'r1', title: 'Linked', infoUrl: 'https://indexer.example/details/1' }),
    ]
    render(
      <SearchResultsSection results={results} bookMediaType="ebook" grabbing={null} onGrab={noop} />,
    )
    const link = screen.getByRole('link', { name: /indexer/ })
    expect(link).toHaveAttribute('href', 'https://indexer.example/details/1')
    expect(link).toHaveAttribute('target', '_blank')
    expect(link).toHaveAttribute('rel', 'noopener noreferrer')
  })

  it('renders no detail link when infoUrl is absent', () => {
    const results = [makeResult({ guid: 'r1', title: 'Unlinked' })]
    render(
      <SearchResultsSection results={results} bookMediaType="ebook" grabbing={null} onGrab={noop} />,
    )
    expect(screen.queryByRole('link', { name: /indexer/ })).toBeNull()
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
  // ?format=-scoped whenever the row carries a real format: the format-less
  // endpoint falls back to the legacy file_path, which on a mislabelled book
  // points at the other format.
  it('renders a format-scoped download link for a single-format book', async () => {
    vi.mocked(api.getBook).mockResolvedValue(
      makeBook({ bookFiles: [makeFile({ id: 1, format: 'ebook', path: '/library/book.epub' })] }),
    )
    renderBookDetailPage()
    const download = await screen.findByRole('link', { name: 'Download' })
    expect(download).toHaveAttribute('href', '/api/v1/book/42/file?format=ebook')
  })

  // The bare legacy file_path row's format is only the media-type proxy; the
  // server resolves ?format= against the path's on-disk shape, and the two
  // can disagree (a book declared audiobook whose file_path is an epub would
  // 404 on download and 400 on delete). Format-less is exact here because the
  // row only exists when it is the book's only file.
  it('goes format-less for the bare legacy file_path row', async () => {
    vi.mocked(api.getBook).mockResolvedValue(
      makeBook({ mediaType: 'audiobook', filePath: '/library/legacy-thing' }),
    )
    renderBookDetailPage()
    const download = await screen.findByRole('link', { name: 'Download' })
    expect(download).toHaveAttribute('href', '/api/v1/book/42/file')

    const group = within(screen.getByTestId('file-group-audiobook'))
    fireEvent.click(group.getByRole('button', { name: /Delete file/ }))
    fireEvent.click(await screen.findByRole('checkbox'))
    fireEvent.click(screen.getByRole('button', { name: 'Delete from disk' }))
    await waitFor(() => expect(api.deleteBookFile).toHaveBeenCalledWith(42, ''))
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

  // Exclude and Rename files moved behind the File card's More menu so the row
  // doesn't present six equally-weighted actions.
  it('toggles exclude via api.toggleExcluded, from the More menu', async () => {
    renderBookDetailPage()
    fireEvent.click(await screen.findByRole('button', { name: /More/ }))
    fireEvent.click(await screen.findByRole('menuitem', { name: 'Exclude' }))
    await waitFor(() => expect(api.toggleExcluded).toHaveBeenCalledWith(42))
  })

  it('keeps Download and Delete on the format group, Re-bind on the section', async () => {
    vi.mocked(api.getBook).mockResolvedValue(makeBook({ filePath: '/library/book.epub' }))
    renderBookDetailPage()
    const group = within(await screen.findByTestId('file-group-ebook'))
    expect(group.getByRole('link', { name: 'Download' })).toBeInTheDocument()
    expect(group.getByRole('button', { name: /Delete file/ })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Re-bind' })).toBeInTheDocument()
    // One file → nothing to disambiguate, so Fix match stays at the surface.
    expect(screen.getByRole('button', { name: 'Fix match' })).toBeInTheDocument()
    // …and the overflow ones off the row, until a menu is opened.
    expect(screen.queryByRole('button', { name: 'Exclude' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Rename files' })).toBeNull()
  })

  it('moves Fix match into the row menus once there are several files', async () => {
    vi.mocked(api.getBook).mockResolvedValue(
      makeBook({
        bookFiles: [
          makeFile({ id: 1, format: 'ebook', path: '/library/book.epub' }),
          makeFile({ id: 2, format: 'ebook', path: '/library/book.mobi' }),
        ],
      }),
    )
    renderBookDetailPage()
    await screen.findByTestId('file-group-ebook')
    expect(screen.queryByRole('button', { name: 'Fix match' })).toBeNull()
    // Each row menu is distinctly named, not N anonymous "More" buttons.
    fireEvent.click(screen.getByRole('button', { name: 'More actions for book.mobi' }))
    expect(await screen.findByRole('menuitem', { name: 'Fix match' })).toBeEnabled()
  })

  it('opens the rename-files modal from the section More menu', async () => {
    vi.mocked(api.getBook).mockResolvedValue(makeBook({ ebookFilePath: '/library/book.epub' }))
    renderBookDetailPage()
    const actions = within(await screen.findByTestId('file-section-actions'))
    fireEvent.click(actions.getByRole('button', { name: /More/ }))
    expect(await screen.findByRole('menuitem', { name: 'Rename files' })).toBeEnabled()
  })

  it('scopes the group delete to that format after confirmation', async () => {
    vi.mocked(api.getBook).mockResolvedValue(
      makeBook({ bookFiles: [makeFile({ id: 1, format: 'ebook', path: '/library/book.epub' })] }),
    )
    renderBookDetailPage()
    const group = within(await screen.findByTestId('file-group-ebook'))
    fireEvent.click(group.getByRole('button', { name: /Delete file/ }))
    // ConfirmDialog gates the confirm button behind the acknowledgement.
    fireEvent.click(await screen.findByRole('checkbox'))
    fireEvent.click(screen.getByRole('button', { name: 'Delete from disk' }))
    await waitFor(() => expect(api.deleteBookFile).toHaveBeenCalledWith(42, '?format=ebook'))
  })

  // The dialog must name every path the request will remove: the old
  // window.confirm printed one path derived from the book's media type while
  // a format-less DELETE removed every file on the book.
  it('lists every path the pending delete will remove', async () => {
    vi.mocked(api.getBook).mockResolvedValue(
      makeBook({
        bookFiles: [
          makeFile({ id: 1, format: 'ebook', path: '/library/book.epub' }),
          makeFile({ id: 2, format: 'ebook', path: '/library/book.mobi' }),
        ],
      }),
    )
    renderBookDetailPage()
    const group = within(await screen.findByTestId('file-group-ebook'))
    fireEvent.click(group.getByRole('button', { name: /Delete file/ }))

    const dialog = await screen.findByText(/Permanently delete 2 file/)
    expect(dialog).toBeInTheDocument()
    const body = dialog.parentElement as HTMLElement
    expect(within(body).getByText('/library/book.epub')).toBeInTheDocument()
    expect(within(body).getByText('/library/book.mobi')).toBeInTheDocument()
  })

  it('sends a format-less delete only from the explicit delete-all action', async () => {
    vi.mocked(api.getBook).mockResolvedValue(
      makeBook({
        mediaType: 'both',
        bookFiles: [
          makeFile({ id: 1, format: 'ebook', path: '/library/book.epub' }),
          makeFile({ id: 2, format: 'audiobook', path: '/library/book-audio' }),
        ],
      }),
    )
    renderBookDetailPage()
    const actions = within(await screen.findByTestId('file-section-actions'))
    fireEvent.click(actions.getByRole('button', { name: /More/ }))
    fireEvent.click(await screen.findByRole('menuitem', { name: 'Delete all files' }))
    fireEvent.click(await screen.findByRole('checkbox'))
    fireEvent.click(screen.getByRole('button', { name: 'Delete from disk' }))
    await waitFor(() => expect(api.deleteBookFile).toHaveBeenCalledWith(42, ''))
  })

  it('deregisters a single row by path without touching disk', async () => {
    vi.mocked(api.getBook).mockResolvedValue(
      makeBook({
        bookFiles: [makeFile({ id: 1, format: 'ebook', path: '/library/stale copy.epub' })],
      }),
    )
    renderBookDetailPage()
    const group = within(await screen.findByTestId('file-group-ebook'))
    fireEvent.click(group.getByRole('button', { name: /More/ }))
    fireEvent.click(await screen.findByRole('menuitem', { name: 'Forget this file' }))
    fireEvent.click(await screen.findByRole('checkbox'))
    fireEvent.click(screen.getByRole('button', { name: 'Forget file' }))
    await waitFor(() =>
      expect(api.deleteBookFile).toHaveBeenCalledWith(42, '?path=%2Flibrary%2Fstale%20copy.epub'),
    )
  })

  // Legacy rows predate migration 028 and have no book_files entry, so
  // deregister would 404 against a path the server cannot resolve.
  it('disables deregister for untracked legacy rows', async () => {
    vi.mocked(api.getBook).mockResolvedValue(makeBook({ filePath: '/library/legacy.epub' }))
    renderBookDetailPage()
    const group = within(await screen.findByTestId('file-group-ebook'))
    fireEvent.click(group.getByRole('button', { name: /More/ }))
    expect(await screen.findByRole('menuitem', { name: 'Forget this file' })).toBeDisabled()
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
      indexerId: 3,
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
  it('renders one group per format, both visible at once', async () => {
    vi.mocked(api.getBook).mockResolvedValue(
      makeBook({
        mediaType: 'both',
        bookFiles: [
          makeFile({ id: 1, format: 'ebook', path: '/library/book.epub' }),
          makeFile({ id: 2, format: 'audiobook', path: '/library/book-audio' }),
        ],
      }),
    )
    renderBookDetailPage()

    // No switcher: hiding one format behind a toggle is what made a
    // registered file invisible in the first place.
    expect(await screen.findByText('/library/book.epub')).toBeInTheDocument()
    expect(screen.getByText('/library/book-audio')).toBeInTheDocument()
    expect(screen.getByTestId('file-group-ebook')).toBeInTheDocument()
    expect(screen.getByTestId('file-group-audiobook')).toBeInTheDocument()
  })

  it('scopes each group download and delete to its own format', async () => {
    vi.mocked(api.getBook).mockResolvedValue(
      makeBook({
        mediaType: 'both',
        bookFiles: [
          makeFile({ id: 1, format: 'ebook', path: '/library/book.epub' }),
          makeFile({ id: 2, format: 'audiobook', path: '/library/book-audio' }),
        ],
      }),
    )
    renderBookDetailPage()

    const audio = within(await screen.findByTestId('file-group-audiobook'))
    expect(audio.getByRole('link', { name: 'Download' })).toHaveAttribute(
      'href',
      '/api/v1/book/42/file?format=audiobook',
    )
    fireEvent.click(audio.getByRole('button', { name: /Delete file/ }))
    fireEvent.click(await screen.findByRole('checkbox'))
    fireEvent.click(screen.getByRole('button', { name: 'Delete from disk' }))
    await waitFor(() => expect(api.deleteBookFile).toHaveBeenCalledWith(42, '?format=audiobook'))
  })

  // The live specimen from the bug report: media_type says audiobook, but
  // book_files holds an epub too. Both files must be visible, each badged by
  // its own format — not by the book's.
  it('shows a file whose format disagrees with the declared media type', async () => {
    vi.mocked(api.getBook).mockResolvedValue(
      makeBook({
        mediaType: 'audiobook',
        bookFiles: [
          makeFile({ id: 1, format: 'audiobook', path: '/library/redshirts-audio' }),
          makeFile({ id: 2, format: 'ebook', path: '/library/redshirts.epub' }),
        ],
      }),
    )
    renderBookDetailPage()

    expect(await screen.findByText('/library/redshirts.epub')).toBeInTheDocument()
    expect(screen.getByText('/library/redshirts-audio')).toBeInTheDocument()

    const ebookGroup = within(screen.getByTestId('file-group-ebook'))
    expect(ebookGroup.getByTestId('badge-ebook')).toBeInTheDocument()
    // The epub's own group must delete only the epub.
    fireEvent.click(ebookGroup.getByRole('button', { name: /Delete file/ }))
    fireEvent.click(await screen.findByRole('checkbox'))
    fireEvent.click(screen.getByRole('button', { name: 'Delete from disk' }))
    await waitFor(() => expect(api.deleteBookFile).toHaveBeenCalledWith(42, '?format=ebook'))
  })

  // The old format switcher told you a wanted format was "Not downloaded";
  // the list must too, or the missing half of a dual-format book vanishes.
  it('shows a Not downloaded placeholder for a wanted format with no file', async () => {
    vi.mocked(api.getBook).mockResolvedValue(
      makeBook({
        mediaType: 'both',
        bookFiles: [makeFile({ id: 1, format: 'ebook', path: '/library/book.epub' })],
      }),
    )
    renderBookDetailPage()

    const audio = within(await screen.findByTestId('file-group-audiobook'))
    expect(audio.getByText('Not downloaded')).toBeInTheDocument()
    // A placeholder offers no actions — nothing to download or delete.
    expect(audio.queryByRole('link', { name: 'Download' })).toBeNull()
    expect(audio.queryByRole('button', { name: /Delete file/ })).toBeNull()
    // The format that IS on disk renders normally next to it.
    expect(within(screen.getByTestId('file-group-ebook')).getByText('/library/book.epub')).toBeInTheDocument()
  })

  it('renders no placeholder for a format the book does not want', async () => {
    vi.mocked(api.getBook).mockResolvedValue(
      makeBook({
        mediaType: 'ebook',
        bookFiles: [makeFile({ id: 1, format: 'ebook', path: '/library/book.epub' })],
      }),
    )
    renderBookDetailPage()
    await screen.findByTestId('file-group-ebook')
    expect(screen.queryByTestId('file-group-audiobook')).toBeNull()
  })

  it('keeps the plain no-file line when nothing at all is on disk', async () => {
    vi.mocked(api.getBook).mockResolvedValue(makeBook({ mediaType: 'both' }))
    renderBookDetailPage()
    expect(await screen.findByText('No file on disk')).toBeInTheDocument()
    expect(screen.queryByTestId('file-group-ebook')).toBeNull()
    expect(screen.queryByTestId('file-group-audiobook')).toBeNull()
  })

  it('marks only the clicked row as Copied', async () => {
    vi.mocked(api.getBook).mockResolvedValue(
      makeBook({
        bookFiles: [
          makeFile({ id: 1, format: 'ebook', path: '/library/book.epub' }),
          makeFile({ id: 2, format: 'ebook', path: '/library/book.mobi' }),
        ],
      }),
    )
    renderBookDetailPage()
    await screen.findByTestId('file-group-ebook')

    const copyButtons = screen.getAllByRole('button', { name: /Copy file path/ })
    expect(copyButtons).toHaveLength(2)
    fireEvent.click(copyButtons[0])
    await waitFor(() => expect(copyButtons[0]).toHaveTextContent('Copied'))
    expect(copyButtons[1]).toHaveTextContent('Copy')
    expect(copyButtons[1]).not.toHaveTextContent('Copied')
  })

  it('renders legacy per-format paths when book_files is empty', async () => {
    vi.mocked(api.getBook).mockResolvedValue(
      makeBook({
        mediaType: 'both',
        ebookFilePath: '/library/legacy.epub',
        audiobookFilePath: '/library/legacy-audio',
      }),
    )
    renderBookDetailPage()

    expect(await screen.findByText('/library/legacy.epub')).toBeInTheDocument()
    expect(screen.getByText('/library/legacy-audio')).toBeInTheDocument()
  })

  // The legacy single file_path carries no format. The server infers it by
  // stat-ing the path; the browser cannot, so the media type is the proxy.
  it('labels a legacy single file_path by the book media type', async () => {
    vi.mocked(api.getBook).mockResolvedValue(
      makeBook({ mediaType: 'audiobook', filePath: '/library/legacy-audio' }),
    )
    renderBookDetailPage()

    expect(await screen.findByTestId('file-group-audiobook')).toBeInTheDocument()
    expect(screen.queryByTestId('file-group-ebook')).toBeNull()
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

describe('BookDetailPage — live import polling (#1161)', () => {
  it('refreshes the book and surfaces the file when an import completes, without a reload', async () => {
    vi.useFakeTimers()
    try {
      const downloading = makeBook({ status: 'downloading', mediaType: 'audiobook', audiobookFilePath: '' })
      const imported = makeBook({ status: 'imported', mediaType: 'audiobook', audiobookFilePath: '/lib/leviathan.m4b' })
      vi.mocked(api.getBook).mockResolvedValueOnce(downloading).mockResolvedValue(imported)

      renderBookDetailPage()

      // Initial load: downloading, no file on disk yet.
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0)
      })
      expect(vi.mocked(api.getBook).mock.calls.length).toBe(1)
      expect(screen.queryByText('/lib/leviathan.m4b')).not.toBeInTheDocument()

      // The background import finishes; the 5s poll picks it up live.
      await act(async () => {
        await vi.advanceTimersByTimeAsync(5000)
      })
      expect(vi.mocked(api.getBook).mock.calls.length).toBeGreaterThan(1)
      expect(screen.getByText('/lib/leviathan.m4b')).toBeInTheDocument()

      // Once the book settles (imported), polling stops — no further fetches.
      const settledCalls = vi.mocked(api.getBook).mock.calls.length
      await act(async () => {
        await vi.advanceTimersByTimeAsync(15000)
      })
      expect(vi.mocked(api.getBook).mock.calls.length).toBe(settledCalls)
    } finally {
      vi.useRealTimers()
    }
  })

  it('does not poll a settled (wanted) book', async () => {
    vi.useFakeTimers()
    try {
      vi.mocked(api.getBook).mockResolvedValue(makeBook({ status: 'wanted' }))
      renderBookDetailPage()
      await vi.advanceTimersByTimeAsync(0)
      const initial = vi.mocked(api.getBook).mock.calls.length
      await vi.advanceTimersByTimeAsync(15000)
      expect(vi.mocked(api.getBook).mock.calls.length).toBe(initial)
    } finally {
      vi.useRealTimers()
    }
  })
})

// Regression guard for the File card's two-column grid.
//
// Separating the two tracks with a comma instead of an underscore compiles to
// `grid-template-columns:92px<comma>1fr`, which is invalid CSS — every browser
// drops the declaration and the card silently collapses to a single track. The
// class name still *looks* right in the DOM and the build still succeeds, so
// nothing catches it. jsdom's CSS parser rejects the same value a browser does,
// so round-tripping the arbitrary value through a style declaration reproduces
// the real failure without a build.
//
// NB: the broken class is deliberately not spelled out literally anywhere in
// this repo. Tailwind scans raw source text, comments included, so writing it
// out re-emits the invalid rule into the shipped CSS.
describe('BookDetailPage — File card grid (regression: invalid arbitrary value)', () => {
  // Pull the grid-cols-[…] arbitrary value off the File card and resolve it the
  // way a browser would: assign it, then read back what survived parsing.
  function computedTracks(el: Element): string {
    const cls = Array.from(el.classList).find(c => c.startsWith('grid-cols-['))
    if (!cls) throw new Error(`no grid-cols-[…] class on ${el.className}`)
    const raw = cls.slice('grid-cols-['.length, -1).replace(/_/g, ' ')
    const probe = document.createElement('div')
    probe.style.gridTemplateColumns = raw
    return probe.style.gridTemplateColumns
  }

  it('declares two grid tracks that survive CSS parsing', async () => {
    const { container } = renderBookDetailPage()
    await screen.findByText(resolveKey('bookDetail.fileHeading')!)

    const grid = container.querySelector('[class*="grid-cols-["]')
    expect(grid, 'File card grid element').not.toBeNull()

    const tracks = computedTracks(grid!)
    // A comma in the arbitrary value makes this empty — the browser drops it.
    expect(tracks, 'grid-template-columns was rejected by the CSS parser').not.toBe('')
    expect(tracks.split(/\s+/).filter(Boolean)).toHaveLength(2)
  })

  it('uses underscore, not comma, as the arbitrary-value separator', async () => {
    const { container } = renderBookDetailPage()
    await screen.findByText(resolveKey('bookDetail.fileHeading')!)

    const grid = container.querySelector('[class*="grid-cols-["]')
    expect(grid!.className).not.toMatch(/grid-cols-\[[^\]]*,/)
  })
})

describe('BookDetailPage — header', () => {
  it('surfaces series name and position in the meta row', async () => {
    // series_books has been populated since v0.7.0; the page never showed it.
    vi.mocked(api.listAuthorSeries).mockResolvedValue([
      {
        id: 7,
        foreignSeriesId: 'ol:s7',
        title: 'Discworld',
        description: '',
        monitored: true,
        books: [{ seriesId: 7, bookId: 42, positionInSeries: '3' }],
      },
      // A series the author is in but this book is not — must not appear.
      {
        id: 8,
        foreignSeriesId: 'ol:s8',
        title: 'Long Earth',
        description: '',
        monitored: true,
        books: [{ seriesId: 8, bookId: 99, positionInSeries: '1' }],
      },
    ] as unknown as Awaited<ReturnType<typeof api.listAuthorSeries>>)

    renderBookDetailPage()
    expect(await screen.findByText('Discworld #3')).toBeInTheDocument()
    expect(screen.queryByText(/Long Earth/)).toBeNull()
  })

  it('renders no series row when the book is in none', async () => {
    vi.mocked(api.listAuthorSeries).mockResolvedValue([])
    renderBookDetailPage()
    await screen.findByRole('heading', { name: 'The Final Empire' })
    expect(screen.queryByText(/#\d/)).toBeNull()
  })

  it('survives the series lookup failing', async () => {
    vi.mocked(api.listAuthorSeries).mockRejectedValue(new Error('boom'))
    renderBookDetailPage()
    // The page still renders; series is simply absent.
    expect(
      await screen.findByRole('heading', { name: 'The Final Empire' }),
    ).toBeInTheDocument()
    expect(screen.queryByText(/#\d/)).toBeNull()
  })

  it('puts Edit in the header, not the File card', async () => {
    renderBookDetailPage()
    const edit = await screen.findByRole('button', { name: /Edit/ })
    const fileHeading = screen.getByText(resolveKey('bookDetail.fileHeading')!)
    // Edit edits metadata, so it must sit above the File section in the DOM.
    expect(
      edit.compareDocumentPosition(fileHeading) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy()
  })

  it('keeps solid red for Delete book and ghost-danger for Delete file', async () => {
    vi.mocked(api.getBook).mockResolvedValue(makeBook({ filePath: '/library/book.epub' }))
    renderBookDetailPage()
    const deleteFile = await screen.findByRole('button', { name: /Delete file/ })
    const deleteBook = screen.getByRole('button', { name: /Delete book/ })
    // Solid red is reserved for the irreversible one.
    expect(deleteBook.className).toContain('bg-red-600')
    expect(deleteFile.className).not.toContain('bg-red-600')
  })
})

// #1707: the page never said which provider its metadata came from, so with
// OpenLibrary, Hardcover, Google Books and DNB all in play a user could not
// tell what record they were looking at or whether to re-bind. The identifier
// is the requirement and it has to be copyable; a link out is a bonus for the
// providers whose public URL can be built from the stored id.
describe('BookDetailPage metadata source (#1707)', () => {
  it('names the provider and shows the id it is bound to', async () => {
    vi.mocked(api.getBook).mockResolvedValue(
      makeBook({ foreignBookId: 'hc:12345', metadataProvider: 'hardcover' }),
    )
    renderBookDetailPage()

    const list = await screen.findByTestId('metadata-source-list')
    expect(within(list).getByText('Hardcover')).toBeInTheDocument()
    expect(within(list).getByText('hc:12345')).toBeInTheDocument()
  })

  it('falls back to the foreign-id prefix when the provider column is empty', async () => {
    vi.mocked(api.getBook).mockResolvedValue(
      makeBook({ foreignBookId: 'dnb:1234567890', metadataProvider: '' }),
    )
    renderBookDetailPage()

    const list = await screen.findByTestId('metadata-source-list')
    expect(within(list).getByText('DNB')).toBeInTheDocument()
    expect(within(list).getByText('dnb:1234567890')).toBeInTheDocument()
  })

  it('copies an id to the clipboard', async () => {
    vi.mocked(api.getBook).mockResolvedValue(
      makeBook({ foreignBookId: 'OL27448W', metadataProvider: 'openlibrary' }),
    )
    renderBookDetailPage()

    const list = await screen.findByTestId('metadata-source-list')
    await act(async () => {
      fireEvent.click(within(list).getByRole('button', { name: 'Copy OL27448W' }))
    })

    expect(navigator.clipboard.writeText).toHaveBeenCalledWith('OL27448W')
    await waitFor(() => expect(within(list).getByText('Copied')).toBeInTheDocument())
  })

  it('links out only for a provider whose public URL is constructible', async () => {
    vi.mocked(api.getBook).mockResolvedValue(
      makeBook({ foreignBookId: 'OL27448W', metadataProvider: 'openlibrary' }),
    )
    renderBookDetailPage()

    const list = await screen.findByTestId('metadata-source-list')
    expect(within(list).getByRole('link', { name: /View on OpenLibrary/ })).toHaveAttribute(
      'href',
      'https://openlibrary.org/works/OL27448W',
    )
  })

  it('keeps an ambiguous numeric Hardcover id visible without linking it', async () => {
    vi.mocked(api.getBook).mockResolvedValue(
      makeBook({ foreignBookId: 'hc:12345', metadataProvider: 'hardcover' }),
    )
    renderBookDetailPage()

    const list = await screen.findByTestId('metadata-source-list')
    expect(within(list).getByText('hc:12345')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Links' })).not.toBeInTheDocument()
  })

  // The identity map from #1705 is what makes "which record am I looking at"
  // answerable when the same book has been resolved through two providers.
  it('lists every other provider id the same book is known by', async () => {
    vi.mocked(api.getBook).mockResolvedValue(
      makeBook({
        foreignBookId: 'OL27448W',
        metadataProvider: 'openlibrary',
        identifiers: [
          {
            bookId: 42,
            provider: 'openlibrary',
            foreignBookId: 'OL27448W',
            createdAt: '2026-01-01T00:00:00Z',
            updatedAt: '2026-01-01T00:00:00Z',
          },
          {
            bookId: 42,
            provider: 'hardcover',
            foreignBookId: 'hc:12345',
            createdAt: '2026-01-01T00:00:00Z',
            updatedAt: '2026-01-01T00:00:00Z',
          },
        ],
      }),
    )
    renderBookDetailPage()

    const list = await screen.findByTestId('metadata-source-list')
    const rows = within(list).getAllByRole('listitem')
    // The primary id is not repeated even though the map also carries it.
    expect(rows).toHaveLength(2)
    expect(within(rows[0]).getByText('OpenLibrary')).toBeInTheDocument()
    expect(within(rows[0]).getByText('Current')).toBeInTheDocument()
    expect(within(rows[1]).getByText('Hardcover')).toBeInTheDocument()
    expect(within(rows[1]).getByText('hc:12345')).toBeInTheDocument()
    expect(within(rows[1]).queryByText('Current')).not.toBeInTheDocument()
  })

  it('shows trustworthy upstream links in an operable disclosure', async () => {
    vi.mocked(api.getBook).mockResolvedValue(
      makeBook({
        foreignBookId: 'OL27448W',
        metadataProvider: 'openlibrary',
        identifiers: [
          {
            bookId: 42,
            provider: 'hardcover',
            foreignBookId: 'hc:project-hail-mary',
            createdAt: '2026-01-01T00:00:00Z',
            updatedAt: '2026-01-01T00:00:00Z',
          },
          {
            bookId: 42,
            provider: 'dnb',
            foreignBookId: 'dnb:1234567890',
            createdAt: '2026-01-01T00:00:00Z',
            updatedAt: '2026-01-01T00:00:00Z',
          },
          {
            bookId: 42,
            provider: 'audiobookshelf',
            foreignBookId: 'abs:local-item',
            createdAt: '2026-01-01T00:00:00Z',
            updatedAt: '2026-01-01T00:00:00Z',
          },
        ],
      }),
    )
    renderBookDetailPage()

    const trigger = await screen.findByRole('button', { name: 'Links' })
    expect(trigger).not.toHaveAttribute('aria-haspopup')
    expect(trigger).toHaveAttribute('aria-controls')
    expect(trigger).toHaveAttribute('aria-expanded', 'false')
    fireEvent.mouseEnter(trigger.parentElement!)
    expect(trigger).toHaveAttribute('aria-expanded', 'true')
    fireEvent.click(trigger)
    fireEvent.mouseLeave(trigger.parentElement!)
    expect(trigger).toHaveAttribute('aria-expanded', 'true')
    fireEvent.mouseEnter(trigger.parentElement!)
    fireEvent.click(trigger)
    expect(trigger).toHaveAttribute('aria-expanded', 'true')
    fireEvent.mouseLeave(trigger.parentElement!)
    expect(trigger).toHaveAttribute('aria-expanded', 'false')
    fireEvent.focus(trigger)
    expect(trigger).toHaveAttribute('aria-expanded', 'false')
    fireEvent.click(trigger)
    expect(trigger).toHaveAttribute('aria-expanded', 'true')
    const menu = screen.getByTestId('book-links-menu')
    expect(trigger).toHaveAttribute('aria-controls', menu.id)
    expect(menu).not.toHaveAttribute('hidden')
    const links = within(menu).getAllByRole('link')
    expect(links).toHaveLength(3)
    expect(within(menu).getByRole('link', { name: /View on OpenLibrary/ })).toHaveAttribute(
      'href',
      'https://openlibrary.org/works/OL27448W',
    )
    expect(within(menu).getByRole('link', { name: /View on Hardcover/ })).toHaveAttribute(
      'href',
      'https://hardcover.app/books/project-hail-mary',
    )
    const dnb = within(menu).getByRole('link', { name: /View on DNB/ })
    expect(dnb).toHaveAttribute('href', 'https://d-nb.info/1234567890')
    expect(dnb).toHaveAttribute('target', '_blank')
    expect(dnb).toHaveAttribute('rel', 'noopener noreferrer')
    fireEvent.keyDown(dnb, { key: 'Escape' })
    expect(trigger).toHaveAttribute('aria-expanded', 'false')
    expect(menu).toHaveAttribute('hidden')
    expect(trigger).toHaveFocus()
  })

  it('hides the Links control when no trustworthy upstream URL exists', async () => {
    vi.mocked(api.getBook).mockResolvedValue(
      makeBook({ foreignBookId: 'abs:local-item', metadataProvider: 'audiobookshelf' }),
    )
    renderBookDetailPage()

    await screen.findByTestId('metadata-source-list')
    expect(screen.queryByRole('button', { name: 'Links' })).not.toBeInTheDocument()
  })
})
