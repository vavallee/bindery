import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import RecommendationCard from './RecommendationCard'
import { Recommendation } from '../api/client'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, opts?: Record<string, unknown>) => {
      if (key === 'discover.dontSuggestAuthor') return `Don't suggest ${String(opts?.author ?? '')}`
      return {
        'discover.addToWanted': 'Add to Wanted',
        'discover.dismiss': 'Dismiss',
      }[key] ?? key
    },
  }),
}))

const baseRec: Recommendation = {
  id: 1,
  userId: 1,
  foreignId: 'ol:test',
  recType: 'series',
  title: 'Test Book',
  authorName: 'Jane Author',
  imageUrl: '',
  description: 'A great book',
  genres: ['Fantasy', 'Adventure', 'Magic'],
  rating: 4.0,
  ratingsCount: 1200,
  language: 'en',
  mediaType: 'ebook',
  score: 0.85,
  reason: 'Next in the series',
  seriesPos: '2',
  dismissed: false,
  batchId: 'b1',
  createdAt: '2026-01-01T00:00:00Z',
}

describe('RecommendationCard — rendering', () => {
  it('renders title, author and reason', () => {
    render(<RecommendationCard rec={baseRec} onDismiss={vi.fn()} onAdd={vi.fn()} onExcludeAuthor={vi.fn()} />)
    expect(screen.getByText('Test Book')).toBeInTheDocument()
    expect(screen.getByText('Jane Author')).toBeInTheDocument()
    expect(screen.getByText('Next in the series')).toBeInTheDocument()
  })

  it('shows a placeholder book icon when no imageUrl', () => {
    render(<RecommendationCard rec={baseRec} onDismiss={vi.fn()} onAdd={vi.fn()} onExcludeAuthor={vi.fn()} />)
    expect(screen.queryByRole('img')).toBeNull()
    // SVG placeholder should be in the cover area
    const coverArea = document.querySelector('.h-36 svg')
    expect(coverArea).not.toBeNull()
  })

  it('renders an img tag when imageUrl is set', () => {
    const rec = { ...baseRec, imageUrl: 'https://example.com/cover.jpg' }
    render(<RecommendationCard rec={rec} onDismiss={vi.fn()} onAdd={vi.fn()} onExcludeAuthor={vi.fn()} />)
    const img = screen.getByRole('img', { name: 'Test Book' })
    expect(img).toBeInTheDocument()
    expect(img.getAttribute('src')).toContain(encodeURIComponent('https://example.com/cover.jpg'))
  })

  it('shows up to 3 genre tags', () => {
    const rec = { ...baseRec, genres: ['Fantasy', 'Adventure', 'Magic', 'Epic', 'Quest'] }
    render(<RecommendationCard rec={rec} onDismiss={vi.fn()} onAdd={vi.fn()} onExcludeAuthor={vi.fn()} />)
    expect(screen.getByText('Fantasy')).toBeInTheDocument()
    expect(screen.getByText('Adventure')).toBeInTheDocument()
    expect(screen.getByText('Magic')).toBeInTheDocument()
    expect(screen.queryByText('Epic')).toBeNull()
    expect(screen.queryByText('Quest')).toBeNull()
  })

  it('shows no genre tags when genres array is empty', () => {
    const rec = { ...baseRec, genres: [] }
    const { container } = render(<RecommendationCard rec={rec} onDismiss={vi.fn()} onAdd={vi.fn()} onExcludeAuthor={vi.fn()} />)
    expect(container.querySelector('.flex.flex-wrap.gap-1')).toBeNull()
  })

  it('shows ratings count when > 0', () => {
    render(<RecommendationCard rec={baseRec} onDismiss={vi.fn()} onAdd={vi.fn()} onExcludeAuthor={vi.fn()} />)
    expect(screen.getByText('(1200)')).toBeInTheDocument()
  })

  it('hides ratings count when ratingsCount is 0', () => {
    const rec = { ...baseRec, ratingsCount: 0 }
    render(<RecommendationCard rec={rec} onDismiss={vi.fn()} onAdd={vi.fn()} onExcludeAuthor={vi.fn()} />)
    expect(screen.queryByText(/\(\d+\)/)).toBeNull()
  })
})

