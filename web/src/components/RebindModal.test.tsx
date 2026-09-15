import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor, act } from '@testing-library/react'
import RebindModal from './RebindModal'
import i18n from '../i18n'

// Keep the real ApiError so `instanceof ApiError` works inside the component;
// mock only the `api` surface so no real HTTP is made.
vi.mock('../api/client', async () => {
  const actual = await vi.importActual<typeof import('../api/client')>('../api/client')
  return {
    ...actual,
    api: { rebindBook: vi.fn(), searchBooks: vi.fn(), lookupISBN: vi.fn(), lookupASIN: vi.fn() },
  }
})

import { api, ApiError } from '../api/client'
import type { Book } from '../api/client'

function book(overrides: Partial<Book> = {}): Book {
  return { id: 7, title: 'State of the Union', ...overrides } as Book
}

const rebindBook = vi.mocked(api.rebindBook)

beforeEach(() => {
  vi.resetAllMocks()
})

describe('RebindModal', () => {
  it('surfaces the force-override panel on a 409 author mismatch instead of a dead-end error', async () => {
    // First submit (force=false) → backend 409 with force_required.
    rebindBook.mockRejectedValueOnce(
      new ApiError(
        409,
        {
          error: 'author mismatch: upstream record belongs to a different author',
          force_required: true,
          current_author: 'Old Author',
          upstream_author: 'New Author',
        },
        'Conflict',
      ),
    )

    render(<RebindModal book={book()} onClose={() => {}} onSuccess={() => {}} />)

    fireEvent.click(screen.getByRole('button', { name: 'Enter an ID' }))
    fireEvent.change(screen.getByPlaceholderText(/OL12345W/), { target: { value: 'OL999W' } })
    fireEvent.click(screen.getByRole('button', { name: 'Re-bind' }))

    // The amber override panel must appear — not the raw red error text.
    await waitFor(() => {
      expect(screen.getByText('Author mismatch')).toBeInTheDocument()
    })
    expect(screen.getByText(/New Author/)).toBeInTheDocument()
    expect(screen.getByText(/Old Author/)).toBeInTheDocument()
    const again = screen.getByRole('button', { name: 'Re-bind anyway' })
    expect(again).toBeInTheDocument()

    // Confirming retries with force=true.
    rebindBook.mockResolvedValueOnce(book({ title: 'State of the Union (corrected)' }))
    fireEvent.click(again)
    await waitFor(() => {
      expect(rebindBook).toHaveBeenLastCalledWith(7, 'openlibrary', 'OL999W', true)
    })
  })

  it('calls onSuccess with force=true after confirming the override', async () => {
    rebindBook
      .mockRejectedValueOnce(
        new ApiError(409, { error: 'author mismatch', force_required: true, current_author: 'A', upstream_author: 'B' }, 'Conflict'),
      )
    const updated = book({ title: 'fixed' })
    rebindBook.mockResolvedValueOnce(updated)

    const onSuccess = vi.fn()
    render(<RebindModal book={book()} onClose={() => {}} onSuccess={onSuccess} />)

    fireEvent.click(screen.getByRole('button', { name: 'Enter an ID' }))
    fireEvent.change(screen.getByPlaceholderText(/OL12345W/), { target: { value: 'OL999W' } })
    fireEvent.click(screen.getByRole('button', { name: 'Re-bind' }))
    const again = await screen.findByRole('button', { name: 'Re-bind anyway' })
    fireEvent.click(again)

    await waitFor(() => expect(onSuccess).toHaveBeenCalledWith(updated))
    expect(rebindBook).toHaveBeenLastCalledWith(7, 'openlibrary', 'OL999W', true)
  })

  it('shows a plain error for a non-409 failure', async () => {
    rebindBook.mockRejectedValueOnce(new ApiError(502, { error: 'upstream unavailable' }, 'Bad Gateway'))

    render(<RebindModal book={book()} onClose={() => {}} onSuccess={() => {}} />)
    fireEvent.click(screen.getByRole('button', { name: 'Enter an ID' }))
    fireEvent.change(screen.getByPlaceholderText(/OL12345W/), { target: { value: 'OL999W' } })
    fireEvent.click(screen.getByRole('button', { name: 'Re-bind' }))

    await waitFor(() => expect(screen.getByText('upstream unavailable')).toBeInTheDocument())
    expect(screen.queryByText('Author mismatch')).not.toBeInTheDocument()
  })

  it('submits exact IDs through the manual-entry form and retains mismatch confirmation', async () => {
    rebindBook.mockRejectedValueOnce(new ApiError(409, { force_required: true, current_author: 'A', upstream_author: 'B' }, 'Conflict'))
      .mockResolvedValueOnce(book())
    render(<RebindModal book={book()} onClose={() => {}} onSuccess={() => {}} />)
    fireEvent.click(screen.getByRole('button', { name: 'Enter an ID' }))
    const input = screen.getByLabelText<HTMLInputElement>('Foreign ID')
    const submit = screen.getByRole<HTMLButtonElement>('button', { name: 'Re-bind' })
    expect(input.form).not.toBeNull()
    expect(submit.form).toBe(input.form)
    expect(submit).toHaveAttribute('type', 'submit')
    fireEvent.submit(input.form!)
    expect(rebindBook).not.toHaveBeenCalled()
    fireEvent.change(input, { target: { value: ' OL999W ' } })
    fireEvent.submit(input.form!)
    const confirm = await screen.findByRole('button', { name: 'Re-bind anyway' })
    expect(rebindBook).toHaveBeenCalledExactlyOnceWith(7, 'openlibrary', 'OL999W', false)
    expect(confirm).toHaveAttribute('type', 'button')
    fireEvent.click(confirm)
    await waitFor(() => expect(rebindBook).toHaveBeenLastCalledWith(7, 'openlibrary', 'OL999W', true))
  })

  it.each([
    ['openlibrary', 'hc:12345'],
    ['openlibrary', 'OL999M'],
    ['openlibrary', 'OLabcW'],
    ['openlibrary', 'ol999w'],
    ['openlibrary', 'https://openlibrary.org/works/OL999W'],
    ['hardcover', 'OL999W'],
    ['hardcover', '12345'],
    ['hardcover', 'hc:'],
    ['hardcover', 'hc: 12345'],
    ['hardcover', 'hc:book/title'],
    ['hardcover', 'HC:12345'],
  ])('rejects invalid manual %s identifier %s on both click and form submission', (provider, id) => {
    render(<RebindModal book={book()} onClose={() => {}} onSuccess={() => {}} />)
    fireEvent.click(screen.getByRole('button', { name: 'Enter an ID' }))
    fireEvent.change(screen.getByLabelText('Provider'), { target: { value: provider } })
    const input = screen.getByLabelText<HTMLInputElement>('Foreign ID')
    fireEvent.change(input, { target: { value: id } })
    const submit = screen.getByRole('button', { name: 'Re-bind' })
    expect(submit).toBeDisabled()
    fireEvent.click(submit)
    fireEvent.submit(input.form!)
    expect(rebindBook).not.toHaveBeenCalled()
  })

  it.each([
    ['openlibrary', 'OL999W'],
    ['hardcover', 'hc:12345'],
    ['hardcover', 'hc:state-of-the-union'],
  ])('accepts a trimmed manual %s identifier and revalidates provider changes', async (provider, id) => {
    rebindBook.mockResolvedValueOnce(book())
    render(<RebindModal book={book()} onClose={() => {}} onSuccess={() => {}} />)
    fireEvent.click(screen.getByRole('button', { name: 'Enter an ID' }))
    const providerInput = screen.getByLabelText('Provider')
    fireEvent.change(providerInput, { target: { value: provider } })
    const input = screen.getByLabelText<HTMLInputElement>('Foreign ID')
    fireEvent.change(input, { target: { value: `  ${id}  ` } })
    const submit = screen.getByRole('button', { name: 'Re-bind' })
    expect(submit).toBeEnabled()
    fireEvent.change(providerInput, { target: { value: provider === 'openlibrary' ? 'hardcover' : 'openlibrary' } })
    expect(submit).toBeDisabled()
    fireEvent.submit(input.form!)
    expect(rebindBook).not.toHaveBeenCalled()
    fireEvent.change(providerInput, { target: { value: provider } })
    fireEvent.submit(input.form!)
    await waitFor(() => expect(rebindBook).toHaveBeenCalledExactlyOnceWith(7, provider, id, false))
  })
})


