import { render, screen, waitFor, fireEvent, within } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { MemoryRouter } from 'react-router'
import DiscoverPage from './DiscoverPage'
import type { Recommendation } from '../api/client'

// Keys come back verbatim so assertions don't depend on English copy.
vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string) => key,
  }),
}))

vi.mock('../api/client', () => ({
  api: {
    listRecommendations: vi.fn(),
    listSettings: vi.fn(),
    refreshRecommendations: vi.fn(),
    dismissRecommendation: vi.fn(),
    addRecommendation: vi.fn(),
    addAuthorExclusion: vi.fn(),
  },
}))

import { api } from '../api/client'

function rec(id: number, recType: string, title: string, authorName = `Author ${id}`): Recommendation {
  return {
    id,
    userId: 1,
    foreignId: `OL${id}W`,
    recType,
    title,
    authorName,
    imageUrl: '',
    description: '',
    genres: [],
    rating: 0,
    ratingsCount: 0,
    language: 'en',
    mediaType: 'ebook',
    score: 1,
    reason: '',
    seriesPos: '',
    dismissed: false,
    batchId: 'b',
    createdAt: '2026-01-01T00:00:00Z',
  }
}

function renderPage() {
  return render(<MemoryRouter><DiscoverPage /></MemoryRouter>)
}

function cardFor(title: string): HTMLElement {
  // The card is the nearest ancestor holding both the title and its actions.
  let el: HTMLElement | null = screen.getByText(title)
  while (el && !within(el).queryByRole('button', { name: 'discover.dismiss' })) el = el.parentElement
  if (!el) throw new Error(`card for ${title} not found`)
  return el
}

