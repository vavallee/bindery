import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import Toggle from './Toggle'

describe('Toggle', () => {
  // jsdom has no layout, so this pins the classes that size the hit area: a
  // 44px square ::before overlay centred on the unchanged 36x20 track.
  it('has a 44px touch target without changing the visible switch', () => {
    const onChange = vi.fn()
    render(<Toggle checked={false} onChange={onChange} title="Enabled" />)
    const toggle = screen.getByRole('switch')

    expect(toggle).toHaveClass('w-9', 'h-5', 'relative')
    expect(toggle).toHaveClass('before:absolute', 'before:w-11', 'before:h-11', 'before:left-1/2', 'before:top-1/2', 'before:-translate-x-1/2', 'before:-translate-y-1/2')

    fireEvent.click(toggle)
    expect(onChange).toHaveBeenCalledTimes(1)
  })
})
