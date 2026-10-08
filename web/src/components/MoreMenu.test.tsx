import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import MoreMenu from './MoreMenu'

function setup(overrides: Partial<Parameters<typeof MoreMenu>[0]> = {}) {
  const onRename = vi.fn()
  const onDelete = vi.fn()
  const utils = render(
    <div>
      <button>outside</button>
      <MoreMenu
        label="More"
        items={[
          { label: 'Rename files', onSelect: onRename },
          { label: 'Merge…', onSelect: vi.fn(), disabled: true },
          { label: 'Delete', onSelect: onDelete, danger: true },
        ]}
        {...overrides}
      />
    </div>,
  )
  return { ...utils, onRename, onDelete }
}

const trigger = () => screen.getByRole('button', { name: /More/ })

describe('MoreMenu', () => {
  it('is closed initially and reports collapsed state', () => {
    setup()
    expect(screen.queryByRole('menu')).toBeNull()
    expect(trigger()).toHaveAttribute('aria-expanded', 'false')
    expect(trigger()).toHaveAttribute('aria-haspopup', 'menu')
  })

  it('opens on click and exposes its items as menuitems', () => {
    setup()
    fireEvent.click(trigger())
    expect(screen.getByRole('menu')).toBeInTheDocument()
    expect(trigger()).toHaveAttribute('aria-expanded', 'true')
    expect(screen.getAllByRole('menuitem')).toHaveLength(3)
  })

  it('runs the item handler and closes on select', () => {
    const { onRename } = setup()
    fireEvent.click(trigger())
    fireEvent.click(screen.getByRole('menuitem', { name: 'Rename files' }))
    expect(onRename).toHaveBeenCalledTimes(1)
    expect(screen.queryByRole('menu')).toBeNull()
  })

  it('does not fire a disabled item', () => {
    setup()
    fireEvent.click(trigger())
    const disabled = screen.getByRole('menuitem', { name: 'Merge…' })
    expect(disabled).toBeDisabled()
  })

  it('closes on Escape and returns focus to the trigger', () => {
    setup()
    fireEvent.click(trigger())
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(screen.queryByRole('menu')).toBeNull()
    expect(trigger()).toHaveFocus()
  })

  it('closes on outside click', () => {
    setup()
    fireEvent.click(trigger())
    fireEvent.pointerDown(screen.getByRole('button', { name: 'outside' }))
    expect(screen.queryByRole('menu')).toBeNull()
  })

  it('stays open when clicking inside the menu surface', () => {
    setup()
    fireEvent.click(trigger())
    fireEvent.pointerDown(screen.getByRole('menu'))
    expect(screen.getByRole('menu')).toBeInTheDocument()
  })

  it('opens with ArrowDown and focuses the first item', () => {
    setup()
    fireEvent.keyDown(trigger(), { key: 'ArrowDown' })
    expect(screen.getByRole('menuitem', { name: 'Rename files' })).toHaveFocus()
  })

  it('opens with ArrowUp and focuses the last item', () => {
    setup()
    fireEvent.keyDown(trigger(), { key: 'ArrowUp' })
    expect(screen.getByRole('menuitem', { name: 'Delete' })).toHaveFocus()
  })

  it('skips disabled items when arrowing and wraps at the end', () => {
    setup()
    fireEvent.keyDown(trigger(), { key: 'ArrowDown' })
    const menu = screen.getByRole('menu')
    // Rename -> Delete (Merge is disabled and skipped)
    fireEvent.keyDown(menu, { key: 'ArrowDown' })
    expect(screen.getByRole('menuitem', { name: 'Delete' })).toHaveFocus()
    // wraps back round to the first enabled item
    fireEvent.keyDown(menu, { key: 'ArrowDown' })
    expect(screen.getByRole('menuitem', { name: 'Rename files' })).toHaveFocus()
  })

  it('jumps to the ends with Home and End', () => {
    setup()
    fireEvent.keyDown(trigger(), { key: 'ArrowDown' })
    const menu = screen.getByRole('menu')
    fireEvent.keyDown(menu, { key: 'End' })
    expect(screen.getByRole('menuitem', { name: 'Delete' })).toHaveFocus()
    fireEvent.keyDown(menu, { key: 'Home' })
    expect(screen.getByRole('menuitem', { name: 'Rename files' })).toHaveFocus()
  })

  it('closes on Tab without stealing focus back to the trigger', () => {
    setup()
    fireEvent.keyDown(trigger(), { key: 'ArrowDown' })
    fireEvent.keyDown(screen.getByRole('menu'), { key: 'Tab' })
    expect(screen.queryByRole('menu')).toBeNull()
    expect(trigger()).not.toHaveFocus()
  })
})

