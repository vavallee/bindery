import { describe, it, expect, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import FilterBar from './FilterBar'
import { FilterGroup } from './FilterPopover'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string, d?: string) => (typeof d === 'string' ? d : key) }),
}))

describe('FilterBar', () => {
  it('keeps status inline and reports the pressed segment', () => {
    const onChange = vi.fn()
    render(
      <FilterBar
        status={{ value: 'wanted', onChange, label: 'Status', options: [
          { value: '', label: 'All' }, { value: 'wanted', label: 'Wanted' }, { value: 'imported', label: 'In library' },
        ] }}
      />,
    )
    expect(screen.getByRole('button', { name: 'Wanted' })).toHaveAttribute('aria-pressed', 'true')
    fireEvent.click(screen.getByRole('button', { name: 'In library' }))
    expect(onChange).toHaveBeenCalledWith('imported')
  })

  it('counts applied facets on the trigger and renders each as a removable chip', () => {
    const onClear = vi.fn()
    render(
      <FilterBar
        filters={{ activeCount: 1, content: <FilterGroup label="Type" value="ebook" onChange={vi.fn()} options={[{ value: 'ebook', label: 'Ebook' }]} /> }}
        chips={[{ key: 'media', label: 'Ebook', onClear }]}
        summary="12 books"
      />,
    )
    expect(screen.getByRole('button', { name: /Filters/ })).toHaveTextContent('1')
    fireEvent.click(screen.getByRole('button', { name: /Ebook/ }))
    expect(onClear).toHaveBeenCalled()
    expect(screen.getByText('12 books')).toBeInTheDocument()
  })

  it('offers sort through the same radio group pattern as the other facets', () => {
    const onChange = vi.fn()
    render(<FilterBar sort={{ value: 'az', onChange, label: 'Sort by', options: [{ value: 'az', label: 'A–Z' }, { value: 'za', label: 'Z–A' }] }} />)
    fireEvent.click(screen.getByRole('button', { name: 'Sort by' }))
    fireEvent.click(screen.getByRole('radio', { name: 'Z–A' }))
    expect(onChange).toHaveBeenCalledWith('za')
  })

  it('reports typed search text and names the field by its placeholder', () => {
    const onChange = vi.fn()
    render(<FilterBar search={{ value: '', onChange, placeholder: 'Search books' }} />)
    fireEvent.change(screen.getByRole('searchbox', { name: 'Search books' }), { target: { value: 'dune' } })
    expect(onChange).toHaveBeenCalledWith('dune')
  })

  it('announces the result count politely when a filter changes it', () => {
    // Mounted empty while loading, so the region exists before its text does.
    const { container, rerender } = render(<FilterBar summary="" />)
    const region = container.querySelector('[aria-live="polite"]')!
    expect(region).toHaveTextContent('')
    rerender(<FilterBar summary="12 books" />)
    expect(container.querySelector('[aria-live="polite"]')).toBe(region)
    expect(region).toHaveTextContent('12 books')
  })
})
