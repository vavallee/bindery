import { describe, it, expect, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import { createPortal } from 'react-dom'
import DataTable, { type Column } from './DataTable'
import { nextSort, splitSort } from './tableSort'

// Strings resolve against en.json, so a key the component asks for that does
// not exist shows up as the raw key and fails the name lookups below.
vi.mock('react-i18next', async () => {
  const en = (await import('../i18n/locales/en.json')).default as Record<string, unknown>
  const lookup = (key: string): unknown =>
    key.split('.').reduce<unknown>((n, p) => (n && typeof n === 'object' ? (n as Record<string, unknown>)[p] : undefined), en)
  const t = (key: string, a?: string | Record<string, unknown>, b?: Record<string, unknown>) => {
    const opts = (typeof a === 'object' ? a : b) ?? {}
    const value = lookup(key)
    return (typeof value === 'string' ? value : key).replace(/\{\{(\w+)\}\}/g, (_, k) => String(opts[k] ?? ''))
  }
  return { useTranslation: () => ({ t }) }
})

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
      <DataTable ariaLabel="Books" rows={rows} rowKey={r => r.id} rowLabel={r => r.title} columns={columns}
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
    render(<DataTable ariaLabel="Books" rows={rows} rowKey={r => r.id} rowLabel={r => r.title} columns={columns}
      sort={{ key: 'title', direction: 'asc', onSort }} />)
    fireEvent.click(screen.getByRole('button', { name: 'Title' }))
    expect(onSort).toHaveBeenCalledWith('title')
    expect(screen.queryByRole('button', { name: 'Year' })).toBeNull()
    expect(screen.getByRole('columnheader', { name: 'Title' })).toHaveAttribute('aria-sort', 'ascending')
  })

  it('navigates from the row but not from the checkbox or the actions cell', () => {
    const onRowClick = vi.fn()
    const action = vi.fn()
    render(<DataTable ariaLabel="Books" rows={rows} rowKey={r => r.id} rowLabel={r => r.title} columns={columns}
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
    render(<DataTable ariaLabel="Books" rows={rows} rowKey={r => r.id} rowLabel={r => r.title} columns={columns}
      selection={{ selected: new Set(), onToggle: vi.fn(), onToggleAll: vi.fn() }} />)
    const cells = screen.getByText('Dune').closest('tr')!.querySelectorAll('td')
    expect(cells[0].className).toContain('sticky left-0')
    expect(cells[1].className).toContain('sticky left-10')
    expect(cells[2].className).not.toContain('sticky')
  })
})

