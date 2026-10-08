import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { api, DuplicateCandidateGroup, DuplicateCandidateMember, LibraryDuplicateCandidates } from '../api/client'
import DuplicatesPage from './DuplicatesPage'

vi.mock('react-i18next', () => {
  const t = (key: string, fallback?: string | Record<string, unknown>) => {
    if (typeof fallback === 'string') return fallback
    const template = String(fallback?.defaultValue ?? key)
    return Object.entries(fallback ?? {}).reduce(
      (text, [name, value]) => name === 'defaultValue' ? text : text.replaceAll(`{{${name}}}`, String(value)),
      template,
    )
  }
  return { useTranslation: () => ({ t }) }
})

vi.mock('../api/client', () => ({
  api: {
    listLibraryDuplicateCandidates: vi.fn(),
    toggleExcluded: vi.fn(),
    excludeEmptyBooks: vi.fn(),
  },
}))

function row(id: number, authorId: number, title: string, hasFiles = false): DuplicateCandidateMember {
  return {
    id,
    foreignBookId: `OL${id}W`,
    authorId,
    title,
    description: '',
    imageUrl: '',
    genres: [],
    monitored: true,
    status: hasFiles ? 'imported' : 'wanted',
    filePath: '',
    mediaType: 'ebook',
    ebookFilePath: '',
    audiobookFilePath: '',
    excluded: false,
    rules: ['article-strip'],
    hasFiles,
    evidence: { files: hasFiles ? [{ kind: 'ebook', format: 'epub' }] : [], isbns: [], isbnCount: 0, asins: [], series: [] },
  }
}

function group(authorId: number, authorName: string, key: string, books: DuplicateCandidateMember[], keeperId?: number): DuplicateCandidateGroup {
  return {
    key,
    authorId,
    authorName,
    rules: ['article-strip'],
    books,
    signals: [],
    conflict: false,
    keeperId,
    suggestedExcludeIds: keeperId ? books.filter(b => b.id !== keeperId).map(b => b.id) : [],
  }
}

const pageOne: LibraryDuplicateCandidates = {
  total: 30,
  count: 2,
  limit: 25,
  offset: 0,
  groups: [
    group(3, 'Andy Weir', 'themartian', [row(31, 3, 'Martian'), row(32, 3, 'The Martian', true)], 32),
    group(5, 'Kristin Hannah', 'nightingale', [row(51, 5, 'Nightingale'), row(52, 5, 'The Nightingale')]),
  ],
}

function renderPage() {
  return render(
    <MemoryRouter>
      <DuplicatesPage />
    </MemoryRouter>,
  )
}

describe('DuplicatesPage', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(api.listLibraryDuplicateCandidates).mockResolvedValue(pageOne)
  })

  it('lists every author\'s groups with the author named', async () => {
    renderPage()
    expect(await screen.findByRole('link', { name: 'Andy Weir' })).toHaveAttribute('href', '/author/3')
    expect(screen.getByRole('link', { name: 'Kristin Hannah' })).toHaveAttribute('href', '/author/5')
    expect(screen.getByText('30 group(s)')).toBeInTheDocument()
    expect(api.listLibraryDuplicateCandidates).toHaveBeenCalledWith(25, 0)
  })

  it('pages through the groups with limit and offset', async () => {
    renderPage()
    await screen.findByRole('link', { name: 'Andy Weir' })
    fireEvent.click(screen.getByRole('button', { name: '2' }))
    await waitFor(() => expect(api.listLibraryDuplicateCandidates).toHaveBeenLastCalledWith(25, 25))
  })

  it('reuses the exclude action and reloads so the group drops out', async () => {
    vi.mocked(api.listLibraryDuplicateCandidates)
      .mockResolvedValueOnce(pageOne)
      .mockResolvedValueOnce({ ...pageOne, total: 29, count: 1, groups: [pageOne.groups[1]] })
    vi.mocked(api.excludeEmptyBooks).mockResolvedValue({ results: { '31': { ok: true } } })
    renderPage()

    fireEvent.click(await screen.findByRole('button', { name: 'Exclude the 1 empty row(s) in this group' }))
    const dialog = await screen.findByTestId('confirm-dialog')
    fireEvent.click(within(dialog).getByRole('button', { name: 'Exclude' }))

    await waitFor(() => expect(api.excludeEmptyBooks).toHaveBeenCalledWith([31]))
    await waitFor(() => expect(screen.queryByRole('link', { name: 'Andy Weir' })).not.toBeInTheDocument())
    expect(screen.getByText('29 group(s)')).toBeInTheDocument()
  })

  it('reports a partial failure from the bulk exclude', async () => {
    vi.mocked(api.excludeEmptyBooks).mockResolvedValue({ results: { '31': { ok: false, error: 'book not owned' } } })
    renderPage()

    fireEvent.click(await screen.findByRole('button', { name: 'Exclude the 1 empty row(s) in this group' }))
    fireEvent.click(within(await screen.findByTestId('confirm-dialog')).getByRole('button', { name: 'Exclude' }))
    expect(await screen.findByText('1 row(s) could not be excluded')).toBeInTheDocument()
  })

  it('shows the empty state', async () => {
    vi.mocked(api.listLibraryDuplicateCandidates).mockResolvedValue({ total: 0, count: 0, limit: 25, offset: 0, groups: [] })
    renderPage()
    expect(await screen.findByText('No duplicate titles found.')).toBeInTheDocument()
  })

  it('shows the load error', async () => {
    vi.mocked(api.listLibraryDuplicateCandidates).mockRejectedValue(new Error('boom'))
    renderPage()
    expect(await screen.findByText('boom')).toBeInTheDocument()
  })
})
