import { describe, it, expect } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import FilterPopover from './FilterPopover'

describe('FilterPopover', () => {
  // vh on a phone is measured with the toolbar hidden, so a 70vh panel ran
  // under the toolbar while it was showing.
  it('caps the open panel in dynamic viewport units', () => {
    render(<FilterPopover label="Filters"><p>body</p></FilterPopover>)
    fireEvent.click(screen.getByRole('button', { name: /Filters/ }))
    const panel = screen.getByText('body').parentElement!
    expect(panel.className).toContain('max-h-[70dvh]')
    expect(panel.className).not.toContain('max-h-[70vh]')
  })
})
