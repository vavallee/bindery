import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { MemoryRouter, Route, Routes, useParams } from 'react-router'
import { api } from '../api/client'
import AuthorsPage from './AuthorsPage'
import BooksPage from './BooksPage'

// Pins what each page does after an add from the unified dialog (#1227). The
// Books page cannot show a new author, so it opens the author; the Authors
// page lists the new row, so it refreshes. The modal itself is stubbed: its
// behaviour is covered in AddToLibraryModal.test.tsx, and here only the
// onAdded contract matters.
vi.mock('../components/AddToLibraryModal', () => ({
  default: ({ onAdded }: { onAdded: (added: unknown) => void }) => (
    <div role="dialog" aria-label="Add to library stub">
      <button type="button" onClick={() => onAdded({ kind: 'author', author: { id: 5, authorName: 'New Author' } })}>stub add author</button>
      <button type="button" onClick={() => onAdded({ kind: 'book', book: { id: 6, title: 'New Book' } })}>stub add book</button>
    </div>
  ),
}))
vi.mock('../components/Pagination', () => ({ default: () => null }))
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string, fallback?: unknown) => (typeof fallback === 'string' ? fallback : key), i18n: { language: 'en' } }),
  Trans: ({ children }: { children: unknown }) => children,
}))

function AuthorProbe() {
  const { id } = useParams()
  return <div>author page {id}</div>
}

function renderAt(page: 'books' | 'authors') {
  return render(
    <MemoryRouter initialEntries={['/']}>
      <Routes>
        <Route path="/" element={page === 'books' ? <BooksPage /> : <AuthorsPage />} />
        <Route path="/author/:id" element={<AuthorProbe />} />
      </Routes>
    </MemoryRouter>,
  )
}

describe('onAdded wiring from the unified Add dialog', () => {
  beforeEach(() => {
    vi.restoreAllMocks()
    vi.spyOn(api, 'listBooks').mockResolvedValue({ items: [], total: 0, limit: 50, offset: 0 })
    vi.spyOn(api, 'listAuthors').mockResolvedValue({ items: [], total: 0, limit: 50, offset: 0 })
  })

  it('opens the new author from the Books page', async () => {
    renderAt('books')
    fireEvent.click(await screen.findByRole('button', { name: 'addToLibrary.addBook' }))
    fireEvent.click(screen.getByRole('button', { name: 'stub add author' }))
    expect(await screen.findByText('author page 5')).toBeInTheDocument()
  })

  it('refreshes the Books list after a book add', async () => {
    renderAt('books')
    await waitFor(() => expect(api.listBooks).toHaveBeenCalledTimes(1))
    fireEvent.click(screen.getByRole('button', { name: 'addToLibrary.addBook' }))
    fireEvent.click(screen.getByRole('button', { name: 'stub add book' }))
    await waitFor(() => expect(api.listBooks).toHaveBeenCalledTimes(2))
    expect(screen.queryByText(/author page/)).not.toBeInTheDocument()
  })

  it('refreshes the Authors list after an author add instead of leaving the page', async () => {
    renderAt('authors')
    await waitFor(() => expect(api.listAuthors).toHaveBeenCalledTimes(1))
    const addAuthor = screen.getAllByRole('button').find(b => /add author/i.test(b.textContent ?? '') || b.textContent === 'authors.addAuthor')
    expect(addAuthor).toBeDefined()
    fireEvent.click(addAuthor!)
    fireEvent.click(screen.getByRole('button', { name: 'stub add author' }))
    await waitFor(() => expect(api.listAuthors).toHaveBeenCalledTimes(2))
    expect(screen.queryByText(/author page/)).not.toBeInTheDocument()
  })
})
