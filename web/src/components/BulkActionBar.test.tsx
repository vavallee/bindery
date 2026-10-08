import { describe, it, expect, vi, afterEach } from 'vitest'
import { fireEvent, render, screen, within } from '@testing-library/react'
import BulkActionBar, { type BulkAction } from './BulkActionBar'
import { mockMatchMedia } from '../test-utils'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, opts?: Record<string, unknown>) =>
      key === 'bulkActionBar.selected' ? `${String(opts?.count)} selected` : key,
  }),
}))

function actions(): BulkAction[] {
  return [
    { label: 'Monitor', onClick: vi.fn() },
    { label: 'Unmonitor', onClick: vi.fn() },
    { label: 'Search', onClick: vi.fn() },
    { label: 'Set ebook', onClick: vi.fn() },
    { label: 'Delete', onClick: vi.fn(), variant: 'danger' },
  ]
}

let restore: () => void = () => {}
afterEach(() => restore())

describe('BulkActionBar', () => {
  it('shows every action inline on a wide screen', () => {
    restore = mockMatchMedia(false)
    render(<BulkActionBar count={2} actions={actions()} onClear={vi.fn()} />)
    for (const label of ['Monitor', 'Unmonitor', 'Search', 'Set ebook', 'Delete', 'bulkActionBar.clear']) {
      expect(screen.getByRole('button', { name: label })).toBeInTheDocument()
    }
    expect(screen.queryByRole('button', { name: /common\.more/ })).not.toBeInTheDocument()
  })

  // Seven or eight buttons wrapped to four or five rows on a phone, and the
  // bar covered the rows and the pagination it was meant to act on.
  it('keeps one row on a phone: two actions plus a More menu', () => {
    restore = mockMatchMedia(q => q.includes('40rem'))
    const list = actions()
    const onClear = vi.fn()
    render(<BulkActionBar count={2} actions={list} onClear={onClear} />)

    expect(screen.getByRole('button', { name: 'Monitor' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Unmonitor' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Search' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Delete' })).not.toBeInTheDocument()

    const more = screen.getByRole('button', { name: /common\.more/ })
    expect(more).toHaveAttribute('aria-haspopup', 'menu')
    expect(more).toHaveAttribute('aria-expanded', 'false')
    fireEvent.click(more)
    expect(more).toHaveAttribute('aria-expanded', 'true')

    const menu = screen.getByRole('menu')
    const items = within(menu).getAllByRole('menuitem').map(i => i.textContent)
    expect(items).toEqual(['Search', 'Set ebook', 'Delete', 'bulkActionBar.clear'])

    fireEvent.click(within(menu).getByRole('menuitem', { name: 'Delete' }))
    expect(list[4].onClick).toHaveBeenCalled()
    expect(screen.queryByRole('menu')).not.toBeInTheDocument()

    fireEvent.click(more)
    fireEvent.click(within(screen.getByRole('menu')).getByRole('menuitem', { name: 'bulkActionBar.clear' }))
    expect(onClear).toHaveBeenCalled()
  })

  it('needs no More menu on a phone when the actions fit', () => {
    restore = mockMatchMedia(true)
    render(<BulkActionBar count={1} actions={actions().slice(0, 2)} onClear={vi.fn()} />)
    expect(screen.queryByRole('button', { name: /common\.more/ })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'bulkActionBar.clear' })).toBeInTheDocument()
  })

  it('keeps caution styling for a caution action moved into More', () => {
    restore = mockMatchMedia(true)
    const list: BulkAction[] = [...actions().slice(0, 2), { label: 'Set both', onClick: vi.fn(), variant: 'caution' }]
    render(<BulkActionBar count={2} actions={list} onClear={vi.fn()} />)
    fireEvent.click(screen.getByRole('button', { name: /common\.more/ }))
    expect(screen.getByRole('menuitem', { name: 'Set both' }).className).toContain('text-amber-700')
  })

  it('disables the More menu items while busy', () => {
    restore = mockMatchMedia(true)
    render(<BulkActionBar count={2} actions={actions()} onClear={vi.fn()} busy />)
    expect(screen.getByRole('button', { name: /common\.more/ })).toBeDisabled()
  })
})
