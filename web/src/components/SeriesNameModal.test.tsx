import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import SeriesNameModal from './SeriesNameModal'

// The dialog role sits on a wrapper <div>, since a <form> may not take it;
// the form inside (display: contents) still submits.
describe('SeriesNameModal', () => {
  it('is a dialog whose form submits the trimmed name', async () => {
    const onSubmit = vi.fn()
    render(<SeriesNameModal title="Add Series" submitLabel="Create" onClose={() => {}} onSubmit={onSubmit} />)
    const dialog = screen.getByRole('dialog', { name: 'Add Series' })
    expect(dialog.tagName).toBe('DIV')
    fireEvent.change(screen.getByRole('textbox'), { target: { value: '  Vindserien  ' } })
    fireEvent.click(screen.getByRole('button', { name: 'Create' }))
    await waitFor(() => expect(onSubmit).toHaveBeenCalledWith('Vindserien'))
  })
})
