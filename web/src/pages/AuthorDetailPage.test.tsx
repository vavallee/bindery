import { useEffect } from 'react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom'
import AuthorDetailPage from './AuthorDetailPage'
import { api } from '../api/client'
import type { Author, Book } from '../api/client'
import '../i18n'

vi.mock('../api/client', async importOriginal => {
  const actual = await importOriginal<typeof import('../api/client')>()
  return {
    ...actual,
    api: {
      ...actual.api,
      getAuthor: vi.fn(),
      listBooks: vi.fn(),
      listAuthors: vi.fn(),
      refreshAuthor: vi.fn(),
      searchAuthorLinkCandidates: vi.fn(),
      relinkAuthorUpstream: vi.fn(),
      updateAuthor: vi.fn(),
      deleteAuthor: vi.fn(),
      deleteAuthorAlias: vi.fn(),
      searchAuthorWanted: vi.fn(),
      bulkActionBooks: vi.fn(),
    },
  }
})

const author: Author = {
  id: 42,
  foreignAuthorId: 'OL123A',
  authorName: 'Brandon Sanderson',
  sortName: 'Sanderson, Brandon',
  description: '',
  imageUrl: '',
  disambiguation: '',
  ratingsCount: 0,
  averageRating: 0,
  monitored: true,
  metadataProvider: 'openlibrary',
}

function makeBook(overrides: Partial<Book> & Pick<Book, 'id' | 'title' | 'status'>): Book {
  const { id, title, status, ...rest } = overrides
  return {
    id,
    foreignBookId: `book-${id}`,
    authorId: 42,
    title,
    description: '',
    imageUrl: '',
    releaseDate: undefined,
    genres: [],
    monitored: true,
    status,
    filePath: '',
    mediaType: 'ebook',
    ebookFilePath: '',
    audiobookFilePath: '',
    excluded: false,
    ...rest,
  }
}

function installLocalStorageMock() {
  const values = new Map<string, string>()
  const storage = {
    get length() {
      return values.size
    },
    clear: vi.fn(() => values.clear()),
    getItem: vi.fn((key: string) => values.get(key) ?? null),
    key: vi.fn((index: number) => Array.from(values.keys())[index] ?? null),
    removeItem: vi.fn((key: string) => {
      values.delete(key)
    }),
    setItem: vi.fn((key: string, value: string) => {
      values.set(key, value)
    }),
  } as Storage
  Object.defineProperty(globalThis, 'localStorage', { value: storage, configurable: true })
  Object.defineProperty(window, 'localStorage', { value: storage, configurable: true })
}

function LocationProbe({ onLocation }: { onLocation?: (location: string) => void }) {
  const location = useLocation()
  useEffect(() => {
    onLocation?.(`${location.pathname}${location.search}${location.hash}`)
  }, [location, onLocation])
  return null
}

function renderAuthorDetailPage(books: Book[], view: 'grid' | 'table' = 'grid', authorOverride: Partial<Author> = {}, initialPath = '/author/42', onLocation?: (location: string) => void) {
  localStorage.setItem('bindery.view.author-detail', view)
  vi.mocked(api.getAuthor).mockResolvedValue({ ...author, ...authorOverride })
  vi.mocked(api.listBooks).mockResolvedValue({ items: books, total: books.length, limit: 100, offset: 0 })

  return render(
    <MemoryRouter initialEntries={[initialPath]}>
      <LocationProbe onLocation={onLocation} />
      <Routes>
        <Route path="/author/:id" element={<AuthorDetailPage />} />
      </Routes>
    </MemoryRouter>,
  )
}

function rowForTitle(title: string): HTMLElement {
  const row = screen.getByText(title).closest('tr')
  if (!row) throw new Error(`No row found for ${title}`)
  return row
}