describe('RecommendationCard — actions', () => {
  const onDismiss = vi.fn()
  const onAdd = vi.fn()
  const onExcludeAuthor = vi.fn()

  beforeEach(() => { vi.clearAllMocks() })

  it('calls onAdd with rec.id when "Add to Wanted" is clicked', () => {
    render(<RecommendationCard rec={baseRec} onDismiss={onDismiss} onAdd={onAdd} onExcludeAuthor={onExcludeAuthor} />)
    fireEvent.click(screen.getByRole('button', { name: 'Add to Wanted' }))
    expect(onAdd).toHaveBeenCalledWith(1)
    expect(onDismiss).not.toHaveBeenCalled()
  })

  it('calls onDismiss with rec.id when "Dismiss" is clicked', () => {
    render(<RecommendationCard rec={baseRec} onDismiss={onDismiss} onAdd={onAdd} onExcludeAuthor={onExcludeAuthor} />)
    fireEvent.click(screen.getByRole('button', { name: 'Dismiss' }))
    expect(onDismiss).toHaveBeenCalledWith(1)
    expect(onAdd).not.toHaveBeenCalled()
  })

  it('"···" button opens the author exclusion dropdown', () => {
    render(<RecommendationCard rec={baseRec} onDismiss={onDismiss} onAdd={onAdd} onExcludeAuthor={onExcludeAuthor} />)
    expect(screen.queryByText(/Don't suggest Jane Author/)).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: '···' }))
    expect(screen.getByText("Don't suggest Jane Author")).toBeInTheDocument()
  })

  it('"···" button toggles dropdown closed on second click', () => {
    render(<RecommendationCard rec={baseRec} onDismiss={onDismiss} onAdd={onAdd} onExcludeAuthor={onExcludeAuthor} />)
    const menuBtn = screen.getByRole('button', { name: '···' })
    fireEvent.click(menuBtn)
    fireEvent.click(menuBtn)
    expect(screen.queryByText(/Don't suggest/)).toBeNull()
  })

  it('calls onExcludeAuthor and closes dropdown when "Don\'t suggest author" is clicked', () => {
    render(<RecommendationCard rec={baseRec} onDismiss={onDismiss} onAdd={onAdd} onExcludeAuthor={onExcludeAuthor} />)
    fireEvent.click(screen.getByRole('button', { name: '···' }))
    fireEvent.click(screen.getByText("Don't suggest Jane Author"))
    expect(onExcludeAuthor).toHaveBeenCalledWith('Jane Author')
    expect(screen.queryByText(/Don't suggest/)).toBeNull()
  })

  it('closes the dropdown when clicking outside', () => {
    render(
      <div>
        <RecommendationCard rec={baseRec} onDismiss={onDismiss} onAdd={onAdd} onExcludeAuthor={onExcludeAuthor} />
        <div data-testid="outside">outside</div>
      </div>
    )
    fireEvent.click(screen.getByRole('button', { name: '···' }))
    expect(screen.getByText("Don't suggest Jane Author")).toBeInTheDocument()

    fireEvent.mouseDown(screen.getByTestId('outside'))
    expect(screen.queryByText(/Don't suggest/)).toBeNull()
  })
})

describe('RecommendationCard — responsive layout', () => {
  it('has a fixed width class for horizontal scroll within rows', () => {
    const { container } = render(<RecommendationCard rec={baseRec} onDismiss={vi.fn()} onAdd={vi.fn()} onExcludeAuthor={vi.fn()} />)
    const card = container.firstChild as HTMLElement
    expect(card.className).toContain('flex-shrink-0')
    expect(card.className).toContain('w-56')
  })

  it('cover image area has a fixed height suitable for both mobile and desktop', () => {
    const { container } = render(<RecommendationCard rec={baseRec} onDismiss={vi.fn()} onAdd={vi.fn()} onExcludeAuthor={vi.fn()} />)
    const coverArea = container.querySelector('.h-36')
    expect(coverArea).not.toBeNull()
  })
})
