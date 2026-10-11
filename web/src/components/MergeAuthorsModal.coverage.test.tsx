import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import MergeAuthorsModal from './MergeAuthorsModal'
import { api } from '../api/client'
import type { Author } from '../api/client'
import { acceptConfirm, cancelConfirm, confirmDialog } from '../test-utils'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, fallback?: unknown) => (typeof fallback === 'string' ? fallback : key),
  }),
}))

vi.mock('../api/client', async importOriginal => {
  const actual = await importOriginal<typeof import('../api/client')>()
  return {
    ...actual,
    api: {
      ...actual.api,
      listBooks: vi.fn(),
      mergeAuthors: vi.fn(),
    },
  }
})

function author(id: number, authorName: string, foreignAuthorId = `OL${id}A`): Author {
  return {
    id,
    foreignAuthorId,
    authorName,
    sortName: authorName,
    description: '',
    imageUrl: '',
    disambiguation: '',
    ratingsCount: 0,
    averageRating: 0,
    monitored: true,
  }
}

const authors = [author(1, 'Ursula Le Guin'), author(2, 'Ursula K. Le Guin'), author(3, 'Andy Weir', '')]

function selects() {
  const [source, target] = screen.getAllByRole('combobox') as HTMLSelectElement[]
  return { source, target }
}

function mergeButton() {
  return screen.getByRole('button', { name: 'mergeAuthorsModal.mergeButton' })
}

