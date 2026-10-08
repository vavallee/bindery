import { useEffect } from 'react'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { http, HttpResponse } from 'msw'
import { MemoryRouter, Route, Routes, useLocation, useNavigate, useNavigationType, type NavigateFunction } from 'react-router'
import BooksPage from './BooksPage'
import AuthorsPage from './AuthorsPage'
import WantedPage from './WantedPage'
import { apiUrl, server } from '../test/msw'
import { api, type Author, type Book } from '../api/client'

// #3052: list state (page, search, filters, sort) lived in useState, so going
// back from a detail page (the Android back gesture, the iOS swipe) landed on
// page 1 with an empty search. These tests drive the real pages through a
// MemoryRouter: open a detail route, go back, and check the list comes back
// as it was, that defaults keep the URL clean, and that typing in the search
// box replaces the history entry instead of pushing one per keystroke.

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, options?: string | Record<string, unknown>) => {
      const labels: Record<string, string> = {
        'common.all': 'All',
        'books.statusWanted': 'Wanted',
        'books.sortTitleAZ': 'A-Z',
        'books.sortTitleZA': 'Z-A',
        'books.searchPlaceholder': 'Search books',
        'authors.searchPlaceholder': 'Search authors',
        'wanted.searchPlaceholder': 'Search wanted',
        'wanted.showExcluded': 'Show excluded',
      }
      if (labels[key]) return labels[key]
      if (typeof options === 'string') return options
      return key
    },
  }),
}))

function makeBook(id: number): Book {
  return {
    id,
    foreignBookId: `book-${id}`,
    authorId: 1,
    title: `Book ${id}`,
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
    author: undefined,
  }
}

function makeAuthor(id: number): Author {
  return {
    id,
    foreignAuthorId: `author-${id}`,
    authorName: `Author ${id}`,
    sortName: `Author ${id}`,
    description: '',
    imageUrl: '',
    disambiguation: '',
    ratingsCount: 0,
    averageRating: 0,
    monitored: true,
    createdAt: '2024-01-01T00:00:00Z',
    updatedAt: '2024-01-01T00:00:00Z',
  } as Author
}

type Seen = { pathname: string; search: string; type: string }
let seen: Seen[] = []
let nav: NavigateFunction

function Probe() {
  const location = useLocation()
  const type = useNavigationType()
  const navigate = useNavigate()
  nav = navigate
  useEffect(() => {
    seen.push({ pathname: location.pathname, search: location.search, type })
  }, [location, type])
  return null
}

const current = () => seen[seen.length - 1]

function renderAt(entries: string[], routes: React.ReactNode) {
  return render(
    <MemoryRouter initialEntries={entries} initialIndex={entries.length - 1}>
      <Probe />
      <Routes>
        {routes}
        <Route path="/start" element={<div>start page</div>} />
        <Route path="/book/:id" element={<div>book detail</div>} />
        <Route path="/author/:id" element={<div>author detail</div>} />
      </Routes>
    </MemoryRouter>,
  )
}

async function goTo(path: string) {
  await act(async () => { await nav(path) })
}
async function goBack() {
  await act(async () => { await nav(-1) })
}

