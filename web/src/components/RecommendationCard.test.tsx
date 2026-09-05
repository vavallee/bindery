import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import RecommendationCard from './RecommendationCard'
import { Recommendation } from '../api/client'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    // Handles both i18next call shapes the components use: t(key, vars) and
    // t(key, 'Inline default', vars). Returning the bare key for the second
    // shape would make any component written that way look like it had a
    // missing string.
    t: (key: string, second?: unknown, third?: Record<string, unknown>) => {
      const vars = (typeof second === 'string' ? third : second) as Record<string, unknown> | undefined
      if (key === 'discover.dontSuggestAuthor') return `Don't suggest ${String(vars?.author ?? '')}`
      const table: Record<string, string> = {
        'discover.addToWanted': 'Add to Wanted',
        'discover.dismiss': 'Dismiss',
      }
      if (table[key]) return table[key]
      if (typeof second !== 'string') return key
      return vars
        ? second.replace(/\{\{(\w+)\}\}/g, (_m, name: string) => String(vars[name] ?? ''))
        : second
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
    const coverArea = document.querySelector('.aspect-\\[2\\/3\\] svg')
    expect(coverArea).not.toBeNull()
  })

  it('renders an img tag when imageUrl is set', () => {
    const rec = { ...baseRec, imageUrl: 'https://example.com/cover.jpg' }
    render(<RecommendationCard rec={rec} onDismiss={vi.fn()} onAdd={vi.fn()} onExcludeAuthor={vi.fn()} />)
    const img = screen.getByRole('img', { name: 'Test Book' })
    expect(img).toBeInTheDocument()
    expect(img.getAttribute('src')).toContain(encodeURIComponent('https://example.com/cover.jpg'))
  })

  it('shows up to 2 genre tags', () => {
    const rec = { ...baseRec, genres: ['Fantasy', 'Adventure', 'Magic', 'Epic', 'Quest'] }
    render(<RecommendationCard rec={rec} onDismiss={vi.fn()} onAdd={vi.fn()} onExcludeAuthor={vi.fn()} />)
    expect(screen.getByText('Fantasy')).toBeInTheDocument()
    expect(screen.getByText('Adventure')).toBeInTheDocument()
    expect(screen.queryByText('Magic')).toBeNull()
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

  // The overflow menu is the shared MoreMenu now, not a hand-rolled dropdown,
  // so these address it by its accessible name rather than by the "···" glyph.
  // MoreMenu gives it the keyboard handling and focus return the local copy
  // never had; what is asserted here is the behaviour the card still owns.
  const openMenu = () => fireEvent.click(screen.getByRole('button', { name: /More actions for/ }))

  it('the overflow menu opens the author exclusion item', () => {
    render(<RecommendationCard rec={baseRec} onDismiss={onDismiss} onAdd={onAdd} onExcludeAuthor={onExcludeAuthor} />)
    expect(screen.queryByText(/Don't suggest Jane Author/)).toBeNull()
    openMenu()
    expect(screen.getByRole('menuitem', { name: "Don't suggest Jane Author" })).toBeInTheDocument()
  })

  it('the overflow menu closes on a second click of its trigger', () => {
    render(<RecommendationCard rec={baseRec} onDismiss={onDismiss} onAdd={onAdd} onExcludeAuthor={onExcludeAuthor} />)
    openMenu()
    openMenu()
    expect(screen.queryByText(/Don't suggest/)).toBeNull()
  })

  it("calls onExcludeAuthor and closes when the exclusion item is chosen", () => {
    render(<RecommendationCard rec={baseRec} onDismiss={onDismiss} onAdd={onAdd} onExcludeAuthor={onExcludeAuthor} />)
    openMenu()
    fireEvent.click(screen.getByRole('menuitem', { name: "Don't suggest Jane Author" }))
    expect(onExcludeAuthor).toHaveBeenCalledWith('Jane Author')
    expect(screen.queryByText(/Don't suggest/)).toBeNull()
  })

  it('closes when the pointer goes down outside it', () => {
    render(
      <div>
        <RecommendationCard rec={baseRec} onDismiss={onDismiss} onAdd={onAdd} onExcludeAuthor={onExcludeAuthor} />
        <div data-testid="outside">outside</div>
      </div>
    )
    openMenu()
    expect(screen.getByRole('menuitem', { name: "Don't suggest Jane Author" })).toBeInTheDocument()

    // MoreMenu listens on pointerdown rather than mousedown, matching native menus.
    fireEvent.pointerDown(screen.getByTestId('outside'))
    expect(screen.queryByText(/Don't suggest/)).toBeNull()
  })

  it('gives each card a distinct accessible name for its menu', () => {
    render(<RecommendationCard rec={baseRec} onDismiss={onDismiss} onAdd={onAdd} onExcludeAuthor={onExcludeAuthor} />)
    expect(screen.getByRole('button', { name: 'More actions for Test Book' })).toBeInTheDocument()
  })
})

describe('RecommendationCard — responsive layout', () => {
  it('uses a fluid card for the recommendation grid', () => {
    const { container } = render(<RecommendationCard rec={baseRec} onDismiss={vi.fn()} onAdd={vi.fn()} onExcludeAuthor={vi.fn()} />)
    const card = container.firstChild as HTMLElement
    expect(card.className).toContain('flex')
    expect(card.className).toContain('flex-col')
    expect(card.className).not.toContain('flex-shrink-0')
    expect(card.className).not.toContain('w-56')
  })

  it('cover image area keeps a portrait aspect ratio', () => {
    const { container } = render(<RecommendationCard rec={baseRec} onDismiss={vi.fn()} onAdd={vi.fn()} onExcludeAuthor={vi.fn()} />)
    const coverArea = container.querySelector('.aspect-\\[2\\/3\\]')
    expect(coverArea).not.toBeNull()
  })
})
