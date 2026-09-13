import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, act } from '@testing-library/react'
import { MemoryRouter, Routes, Route, useLocation } from 'react-router'
import LibrarySearch from './LibrarySearch'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, options?: Record<string, unknown>) => {
      if (key === 'librarySearch.addToBindery') return `Add "${options?.query ?? ''}" to Bindery`
      return ({
        'librarySearch.label': 'Search library',
        'librarySearch.placeholder': 'Search library',
        'librarySearch.authors': 'Authors',
        'librarySearch.books': 'Books',
        'librarySearch.series': 'Series',
        'librarySearch.searching': 'Searching…',
        'librarySearch.noMatches': 'Nothing in your library matches',
        'librarySearch.failed': 'Library search failed',
      }[key] ?? key)
    },
  }),
}))

vi.mock('../api/client', () => ({
  api: {
    searchLibrary: vi.fn(),
  },
}))

// The real modal fans out to the metadata search; stub it so this test only
// proves the handoff (the prop it receives), not the modal itself.
vi.mock('./AddBookModal', () => ({
  default: ({ initialQuery }: { initialQuery?: string }) => (
    <div role="dialog" data-testid="add-book-modal">{initialQuery}</div>
  ),
}))

import { api } from '../api/client'

function LocationProbe() {
  const location = useLocation()
  const state = location.state as { seriesId?: number } | null
  return <div data-testid="location">{location.pathname}{state?.seriesId ? `#series=${state.seriesId}` : ''}</div>
}

function renderSearch(props: Partial<React.ComponentProps<typeof LibrarySearch>> = {}) {
  return render(
    <MemoryRouter initialEntries={['/']}>
      <LibrarySearch {...props} />
      <Routes>
        <Route path="*" element={<LocationProbe />} />
      </Routes>
    </MemoryRouter>,
  )
}

const results = {
  authors: [{ id: 3, name: 'Ursula K. Le Guin' }],
  books: [{ id: 11, title: 'A Wizard of Earthsea', authorId: 3, authorName: 'Ursula K. Le Guin' }],
  series: [{ id: 7, title: 'Earthsea Cycle' }],
}

async function typeAndWait(value: string) {
  const input = screen.getByRole('combobox', { name: 'Search library' })
  fireEvent.change(input, { target: { value } })
  // Past the 300 ms debounce.
  await act(async () => {
    await vi.advanceTimersByTimeAsync(350)
  })
  return input
}

