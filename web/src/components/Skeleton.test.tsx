import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { SkeletonCards, SkeletonRows } from './Skeleton'

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k: string) => (k === 'common.loading' ? 'Loading…' : k) }) }))

// One announcement for the wait; the placeholder shapes stay out of the
// accessibility tree.
describe('Skeleton', () => {
  it('announces loading once and hides the row shapes', () => {
    const { container } = render(<SkeletonRows rows={3} />)
    expect(screen.getByRole('status')).toHaveTextContent('Loading…')
    const shapes = container.querySelector('[aria-hidden="true"]')!
    expect(shapes.children).toHaveLength(3)
  })

  it('takes a specific label', () => {
    render(<SkeletonRows label="Loading history" />)
    expect(screen.getByRole('status')).toHaveTextContent('Loading history')
  })

  it('draws card shapes the same way', () => {
    render(<SkeletonCards />)
    expect(screen.getByRole('status')).toHaveTextContent('Loading…')
  })
})
