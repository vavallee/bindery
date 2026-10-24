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
    ['三体', '三体'],
    ['ノルウェイの森', 'ノル'],
    ['🚀 Rocket Science', '🚀R'],
    ['𠜎 Ancient', '𠜎A'],
    ['한강 소년이 온다', '한소'],
  ])('%s -> %s', (title, expected) => {
    expect(titleInitials(title)).toBe(expected)
  })
})

describe('CoverPlaceholder', () => {
  it('sets initials instead of the title when the card already prints the title', () => {
    const { container } = render(<CoverPlaceholder id={7} title="The Dispossessed" show="initials" />)
    expect(container).toHaveTextContent('D')
    expect(container).not.toHaveTextContent('The Dispossessed')
  })

  it('carries colour only at the table thumbnail size', () => {
    const { container } = render(<CoverPlaceholder id={7} title="The Dispossessed" size="xs" />)
    expect(container.textContent).toBe('')
  })
})
