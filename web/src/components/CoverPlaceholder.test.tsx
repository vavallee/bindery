import { describe, it, expect } from 'vitest'
import { render } from '@testing-library/react'
import CoverPlaceholder from './CoverPlaceholder'
import { titleInitials } from './coverPalette'

describe('titleInitials', () => {
  it.each([
    ['The Left Hand of Darkness', 'LH'],
    ['1984', '1'],
    ['"Pride and prejudice."', 'PA'],
    ['Dune', 'D'],
    ['A', 'A'],
  ])('%s -> %s', (title, expected) => {
    expect(titleInitials(title)).toBe(expected)
  })
})

describe('CoverPlaceholder', () => {
  it('sets initials instead of the title when the card already prints the title', () => {
    const { container } = render(<CoverPlaceholder id={7} title="The Dispossessed" label="initials" />)
    expect(container).toHaveTextContent('D')
    expect(container).not.toHaveTextContent('The Dispossessed')
  })

  it('carries colour only at the table thumbnail size', () => {
    const { container } = render(<CoverPlaceholder id={7} title="The Dispossessed" size="xs" />)
    expect(container.textContent).toBe('')
  })
})