describe('AuthorDetailPage', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    installLocalStorageMock()
    vi.mocked(api.searchAuthorWanted).mockResolvedValue({
      results: { '42': { ok: true } },
    })
    vi.mocked(api.searchAuthorLinkCandidates).mockResolvedValue([])
    vi.mocked(api.relinkAuthorUpstream).mockResolvedValue(author)
  })

  it('searches all wanted books for the current author', async () => {
    renderAuthorDetailPage([
      makeBook({ id: 10, title: 'Wanted Book', status: 'wanted' }),
      makeBook({ id: 11, title: 'Imported Book', status: 'imported' }),
    ])

    const button = await screen.findByRole('button', { name: 'Search all wanted' })
    expect(button).toBeEnabled()

    fireEvent.click(button)

    await waitFor(() => expect(api.searchAuthorWanted).toHaveBeenCalledWith(42))
  })

  it('disables author search when there are no monitored wanted books', async () => {
    renderAuthorDetailPage([
      makeBook({ id: 10, title: 'Unmonitored Wanted Book', status: 'wanted', monitored: false }),
      makeBook({ id: 11, title: 'Imported Book', status: 'imported' }),
    ])

    const button = await screen.findByRole('button', { name: 'Search all wanted' })
    expect(button).toBeDisabled()

    fireEvent.click(button)

    expect(api.searchAuthorWanted).not.toHaveBeenCalled()
  })

  it('shows link metadata for unlinked authors', async () => {
    renderAuthorDetailPage([], 'grid', {
      foreignAuthorId: 'abs:author:library:emilia-jae',
      authorName: 'Emilia Jae',
      sortName: 'Jae, Emilia',
      metadataProvider: 'audiobookshelf',
    })

    expect(await screen.findByRole('button', { name: 'Link metadata' })).toBeInTheDocument()
  })

  it('shows link metadata for calibre-provider authors with legacy IDs', async () => {
    renderAuthorDetailPage([], 'grid', {
      foreignAuthorId: 'legacy-calibre-author',
      authorName: 'Calibre Author',
      sortName: 'Author, Calibre',
      metadataProvider: 'calibre',
      description: 'Imported from Calibre.',
      imageUrl: 'https://example.com/calibre.jpg',
      ratingsCount: 12,
      averageRating: 4.1,
    })

    expect(await screen.findByRole('button', { name: 'Link metadata' })).toBeInTheDocument()
  })

  it('shows find-better metadata for linked sparse authors and relinks a selected candidate', async () => {
    const sparseAuthor = {
      foreignAuthorId: 'OL13200512A',
      authorName: 'Emilia Jae',
      sortName: 'Jae, Emilia',
      metadataProvider: 'openlibrary',
      description: '',
      imageUrl: '',
      disambiguation: '',
      ratingsCount: 0,
      averageRating: 0,
    }
    const hardcoverAuthor = {
      ...author,
      ...sparseAuthor,
      foreignAuthorId: 'hc:emilia-jae',
      metadataProvider: 'hardcover',
      description: 'Fantasy author.',
      statistics: { bookCount: 3, availableBookCount: 0, wantedBookCount: 0 },
    }
    vi.mocked(api.searchAuthorLinkCandidates).mockResolvedValue([hardcoverAuthor])
    vi.mocked(api.relinkAuthorUpstream).mockResolvedValue(hardcoverAuthor)
    vi.mocked(api.getAuthor).mockResolvedValueOnce({ ...author, ...sparseAuthor }).mockResolvedValue(hardcoverAuthor)

    renderAuthorDetailPage([], 'grid', sparseAuthor)

    fireEvent.click(await screen.findByRole('button', { name: 'Find better metadata' }))

    await waitFor(() => expect(api.searchAuthorLinkCandidates).toHaveBeenCalledWith(42, 'Emilia Jae'))
    expect(await screen.findByText('Hardcover')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Link' }))

    await waitFor(() => expect(api.relinkAuthorUpstream).toHaveBeenCalledWith(42, {
      foreignAuthorId: 'hc:emilia-jae',
      authorName: 'Emilia Jae',
    }))
  })

  it('opens link metadata from the query string once and removes the trigger param', async () => {
    const locations: string[] = []
    renderAuthorDetailPage([], 'grid', {
      foreignAuthorId: 'abs:author:library:emilia-jae',
      authorName: 'Emilia Jae',
      sortName: 'Jae, Emilia',
      metadataProvider: 'audiobookshelf',
    }, '/author/42?linkMetadata=1&view=detail', location => locations.push(location))

    expect(await screen.findByRole('heading', { name: 'Link metadata' })).toBeInTheDocument()
    await waitFor(() => expect(api.searchAuthorLinkCandidates).toHaveBeenCalledWith(42, 'Emilia Jae'))
    await waitFor(() => expect(locations[locations.length - 1]).toBe('/author/42?view=detail'))
  })

  it('does not reopen a query-opened metadata modal after a relink reloads the author', async () => {
    const sparseAuthor = {
      foreignAuthorId: 'OL13200512A',
      authorName: 'Emilia Jae',
      sortName: 'Jae, Emilia',
      metadataProvider: 'openlibrary',
      description: '',
      imageUrl: '',
      disambiguation: '',
      ratingsCount: 0,
      averageRating: 0,
    }
    const dnbAuthor = {
      ...author,
      ...sparseAuthor,
      foreignAuthorId: 'dnb:123456789',
      metadataProvider: 'dnb',
      description: 'DNB author record.',
    }
    vi.mocked(api.searchAuthorLinkCandidates).mockResolvedValue([dnbAuthor])
    vi.mocked(api.relinkAuthorUpstream).mockResolvedValue(dnbAuthor)
    vi.mocked(api.getAuthor).mockResolvedValueOnce({ ...author, ...sparseAuthor }).mockResolvedValue(dnbAuthor)

    renderAuthorDetailPage([], 'grid', sparseAuthor, '/author/42?linkMetadata=1')

    expect(await screen.findByText('DNB')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Link' }))

    await waitFor(() => expect(api.relinkAuthorUpstream).toHaveBeenCalledWith(42, {
      foreignAuthorId: 'dnb:123456789',
      authorName: 'Emilia Jae',
    }))
    await waitFor(() => expect(api.getAuthor).toHaveBeenCalledTimes(2))
    expect(screen.queryByRole('heading', { name: 'Link metadata' })).not.toBeInTheDocument()
  })

  it('removes an author alias after confirmation', async () => {
    vi.mocked(api.deleteAuthorAlias).mockResolvedValue()
    const confirmSpy = vi.spyOn(window, 'confirm').mockReturnValue(true)
    renderAuthorDetailPage([], 'grid', {
      aliases: [{ id: 7, authorId: 42, name: 'Robert Jordan', createdAt: '2026-01-01T00:00:00Z' }],
    })

    await screen.findByText('Robert Jordan')
    fireEvent.click(screen.getByRole('button', { name: 'Remove alias Robert Jordan' }))

    await waitFor(() => {
      expect(api.deleteAuthorAlias).toHaveBeenCalledWith(42, 7)
    })
    await waitFor(() => {
      expect(screen.queryByText('Robert Jordan')).not.toBeInTheDocument()
    })
    expect(confirmSpy).toHaveBeenCalledWith('Remove alias "Robert Jordan" from Brandon Sanderson?')
    confirmSpy.mockRestore()
  })

  it('keeps an author alias when removal is cancelled', async () => {
    const confirmSpy = vi.spyOn(window, 'confirm').mockReturnValue(false)
    renderAuthorDetailPage([], 'grid', {
      aliases: [{ id: 8, authorId: 42, name: 'Pen Name', createdAt: '2026-01-01T00:00:00Z' }],
    })

    await screen.findByText('Pen Name')
    fireEvent.click(screen.getByRole('button', { name: 'Remove alias Pen Name' }))

    expect(api.deleteAuthorAlias).not.toHaveBeenCalled()
    expect(screen.getByText('Pen Name')).toBeInTheDocument()
    confirmSpy.mockRestore()
  })

  it('keeps table metadata visible and repeats it in compact title rows', async () => {
    renderAuthorDetailPage(
      [
        makeBook({
          id: 101,
          title: 'Firefight',
          status: 'wanted',
          mediaType: 'ebook',
          releaseDate: '2008-01-01T00:00:00Z',
        }),
        makeBook({
          id: 102,
          title: 'Snapshot',
          status: 'downloaded',
          mediaType: 'audiobook',
          releaseDate: '2023-10-10',
        }),
        makeBook({
          id: 103,
          title: 'Dual Format',
          status: 'imported',
          mediaType: 'both',
          releaseDate: '2022-05-05',
          excluded: true,
        }),
      ],
      'table',
    )

    await screen.findByText('Firefight')
    const table = screen.getByRole('table')

    expect(table).toHaveClass('table-fixed')
    expect(within(table).getByRole('columnheader', { name: 'Title' })).toHaveClass('sm:w-[46%]')
    expect(within(table).getByRole('columnheader', { name: /Published/ })).toBeInTheDocument()
    expect(within(table).getByRole('columnheader', { name: 'Type' })).toBeInTheDocument()
    expect(within(table).getByRole('columnheader', { name: 'Status' })).toBeInTheDocument()

    // Cells: [0]=row checkbox (bulk select), [1]=title+inline metadata,
    // [2]=published year, [3]=type, [4]=status. Checkbox column was added
    // for #791 bulk multi-select.
    const firefightCells = within(rowForTitle('Firefight')).getAllByRole('cell')
    expect(firefightCells).toHaveLength(5)
    expect(within(firefightCells[0]).getByRole('checkbox')).toBeInTheDocument()
    expect(firefightCells[1]).toHaveTextContent('Wanted')
    expect(firefightCells[1]).toHaveTextContent('📖 Ebook')
    expect(firefightCells[1]).toHaveTextContent('2008')
    expect(firefightCells[1]).not.toHaveTextContent('2008-01-01')
    expect(firefightCells[2]).toHaveTextContent('2008')
    expect(firefightCells[2]).not.toHaveTextContent('2008-01-01')
    expect(firefightCells[3]).toHaveTextContent('📖 Ebook')
    expect(firefightCells[4]).toHaveTextContent('Wanted')

    const snapshotCells = within(rowForTitle('Snapshot')).getAllByRole('cell')
    expect(snapshotCells[1]).toHaveTextContent('Downloaded')
    expect(snapshotCells[1]).toHaveTextContent('🎧 Audiobook')
    expect(snapshotCells[1]).toHaveTextContent('2023')
    expect(snapshotCells[2]).toHaveTextContent('2023')
    expect(snapshotCells[3]).toHaveTextContent('🎧 Audiobook')
    expect(snapshotCells[4]).toHaveTextContent('Downloaded')

    const dualFormatCells = within(rowForTitle('Dual Format')).getAllByRole('cell')
    expect(dualFormatCells[1]).toHaveTextContent('Imported')
    expect(dualFormatCells[1]).toHaveTextContent('📖🎧 Both')
    expect(dualFormatCells[1]).toHaveTextContent('2022')
    expect(dualFormatCells[1]).toHaveTextContent('Excluded')
    expect(dualFormatCells[2]).toHaveTextContent('2022')
    expect(dualFormatCells[3]).toHaveTextContent('📖🎧 Both')
    expect(dualFormatCells[4]).toHaveTextContent('Imported')
    expect(dualFormatCells[4]).toHaveTextContent('Excluded')
  })

  it('bulk-excludes selected books via /book/bulk', async () => {
    vi.mocked(api.bulkActionBooks).mockResolvedValue({
      results: { '101': { ok: true }, '102': { ok: true } },
    })
    renderAuthorDetailPage(
      [
        makeBook({ id: 101, title: 'Drop One', status: 'wanted' }),
        makeBook({ id: 102, title: 'Drop Two', status: 'wanted' }),
        makeBook({ id: 103, title: 'Keep', status: 'wanted' }),
      ],
      'table',
    )

    await screen.findByText('Drop One')
    const row1 = rowForTitle('Drop One')
    const row2 = rowForTitle('Drop Two')
    fireEvent.click(within(row1).getByRole('checkbox'))
    fireEvent.click(within(row2).getByRole('checkbox'))

    // Confirm dialog for exclude — auto-accept.
    const confirmSpy = vi.spyOn(window, 'confirm').mockReturnValue(true)
    const excludeBtn = await screen.findByRole('button', { name: 'Exclude' })
    fireEvent.click(excludeBtn)

    await waitFor(() => {
      expect(api.bulkActionBooks).toHaveBeenCalledWith([101, 102], 'exclude')
    })
    confirmSpy.mockRestore()
  })

  it('surfaces partial-failure summary when some bulk actions fail', async () => {
    vi.mocked(api.bulkActionBooks).mockResolvedValue({
      results: { '201': { ok: true }, '202': { ok: false, error: 'gone' } },
    })
    renderAuthorDetailPage(
      [
        makeBook({ id: 201, title: 'Mon One', status: 'wanted', monitored: false }),
        makeBook({ id: 202, title: 'Mon Two', status: 'wanted', monitored: false }),
      ],
      'table',
    )

    await screen.findByText('Mon One')
    fireEvent.click(within(rowForTitle('Mon One')).getByRole('checkbox'))
    fireEvent.click(within(rowForTitle('Mon Two')).getByRole('checkbox'))

    fireEvent.click(await screen.findByRole('button', { name: 'Monitor' }))

    await waitFor(() => {
      expect(screen.getByText(/Monitor: 1 of 2 succeeded\. First error: gone/)).toBeInTheDocument()
    })
  })
})
