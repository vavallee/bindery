import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { http, HttpResponse } from 'msw'
import { MemoryRouter } from 'react-router'
import BooksPage from './BooksPage'
import { apiUrl, server } from '../test/msw'
import { api, type Book } from '../api/client'

// BooksPage talks to the API through the real `api` client. Most tests mock at
// the network layer with MSW so the fetch/parse path is exercised end to end;
// the stale-response regression spies on listBooks only to control completion
// order precisely.
//
// i18n and Pagination are stubbed exactly like the other list-page tests
// (WantedPage/QueuePage/AuthorsPage) so the assertions can target stable,
// human-readable strings.
vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, options?: string | Record<string, unknown>) => {
      const labels: Record<string, string> = {
        'books.title': 'Books',
        'books.countLabel': 'books',
        'books.searchPlaceholder': 'Search by title or author...',
        'books.sortLabel': 'Sort:',
        'books.sortTitleAZ': 'A-Z',
        'books.sortTitleZA': 'Z-A',
        'books.sortNewest': 'Newest',
        'books.sortOldest': 'Oldest',
        'books.typeLabel': 'Type:',
        'books.empty': 'No books in your library yet',
        'books.emptyHint':
          'Add a book directly, or add an author to monitor their catalogue.',
        'books.noMatch': 'No books match your search.',
        'books.statusWanted': 'Wanted',
        'books.statusDownloading': 'Downloading',
        'books.statusImported': 'Imported',
        'books.statusSkipped': 'Skipped',
        'books.colTitle': 'Title',
        'books.colAuthor': 'Author',
        'books.colYear': 'Year',
        'books.colType': 'Type',
        'books.colStatus': 'Status',
        'addBookModal.title': 'Add Book',
        'addBookModal.description': 'Search by title, ISBN, or ASIN to add a specific book to your wanted list.',
        'addBookModal.searchPlaceholder': 'Title, ISBN, or ASIN',
        'addBookModal.searching': 'Searching...',
        'addBookModal.select': 'Select',
        'addBookModal.selectBook': `Select ${(options as Record<string, unknown> | undefined)?.title ?? ''}`,
        'addBookModal.confirmAdd': 'Add book',
        'addBookModal.backToResults': 'Back to results',
        'addBookModal.noCover': 'No cover',
        'addBookModal.format': 'Format',
        'addBookModal.formatLabel': 'Format to add',
        'addBookModal.formatHint': 'Choose which format to add',
        'addBookModal.defaultFormat': 'Default',
        'addBookModal.autoSearchLabel': 'Search indexers after adding',
        'addBookModal.autoSearchHint': 'Try to grab the book automatically after adding it to wanted.',
        'addBookModal.adding': 'Adding...',
        'common.all': 'All',
        'common.loading': 'Loading...',
        'common.ebook': 'Ebook',
        'common.audiobook': 'Audiobook',
        'common.both': 'Both',
        'common.search': 'Search',
        'common.cancel': 'Cancel',
      }
      if (labels[key]) return labels[key]
      if (typeof options === 'string') return options
      return key
    },
  }),
}))

// Pagination renders nothing here; its own behaviour is covered elsewhere and
// it is irrelevant to the data/empty/error states under test.
vi.mock('../components/Pagination', () => ({ default: () => null }))

function makeBook(overrides: Partial<Book> & Pick<Book, 'id' | 'title'>): Book {
  const { id, title, ...rest } = overrides
  return {
    id,
    foreignBookId: `book-${id}`,
    authorId: 1,
    title,
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
    author: undefined,
    ...rest,
  }
}

// useNeedsSetup fires these on mount; an empty config keeps the onboarding
// guidance out of the way so the empty-state assertion stays focused.
function stubSetupEndpoints() {
  server.use(
    http.get(apiUrl('/indexer'), () => HttpResponse.json([])),
    http.get(apiUrl('/downloadclient'), () => HttpResponse.json([])),
  )
}

function renderBooksPage() {
  return render(
    <MemoryRouter>
      <BooksPage />
    </MemoryRouter>,
  )
}

