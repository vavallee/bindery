import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import Pagination from './Pagination'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string) => ({ 'pagination.previous': 'Previous', 'pagination.next': 'Next' }[key] ?? key),
  }),
}))

const defaults = {
  page: 1,
  totalPages: 5,
  pageSize: 25,
  totalItems: 120,
  onPageChange: vi.fn(),
  onPageSizeChange: vi.fn(),
}

describe('Pagination', () => {
  it('returns nothing when totalItems is 0', () => {
    const { container } = render(<Pagination {...defaults} totalItems={0} />)
    expect(container.firstChild).toBeNull()
  })

  it('shows correct item range for page 1', () => {
    render(<Pagination {...defaults} page={1} pageSize={25} totalItems={120} />)
    expect(screen.getByText('1–25 of 120')).toBeInTheDocument()
  })

  it('shows correct item range for last page', () => {
    render(<Pagination {...defaults} page={5} pageSize={25} totalItems={120} />)
    expect(screen.getByText('101–120 of 120')).toBeInTheDocument()
  })

  it('disables Previous and first-page buttons on page 1', () => {
    render(<Pagination {...defaults} page={1} />)
    expect(screen.getByRole('button', { name: '«' })).toBeDisabled()
    expect(screen.getByRole('button', { name: /‹.*Previous/i })).toBeDisabled()
  })

  it('disables Next and last-page buttons on the last page', () => {
    render(<Pagination {...defaults} page={5} totalPages={5} />)
    expect(screen.getByRole('button', { name: '»' })).toBeDisabled()
    expect(screen.getByRole('button', { name: /Next.*›/i })).toBeDisabled()
  })

  it('calls onPageChange with correct page when a page button is clicked', () => {
    const onPageChange = vi.fn()
    render(<Pagination {...defaults} page={1} totalPages={5} onPageChange={onPageChange} />)
    fireEvent.click(screen.getByRole('button', { name: '3' }))
    expect(onPageChange).toHaveBeenCalledWith(3)
  })

  it('calls onPageChange(page-1) when Previous is clicked', () => {
    const onPageChange = vi.fn()
    render(<Pagination {...defaults} page={3} totalPages={5} onPageChange={onPageChange} />)
    fireEvent.click(screen.getByRole('button', { name: /‹.*Previous/i }))
    expect(onPageChange).toHaveBeenCalledWith(2)
  })

  it('calls onPageChange(page+1) when Next is clicked', () => {
    const onPageChange = vi.fn()
    render(<Pagination {...defaults} page={3} totalPages={5} onPageChange={onPageChange} />)
    fireEvent.click(screen.getByRole('button', { name: /Next.*›/i }))
    expect(onPageChange).toHaveBeenCalledWith(4)
  })

  it('calls onPageChange(1) when first-page button is clicked', () => {
    const onPageChange = vi.fn()
    render(<Pagination {...defaults} page={3} totalPages={5} onPageChange={onPageChange} />)
    fireEvent.click(screen.getByRole('button', { name: '«' }))
    expect(onPageChange).toHaveBeenCalledWith(1)
  })

  it('calls onPageChange(totalPages) when last-page button is clicked', () => {
    const onPageChange = vi.fn()
    render(<Pagination {...defaults} page={3} totalPages={5} onPageChange={onPageChange} />)
    fireEvent.click(screen.getByRole('button', { name: '»' }))
    expect(onPageChange).toHaveBeenCalledWith(5)
  })

  it('shows ellipsis when totalPages > 7 and page is in the middle', () => {
    render(<Pagination {...defaults} page={5} totalPages={10} />)
    const ellipses = screen.getAllByText('…')
    expect(ellipses.length).toBe(2) // one before, one after
  })

  it('shows no ellipsis when totalPages <= 7', () => {
    render(<Pagination {...defaults} page={4} totalPages={7} />)
    expect(screen.queryByText('…')).toBeNull()
    // All 7 page buttons present
    for (let i = 1; i <= 7; i++) {
      expect(screen.getByRole('button', { name: String(i) })).toBeInTheDocument()
    }
  })

  it('calls onPageSizeChange when page size selector changes', () => {
    const onPageSizeChange = vi.fn()
    render(<Pagination {...defaults} onPageSizeChange={onPageSizeChange} />)
    fireEvent.change(screen.getByRole('combobox'), { target: { value: '50' } })
    expect(onPageSizeChange).toHaveBeenCalledWith(50)
  })

  it('renders custom pageSizeOptions', () => {
    render(<Pagination {...defaults} pageSizeOptions={[10, 20, 30]} />)
    const select = screen.getByRole('combobox')
    const options = Array.from(select.querySelectorAll('option')).map(o => o.value)
    expect(options).toEqual(['10', '20', '30'])
  })

  it('highlights the active page button', () => {
    render(<Pagination {...defaults} page={2} totalPages={5} />)
    const activeBtn = screen.getByRole('button', { name: '2' })
    expect(activeBtn.className).toContain('bg-slate-300')
    const inactiveBtn = screen.getByRole('button', { name: '3' })
    expect(inactiveBtn.className).not.toContain('bg-slate-300')
  })

  // Mobile layout: stacks vertically (flex-col) and switches to row on sm breakpoint
  it('has flex-col base class for mobile stacking', () => {
    const { container } = render(<Pagination {...defaults} />)
    const wrapper = container.firstChild as HTMLElement
    expect(wrapper.className).toContain('flex-col')
    expect(wrapper.className).toContain('sm:flex-row')
  })
})
