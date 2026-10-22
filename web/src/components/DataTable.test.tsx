import { describe, it, expect, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import DataTable, { type Column } from './DataTable'
import { nextSort, splitSort } from './tableSort'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, a?: string | Record<string, unknown>, b?: Record<string, unknown>) => {
      const opts = (typeof a === 'object' ? a : b) ?? {}
      if (key === 'dataTable.selectRow') return `Select ${String(opts.name)}`
      if (key === 'dataTable.selectPage') return 'Select all on this page'
      return key
    },
  }),
}))

interface Row { id: number; title: string; year: number }
const rows: Row[] = [{ id: 1, title: 'Dune', year: 1965 }, { id: 2, title: 'Hyperion', year: 1989 }]
const columns: Column<Row>[] = [
  { key: 'title', header: 'Title', sortKey: 'title', cell: r => r.title },
  { key: 'year', header: 'Year', cell: r => r.year },
]
const SORTS = { title: { asc: 'title-az', desc: 'title-za' }, year: { asc: 'date-old', desc: 'date-new' } }

describe('DataTable', () => {
  it('uses square, named checkboxes and a header box that selects the page', () => {
    const onToggleAll = vi.fn()
    const onToggle = vi.fn()
    render(
      <DataTable rows={rows} rowKey={r => r.id} rowLabel={r => r.title} columns={columns}
        selection={{ selected: new Set([1]), onToggle, onToggleAll }} />,
    )
    const box = screen.getByRole('checkbox', { name: 'Select Dune' })
    expect(box).toBeChecked()
    expect(box.className).toMatch(/(^|\s)rounded(\s|$)/)
    expect(box.className).not.toContain('rounded-full')
    const all = screen.getByRole('checkbox', { name: 'Select all on this page' }) as HTMLInputElement
    expect(all.indeterminate).toBe(true)
    fireEvent.click(all)
    expect(onToggleAll).toHaveBeenCalledWith(true)
    fireEvent.click(screen.getByRole('checkbox', { name: 'Select Hyperion' }))
    expect(onToggle).toHaveBeenCalledWith(2)
  })

  it('sorts only through columns that declare a sort key', () => {
    const onSort = vi.fn()
    render(<DataTable rows={rows} rowKey={r => r.id} rowLabel={r => r.title} columns={columns}
      sort={{ key: 'title', direction: 'asc', onSort }} />)
    fireEvent.click(screen.getByRole('button', { name: 'Title' }))
    expect(onSort).toHaveBeenCalledWith('title')
    expect(screen.queryByRole('button', { name: 'Year' })).toBeNull()
    expect(screen.getByRole('columnheader', { name: 'Title' })).toHaveAttribute('aria-sort', 'ascending')
  })

  it('navigates from the row but not from the checkbox or the actions cell', () => {
    const onRowClick = vi.fn()
    const action = vi.fn()
    render(<DataTable rows={rows} rowKey={r => r.id} rowLabel={r => r.title} columns={columns}
      onRowClick={onRowClick}
      selection={{ selected: new Set(), onToggle: vi.fn(), onToggleAll: vi.fn() }}
      rowActions={r => <button onClick={action}>Act {r.title}</button>} />)
    fireEvent.click(screen.getByText('1989'))
    expect(onRowClick).toHaveBeenCalledWith(rows[1], 1)
    onRowClick.mockClear()
    fireEvent.click(screen.getByRole('checkbox', { name: 'Select Dune' }))
    fireEvent.click(screen.getByRole('button', { name: 'Act Dune' }))
    expect(action).toHaveBeenCalled()
    expect(onRowClick).not.toHaveBeenCalled()
  })

  it('pins the selection box and the first column for narrow screens', () => {
    render(<DataTable rows={rows} rowKey={r => r.id} rowLabel={r => r.title} columns={columns}
      selection={{ selected: new Set(), onToggle: vi.fn(), onToggleAll: vi.fn() }} />)
    const cells = screen.getByText('Dune').closest('tr')!.querySelectorAll('td')
    expect(cells[0].className).toContain('sticky left-0')
    expect(cells[1].className).toContain('sticky left-10')
    expect(cells[2].className).not.toContain('sticky')
  })
})

describe('splitSort / nextSort', () => {
  it('maps a combined sort value to a column and direction', () => {
    expect(splitSort('title-za', SORTS)).toEqual({ key: 'title', direction: 'desc' })
    expect(splitSort('date-old', SORTS)).toEqual({ key: 'year', direction: 'asc' })
    expect(splitSort('bogus', SORTS)).toEqual({ key: '', direction: 'asc' })
  })

  it('flips the active column and starts another ascending', () => {
    expect(nextSort('title-az', 'title', SORTS)).toBe('title-za')
    expect(nextSort('title-za', 'title', SORTS)).toBe('title-az')
    expect(nextSort('title-az', 'year', SORTS)).toBe('date-old')
    expect(nextSort('title-az', 'nope', SORTS)).toBe('title-az')
  })
})
