import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'

const COMMON: Record<string, string> = {
  'common.save': 'Save',
  'common.saving': 'Saving...',
  'common.cancel': 'Cancel',
}

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, def?: string | Record<string, unknown>) => {
      if (typeof def === 'string') return def
      return COMMON[key] ?? key
    },
  }),
}))

vi.mock('../api/client', () => ({
  api: {
    updateBook: vi.fn(),
  },
}))

import { api } from '../api/client'
import EditBookModal from './EditBookModal'
import type { Book } from '../api/client'

const BOOK = {
  id: 7,
  title: 'Original',
  description: 'Desc',
  genres: ['Old'],
  language: 'en',
  releaseDate: '2020-01-02T00:00:00Z',
  lockedFields: [],
} as unknown as Book

describe('EditBookModal (#1237, #1446)', () => {
  const onClose = vi.fn()
  const onSaved = vi.fn()

  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(api.updateBook).mockResolvedValue({ ...BOOK, title: 'Changed' } as never)
  })

  it('sends only the fields the user changed', async () => {
    render(<EditBookModal book={BOOK} onClose={onClose} onSaved={onSaved} />)
    fireEvent.change(screen.getByLabelText('Title'), { target: { value: 'Changed' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(api.updateBook).toHaveBeenCalled())
    expect(vi.mocked(api.updateBook).mock.calls[0]).toEqual([7, { title: 'Changed' }])
    expect(onSaved).toHaveBeenCalled()
    expect(onClose).toHaveBeenCalled()
  })

  it('parses comma-separated genres', async () => {
    render(<EditBookModal book={BOOK} onClose={onClose} onSaved={onSaved} />)
    fireEvent.change(screen.getByLabelText('Genres (comma-separated)'), {
      target: { value: ' Fantasy , Epic ,, ' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(api.updateBook).toHaveBeenCalled())
    expect(vi.mocked(api.updateBook).mock.calls[0][1]).toEqual({ genres: ['Fantasy', 'Epic'] })
  })

  it('closes without a request when nothing changed', async () => {
    render(<EditBookModal book={BOOK} onClose={onClose} onSaved={onSaved} />)
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(onClose).toHaveBeenCalled())
    expect(api.updateBook).not.toHaveBeenCalled()
  })

  it('shows the unlock action only when fields are locked, and it clears the set', async () => {
    render(<EditBookModal book={BOOK} onClose={onClose} onSaved={onSaved} />)
    expect(screen.queryByRole('button', { name: 'Unlock all fields' })).toBeNull()

    render(
      <EditBookModal
        book={{ ...BOOK, lockedFields: ['title', 'genres'] } as Book}
        onClose={onClose}
        onSaved={onSaved}
      />,
    )
    fireEvent.click(screen.getByRole('button', { name: 'Unlock all fields' }))
    await waitFor(() => expect(api.updateBook).toHaveBeenCalled())
    expect(vi.mocked(api.updateBook).mock.calls[0]).toEqual([7, { lockedFields: [] }])
  })
})