describe('MergeAuthorsModal', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(api.listBooks).mockResolvedValue({ items: [], total: 4, limit: 50, offset: 0 })
    vi.mocked(api.mergeAuthors).mockResolvedValue({ BooksReparented: 4, AliasesMigrated: 0, AliasesCreated: 1, TargetUpdated: true })
  })

  it('lists authors alphabetically and keeps merge disabled until both sides are picked', () => {
    render(<MergeAuthorsModal authors={authors} onClose={vi.fn()} onMerged={vi.fn()} />)
    const { source, target } = selects()
    expect(within(source).getAllByRole('option').map(o => o.textContent)).toEqual(['—', 'Andy Weir', 'Ursula K. Le Guin', 'Ursula Le Guin'])
    expect(target.value).toBe('')
    expect(mergeButton()).toBeDisabled()

    fireEvent.change(source, { target: { value: '1' } })
    expect(mergeButton()).toBeDisabled()
    // The chosen source drops out of the target list so the two can't match.
    expect(within(target).queryByRole('option', { name: 'Ursula Le Guin' })).toBeNull()
  })

  it('lists authors by last name, as the Authors page does (#2805)', () => {
    const byLastName = [
      { ...author(1, 'Andy Weir'), sortName: 'Weir, Andy' },
      { ...author(2, 'Ursula K. Le Guin'), sortName: 'Le Guin, Ursula K.' },
      { ...author(3, 'Iain Banks'), sortName: '' },
      // Two rows with the same sort name are ordered by display name.
      { ...author(4, 'Ursula Le Guin'), sortName: 'Le Guin, Ursula K.' },
      { ...author(5, 'Ann Banks'), sortName: '' },
    ]
    render(<MergeAuthorsModal authors={byLastName} onClose={vi.fn()} onMerged={vi.fn()} />)
    const { source } = selects()
    // A row without a sort name falls back to its display name.
    expect(within(source).getAllByRole('option').map(o => o.textContent)).toEqual(
      ['—', 'Ann Banks', 'Iain Banks', 'Ursula K. Le Guin', 'Ursula Le Guin', 'Andy Weir'])
  })

  it('preselects the target, previews the book count and alias, and merges after confirmation', async () => {
    const onClose = vi.fn()
    const onMerged = vi.fn()
    render(<MergeAuthorsModal authors={authors} initialTargetId={2} onClose={onClose} onMerged={onMerged} />)
    const { source, target } = selects()
    expect(target.value).toBe('2')

    fireEvent.change(source, { target: { value: '1' } })
    await waitFor(() => expect(api.listBooks).toHaveBeenCalledWith({ authorId: 1 }))
    expect(await screen.findByText('4 book(s) will move to the target.')).toBeInTheDocument()
    expect(screen.getByText('(OL1A)', { exact: false })).toBeInTheDocument()

    fireEvent.click(mergeButton())
    expect(api.mergeAuthors).not.toHaveBeenCalled()
    await acceptConfirm()

    await waitFor(() => expect(api.mergeAuthors).toHaveBeenCalledWith(2, 1))
    await waitFor(() => expect(onMerged).toHaveBeenCalled())
    expect(onClose).toHaveBeenCalled()
  })

  it('does nothing when the confirmation is cancelled', async () => {
    render(<MergeAuthorsModal authors={authors} initialTargetId={2} onClose={vi.fn()} onMerged={vi.fn()} />)
    fireEvent.change(selects().source, { target: { value: '3' } })
    await screen.findByText('4 book(s) will move to the target.')
    fireEvent.click(mergeButton())
    await cancelConfirm()
    await waitFor(() => expect(confirmDialog()).toBeNull())
    expect(api.mergeAuthors).not.toHaveBeenCalled()
  })

  it('shows a counting placeholder when the book count lookup fails, and omits a blank foreign id', async () => {
    vi.mocked(api.listBooks).mockRejectedValue(new Error('offline'))
    render(<MergeAuthorsModal authors={authors} initialTargetId={1} onClose={vi.fn()} onMerged={vi.fn()} />)
    fireEvent.change(selects().source, { target: { value: '3' } })
    await waitFor(() => expect(api.listBooks).toHaveBeenCalled())
    expect(screen.getByText('Counting books…')).toBeInTheDocument()
    expect(screen.queryByText(/\(\)/)).toBeNull()
  })

  it('reports a merge failure and stays open', async () => {
    vi.mocked(api.mergeAuthors).mockRejectedValue(new Error('target is locked'))
    const onClose = vi.fn()
    render(<MergeAuthorsModal authors={authors} initialTargetId={2} onClose={onClose} onMerged={vi.fn()} />)
    fireEvent.change(selects().source, { target: { value: '1' } })
    await screen.findByText('4 book(s) will move to the target.')
    fireEvent.click(mergeButton())
    await acceptConfirm()
    expect(await screen.findByText('target is locked')).toBeInTheDocument()
    expect(onClose).not.toHaveBeenCalled()
    expect(mergeButton()).toBeEnabled()
  })

  it('falls back to a generic message for a non-Error rejection', async () => {
    vi.mocked(api.mergeAuthors).mockRejectedValue('nope')
    render(<MergeAuthorsModal authors={authors} initialTargetId={2} onClose={vi.fn()} onMerged={vi.fn()} />)
    fireEvent.change(selects().source, { target: { value: '1' } })
    await screen.findByText('4 book(s) will move to the target.')
    fireEvent.click(mergeButton())
    await acceptConfirm()
    expect(await screen.findByText('Merge failed')).toBeInTheDocument()
  })

  it('clears the preview when the source is unset', async () => {
    render(<MergeAuthorsModal authors={authors} initialTargetId={2} onClose={vi.fn()} onMerged={vi.fn()} />)
    const { source } = selects()
    fireEvent.change(source, { target: { value: '1' } })
    await screen.findByText('4 book(s) will move to the target.')
    fireEvent.change(source, { target: { value: '' } })
    await waitFor(() => expect(screen.queryByText('4 book(s) will move to the target.')).toBeNull())
    expect(mergeButton()).toBeDisabled()
  })

  it('closes from Cancel and from the backdrop, but not from a click inside the panel', () => {
    const onClose = vi.fn()
    const { container } = render(<MergeAuthorsModal authors={authors} onClose={onClose} onMerged={vi.fn()} />)
    fireEvent.click(screen.getByText('mergeAuthorsModal.description'))
    expect(onClose).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'mergeAuthorsModal.cancel' }))
    expect(onClose).toHaveBeenCalledTimes(1)
    fireEvent.click(container.firstChild as HTMLElement)
    expect(onClose).toHaveBeenCalledTimes(2)
  })
})
