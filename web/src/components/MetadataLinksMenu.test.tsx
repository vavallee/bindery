import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import MetadataLinksMenu from './MetadataLinksMenu'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, options?: Record<string, unknown>) =>
      key === 'common.viewOnSource' ? `View on ${String(options?.source)}` : key === 'common.links' ? 'Links' : key,
  }),
}))

const links = [{ label: 'OpenLibrary', url: 'https://openlibrary.org/works/OL1W' }]

// What a phone sends for a tap: a touch pointerenter, the emulated
// mouseenter browsers fire for legacy pages, then the click. No leave follows
// until the finger touches somewhere else.
function tap(trigger: HTMLElement) {
  const root = trigger.parentElement!
  fireEvent.pointerEnter(root, { pointerType: 'touch' })
  fireEvent.mouseEnter(root)
  fireEvent.click(trigger)
}

describe('MetadataLinksMenu on touch', () => {
  it('opens on one tap and closes on the next', () => {
    render(<MetadataLinksMenu links={links} />)
    const trigger = screen.getByRole('button', { name: 'Links' })

    tap(trigger)
    expect(trigger).toHaveAttribute('aria-expanded', 'true')
    expect(screen.getByTestId('book-links-menu')).not.toHaveAttribute('hidden')

    tap(trigger)
    expect(trigger).toHaveAttribute('aria-expanded', 'false')
    expect(screen.getByTestId('book-links-menu')).toHaveAttribute('hidden')
  })

  it('still opens on mouse hover and closes when the mouse leaves', () => {
    render(<MetadataLinksMenu links={links} />)
    const trigger = screen.getByRole('button', { name: 'Links' })

    fireEvent.pointerEnter(trigger.parentElement!, { pointerType: 'mouse' })
    expect(trigger).toHaveAttribute('aria-expanded', 'true')
    fireEvent.pointerLeave(trigger.parentElement!, { pointerType: 'mouse' })
    expect(trigger).toHaveAttribute('aria-expanded', 'false')
  })
})
