import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import EmptyState from './EmptyState'

describe('EmptyState', () => {
  it('says what fills an empty list, with its action', () => {
    const { container } = render(<EmptyState title="No authors yet" hint="Add an author to start." action={<button>Add author</button>} />)
    expect(container.firstElementChild).toHaveAttribute('data-empty-state', 'empty')
    expect(screen.getByText('No authors yet')).toBeInTheDocument()
    expect(screen.getByText('Add an author to start.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Add author' })).toBeInTheDocument()
  })

  // A filter that hides everything must not read as an empty library.
  it('marks a filtered-out list as no-match, not empty', () => {
    const { container } = render(<EmptyState variant="no-match" title="No books match" />)
    expect(container.firstElementChild).toHaveAttribute('data-empty-state', 'no-match')
    expect(screen.queryByRole('button')).toBeNull()
  })
})