describe('DiscoverPage rows and actions', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(api.listSettings).mockResolvedValue([{ key: 'recommendations.enabled', value: 'true' }] as never)
    vi.mocked(api.dismissRecommendation).mockResolvedValue(undefined as never)
    vi.mocked(api.addRecommendation).mockResolvedValue(undefined as never)
    vi.mocked(api.addAuthorExclusion).mockResolvedValue(undefined as never)
  })

  it('shows loading first, then one row per recommendation type that has entries', async () => {
    vi.mocked(api.listRecommendations).mockResolvedValue([
      rec(1, 'series', 'Series Pick'),
      rec(2, 'genre_similar', 'Similar Pick'),
      rec(3, 'list_cross', 'List Pick'),
    ])
    renderPage()
    expect(screen.getByText('common.loading')).toBeInTheDocument()
    expect(await screen.findByText('Series Pick')).toBeInTheDocument()
    expect(api.listRecommendations).toHaveBeenCalledWith({ limit: 100 })
    expect(screen.getByRole('heading', { name: 'discover.rows.series' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'discover.rows.genre_similar' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'discover.rows.list_cross' })).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: 'discover.rows.author_new' })).toBeNull()
    // Genre rows exist, so this is not a cold start.
    expect(screen.queryByText('discover.empty.coldStart')).toBeNull()
    expect(document.title).toBe('Discover · Bindery')
  })

  it('explains a cold start when only series or new-from-author rows exist', async () => {
    vi.mocked(api.listRecommendations).mockResolvedValue([rec(1, 'author_new', 'Fresh Pick')])
    renderPage()
    expect(await screen.findByText('discover.empty.coldStart')).toBeInTheDocument()
  })

  it('dismisses a card optimistically and tells the server', async () => {
    vi.mocked(api.listRecommendations).mockResolvedValue([rec(1, 'series', 'Keep Me'), rec(2, 'series', 'Drop Me')])
    vi.mocked(api.dismissRecommendation).mockRejectedValue(new Error('offline'))
    renderPage()
    await screen.findByText('Drop Me')
    fireEvent.click(within(cardFor('Drop Me')).getByRole('button', { name: 'discover.dismiss' }))
    expect(screen.queryByText('Drop Me')).toBeNull()
    expect(screen.getByText('Keep Me')).toBeInTheDocument()
    await waitFor(() => expect(api.dismissRecommendation).toHaveBeenCalledWith(2))
    // A failed dismiss does not bring the card back.
    expect(screen.queryByText('Drop Me')).toBeNull()
  })

  it('adds a card to Wanted, removes it and shows a toast', async () => {
    vi.mocked(api.listRecommendations).mockResolvedValue([rec(5, 'series', 'Want Me')])
    vi.mocked(api.addRecommendation).mockRejectedValue(new Error('offline'))
    renderPage()
    await screen.findByText('Want Me')
    fireEvent.click(within(cardFor('Want Me')).getByRole('button', { name: 'discover.addToWanted' }))
    await waitFor(() => expect(api.addRecommendation).toHaveBeenCalledWith(5))
    expect(screen.queryByText('Want Me')).toBeNull()
    expect(screen.getByText('discover.addedToWanted')).toBeInTheDocument()
  })

  it('excludes an author and drops every card by them', async () => {
    vi.mocked(api.listRecommendations).mockResolvedValue([
      rec(1, 'series', 'First By X', 'Writer X'),
      rec(2, 'genre_similar', 'Second By X', 'Writer X'),
      rec(3, 'genre_similar', 'By Y', 'Writer Y'),
    ])
    vi.mocked(api.addAuthorExclusion).mockRejectedValue(new Error('offline'))
    renderPage()
    await screen.findByText('First By X')
    fireEvent.click(within(cardFor('First By X')).getByRole('button', { name: /discover.moreActionsFor|More actions/ }))
    fireEvent.click(await screen.findByRole('menuitem', { name: 'discover.dontSuggestAuthor' }))
    await waitFor(() => expect(api.addAuthorExclusion).toHaveBeenCalledWith('Writer X'))
    expect(screen.queryByText('First By X')).toBeNull()
    expect(screen.queryByText('Second By X')).toBeNull()
    expect(screen.getByText('By Y')).toBeInTheDocument()
  })

  it('treats a failed recommendations load as empty', async () => {
    vi.mocked(api.listRecommendations).mockRejectedValue(new Error('500'))
    renderPage()
    expect(await screen.findByText('discover.empty.noRecs')).toBeInTheDocument()
  })

  it('assumes the feature is on when settings cannot be read', async () => {
    vi.mocked(api.listRecommendations).mockResolvedValue([])
    vi.mocked(api.listSettings).mockRejectedValue(new Error('403'))
    renderPage()
    expect(await screen.findByText('discover.empty.noRecs')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'discover.refresh' })).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'discover.empty.goToSettings' })).toBeNull()
  })

  it('refreshes, shows the busy state, then reloads after regeneration', async () => {
    vi.mocked(api.listRecommendations).mockResolvedValueOnce([]).mockResolvedValue([rec(9, 'series', 'New Pick')])
    vi.mocked(api.refreshRecommendations).mockResolvedValue(undefined as never)
    renderPage()
    await screen.findByText('discover.empty.noRecs')
    fireEvent.click(screen.getByRole('button', { name: 'discover.refresh' }))
    expect(await screen.findByRole('button', { name: 'discover.refreshing' })).toBeDisabled()
    await waitFor(() => expect(api.refreshRecommendations).toHaveBeenCalled())
    // The page waits for background regeneration before re-fetching.
    expect(await screen.findByText('New Pick', {}, { timeout: 10000 })).toBeInTheDocument()
    expect(await screen.findByRole('button', { name: 'discover.refresh' })).toBeEnabled()
  })

  it('re-enables Refresh straight away when the refresh request fails', async () => {
    vi.mocked(api.listRecommendations).mockResolvedValue([])
    vi.mocked(api.refreshRecommendations).mockRejectedValue(new Error('busy'))
    renderPage()
    await screen.findByText('discover.empty.noRecs')
    fireEvent.click(screen.getByRole('button', { name: 'discover.refresh' }))
    await waitFor(() => expect(api.refreshRecommendations).toHaveBeenCalled())
    await waitFor(() => expect(screen.getByRole('button', { name: 'discover.refresh' })).toBeEnabled())
    expect(api.listRecommendations).toHaveBeenCalledTimes(1)
  })

  it('restores the document title on unmount', async () => {
    vi.mocked(api.listRecommendations).mockResolvedValue([])
    const { unmount } = renderPage()
    await screen.findByText('discover.empty.noRecs')
    unmount()
    expect(document.title).toBe('Bindery')
  })
})
