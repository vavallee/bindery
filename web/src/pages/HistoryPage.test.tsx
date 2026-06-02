import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import HistoryPage from './HistoryPage'
import { api } from '../api/client'
import type { BlocklistEntry, HistoryEvent } from '../api/client'

vi.mock('../api/client', async importOriginal => {
  const actual = await importOriginal<typeof import('../api/client')>()
  return {
    ...actual,
    api: {
      ...actual.api,
      listHistory: vi.fn(),
      deleteHistory: vi.fn(),
      blocklistFromHistory: vi.fn(),
    },
  }
})

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string) => {
      const labels: Record<string, string> = {
        'common.loading': 'Loading...',
        'history.title': 'History',
        'history.allEventTypes': 'All event types',
        'history.empty': 'No history events found',
        'history.colEvent': 'Event',
        'history.colSourceTitle': 'Source Title',
        'history.colType': 'Type',
        'history.colSize': 'Size',
        'history.colDate': 'Date',
        'history.blocklist': 'Blocklist',
        'history.delete': 'Delete',
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
      pageSize: 100,
      totalItems: items.length,
      onPageChange: vi.fn(),
      onPageSizeChange: vi.fn(),
    },
    reset: vi.fn(),
  }),
}))

vi.mock('../components/Pagination', () => ({ default: () => null }))

function makeHistory(overrides: Partial<HistoryEvent> = {}): HistoryEvent {
  return {
    id: 1,
    bookId: 42,
    eventType: 'grabbed',
    sourceTitle: 'Dune EPUB',
    data: '{}',
    createdAt: '2026-05-01T12:00:00Z',
    ...overrides,
  }
}

function makeBlocklistEntry(overrides: Partial<BlocklistEntry> = {}): BlocklistEntry {
  return {
    id: 20,
    bookId: 42,
    guid: 'blocked-guid',
    title: 'Blocked Release',
    reason: 'Blocked from history',
    createdAt: '2026-05-01T12:00:00Z',
    ...overrides,
  }
}

function renderHistoryPage() {
  return render(
    <MemoryRouter>
      <HistoryPage />
    </MemoryRouter>,
  )
}

function desktopTable() {
  return screen.getByRole('table')
}

async function findDesktopTable() {
  return screen.findByRole('table')
}

function rowFor(text: string) {
  const cellText = within(desktopTable()).getByText(text)
  return cellText.closest('tr')!
}

beforeEach(() => {
  vi.clearAllMocks()
  document.title = 'Bindery'
  vi.mocked(api.listHistory).mockResolvedValue({ items: [], total: 0, limit: 100, offset: 0 })
  vi.mocked(api.deleteHistory).mockResolvedValue(undefined)
  vi.mocked(api.blocklistFromHistory).mockResolvedValue(makeBlocklistEntry())
})

