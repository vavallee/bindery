import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import RebindModal from './RebindModal'

// Keep the real ApiError so `instanceof ApiError` works inside the component;
// mock only the `api` surface so no real HTTP is made.
vi.mock('../api/client', async () => {
  const actual = await vi.importActual<typeof import('../api/client')>('../api/client')
  return {
    ...actual,
    api: { rebindBook: vi.fn() },
  }
})

import { api, ApiError } from '../api/client'
import type { Book } from '../api/client'

function book(overrides: Partial<Book> = {}): Book {
  return { id: 7, title: 'State of the Union', ...overrides } as Book
}

const rebindBook = vi.mocked(api.rebindBook)

beforeEach(() => {
  rebindBook.mockReset()
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
    fireEvent.change(screen.getByPlaceholderText(/OL12345W/), { target: { value: 'OL999W' } })
    fireEvent.click(screen.getByRole('button', { name: 'Re-bind' }))

    await waitFor(() => expect(screen.getByText('upstream unavailable')).toBeInTheDocument())
    expect(screen.queryByText('Author mismatch')).not.toBeInTheDocument()
  })
})