beforeEach(() => {
  seen = []
  server.use(
    http.get(apiUrl('/indexer'), () => HttpResponse.json([])),
    http.get(apiUrl('/downloadclient'), () => HttpResponse.json([])),
    http.get(apiUrl('/system/setup-state'), () => new HttpResponse(null, { status: 404 })),
  )
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('BooksPage list state in the URL', () => {
  const booksRoute = <Route path="/books" element={<BooksPage />} />

  beforeEach(() => {
    vi.spyOn(api, 'listBooks').mockImplementation(async ({ offset = 0 } = {}) => ({
      items: [makeBook(offset + 1)],
      total: 150,
      limit: 50,
      offset,
    }))
  })

  const lastListArgs = () => {
    const calls = vi.mocked(api.listBooks).mock.calls
    return calls[calls.length - 1][0]
  }

  it('comes back to the same page and search after opening a book', async () => {
    renderAt(['/books'], booksRoute)
    fireEvent.change(await screen.findByPlaceholderText('Search books'), { target: { value: 'dune' } })
    await waitFor(() => expect(current().search).toBe('?q=dune'))

    fireEvent.click(await screen.findByRole('button', { name: '3' }))
    await waitFor(() => expect(current().search).toBe('?q=dune&page=3'))
    await waitFor(() => expect(lastListArgs()).toMatchObject({ offset: 100, search: 'dune' }))

    await goTo('/book/101')
    expect(await screen.findByText('book detail')).toBeInTheDocument()
    vi.mocked(api.listBooks).mockClear()

    await goBack()
    expect(await screen.findByPlaceholderText('Search books')).toHaveValue('dune')
    await waitFor(() => expect(lastListArgs()).toMatchObject({ offset: 100, search: 'dune' }))
    // The page from the URL is fetched straight away, not page 1 first.
    expect(vi.mocked(api.listBooks).mock.calls.every(([args]) => args?.offset === 100)).toBe(true)
    expect(current().search).toBe('?q=dune&page=3')
  })

  it('opens on the page and filters a link points to', async () => {
    renderAt(['/books?status=wanted&sort=title-za&page=2'], booksRoute)
    await waitFor(() => expect(lastListArgs()).toMatchObject({ offset: 50, status: 'wanted', sort: 'title-za' }))
    expect(current().search).toBe('?status=wanted&sort=title-za&page=2')
  })

  it('pushes filter changes and leaves defaults out of the URL', async () => {
    renderAt(['/books'], booksRoute)
    await screen.findByPlaceholderText('Search books')
    expect(current().search).toBe('')

    fireEvent.click(screen.getByRole('button', { name: 'Wanted' }))
    await waitFor(() => expect(current()).toMatchObject({ search: '?status=wanted', type: 'PUSH' }))

    fireEvent.click(screen.getByRole('button', { name: 'All' }))
    await waitFor(() => expect(current().search).toBe(''))
    await waitFor(() => expect(lastListArgs()).toMatchObject({ status: undefined, offset: 0 }))

    // Back steps through the filter change rather than leaving the page.
    await goBack()
    await waitFor(() => expect(current()).toMatchObject({ pathname: '/books', search: '?status=wanted' }))
  })

  it('drops the page when a filter changes, and back restores it', async () => {
    renderAt(['/books?page=3'], booksRoute)
    await waitFor(() => expect(lastListArgs()).toMatchObject({ offset: 100 }))
    fireEvent.click(screen.getByRole('button', { name: 'Wanted' }))
    await waitFor(() => expect(current().search).toBe('?status=wanted'))
    await waitFor(() => expect(lastListArgs()).toMatchObject({ offset: 0, status: 'wanted' }))
    await goBack()
    await waitFor(() => expect(lastListArgs()).toMatchObject({ offset: 100, status: undefined }))
  })

  it('keeps the page when back crosses a filter change whose total was smaller', async () => {
    vi.mocked(api.listBooks).mockImplementation(async ({ offset = 0, status } = {}) => ({
      items: [makeBook(offset + 1)],
      total: status === 'wanted' ? 10 : 150,
      limit: 50,
      offset,
    }))
    renderAt(['/books?page=3'], booksRoute)
    expect((await screen.findAllByText('Book 101')).length).toBeGreaterThan(0)

    fireEvent.click(screen.getByRole('button', { name: 'Wanted' }))
    expect((await screen.findAllByText('Book 1')).length).toBeGreaterThan(0)
    expect(current().search).toBe('?status=wanted')

    // The wanted list's total (10, one page) must not clamp page 3 of the
    // unfiltered list on the way back.
    await goBack()
    expect((await screen.findAllByText('Book 101')).length).toBeGreaterThan(0)
    await new Promise(r => setTimeout(r, 50))
    expect(current().search).toBe('?page=3')
    expect(lastListArgs()).toMatchObject({ offset: 100, status: undefined })
  })

  it('does not rewrite the page after a failed fetch', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    vi.mocked(api.listBooks).mockRejectedValue(new Error('boom'))
    renderAt(['/books?page=3'], booksRoute)
    await waitFor(() => expect(vi.mocked(api.listBooks)).toHaveBeenCalled())
    await new Promise(r => setTimeout(r, 50))
    expect(current().search).toBe('?page=3')
    expect(seen.every(s => s.type !== 'REPLACE')).toBe(true)
  })

  it('clears the bulk selection when the page or a filter changes', async () => {
    renderAt(['/books'], booksRoute)
    fireEvent.click(await screen.findByTitle('Select Book 1'))
    expect(screen.getByTitle('Select Book 1')).toBeChecked()

    fireEvent.click(screen.getByRole('button', { name: '2' }))
    expect((await screen.findAllByText('Book 51')).length).toBeGreaterThan(0)
    await goBack()
    expect(await screen.findByTitle('Select Book 1')).not.toBeChecked()

    fireEvent.click(screen.getByTitle('Select Book 1'))
    expect(screen.getByTitle('Select Book 1')).toBeChecked()
    fireEvent.click(screen.getByRole('button', { name: 'Wanted' }))
    await waitFor(() => expect(current().search).toBe('?status=wanted'))
    expect(await screen.findByTitle('Select Book 1')).not.toBeChecked()
  })

  it('replaces the history entry while typing instead of pushing one per keystroke', async () => {
    renderAt(['/start', '/books'], booksRoute)
    const box = await screen.findByPlaceholderText('Search books')
    for (const value of ['d', 'du', 'dun', 'dune']) {
      fireEvent.change(box, { target: { value } })
    }
    await waitFor(() => expect(current().search).toBe('?q=dune'))
    fireEvent.change(box, { target: { value: 'dune messiah' } })
    await waitFor(() => expect(current().search).toBe('?q=dune+messiah'))
    expect(seen.slice(1).every(s => s.type === 'REPLACE')).toBe(true)

    // One step back leaves the list: no entry per keystroke to wade through.
    await goBack()
    await waitFor(() => expect(current().pathname).toBe('/start'))
  })
})

describe('AuthorsPage list state in the URL', () => {
  beforeEach(() => {
    vi.spyOn(api, 'listAuthors').mockImplementation(async ({ offset = 0 } = {}) => ({
      items: [makeAuthor(offset + 1)],
      total: 120,
      limit: 50,
      offset,
    }))
    vi.spyOn(api, 'refreshAllAuthorsStatus').mockResolvedValue(null as never)
  })

  it('comes back to the same page, search and sort after opening an author', async () => {
    renderAt(['/?q=le+guin&sort=za&page=2'], <Route path="/" element={<AuthorsPage />} />)
    await waitFor(() => expect(vi.mocked(api.listAuthors)).toHaveBeenLastCalledWith(
      expect.objectContaining({ offset: 50, search: 'le guin', sort: 'za' }),
    ))
    await goTo('/author/51')
    expect(await screen.findByText('author detail')).toBeInTheDocument()
    vi.mocked(api.listAuthors).mockClear()

    await goBack()
    expect(await screen.findByPlaceholderText('Search authors')).toHaveValue('le guin')
    await waitFor(() => expect(vi.mocked(api.listAuthors)).toHaveBeenLastCalledWith(
      expect.objectContaining({ offset: 50, search: 'le guin', sort: 'za' }),
    ))
    expect(current().search).toBe('?q=le+guin&sort=za&page=2')
  })
})

describe('AuthorsPage bulk selection', () => {
  it('clears the selection when the page changes', async () => {
    vi.spyOn(api, 'listAuthors').mockImplementation(async ({ offset = 0 } = {}) => ({
      items: [makeAuthor(offset + 1)],
      total: 120,
      limit: 50,
      offset,
    }))
    vi.spyOn(api, 'refreshAllAuthorsStatus').mockResolvedValue(null as never)
    renderAt(['/'], <Route path="/" element={<AuthorsPage />} />)
    fireEvent.click(await screen.findByTitle('Select Author 1'))
    expect(screen.getByTitle('Select Author 1')).toBeChecked()
    fireEvent.click(screen.getByRole('button', { name: '2' }))
    await waitFor(() => expect(current().search).toBe('?page=2'))
    await goBack()
    expect(await screen.findByTitle('Select Author 1')).not.toBeChecked()
  })
})

describe('WantedPage list state in the URL', () => {
  beforeEach(() => {
    vi.spyOn(api, 'listWanted').mockResolvedValue(Array.from({ length: 120 }, (_, i) => makeBook(i + 1)))
  })

  it('comes back to the same page and search after opening a book', async () => {
    renderAt(['/wanted'], <Route path="/wanted" element={<WantedPage />} />)
    await screen.findByText('Book 1')
    fireEvent.click(screen.getByRole('button', { name: '3' }))
    await waitFor(() => expect(current()).toMatchObject({ search: '?page=3', type: 'PUSH' }))
    expect((await screen.findAllByText('Book 101')).length).toBeGreaterThan(0)

    await goTo('/book/101')
    expect(await screen.findByText('book detail')).toBeInTheDocument()
    await goBack()
    expect((await screen.findAllByText('Book 101')).length).toBeGreaterThan(0)
    expect(screen.queryByText('Book 1')).not.toBeInTheDocument()
  })

  it('clears the bulk selection when the page changes', async () => {
    renderAt(['/wanted'], <Route path="/wanted" element={<WantedPage />} />)
    await screen.findByText('Book 1')
    fireEvent.click(screen.getByLabelText('common.selectAllPage'))
    expect(screen.getByLabelText('common.selectAllPage')).toBeChecked()
    fireEvent.click(screen.getByRole('button', { name: '2' }))
    expect((await screen.findAllByText('Book 51')).length).toBeGreaterThan(0)
    expect(screen.getByLabelText('common.selectAllPage')).not.toBeChecked()
    await goBack()
    await screen.findByText('Book 1')
    expect(screen.getByLabelText('common.selectAllPage')).not.toBeChecked()
  })

  it('keeps the search and the excluded toggle in the URL', async () => {
    renderAt(['/wanted'], <Route path="/wanted" element={<WantedPage />} />)
    await screen.findByText('Book 1')
    fireEvent.click(screen.getByLabelText('Show excluded'))
    await waitFor(() => expect(current().search).toBe('?excluded=1'))
    fireEvent.change(screen.getByPlaceholderText('Search wanted'), { target: { value: 'Book 7' } })
    await waitFor(() => expect(current()).toMatchObject({ search: '?excluded=1&q=Book+7', type: 'REPLACE' }))

    await goTo('/book/7')
    await goBack()
    expect(await screen.findByPlaceholderText('Search wanted')).toHaveValue('Book 7')
    expect(screen.getByLabelText('Show excluded')).toBeChecked()
    expect(vi.mocked(api.listWanted)).toHaveBeenLastCalledWith({ includeExcluded: true })
  })
})