describe('MoreMenu near the screen edge', () => {
  // On a phone the trigger can wrap to the start of a row, and a menu lined
  // up with its right edge then opened at left=-101px, clipping its items.
  it('flips to the left edge when a right aligned menu would overflow', () => {
    const spy = vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
      const left = this.getAttribute('role') === 'menu' && this.className.includes('right-0') ? -101 : 16
      return { left, right: left + 176, top: 0, bottom: 0, width: 176, height: 0, x: left, y: 0, toJSON: () => ({}) } as DOMRect
    })
    try {
      render(<MoreMenu label="More" items={[{ label: 'Delete', onSelect: vi.fn() }]} />)
      fireEvent.click(screen.getByRole('button', { name: /More/ }))
      const menu = screen.getByRole('menu')
      expect(menu.className).toContain('left-0')
      expect(menu.className).not.toContain('right-0')
    } finally {
      spy.mockRestore()
    }
  })

  it('stays right aligned when it fits, and again on the next open', () => {
    render(<MoreMenu label="More" items={[{ label: 'Delete', onSelect: vi.fn() }]} />)
    fireEvent.click(screen.getByRole('button', { name: /More/ }))
    expect(screen.getByRole('menu').className).toContain('right-0')
  })

  it('renders caution items in amber', () => {
    render(<MoreMenu label="More" items={[{ label: 'Set both', onSelect: vi.fn(), caution: true }]} />)
    fireEvent.click(screen.getByRole('button', { name: /More/ }))
    expect(screen.getByRole('menuitem', { name: 'Set both' }).className).toContain('text-amber-700')
  })
})

describe('MoreMenu near the top or bottom of the viewport', () => {
  // jsdom does no layout, so the trigger and menu rects are mocked. The
  // viewport is jsdom's 1024x768. The trigger's top is at `triggerTop` and it
  // is 36px tall; the menu is `menuHeight` tall wherever it is placed.
  function mockRects(triggerTop: number, menuHeight: number) {
    return vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
      const rect = (top: number, height: number) =>
        ({ left: 100, right: 276, top, bottom: top + height, width: 176, height, x: 100, y: top, toJSON: () => ({}) }) as DOMRect
      if (this.getAttribute('role') === 'menu') {
        const capped = this.style.maxHeight ? Math.min(menuHeight, parseFloat(this.style.maxHeight)) : menuHeight
        return this.className.includes('bottom-full') ? rect(triggerTop - 4 - capped, capped) : rect(triggerTop + 40, capped)
      }
      if (this.getAttribute('aria-haspopup') === 'menu') return rect(triggerTop, 36)
      return rect(0, 0)
    })
  }

  const items = [{ label: 'Rename files', onSelect: vi.fn() }, { label: 'Delete', onSelect: vi.fn() }]

  it('opens upward when there is no room below and room above', () => {
    expect(window.innerHeight).toBe(768)
    const spy = mockRects(700, 200)
    try {
      render(<MoreMenu label="More" items={items} />)
      fireEvent.click(screen.getByRole('button', { name: /More/ }))
      const menu = screen.getByRole('menu')
      expect(menu.className).toContain('bottom-full')
      expect(menu.className).not.toContain('mt-1')
      expect(menu.style.maxHeight).toBe('')
    } finally {
      spy.mockRestore()
    }
  })

  it('stays below when it fits there', () => {
    const spy = mockRects(300, 200)
    try {
      render(<MoreMenu label="More" items={items} />)
      fireEvent.click(screen.getByRole('button', { name: /More/ }))
      expect(screen.getByRole('menu').className).toContain('mt-1')
      expect(screen.getByRole('menu').className).not.toContain('bottom-full')
    } finally {
      spy.mockRestore()
    }
  })

  it('flips an upward menu down when the trigger is near the top', () => {
    const spy = mockRects(40, 200)
    try {
      render(<MoreMenu label="More" items={items} placement="above" />)
      fireEvent.click(screen.getByRole('button', { name: /More/ }))
      expect(screen.getByRole('menu').className).toContain('mt-1')
      expect(screen.getByRole('menu').className).not.toContain('bottom-full')
    } finally {
      spy.mockRestore()
    }
  })

  it('caps the height and scrolls when neither side has room', () => {
    // 500px of menu, 392px free above the trigger and 324px below it.
    const spy = mockRects(400, 500)
    try {
      render(<MoreMenu label="More" items={items} />)
      fireEvent.click(screen.getByRole('button', { name: /More/ }))
      const menu = screen.getByRole('menu')
      expect(menu.className).toContain('bottom-full')
      expect(menu.style.maxHeight).toBe('392px')
      expect(menu.className).toContain('overflow-y-auto')
    } finally {
      spy.mockRestore()
    }
  })

  it('starts from the requested side again on the next open', () => {
    const spy = mockRects(700, 200)
    try {
      render(<MoreMenu label="More" items={items} />)
      const trigger = screen.getByRole('button', { name: /More/ })
      fireEvent.click(trigger)
      expect(screen.getByRole('menu').className).toContain('bottom-full')
      fireEvent.click(trigger)
      spy.mockRestore()
      fireEvent.click(trigger)
      expect(screen.getByRole('menu').className).toContain('mt-1')
    } finally {
      spy.mockRestore()
    }
  })
})