describe('DataTable selection and sort edges', () => {
  const sel = (selected: number[]) => ({ selected: new Set(selected), onToggle: vi.fn(), onToggleAll: vi.fn() })

  it('clears the page from the header box when every row is selected', () => {
    const selection = sel([1, 2])
    render(<DataTable ariaLabel="Books" rows={rows} rowKey={r => r.id} rowLabel={r => r.title} columns={columns} selection={selection} />)
    const all = screen.getByRole('checkbox', { name: 'Select all on this page' }) as HTMLInputElement
    expect(all).toBeChecked()
    expect(all.indeterminate).toBe(false)
    fireEvent.click(all)
    expect(selection.onToggleAll).toHaveBeenCalledWith(false)
  })

  it('disables the header box when there are no rows', () => {
    render(<DataTable ariaLabel="Books" rows={[]} rowKey={(r: Row) => r.id} rowLabel={r => r.title} columns={columns} selection={sel([])} />)
    expect(screen.getByRole('checkbox', { name: 'Select all on this page' })).toBeDisabled()
  })

  it('shows a default cursor on the header box label only while there are no rows', () => {
    const { rerender } = render(<DataTable ariaLabel="Books" rows={[]} rowKey={(r: Row) => r.id} rowLabel={r => r.title} columns={columns} selection={sel([])} />)
    const label = () => screen.getByRole('checkbox', { name: 'Select all on this page' }).closest('label')!
    expect(label().className).toContain('cursor-default')
    expect(label().className).not.toContain('cursor-pointer')
    rerender(<DataTable ariaLabel="Books" rows={rows} rowKey={r => r.id} rowLabel={r => r.title} columns={columns} selection={sel([])} />)
    expect(label().className).toContain('cursor-pointer')
    expect(label().className).not.toContain('cursor-default')
  })

  it('gives the header box the same full-cell hit area as a row box', () => {
    render(<DataTable ariaLabel="Books" rows={rows} rowKey={r => r.id} rowLabel={r => r.title} columns={columns} selection={sel([])} />)
    const label = screen.getByRole('checkbox', { name: 'Select all on this page' }).closest('label')!
    expect(label.className).toContain('pointer-coarse:min-h-11')
  })

  it('marks the sorted column descending and the other sortable ones as unsorted', () => {
    const cols: Column<Row>[] = [...columns.slice(0, 1), { key: 'year', header: 'Year', sortKey: 'year', cell: r => r.year }]
    render(<DataTable ariaLabel="Books" rows={rows} rowKey={r => r.id} rowLabel={r => r.title} columns={cols}
      sort={{ key: 'year', direction: 'desc', onSort: vi.fn() }} />)
    expect(screen.getByRole('columnheader', { name: 'Year' })).toHaveAttribute('aria-sort', 'descending')
    expect(screen.getByRole('columnheader', { name: 'Title' })).toHaveAttribute('aria-sort', 'none')
  })

  it('renders a sort key as a plain header when the table does not sort', () => {
    render(<DataTable ariaLabel="Books" rows={rows} rowKey={r => r.id} rowLabel={r => r.title} columns={columns} />)
    expect(screen.queryByRole('button', { name: 'Title' })).toBeNull()
    expect(screen.getByRole('columnheader', { name: 'Title' })).toBeInTheDocument()
  })

  it('opens a clickable row from the keyboard', () => {
    const onRowClick = vi.fn()
    render(<DataTable ariaLabel="Books" rows={rows} rowKey={r => r.id} rowLabel={r => r.title} columns={columns} onRowClick={onRowClick}
      rowActions={r => <button>Act {r.title}</button>} />)
    const row = screen.getByText('Hyperion').closest('tr')!
    expect(row).toHaveAttribute('tabindex', '0')
    fireEvent.keyDown(row, { key: 'Enter' })
    expect(onRowClick).toHaveBeenCalledWith(rows[1], 1)
    onRowClick.mockClear()
    // Enter on a control inside the row belongs to that control.
    fireEvent.keyDown(screen.getByRole('button', { name: 'Act Hyperion' }), { key: 'Enter' })
    expect(onRowClick).not.toHaveBeenCalled()
  })

  // Enter on a focused link or button fires a click that bubbles to the row.
  it('does not open the row for a click on a control inside a data cell', () => {
    const onRowClick = vi.fn()
    const onLink = vi.fn()
    const cols: Column<Row>[] = [
      { key: 'title', header: 'Title', cell: r => <a href="#x" onClick={e => { e.preventDefault(); onLink(r.id) }}>{r.title}</a> },
      { key: 'year', header: 'Year', cell: r => <span>{r.year}</span> },
    ]
    render(<DataTable ariaLabel="Books" rows={rows} rowKey={r => r.id} rowLabel={r => r.title} columns={cols} onRowClick={onRowClick} />)
    fireEvent.click(screen.getByRole('link', { name: 'Dune' }))
    expect(onLink).toHaveBeenCalledWith(1)
    expect(onRowClick).not.toHaveBeenCalled()
    // Plain text in a cell still opens the row.
    fireEvent.click(screen.getByText('1965'))
    expect(onRowClick).toHaveBeenCalledWith(rows[0], 0)
  })

  // A portal's DOM sits outside the row but its React events still bubble
  // to it, so a dialog opened from a cell must not also open the row.
  it('does not open the row for a click inside a portal rendered from a data cell', () => {
    const onRowClick = vi.fn()
    const onConfirm = vi.fn()
    const cols: Column<Row>[] = [
      {
        key: 'title',
        header: 'Title',
        cell: r => (
          <>
            {r.title}
            {createPortal(<div><button onClick={onConfirm}>Confirm {r.id}</button><p>Sure about {r.id}?</p></div>, document.body)}
          </>
        ),
      },
    ]
    render(<DataTable ariaLabel="Books" rows={rows} rowKey={r => r.id} rowLabel={r => r.title} columns={cols} onRowClick={onRowClick} />)
    fireEvent.click(screen.getByRole('button', { name: 'Confirm 1' }))
    expect(onConfirm).toHaveBeenCalledTimes(1)
    fireEvent.click(screen.getByText('Sure about 1?'))
    expect(onRowClick).not.toHaveBeenCalled()
    // The cell's own text still opens the row.
    fireEvent.click(screen.getByText('Dune'))
    expect(onRowClick).toHaveBeenCalledWith(rows[0], 0)
  })

  it('names the table', () => {
    render(<DataTable ariaLabel="Books" rows={rows} rowKey={r => r.id} rowLabel={r => r.title} columns={columns} />)
    expect(screen.getByRole('table', { name: 'Books' })).toBeInTheDocument()
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
