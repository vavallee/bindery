import { render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { MemoryRouter } from 'react-router'
import DiscoverPage from './DiscoverPage'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string) => {
      const strings: Record<string, string> = {
        'common.loading': 'Loading...',
        'discover.title': 'Discover',
        'discover.subtitle': 'Based on your library',
        'discover.refresh': 'Refresh',
        'discover.empty.disabled': 'Book recommendations are turned off.',
        'discover.empty.goToSettings': 'Go to book recommendations',
        'discover.empty.noRecs': "Your library doesn't have enough data yet.",
      }
      return strings[key] ?? key
    },
  }),
}))

vi.mock('../api/client', () => ({
  api: {
    listRecommendations: vi.fn(),
    listSettings: vi.fn(),
    refreshRecommendations: vi.fn(),
  },
}))

import { api } from '../api/client'

function renderPage() {
  return render(<MemoryRouter><DiscoverPage /></MemoryRouter>)
}

describe('DiscoverPage, recommendations turned off', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(api.listRecommendations).mockResolvedValue([])
  })

  // The toggle lives on the Metadata tab, so a link to bare /settings dropped
  // the user on General with nothing to find.
  it('sends the call to action to the tab the setting is actually on', async () => {
    vi.mocked(api.listSettings).mockResolvedValue([{ key: 'recommendations.enabled', value: 'false' }] as never)
    renderPage()

    const cta = await screen.findByRole('link', { name: 'Go to book recommendations' })
    expect(cta).toHaveAttribute('href', '/settings?tab=metadata')
  })

  it('hides Refresh, which cannot produce anything while the feature is off', async () => {
    vi.mocked(api.listSettings).mockResolvedValue([{ key: 'recommendations.enabled', value: 'false' }] as never)
    renderPage()

    await screen.findByRole('link', { name: 'Go to book recommendations' })
    expect(screen.queryByRole('button', { name: 'Refresh' })).not.toBeInTheDocument()
  })

  it('keeps Refresh in the empty state where it can still change the result', async () => {
    vi.mocked(api.listSettings).mockResolvedValue([{ key: 'recommendations.enabled', value: 'true' }] as never)
    renderPage()

    await screen.findByText("Your library doesn't have enough data yet.")
    await waitFor(() => expect(screen.getByRole('button', { name: 'Refresh' })).toBeInTheDocument())
  })
})
