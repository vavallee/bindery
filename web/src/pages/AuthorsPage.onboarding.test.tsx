import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import AuthorsPage from './AuthorsPage'
import { api } from '../api/client'
import type { Indexer, DownloadClient } from '../api/client'

vi.mock('../api/client', async importOriginal => {
  const actual = await importOriginal<typeof import('../api/client')>()
  return {
    ...actual,
    api: {
      ...actual.api,
      listAuthors: vi.fn(),
      listIndexers: vi.fn(),
      listDownloadClients: vi.fn(),
    },
  }
})

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, fallback?: string) => {
      const labels: Record<string, string> = {
        'authors.empty': 'No authors yet',
        'authors.emptyHint': 'Click Add Author to start',
        'gettingStarted.title': 'Getting started',
        'gettingStarted.reasonAuthors': 'Configure an indexer and a download client before adding authors.',
        'gettingStarted.indexers': 'Set up Indexers',
        'gettingStarted.downloadClients': 'Set up Download Clients',
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

const fakeIndexer = { id: 1, name: 'NZBgeek' } as unknown as Indexer
const fakeClient = { id: 1, name: 'qBittorrent' } as unknown as DownloadClient

function renderPage() {
  return render(
    <MemoryRouter>
      <AuthorsPage />
    </MemoryRouter>,
  )
}

describe('AuthorsPage first-run onboarding guidance', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(api.listAuthors).mockResolvedValue({ items: [], total: 0, limit: 100, offset: 0 })
  })

  it('shows the getting-started guidance with links to settings when there are no authors and no indexers/clients', async () => {
    vi.mocked(api.listIndexers).mockResolvedValue([])
    vi.mocked(api.listDownloadClients).mockResolvedValue([])

    renderPage()

    const heading = await screen.findByText('Getting started')
    expect(heading).toBeInTheDocument()

    const indexersLink = screen.getByRole('link', { name: 'Set up Indexers' })
    expect(indexersLink).toHaveAttribute('href', '/settings?tab=indexers')
    const clientsLink = screen.getByRole('link', { name: 'Set up Download Clients' })
    expect(clientsLink).toHaveAttribute('href', '/settings?tab=clients')
  })

  it('does NOT show the guidance when at least one indexer exists', async () => {
    vi.mocked(api.listIndexers).mockResolvedValue([fakeIndexer])
    vi.mocked(api.listDownloadClients).mockResolvedValue([])

    renderPage()

    // Normal empty state still renders...
    expect(await screen.findByText('No authors yet')).toBeInTheDocument()
    // ...but the guidance does not.
    await waitFor(() => expect(api.listIndexers).toHaveBeenCalled())
    expect(screen.queryByText('Getting started')).not.toBeInTheDocument()
  })

  it('does NOT show the guidance when at least one download client exists', async () => {
    vi.mocked(api.listIndexers).mockResolvedValue([])
    vi.mocked(api.listDownloadClients).mockResolvedValue([fakeClient])

    renderPage()

    expect(await screen.findByText('No authors yet')).toBeInTheDocument()
    await waitFor(() => expect(api.listDownloadClients).toHaveBeenCalled())
    expect(screen.queryByText('Getting started')).not.toBeInTheDocument()
  })
})
