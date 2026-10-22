import { describe, it, expect } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import BookStatusLegend from './BookStatusLegend'

describe('BookStatusLegend', () => {
  it('is a named "?" control that explains all four badges, and closes on Escape', () => {
    const { container } = render(<BookStatusLegend />)
    const summary = screen.getByLabelText('What do the status labels mean?')
    expect(summary).toHaveTextContent('?')
    const details = container.querySelector('details')!
    details.open = true
    expect(container.querySelectorAll('li')).toHaveLength(4)
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(details.open).toBe(false)
  })
})