describe('metadata search', () => {
  const hardcover = book({ foreignBookId: 'hc:state-of-the-union', title: 'State of the Union', author: { authorName: 'Correct Author' } as Book['author'] })

  it('marks owned results, blocks other library rows, and still allows the current book', async () => {
    vi.stubGlobal('__BINDERY_BASE__', '/bindery')
    try {
      vi.mocked(api.searchBooks).mockResolvedValue([
        book({ foreignBookId: 'OL99W', title: 'Other library book', libraryBookId: 99 }),
        book({ foreignBookId: 'OL7W', title: 'Current book', libraryBookId: 7 }),
        hardcover,
      ])
      rebindBook.mockResolvedValueOnce(book())
      render(<RebindModal book={book()} onClose={() => {}} onSuccess={() => {}} />)
      fireEvent.click(screen.getByRole('button', { name: 'Search' }))
      const other = await screen.findByRole('radio', { name: /Other library book/ })
      expect(other).toBeDisabled()
      fireEvent.click(screen.getByText('Other library book'))
      expect(screen.getByRole('button', { name: 'Re-bind' })).toBeDisabled()
      expect(screen.getAllByText('In your library')).toHaveLength(2)
      expect(screen.getByRole('link', { name: 'Open Other library book' })).toHaveAttribute('href', '/bindery/book/99')
      expect(rebindBook).not.toHaveBeenCalled()
      const current = screen.getByRole('radio', { name: /Current book/ })
      expect(current).toBeEnabled()
      fireEvent.click(current)
      fireEvent.click(screen.getByRole('button', { name: 'Re-bind' }))
      await waitFor(() => expect(rebindBook).toHaveBeenCalledExactlyOnceWith(7, 'openlibrary', 'OL7W', false))
    } finally {
      vi.unstubAllGlobals()
    }
  })

  it('uses the same identifier format rules for search candidates', async () => {
    vi.mocked(api.searchBooks).mockResolvedValue([
      hardcover,
      ...['hc:', 'hc: ', 'hc:book/title', 'OLabcW', 'HC:12345', 'ol999w'].map(foreignBookId => book({ foreignBookId })),
    ])
    render(<RebindModal book={book()} onClose={() => {}} onSuccess={() => {}} />)
    fireEvent.click(screen.getByRole('button', { name: 'Search' }))
    const results = await screen.findAllByRole('radio')
    expect(results).toHaveLength(1)
    fireEvent.click(results[0])
    expect(screen.getByRole('button', { name: 'Re-bind' })).toBeEnabled()
  })

  it('searches with the existing title, then rebinds only after a result is selected and confirmed', async () => {
    vi.mocked(api.searchBooks).mockResolvedValue([hardcover, book({ foreignBookId: 'OL99W', title: 'Another edition' })])
    const onSuccess = vi.fn()
    rebindBook.mockResolvedValue(hardcover)
    render(<RebindModal book={book({ foreignBookId: 'abs:item', metadataProvider: 'audiobookshelf' })} onClose={() => {}} onSuccess={onSuccess} />)
    expect(screen.getByLabelText('Title, author, ISBN or ASIN')).toHaveValue('State of the Union')
    expect(screen.getByLabelText('Title, author, ISBN or ASIN')).toHaveFocus()
    expect(screen.getByRole('button', { name: 'Re-bind' })).toBeDisabled()
    fireEvent.click(screen.getByRole('button', { name: 'Search' }))
    const result = await screen.findByRole('radio', { name: /Correct Author/ })
    expect(api.searchBooks).toHaveBeenCalledWith('State of the Union')
    expect(rebindBook).not.toHaveBeenCalled()
    fireEvent.click(result)
    expect(rebindBook).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Re-bind' }))
    await waitFor(() => expect(onSuccess).toHaveBeenCalledWith(hardcover))
    expect(rebindBook).toHaveBeenCalledWith(7, 'hardcover', 'hc:state-of-the-union', false)
  })

  it('uses the ISBN lookup and excludes results the rebind endpoint cannot accept', async () => {
    vi.mocked(api.lookupISBN).mockResolvedValue(book({ foreignBookId: 'gb:123', title: 'Unsupported provider' }))
    render(<RebindModal book={book()} onClose={() => {}} onSuccess={() => {}} />)
    fireEvent.change(screen.getByLabelText('Title, author, ISBN or ASIN'), { target: { value: '978-0-306-40615-7' } })
    fireEvent.click(screen.getByRole('button', { name: 'Search' }))
    await screen.findByText(/No OpenLibrary or Hardcover matches/)
    expect(api.lookupISBN).toHaveBeenCalledWith('9780306406157')
    expect(api.searchBooks).not.toHaveBeenCalled()
    expect(screen.queryByRole('radio')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Re-bind' })).toBeDisabled()
  })

  it('discards an obsolete search response when the query changes', async () => {
    let finish!: (value: Book[]) => void
    vi.mocked(api.searchBooks).mockReturnValue(new Promise(resolve => { finish = resolve }))
    render(<RebindModal book={book()} onClose={() => {}} onSuccess={() => {}} />)
    fireEvent.click(screen.getByRole('button', { name: 'Search' }))
    fireEvent.change(screen.getByLabelText('Title, author, ISBN or ASIN'), { target: { value: 'New query' } })
    await act(async () => { finish([hardcover]) })
    expect(screen.queryByRole('radio')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Re-bind' })).toBeDisabled()
  })

  it('keeps ISBN-only OpenLibrary editions viewable without allowing rebind', async () => {
    vi.mocked(api.lookupISBN).mockResolvedValue(book({ foreignBookId: 'OL999M', title: 'Standalone edition' }))
    render(<RebindModal book={book()} onClose={() => {}} onSuccess={() => {}} />)
    fireEvent.change(screen.getByLabelText('Title, author, ISBN or ASIN'), { target: { value: '9780306406157' } })
    fireEvent.click(screen.getByRole('button', { name: 'Search' }))
    const edition = await screen.findByRole('radio', { name: /Standalone edition/ })
    expect(edition).toBeDisabled()
    fireEvent.click(screen.getByText('Standalone edition'))
    expect(edition).not.toBeChecked()
    expect(screen.getByText(/Re-bind requires an OpenLibrary work/)).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Links' }))
    expect(screen.getByRole('link', { name: /View on OpenLibrary/ })).toHaveAttribute('href', 'https://openlibrary.org/books/OL999M')
    const submit = screen.getByRole('button', { name: 'Re-bind' })
    expect(submit).toBeDisabled()
    fireEvent.click(submit)
    expect(rebindBook).not.toHaveBeenCalled()
  })

  it('shows search errors and allows retrying', async () => {
    vi.mocked(api.searchBooks).mockRejectedValueOnce(new Error('Provider unavailable')).mockResolvedValueOnce([])
    render(<RebindModal book={book()} onClose={() => {}} onSuccess={() => {}} />)
    fireEvent.click(screen.getByRole('button', { name: 'Search' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Provider unavailable')
    fireEvent.click(screen.getByRole('button', { name: 'Search' }))
    await screen.findByText(/No OpenLibrary or Hardcover matches/)
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('keeps the selected identifier when an author mismatch requires confirmation', async () => {
    vi.mocked(api.searchBooks).mockResolvedValue([hardcover])
    rebindBook.mockRejectedValueOnce(new ApiError(409, { force_required: true, current_author: 'A', upstream_author: 'B' }, 'Conflict')).mockResolvedValueOnce(hardcover)
    render(<RebindModal book={book()} onClose={() => {}} onSuccess={() => {}} />)
    fireEvent.click(screen.getByRole('button', { name: 'Search' }))
    fireEvent.click(await screen.findByRole('radio'))
    fireEvent.click(screen.getByRole('button', { name: 'Re-bind' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Re-bind anyway' }))
    await waitFor(() => expect(rebindBook).toHaveBeenLastCalledWith(7, 'hardcover', 'hc:state-of-the-union', true))
  })

  it('preserves exact Hardcover ID entry and reports duplicate conflicts without offering an override', async () => {
    rebindBook.mockRejectedValueOnce(new ApiError(409, { error: 'a different book already uses that foreign ID' }, 'Conflict'))
    render(<RebindModal book={book()} onClose={() => {}} onSuccess={() => {}} />)
    fireEvent.click(screen.getByRole('button', { name: 'Enter an ID' }))
    fireEvent.change(screen.getByLabelText('Provider'), { target: { value: 'hardcover' } })
    fireEvent.change(screen.getByLabelText('Foreign ID'), { target: { value: '  hc:12345  ' } })
    fireEvent.click(screen.getByRole('button', { name: 'Re-bind' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('This metadata record conflicts with an existing book.')
    expect(rebindBook).toHaveBeenCalledWith(7, 'hardcover', 'hc:12345', false)
    expect(screen.queryByRole('button', { name: 'Re-bind anyway' })).not.toBeInTheDocument()
  })
})


it('translates duplicate-ID conflicts instead of displaying the server message', async () => {
  await i18n.changeLanguage('fr')
  try {
    rebindBook.mockRejectedValueOnce(new ApiError(409, { error: 'a different book already uses that foreign ID' }, 'Conflict'))
    render(<RebindModal book={book()} onClose={() => {}} onSuccess={() => {}} />)
    fireEvent.click(screen.getByRole('button', { name: i18n.t('bookRebind.manual') }))
    fireEvent.change(screen.getByLabelText(i18n.t('bookRebind.identifier')), { target: { value: 'OL999W' } })
    fireEvent.click(screen.getByRole('button', { name: i18n.t('bookDetail.rebind') }))
    expect(await screen.findByRole('alert')).toHaveTextContent(i18n.t('bookRebind.conflict'))
    expect(screen.queryByText('a different book already uses that foreign ID')).not.toBeInTheDocument()
  } finally {
    await act(async () => { await i18n.changeLanguage('en') })
  }
})


it('reuses upstream links without selecting or rebinding, including OpenLibrary editions', async () => {
  vi.mocked(api.searchBooks).mockResolvedValue([
    book({ foreignBookId: 'hc:dune-messiah', title: 'Dune Messiah' }),
    book({ foreignBookId: 'OL12345M', title: 'Dune Messiah edition' }),
    book({ foreignBookId: 'hc:12345', title: 'Numeric Hardcover ID' }),
  ])
  render(<RebindModal book={book()} onClose={() => {}} onSuccess={() => {}} />)
  fireEvent.click(screen.getByRole('button', { name: 'Search' }))
  const menus = await screen.findAllByRole('button', { name: 'Links' })
  expect(menus).toHaveLength(2) // Numeric Hardcover IDs have no reliable public URL.
  fireEvent.click(menus[0])
  const hardcoverLink = screen.getByRole('link', { name: /View on Hardcover/ })
  expect(hardcoverLink).toHaveAttribute('href', 'https://hardcover.app/books/dune-messiah')
  expect(hardcoverLink).toHaveAttribute('target', '_blank')
  expect(hardcoverLink).toHaveAttribute('rel', 'noopener noreferrer')
  fireEvent.click(menus[1])
  expect(screen.getByRole('link', { name: /View on OpenLibrary/ })).toHaveAttribute('href', 'https://openlibrary.org/books/OL12345M')
  expect(screen.getAllByRole('radio').every(radio => !(radio as HTMLInputElement).checked)).toBe(true)
  expect(screen.getByRole('button', { name: 'Re-bind' })).toBeDisabled()
  expect(rebindBook).not.toHaveBeenCalled()
})


it('restores focus to the opener when the dialog unmounts', () => {
  const opener = document.createElement('button')
  document.body.append(opener)
  opener.focus()
  const { unmount } = render(<RebindModal book={book()} onClose={() => {}} onSuccess={() => {}} />)
  expect(screen.getByLabelText('Title, author, ISBN or ASIN')).toHaveFocus()
  unmount()
  expect(opener).toHaveFocus()
  opener.remove()
})
