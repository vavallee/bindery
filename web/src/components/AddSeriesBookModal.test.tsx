import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import AddSeriesBookModal from './AddSeriesBookModal'
import { api } from '../api/client'
import type { Book, Series } from '../api/client'

vi.mock('../api/client', async importOriginal => {
  const actual = await importOriginal<typeof import('../api/client')>()
  return {
    ...actual,
    api: {
      ...actual.api,
      listAllBooks: vi.fn(async () => [{ id: 7, title: 'Fjellvinden', authorId: 1 } as Book]),
      listAllAuthors: vi.fn(async () => []),
      linkBookToSeries: vi.fn(async () => series),
    },
  }
})

const series: Series = { id: 3, title: 'Fjellserien', foreignSeriesId: 's:3', description: '', monitored: false, books: [] }

describe('AddSeriesBookModal', () => {
  // The server takes a book without a position (unnumbered series, omnibus
  // volumes), so the dialog does too.
  it('adds a book without a position', async () => {
    const onLinked = vi.fn()
    render(<AddSeriesBookModal series={series} onClose={() => {}} onLinked={onLinked} />)
    fireEvent.click(await screen.findByRole('radio', { name: /Fjellvinden/ }))
    expect(screen.getByLabelText('Position (optional)')).toHaveValue('')
    fireEvent.click(screen.getByRole('button', { name: 'Add' }))
    await waitFor(() => expect(onLinked).toHaveBeenCalled())
    expect(api.linkBookToSeries).toHaveBeenCalledWith(3, { bookId: 7, positionInSeries: '', primarySeries: true })
  })
})
