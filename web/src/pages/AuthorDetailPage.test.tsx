import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor, within, act } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import AuthorDetailPage from './AuthorDetailPage'
import { api } from '../api/client'
import type { Author, Book } from '../api/client'

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
      deleteAuthorAlias: vi.fn(),
      searchAuthorWanted: vi.fn(),
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

function renderAuthorDetailPage(books: Book[], view: 'grid' | 'table' = 'grid', authorData: Author = author) {
  localStorage.setItem('bindery.view.author-detail', view)
  vi.mocked(api.getAuthor).mockResolvedValue(authorData)
  vi.mocked(api.listBooks).mockResolvedValue(books)

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
    vi.mocked(api.deleteAuthorAlias).mockResolvedValue(undefined)
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

  it('removes an author alias', async () => {
    const confirmSpy = vi.spyOn(window, 'confirm').mockReturnValue(true)
    renderAuthorDetailPage([], 'grid', {
      ...author,
      aliases: [
        { id: 1, authorId: 42, name: 'Robert Jordan', createdAt: '2026-05-12T00:00:00Z' },
        { id: 2, authorId: 42, name: 'GraphicAudio', sourceOlId: 'abs', createdAt: '2026-05-12T00:00:00Z' },
      ],
    })

    await screen.findByText('Robert Jordan')
    fireEvent.click(screen.getByRole('button', { name: 'Remove alias Robert Jordan' }))

    await waitFor(() => expect(api.deleteAuthorAlias).toHaveBeenCalledWith(42, 1))
    expect(screen.queryByText('Robert Jordan')).not.toBeInTheDocument()
    expect(screen.getByText('GraphicAudio')).toBeInTheDocument()
    confirmSpy.mockRestore()
  })

  it('keeps concurrent alias removals from restoring stale aliases', async () => {
    const confirmSpy = vi.spyOn(window, 'confirm').mockReturnValue(true)
    const deletes: Array<() => void> = []
    vi.mocked(api.deleteAuthorAlias).mockImplementation(() => new Promise(resolve => {
      deletes.push(resolve)
    }))
    renderAuthorDetailPage([], 'grid', {
      ...author,
      aliases: [
        { id: 1, authorId: 42, name: 'Robert Jordan', createdAt: '2026-05-12T00:00:00Z' },
        { id: 2, authorId: 42, name: 'GraphicAudio', sourceOlId: 'abs', createdAt: '2026-05-12T00:00:00Z' },
      ],
    })

    await screen.findByText('Robert Jordan')
    fireEvent.click(screen.getByRole('button', { name: 'Remove alias Robert Jordan' }))
    fireEvent.click(screen.getByRole('button', { name: 'Remove alias GraphicAudio' }))
    await waitFor(() => expect(api.deleteAuthorAlias).toHaveBeenCalledTimes(2))

    await act(async () => { deletes[1]() })
    await act(async () => { deletes[0]() })

    expect(screen.queryByText('Robert Jordan')).not.toBeInTheDocument()
    expect(screen.queryByText('GraphicAudio')).not.toBeInTheDocument()
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

    const firefightCells = within(rowForTitle('Firefight')).getAllByRole('cell')
    expect(firefightCells).toHaveLength(4)
    expect(firefightCells[0]).toHaveTextContent('Wanted')
    expect(firefightCells[0]).toHaveTextContent('📖 Ebook')
    expect(firefightCells[0]).toHaveTextContent('2008')
    expect(firefightCells[0]).not.toHaveTextContent('2008-01-01')
    expect(firefightCells[1]).toHaveTextContent('2008')
    expect(firefightCells[1]).not.toHaveTextContent('2008-01-01')
    expect(firefightCells[2]).toHaveTextContent('📖 Ebook')
    expect(firefightCells[3]).toHaveTextContent('Wanted')

    const snapshotCells = within(rowForTitle('Snapshot')).getAllByRole('cell')
    expect(snapshotCells[0]).toHaveTextContent('Downloaded')
    expect(snapshotCells[0]).toHaveTextContent('🎧 Audiobook')
    expect(snapshotCells[0]).toHaveTextContent('2023')
    expect(snapshotCells[1]).toHaveTextContent('2023')
    expect(snapshotCells[2]).toHaveTextContent('🎧 Audiobook')
    expect(snapshotCells[3]).toHaveTextContent('Downloaded')

    const dualFormatCells = within(rowForTitle('Dual Format')).getAllByRole('cell')
    expect(dualFormatCells[0]).toHaveTextContent('In Library')
    expect(dualFormatCells[0]).toHaveTextContent('📖🎧 Both')
    expect(dualFormatCells[0]).toHaveTextContent('2022')
    expect(dualFormatCells[0]).toHaveTextContent('Excluded')
    expect(dualFormatCells[1]).toHaveTextContent('2022')
    expect(dualFormatCells[2]).toHaveTextContent('📖🎧 Both')
    expect(dualFormatCells[3]).toHaveTextContent('In Library')
    expect(dualFormatCells[3]).toHaveTextContent('Excluded')
  })
})
