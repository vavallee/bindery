import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import AuthorsPage from './AuthorsPage'
import { api } from '../api/client'
import type { Series } from '../api/client'

const { navigateMock } = vi.hoisted(() => ({
  navigateMock: vi.fn(),
}))

vi.mock('react-router-dom', async importOriginal => {
  const actual = await importOriginal<typeof import('react-router-dom')>()
  return {
    ...actual,
    useNavigate: () => navigateMock,
  }
})

vi.mock('../api/client', async importOriginal => {
  const actual = await importOriginal<typeof import('../api/client')>()
  return {
    ...actual,
    api: {
      ...actual.api,
      listAuthors: vi.fn(),
      createSeries: vi.fn(),
    },
  }
})

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, fallback?: string) => {
      const labels: Record<string, string> = {
        'authors.title': 'Authors',
        'authors.merge': 'Merge',
        'authors.addAuthor': 'Add Author',
        'authors.searchPlaceholder': 'Search authors...',
        'authors.sortAZ': 'A-Z',
        'authors.sortZA': 'Z-A',
        'authors.sortRecent': 'Recent',
        'authors.filterMonitored': 'Monitored:',
        'authors.filterAll': 'All',
        'authors.filterMonitoredOnly': 'Monitored',
        'authors.filterUnmonitored': 'Unmonitored',
        'authors.empty': 'No authors found',
        'authors.emptyHint': 'Add an author to get started',
      }
      return labels[key] ?? fallback ?? key
    },
  }),
}))

vi.mock('../components/usePagination', () => ({
  usePagination: <T,>(items: T[]) => ({
    pageItems: items,
    paginationProps: { page: 1, totalPages: 1, pageSize: 50, totalItems: items.length, onPageChange: vi.fn(), onPageSizeChange: vi.fn() },
    reset: vi.fn(),
  }),
}))

vi.mock('../components/Pagination', () => ({ default: () => null }))

describe('AuthorsPage', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(api.listAuthors).mockResolvedValue({ items: [], total: 0, limit: 100, offset: 0 })
  })

  it('creates a series from the authors toolbar and opens it on the series page', async () => {
    const created: Series = {
      id: 44,
      foreignSeriesId: 'manual:series:44',
      title: 'Dune Chronicles',
      description: '',
      monitored: false,
      books: [],
    }
    vi.mocked(api.createSeries).mockResolvedValue(created)

    render(
      <MemoryRouter>
        <AuthorsPage />
      </MemoryRouter>,
    )

    fireEvent.click(await screen.findByRole('button', { name: 'Add Series' }))
    const dialog = await screen.findByRole('dialog', { name: 'Add Series' })
    fireEvent.change(within(dialog).getByLabelText('Name'), { target: { value: 'Dune Chronicles' } })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Add Series' }))

    await waitFor(() => expect(api.createSeries).toHaveBeenCalledWith({ title: 'Dune Chronicles' }))
    expect(navigateMock).toHaveBeenCalledWith('/series', { state: { seriesId: created.id } })
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Add Series' })).not.toBeInTheDocument())
  })
})
