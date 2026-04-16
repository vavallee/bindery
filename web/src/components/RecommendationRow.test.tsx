import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import RecommendationRow from './RecommendationRow'
import { Recommendation } from '../api/client'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, opts?: Record<string, unknown>) => {
      if (key === 'discover.dontSuggestAuthor') return `Don't suggest ${String(opts?.author ?? '')}`
      return { 'discover.addToWanted': 'Add to Wanted', 'discover.dismiss': 'Dismiss' }[key] ?? key
    },
  }),
}))

const makeRec = (id: number, title: string): Recommendation => ({
  id,
  userId: 1,
  foreignId: `ol:${id}`,
  recType: 'series',
  title,
  authorName: `Author ${id}`,
  imageUrl: '',
  description: '',
  genres: [],
  rating: 3.5,
  ratingsCount: 0,
  language: 'en',
  mediaType: 'ebook',
  score: 0.5,
  reason: 'Test reason',
  seriesPos: '',
  dismissed: false,
  batchId: 'b1',
  createdAt: '2026-01-01T00:00:00Z',
})

describe('RecommendationRow', () => {
  it('returns nothing when recommendations array is empty', () => {
    const { container } = render(
      <RecommendationRow title="Series" recommendations={[]} onDismiss={vi.fn()} onAdd={vi.fn()} onExcludeAuthor={vi.fn()} />
    )
    expect(container.firstChild).toBeNull()
  })

  it('renders the row title', () => {
    render(
      <RecommendationRow title="Next in Series" recommendations={[makeRec(1, 'Book One')]} onDismiss={vi.fn()} onAdd={vi.fn()} onExcludeAuthor={vi.fn()} />
    )
    expect(screen.getByText('Next in Series')).toBeInTheDocument()
  })

  it('renders a card for each recommendation', () => {
    const recs = [makeRec(1, 'Alpha'), makeRec(2, 'Beta'), makeRec(3, 'Gamma')]
    render(
      <RecommendationRow title="Row" recommendations={recs} onDismiss={vi.fn()} onAdd={vi.fn()} onExcludeAuthor={vi.fn()} />
    )
    expect(screen.getByText('Alpha')).toBeInTheDocument()
    expect(screen.getByText('Beta')).toBeInTheDocument()
    expect(screen.getByText('Gamma')).toBeInTheDocument()
  })

  it('has overflow-x-auto class for horizontal scroll on mobile', () => {
    const { container } = render(
      <RecommendationRow title="Row" recommendations={[makeRec(1, 'X')]} onDismiss={vi.fn()} onAdd={vi.fn()} onExcludeAuthor={vi.fn()} />
    )
    expect(container.querySelector('.overflow-x-auto')).not.toBeNull()
  })

  it('passes onDismiss through to each card', () => {
    const onDismiss = vi.fn()
    render(
      <RecommendationRow title="Row" recommendations={[makeRec(7, 'Dismissible')]} onDismiss={onDismiss} onAdd={vi.fn()} onExcludeAuthor={vi.fn()} />
    )
    fireEvent.click(screen.getByRole('button', { name: 'Dismiss' }))
    expect(onDismiss).toHaveBeenCalledWith(7)
  })

  it('passes onAdd through to each card', () => {
    const onAdd = vi.fn()
    render(
      <RecommendationRow title="Row" recommendations={[makeRec(9, 'Addable')]} onDismiss={vi.fn()} onAdd={onAdd} onExcludeAuthor={vi.fn()} />
    )
    fireEvent.click(screen.getByRole('button', { name: 'Add to Wanted' }))
    expect(onAdd).toHaveBeenCalledWith(9)
  })
})