describe('HistoryPage', () => {
  it('renders history rows with parsed details, media type, and size', async () => {
    vi.mocked(api.listHistory).mockResolvedValue({
      items: [
        makeHistory({
          id: 1,
          eventType: 'grabbed',
          sourceTitle: 'Dune EPUB',
          data: JSON.stringify({ path: '/library/Dune.epub', size: 1048576 }),
        }),
        makeHistory({
          id: 2,
          eventType: 'downloadFailed',
          sourceTitle: 'Dune MP3',
          data: JSON.stringify({ message: 'Download client rejected release', size: 2147483648 }),
        }),
        makeHistory({
          id: 3,
          eventType: 'bookImported',
          sourceTitle: '',
          data: JSON.stringify({ size: 0 }),
        }),
      ],
      total: 3,
      limit: 100,
      offset: 0,
    })

    renderHistoryPage()

    const table = await findDesktopTable()
    expect(await within(table).findByText('Dune EPUB')).toBeInTheDocument()

    const ebookRow = rowFor('Dune EPUB')
    expect(within(ebookRow).getByText('grabbed')).toBeInTheDocument()
    expect(within(ebookRow).getByText('/library/Dune.epub')).toBeInTheDocument()
    expect(within(ebookRow).getByText('📖 Ebook')).toBeInTheDocument()
    expect(within(ebookRow).getByText('1 MB')).toBeInTheDocument()

    const audioRow = rowFor('Dune MP3')
    expect(within(audioRow).getByText('downloadFailed')).toBeInTheDocument()
    expect(within(audioRow).getByText('Download client rejected release')).toBeInTheDocument()
    expect(within(audioRow).getByText('🎧 Audiobook')).toBeInTheDocument()
    expect(within(audioRow).getByText('2.0 GB')).toBeInTheDocument()

    const importedRow = rowFor('bookImported')
    expect(within(importedRow).getAllByText('—').length).toBeGreaterThan(0)
  })

  it('filters history by event type', async () => {
    vi.mocked(api.listHistory)
      .mockResolvedValueOnce({
        items: [
          makeHistory({ id: 1, eventType: 'grabbed', sourceTitle: 'Grabbed Release' }),
          makeHistory({ id: 2, eventType: 'downloadFailed', sourceTitle: 'Failed Release' }),
        ],
        total: 2,
        limit: 100,
        offset: 0,
      })
      .mockResolvedValueOnce({
        items: [
          makeHistory({ id: 2, eventType: 'downloadFailed', sourceTitle: 'Failed Release' }),
        ],
        total: 1,
        limit: 100,
        offset: 0,
      })

    renderHistoryPage()

    const table = await findDesktopTable()
    expect(await within(table).findByText('Grabbed Release')).toBeInTheDocument()

    fireEvent.change(screen.getByRole('combobox'), { target: { value: 'downloadFailed' } })

    await waitFor(() => expect(api.listHistory).toHaveBeenLastCalledWith({ eventType: 'downloadFailed' }))
    expect(await within(desktopTable()).findByText('Failed Release')).toBeInTheDocument()
    await waitFor(() => expect(within(desktopTable()).queryByText('Grabbed Release')).not.toBeInTheDocument())
  })

  it('blocklists blocklistable events and removes them from local history', async () => {
    vi.mocked(api.listHistory).mockResolvedValue({
      items: [
        makeHistory({ id: 7, eventType: 'importFailed', sourceTitle: 'Blocklist Me' }),
        makeHistory({ id: 8, eventType: 'bookImported', sourceTitle: 'Keep Me' }),
      ],
      total: 2,
      limit: 100,
      offset: 0,
    })

    renderHistoryPage()

    const targetRow = (await within(await findDesktopTable()).findByText('Blocklist Me')).closest('tr')!
    fireEvent.click(within(targetRow).getByRole('button', { name: 'Blocklist' }))

    await waitFor(() => expect(api.blocklistFromHistory).toHaveBeenCalledWith(7))
    await waitFor(() => expect(within(desktopTable()).queryByText('Blocklist Me')).not.toBeInTheDocument())
    expect(within(desktopTable()).getByText('Keep Me')).toBeInTheDocument()
  })

  it('deletes history events and removes them from local history', async () => {
    vi.mocked(api.listHistory).mockResolvedValue({
      items: [
        makeHistory({ id: 11, eventType: 'grabbed', sourceTitle: 'Delete Me' }),
        makeHistory({ id: 12, eventType: 'bookImported', sourceTitle: 'Keep Me' }),
      ],
      total: 2,
      limit: 100,
      offset: 0,
    })

    renderHistoryPage()

    const targetRow = (await within(await findDesktopTable()).findByText('Delete Me')).closest('tr')!
    fireEvent.click(within(targetRow).getByRole('button', { name: 'Delete' }))

    await waitFor(() => expect(api.deleteHistory).toHaveBeenCalledWith(11))
    await waitFor(() => expect(within(desktopTable()).queryByText('Delete Me')).not.toBeInTheDocument())
    expect(within(desktopTable()).getByText('Keep Me')).toBeInTheDocument()
  })

  it('renders only delete actions for non-blocklistable events', async () => {
    vi.mocked(api.listHistory).mockResolvedValue({
      items: [
        makeHistory({ id: 21, eventType: 'bookImported', sourceTitle: 'Imported Release' }),
        makeHistory({ id: 22, eventType: 'deleted', sourceTitle: 'Deleted Release' }),
      ],
      total: 2,
      limit: 100,
      offset: 0,
    })

    renderHistoryPage()

    const importedRow = (await within(await findDesktopTable()).findByText('Imported Release')).closest('tr')!
    const deletedRow = rowFor('Deleted Release')

    expect(within(importedRow).queryByRole('button', { name: 'Blocklist' })).not.toBeInTheDocument()
    expect(within(importedRow).getByRole('button', { name: 'Delete' })).toBeInTheDocument()
    expect(within(deletedRow).queryByRole('button', { name: 'Blocklist' })).not.toBeInTheDocument()
    expect(within(deletedRow).getByRole('button', { name: 'Delete' })).toBeInTheDocument()
  })

  it('renders the empty history state', async () => {
    renderHistoryPage()

    expect(await screen.findByText('No history events found')).toBeInTheDocument()
    expect(api.listHistory).toHaveBeenCalledWith(undefined)
  })
})
