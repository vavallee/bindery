import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import AddBookConfirm from './AddBookConfirm'
import AddAuthorConfirm from './AddAuthorConfirm'

// For a requester the Add Book and Add Author confirm steps become a request:
// only the kind, the provider id and a format go to POST /requests, nothing
// is added, and none of the admin lookups (profiles, root folders, indexers,
// clients) run.

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, fallback?: unknown) => {
      const strings: Record<string, string> = {
        'requests.confirm.requestBook': 'Request book',
        'requests.confirm.requestAuthor': 'Request author',
        'requests.confirm.formatLabel': 'Format to request',
        'requests.confirm.bookHint': 'An admin will look at your request.',
        'requests.confirm.authorHint': 'An admin will look at your request.',
        'common.ebook': 'Ebook',
        'common.audiobook': 'Audiobook',
        'common.both': 'Both',
        'common.cancel': 'Cancel',
      }
      return strings[key] ?? (typeof fallback === 'string' ? fallback : key)
    },
  }),
}))

vi.mock('../auth/AuthContext', () => ({
  useIsRequester: () => true,
}))

vi.mock('../api/client', () => ({
  api: {
    createRequest: vi.fn(),
    addBook: vi.fn(),
    addAuthor: vi.fn(),
    listIndexers: vi.fn(),
    listDownloadClients: vi.fn(),
    listMetadataProfiles: vi.fn(),
    listRootFolders: vi.fn(),
    getSetting: vi.fn(),
  },
}))

import { api } from '../api/client'
import type { Author, Book, LibraryRequest } from '../api/client'

const created: LibraryRequest = {
  id: 7, kind: 'book', foreignId: 'OL1W', mediaType: 'audiobook', title: 'Dune', authorName: 'Frank Herbert',
  status: 'pending', createdAt: '2026-09-17T00:00:00Z', fulfilled: false,
}

const dune = {
  id: 0, foreignBookId: 'OL1W', authorId: 0, title: 'Dune', description: '', imageUrl: '', genres: [],
  monitored: true, status: 'wanted', filePath: '', mediaType: 'ebook', ebookFilePath: '', audiobookFilePath: '',
  excluded: false, author: { authorName: 'Frank Herbert', foreignAuthorId: 'OL-FH' },
} as unknown as Book

const herbert = { foreignAuthorId: 'OL-FH', authorName: 'Frank Herbert', disambiguation: 'Dune' } as unknown as Author

describe('confirm steps for a requester', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(api.createRequest).mockResolvedValue(created)
  })

  it('sends a book request with only the kind, id and chosen format', async () => {
    const onRequested = vi.fn()
    const onAdded = vi.fn()
    render(<AddBookConfirm book={dune} searchedISBN={null} onBack={vi.fn()} onClose={vi.fn()} onAdded={onAdded} onRequested={onRequested} />)

    expect(screen.queryByRole('checkbox')).not.toBeInTheDocument()
    fireEvent.change(screen.getByRole('combobox', { name: 'Format to request' }), { target: { value: 'audiobook' } })
    fireEvent.click(screen.getByRole('button', { name: 'Request book' }))

    await waitFor(() => expect(onRequested).toHaveBeenCalledWith(created))
    expect(api.createRequest).toHaveBeenCalledWith('book', 'OL1W', 'audiobook')
    expect(api.addBook).not.toHaveBeenCalled()
    expect(onAdded).not.toHaveBeenCalled()
  })

  it('offers only the format the library does not already have', async () => {
    const half = { ...dune, requestableFormats: ['audiobook'] } as unknown as Book
    render(<AddBookConfirm book={half} searchedISBN={null} onBack={vi.fn()} onClose={vi.fn()} onAdded={vi.fn()} onRequested={vi.fn()} />)

    const select = screen.getByRole('combobox', { name: 'Format to request' }) as HTMLSelectElement
    expect(select.value).toBe('audiobook')
    const enabled = Array.from(select.options).filter(o => !o.disabled).map(o => o.value)
    expect(enabled).toEqual(['audiobook'])

    fireEvent.click(screen.getByRole('button', { name: 'Request book' }))
    await waitFor(() => expect(api.createRequest).toHaveBeenCalledWith('book', 'OL1W', 'audiobook'))
  })

  it('leaves every format open when both can still be requested', () => {
    const open = { ...dune, requestableFormats: ['ebook', 'audiobook'] } as unknown as Book
    render(<AddBookConfirm book={open} searchedISBN={null} onBack={vi.fn()} onClose={vi.fn()} onAdded={vi.fn()} onRequested={vi.fn()} />)
    const select = screen.getByRole('combobox', { name: 'Format to request' }) as HTMLSelectElement
    expect(select.value).toBe('')
    expect(Array.from(select.options).every(o => !o.disabled)).toBe(true)
  })

  it('sends an author request and runs none of the admin lookups', async () => {
    const onRequested = vi.fn()
    render(<AddAuthorConfirm author={herbert} defaults={null} primaryProvider={null} onBack={vi.fn()} onClose={vi.fn()} onAdded={vi.fn()} onRequested={onRequested} />)

    fireEvent.click(screen.getByRole('button', { name: 'Request author' }))

    await waitFor(() => expect(onRequested).toHaveBeenCalled())
    expect(api.createRequest).toHaveBeenCalledWith('author', 'OL-FH', '')
    expect(api.addAuthor).not.toHaveBeenCalled()
    expect(api.listIndexers).not.toHaveBeenCalled()
    expect(api.listDownloadClients).not.toHaveBeenCalled()
    expect(screen.queryByRole('combobox', { name: /root folder/i })).not.toBeInTheDocument()
  })

  it('shows the server sentence when the request is refused', async () => {
    vi.mocked(api.createRequest).mockRejectedValue(new Error('This book is already in the library.'))
    render(<AddBookConfirm book={dune} searchedISBN={null} onBack={vi.fn()} onClose={vi.fn()} onAdded={vi.fn()} />)
    fireEvent.click(screen.getByRole('button', { name: 'Request book' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('This book is already in the library.')
  })
})
