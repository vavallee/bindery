import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import AddBookModal from './AddBookModal'

// Mock the api/client module so no real HTTP calls are made.
vi.mock('../api/client', () => ({
  api: {
    searchBooks: vi.fn(),
    lookupISBN: vi.fn(),
    addBook: vi.fn(),
  },
}))

import { api } from '../api/client'

describe('AddBookModal — null search results (#1188)', () => {
  const onClose = vi.fn()
  const onAdded = vi.fn()

  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('renders "No results found." when the search returns null instead of crashing', async () => {
    // The backend can encode an empty success as `null`; the modal must treat
    // that as an empty list rather than calling `.map()` on null.
    vi.mocked(api.searchBooks).mockResolvedValue(null as unknown as never)

    render(<AddBookModal onClose={onClose} onAdded={onAdded} />)

    fireEvent.change(screen.getByPlaceholderText(/Title or ISBN/i), {
      target: { value: 'qzznomatch' },
    })
    fireEvent.click(screen.getByRole('button', { name: /^search$/i }))

    await waitFor(() =>
      expect(screen.getByText(/no results found/i)).toBeInTheDocument()
    )
  })
})
