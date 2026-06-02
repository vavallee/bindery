import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
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
      updateAuthor: vi.fn(),
      deleteAuthor: vi.fn(),
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

function renderAuthorDetailPage(books: Book[], view: 'grid' | 'table' = 'grid') {
  localStorage.setItem('bindery.view.author-detail', view)
  vi.mocked(api.getAuthor).mockResolvedValue(author)
  vi.mocked(api.listBooks).mockResolvedValue({ items: books, total: books.length, limit: 100, offset: 0 })

  return render(
    <MemoryRouter initialEntries={['/author/42']}>
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
    expect(dualFormatCells[1]).toHaveTextContent('In Library')
    expect(dualFormatCells[1]).toHaveTextContent('📖🎧 Both')
    expect(dualFormatCells[1]).toHaveTextContent('2022')
    expect(dualFormatCells[1]).toHaveTextContent('Excluded')
    expect(dualFormatCells[2]).toHaveTextContent('2022')
    expect(dualFormatCells[3]).toHaveTextContent('📖🎧 Both')
    expect(dualFormatCells[4]).toHaveTextContent('In Library')
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
