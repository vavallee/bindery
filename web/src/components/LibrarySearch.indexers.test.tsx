import { describe, it, expect, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router'
import LibrarySearch from './LibrarySearch'
import { api } from '../api/client'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, opts?: Record<string, unknown>) =>
      key === 'librarySearch.searchIndexers' ? `Search indexers for ${String(opts?.query)}` : key,
  }),
}))

function Where() {
  const loc = useLocation()
  return <p data-testid="where">{loc.pathname}{loc.search}</p>
}

describe('LibrarySearch indexer row', () => {
  // The magnifier icon beside the box opened indexer search with the same
  // glyph as the library search. Indexer search is now a row in the results.
  it('offers indexer search for the query and opens /search with it', async () => {
    vi.spyOn(api, 'searchLibrary').mockResolvedValue({ authors: [], books: [], series: [] })
    render(
      <MemoryRouter>
        <LibrarySearch />
        <Routes><Route path="*" element={<Where />} /></Routes>
      </MemoryRouter>,
    )
    fireEvent.change(screen.getByRole('combobox'), { target: { value: 'dune messiah' } })
    const row = await screen.findByTestId('library-search-indexers')
    expect(row).toHaveTextContent('Search indexers for dune messiah')
    fireEvent.click(row)
    expect(screen.getByTestId('where')).toHaveTextContent('/search?q=dune%20messiah')
  })
})