describe('LibrarySearch', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.useFakeTimers()
  })

  it('renders nothing and calls nothing for an empty query', async () => {
    renderSearch()
    await typeAndWait('   ')
    expect(api.searchLibrary).not.toHaveBeenCalled()
    expect(screen.queryByRole('listbox')).not.toBeInTheDocument()
    expect(screen.queryByTestId('library-search-add')).not.toBeInTheDocument()
  })

  it('debounces keystrokes into one request', async () => {
    vi.mocked(api.searchLibrary).mockResolvedValue(results)
    renderSearch()
    const input = screen.getByRole('combobox', { name: 'Search library' })
    fireEvent.change(input, { target: { value: 'e' } })
    fireEvent.change(input, { target: { value: 'ea' } })
    fireEvent.change(input, { target: { value: 'earth' } })
    await act(async () => {
      await vi.advanceTimersByTimeAsync(350)
    })
    expect(api.searchLibrary).toHaveBeenCalledTimes(1)
    expect(api.searchLibrary).toHaveBeenCalledWith('earth')
  })

  it('renders the three sections and the add row', async () => {
    vi.mocked(api.searchLibrary).mockResolvedValue(results)
    renderSearch()
    await typeAndWait('earth')

    const list = screen.getByRole('listbox')
    expect(list).toBeVisible()
    expect(screen.getByText('Authors')).toBeInTheDocument()
    expect(screen.getByText('Books')).toBeInTheDocument()
    expect(screen.getByText('Series')).toBeInTheDocument()
    expect(screen.getByRole('option', { name: 'Ursula K. Le Guin' })).toBeInTheDocument()
    expect(screen.getByRole('option', { name: /A Wizard of Earthsea/ })).toBeInTheDocument()
    expect(screen.getByRole('option', { name: 'Earthsea Cycle' })).toBeInTheDocument()
    expect(screen.getByRole('option', { name: 'Add "earth" to Bindery' })).toBeInTheDocument()
  })

  it('shows the no-match message but keeps the add row', async () => {
    vi.mocked(api.searchLibrary).mockResolvedValue({ authors: [], books: [], series: [] })
    renderSearch()
    await typeAndWait('zzz')
    expect(screen.getByText('Nothing in your library matches')).toBeInTheDocument()
    expect(screen.getByRole('option', { name: 'Add "zzz" to Bindery' })).toBeInTheDocument()
  })

  it('navigates to the author, book and series on click', async () => {
    vi.mocked(api.searchLibrary).mockResolvedValue(results)
    renderSearch()

    await typeAndWait('earth')
    fireEvent.click(screen.getByRole('option', { name: /A Wizard of Earthsea/ }))
    expect(screen.getByTestId('location')).toHaveTextContent('/book/11')
    // Selecting clears the box and closes the list.
    expect(screen.getByRole('combobox', { name: 'Search library' })).toHaveValue('')
    expect(screen.queryByRole('listbox')).not.toBeInTheDocument()

    await typeAndWait('earth')
    fireEvent.click(screen.getByRole('option', { name: 'Ursula K. Le Guin' }))
    expect(screen.getByTestId('location')).toHaveTextContent('/author/3')

    await typeAndWait('earth')
    fireEvent.click(screen.getByRole('option', { name: 'Earthsea Cycle' }))
    expect(screen.getByTestId('location')).toHaveTextContent('/series#series=7')
  })

  it('moves with the arrow keys and selects with Enter', async () => {
    vi.mocked(api.searchLibrary).mockResolvedValue(results)
    renderSearch()
    const input = await typeAndWait('earth')

    fireEvent.keyDown(input, { key: 'ArrowDown' })
    fireEvent.keyDown(input, { key: 'ArrowDown' })
    const book = screen.getByRole('option', { name: /A Wizard of Earthsea/ })
    expect(book).toHaveAttribute('aria-selected', 'true')
    expect(input).toHaveAttribute('aria-activedescendant', book.id)

    fireEvent.keyDown(input, { key: 'Enter' })
    expect(screen.getByTestId('location')).toHaveTextContent('/book/11')
  })

  it('Escape closes the dropdown and does not clear the query', async () => {
    vi.mocked(api.searchLibrary).mockResolvedValue(results)
    renderSearch()
    const input = await typeAndWait('earth')
    expect(screen.getByRole('listbox')).toBeVisible()

    fireEvent.keyDown(input, { key: 'Escape' })
    expect(screen.queryByRole('listbox')).not.toBeInTheDocument()
    expect(input).toHaveValue('earth')
    expect(input).toHaveAttribute('aria-expanded', 'false')

    // A second Escape on a closed list clears the box.
    fireEvent.keyDown(input, { key: 'Escape' })
    expect(input).toHaveValue('')
  })

  it('the add row opens the Add Book modal with the query', async () => {
    vi.mocked(api.searchLibrary).mockResolvedValue({ authors: [], books: [], series: [] })
    renderSearch()
    await typeAndWait('The Dispossessed')
    fireEvent.click(screen.getByTestId('library-search-add'))

    const modal = screen.getByTestId('add-book-modal')
    expect(modal).toHaveTextContent('The Dispossessed')
    expect(screen.queryByRole('listbox')).not.toBeInTheDocument()
  })

  it('Enter with nothing highlighted takes the add row', async () => {
    vi.mocked(api.searchLibrary).mockResolvedValue(results)
    renderSearch()
    const input = await typeAndWait('earth')
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(screen.getByTestId('add-book-modal')).toHaveTextContent('earth')
  })

  it('ignores a stale response that lands after a newer query', async () => {
    let resolveFirst: (value: typeof results) => void = () => {}
    vi.mocked(api.searchLibrary)
      .mockImplementationOnce(() => new Promise(resolve => { resolveFirst = resolve }))
      .mockResolvedValueOnce({ authors: [], books: [], series: [{ id: 9, title: 'Hainish Cycle' }] })
    renderSearch()
    await typeAndWait('earth')
    await typeAndWait('hainish')
    expect(screen.getByRole('option', { name: 'Hainish Cycle' })).toBeInTheDocument()

    await act(async () => {
      resolveFirst(results)
    })
    expect(screen.queryByRole('option', { name: 'Earthsea Cycle' })).not.toBeInTheDocument()
    expect(screen.getByRole('option', { name: 'Hainish Cycle' })).toBeInTheDocument()
  })
})