beforeEach(() => {
  document.title = 'Bindery'
  stubSetupEndpoints()
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('BooksPage', () => {
  it('renders book titles returned by the server', async () => {
    server.use(
      http.get(apiUrl('/book'), () =>
        HttpResponse.json({
          items: [
            makeBook({ id: 1, title: 'Dune' }),
            makeBook({ id: 2, title: 'Hyperion' }),
          ],
          total: 2,
          limit: 50,
          offset: 0,
        }),
      ),
    )

    renderBooksPage()

    // In the default grid view the title appears both as the card heading and
    // (for cover-less books) inside the placeholder, so assert via the heading.
    expect(
      await screen.findByRole('heading', { name: 'Dune' }),
    ).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Hyperion' })).toBeInTheDocument()
    // The empty state must not show when the library has books.
    expect(
      screen.queryByText('No books in your library yet'),
    ).not.toBeInTheDocument()
  })

  it('shows the empty-state copy and hint when the server returns no books', async () => {
    server.use(
      http.get(apiUrl('/book'), () =>
        HttpResponse.json({ items: [], total: 0, limit: 50, offset: 0 }),
      ),
    )

    renderBooksPage()

    expect(
      await screen.findByText('No books in your library yet'),
    ).toBeInTheDocument()
    expect(
      screen.getByText(
        'Add a book directly, or add an author to monitor their catalogue.',
      ),
    ).toBeInTheDocument()
    // The "no match" copy is for a filtered empty result, not a truly empty library.
    expect(screen.queryByText('No books match your search.')).not.toBeInTheDocument()
  })

  it('does not crash and leaves the page in a handled empty state when the server errors', async () => {
    // load()'s catch() swallows the error (console.error) and clears loading,
    // so a 500 must not throw; the page should settle out of the loading state
    // with no books rendered.
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {})
    server.use(
      http.get(apiUrl('/book'), () => new HttpResponse(null, { status: 500 })),
    )

    renderBooksPage()

    // The page header always renders; its presence proves no crash/unmount.
    expect(await screen.findByRole('heading', { name: 'Books' })).toBeInTheDocument()
    // Once the rejected request settles, the loading indicator is gone and the
    // empty-state copy is shown (books stays []).
    expect(
      await screen.findByText('No books in your library yet'),
    ).toBeInTheDocument()
    expect(screen.queryByText('Loading...')).not.toBeInTheDocument()
    expect(consoleError).toHaveBeenCalled()

    consoleError.mockRestore()
  })

  it('opens Add Book from the page and reloads after a successful add', async () => {
    let listCalls = 0
    server.use(
      http.get(apiUrl('/book'), () => {
        listCalls++
        return HttpResponse.json({ items: [], total: 0, limit: 50, offset: 0 })
      }),
      http.get(apiUrl('/search/book'), () => HttpResponse.json([
        makeBook({ id: 0, title: 'Dune', foreignBookId: 'OL1W' }),
      ])),
      http.post(apiUrl('/author/book'), () => HttpResponse.json(
        makeBook({ id: 1, title: 'Dune', foreignBookId: 'OL1W' }),
      )),
    )

    renderBooksPage()
    await screen.findByText('No books in your library yet')

    fireEvent.click(screen.getByRole('button', { name: 'Add Book' }))
    expect(screen.getByRole('dialog', { name: 'Add Book' })).toBeInTheDocument()

    fireEvent.change(screen.getByPlaceholderText('Title, ISBN, or ASIN'), { target: { value: 'Dune' } })
    fireEvent.click(screen.getByRole('button', { name: 'Search' }))
    await screen.findByText('Dune')
    fireEvent.click(screen.getByRole('button', { name: 'Select Dune' }))
    fireEvent.click(screen.getByRole('button', { name: 'Add book' }))

    await waitFor(() => expect(listCalls).toBeGreaterThanOrEqual(2))
    expect(screen.queryByRole('dialog', { name: 'Add Book' })).not.toBeInTheDocument()
  })

  it('ignores an older list response while the post-add refresh is pending', async () => {
    let resolveInitial!: (page: Awaited<ReturnType<typeof api.listBooks>>) => void
    let resolveRefresh!: (page: Awaited<ReturnType<typeof api.listBooks>>) => void
    const initial = new Promise<Awaited<ReturnType<typeof api.listBooks>>>(resolve => {
      resolveInitial = resolve
    })
    const refresh = new Promise<Awaited<ReturnType<typeof api.listBooks>>>(resolve => {
      resolveRefresh = resolve
    })
    const addedBook = makeBook({ id: 1, title: 'Dune', foreignBookId: 'OL1W' })
    const listBooks = vi.spyOn(api, 'listBooks')
      .mockReturnValueOnce(initial)
      .mockReturnValueOnce(refresh)
    server.use(
      http.get(apiUrl('/search/book'), () => HttpResponse.json([
        makeBook({ id: 0, title: 'Dune', foreignBookId: 'OL1W' }),
      ])),
      http.post(apiUrl('/author/book'), () => HttpResponse.json(addedBook)),
    )

    renderBooksPage()
    fireEvent.click(screen.getByRole('button', { name: 'Add Book' }))
    fireEvent.change(screen.getByPlaceholderText('Title, ISBN, or ASIN'), { target: { value: 'Dune' } })
    fireEvent.click(screen.getByRole('button', { name: 'Search' }))
    await screen.findByText('Dune')
    fireEvent.click(screen.getByRole('button', { name: 'Select Dune' }))
    fireEvent.click(screen.getByRole('button', { name: 'Add book' }))

    await waitFor(() => expect(listBooks).toHaveBeenCalledTimes(2))
    expect(screen.getByText('Loading...')).toBeInTheDocument()

    await act(async () => {
      resolveInitial({
        items: [makeBook({ id: 99, title: 'Stale result' })],
        total: 1,
        limit: 50,
        offset: 0,
      })
    })
    expect(screen.queryByRole('heading', { name: 'Stale result' })).not.toBeInTheDocument()
    expect(screen.getByText('Loading...')).toBeInTheDocument()

    await act(async () => {
      resolveRefresh({ items: [addedBook], total: 1, limit: 50, offset: 0 })
    })
    expect(screen.getByRole('heading', { name: 'Dune' })).toBeInTheDocument()
    expect(screen.queryByText('Loading...')).not.toBeInTheDocument()
  })
})

// Column-header sorting on the Books table. Reported by a user on v1.30.1 as
// "book titles are sortable by published date but not type or status" — the
// sort works and has since v1.28.0 (#1349), but nothing on screen said so:
// the Sort toolbar above the table lists only title and date, Tailwind v4's
// Preflight gives <button> no pointer cursor, and the ▲/▼ marker only appeared
// on the column already being sorted. An unsorted Type header was therefore
// indistinguishable from plain text.
describe('BooksPage — sortable column headers', () => {
  beforeEach(() => {
    // The table view is where the headers live; the page defaults to grid.
    localStorage.setItem('bindery.view.books', 'table')
  })

  afterEach(() => {
    localStorage.clear()
  })

  it('sorts by type and status from the column headers, not just title and date', async () => {
    const sorts: string[] = []
    server.use(
      http.get(apiUrl('/book'), ({ request }) => {
        sorts.push(new URL(request.url).searchParams.get('sort') ?? '')
        return HttpResponse.json({
          items: [makeBook({ id: 1, title: 'Dune' })],
          total: 1,
          limit: 50,
          offset: 0,
        })
      }),
    )

    renderBooksPage()
    await screen.findByRole('button', { name: 'Type' })

    // Re-query before every click. SortableHeader is declared inside the
    // component body, so each render produces a new component identity and
    // React remounts the header cells — a node captured before a click is
    // detached by the time the next one lands.
    const click = (name: string) =>
      fireEvent.click(screen.getByRole('button', { name }))

    click('Type')
    await waitFor(() => expect(sorts).toContain('type-az'))
    // A second click on the same column flips the direction.
    click('Type')
    await waitFor(() => expect(sorts).toContain('type-za'))

    click('Status')
    await waitFor(() => expect(sorts).toContain('status-az'))
  })

  it('marks every sortable header as clickable, not only the active one', async () => {
    server.use(
      http.get(apiUrl('/book'), () =>
        HttpResponse.json({
          items: [makeBook({ id: 1, title: 'Dune' })],
          total: 1,
          limit: 50,
          offset: 0,
        }),
      ),
    )

    renderBooksPage()

    // Title is the default sort, so it is the active column; Type and Status
    // are inactive. All three must still look interactive — that inactive
    // columns looked inert is the whole of the reported bug.
    for (const name of ['Title', 'Type', 'Status']) {
      const header = await screen.findByRole('button', { name })
      expect(header.className).toContain('cursor-pointer')
      expect(header).toHaveAttribute('title')
    }
  })
})
