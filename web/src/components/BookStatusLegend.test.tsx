import { describe, it, expect } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import BookStatusLegend from './BookStatusLegend'

function openLegend() {
  const utils = render(<><BookStatusLegend /><button>elsewhere</button></>)
  const summary = screen.getByLabelText('What do the status labels mean?')
  const details = utils.container.querySelector('details')!
  fireEvent.click(summary)
  // jsdom toggles <details> from its summary; set it in case it does not.
  details.open = true
  return { summary, details }
}

describe('BookStatusLegend', () => {
  it('is a named "?" control that explains all four badges, and closes on Escape', () => {
    const { summary, details } = openLegend()
    expect(summary).toHaveTextContent('?')
    expect(details.querySelectorAll('li')).toHaveLength(4)
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(details.open).toBe(false)
    expect(summary).toHaveFocus()
  })

  it('closes on a press outside it, and stays open for a press inside', () => {
    const { details } = openLegend()
    fireEvent.pointerDown(details.querySelector('li')!)
    expect(details.open).toBe(true)
    fireEvent.pointerDown(screen.getByRole('button', { name: 'elsewhere' }))
    expect(details.open).toBe(false)
  })

  it('closes when focus moves past it', () => {
    const { summary, details } = openLegend()
    fireEvent.blur(summary, { relatedTarget: screen.getByRole('button', { name: 'elsewhere' }) })
    expect(details.open).toBe(false)
  })

  // A click on the panel's text, or the window losing focus, blurs the
  // summary with no relatedTarget; the panel must stay open.
  it('stays open when focus goes nowhere in particular', () => {
    const { summary, details } = openLegend()
    fireEvent.blur(summary, { relatedTarget: null })
    expect(details.open).toBe(true)
  })
})
