import { describe, it, expect, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { useConfirmDialog, type ConfirmOptions } from './useConfirmDialog'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, fallback?: string) => (typeof fallback === 'string' ? fallback : key),
  }),
}))

function Harness({ options, onResult }: { options: ConfirmOptions; onResult: (v: boolean) => void }) {
  const { confirm, confirmDialog } = useConfirmDialog()
  return (
    <div>
      {confirmDialog}
      <button onClick={async () => onResult(await confirm(options))}>Ask</button>
    </div>
  )
}

const dialog = () => screen.getByTestId('confirm-dialog')
const confirmButton = () => {
  const buttons = within(dialog()).getAllByRole('button')
  return buttons[buttons.length - 1] as HTMLButtonElement
}
const cancelButton = () => {
  const buttons = within(dialog()).getAllByRole('button')
  return buttons[buttons.length - 2] as HTMLButtonElement
}

describe('useConfirmDialog', () => {
  it('renders nothing until something asks', () => {
    render(<Harness options={{ title: 'T', body: 'B' }} onResult={vi.fn()} />)
    expect(screen.queryByTestId('confirm-dialog')).not.toBeInTheDocument()
  })

  it('resolves true when confirmed and shows the message on screen', async () => {
    const onResult = vi.fn()
    render(<Harness options={{ title: 'Delete it?', body: 'This cannot be undone.' }} onResult={onResult} />)

    fireEvent.click(screen.getByText('Ask'))
    expect(await screen.findByText('Delete it?')).toBeInTheDocument()
    expect(screen.getByText('This cannot be undone.')).toBeInTheDocument()

    await act(async () => { fireEvent.click(confirmButton()) })
    expect(onResult).toHaveBeenCalledWith(true)
    await waitFor(() => expect(screen.queryByTestId('confirm-dialog')).not.toBeInTheDocument())
  })

  it('resolves false when cancelled', async () => {
    const onResult = vi.fn()
    render(<Harness options={{ title: 'T', body: 'B' }} onResult={onResult} />)

    fireEvent.click(screen.getByText('Ask'))
    await screen.findByTestId('confirm-dialog')
    await act(async () => { fireEvent.click(cancelButton()) })

    expect(onResult).toHaveBeenCalledWith(false)
    await waitFor(() => expect(screen.queryByTestId('confirm-dialog')).not.toBeInTheDocument())
  })

  // The checkbox gate used to be mandatory, which is too heavy for a routine
  // "delete this profile" (#2359). It is now opt-in per prompt.
  it('has no acknowledge checkbox and a live confirm button by default', async () => {
    render(<Harness options={{ title: 'T', body: 'B' }} onResult={vi.fn()} />)
    fireEvent.click(screen.getByText('Ask'))
    await screen.findByTestId('confirm-dialog')

    expect(within(dialog()).queryByRole('checkbox')).not.toBeInTheDocument()
    expect(confirmButton()).not.toBeDisabled()
  })

  it('gates the confirm button behind the acknowledge box when one is asked for', async () => {
    const onResult = vi.fn()
    render(
      <Harness
        options={{ title: 'T', body: 'B', acknowledgeLabel: 'I understand.' }}
        onResult={onResult}
      />,
    )
    fireEvent.click(screen.getByText('Ask'))
    await screen.findByTestId('confirm-dialog')

    expect(confirmButton()).toBeDisabled()
    fireEvent.click(within(dialog()).getByRole('checkbox'))
    expect(confirmButton()).not.toBeDisabled()

    await act(async () => { fireEvent.click(confirmButton()) })
    expect(onResult).toHaveBeenCalledWith(true)
  })

  // window.confirm could not be asked twice at once either; the point is that
  // the second caller gets an answer instead of awaiting a promise forever.
  it('declines a second prompt while one is open', async () => {
    const onResult = vi.fn()
    render(<Harness options={{ title: 'T', body: 'B' }} onResult={onResult} />)

    fireEvent.click(screen.getByText('Ask'))
    await screen.findByTestId('confirm-dialog')
    await act(async () => { fireEvent.click(screen.getByText('Ask')) })

    expect(onResult).toHaveBeenCalledWith(false)
    expect(screen.getByTestId('confirm-dialog')).toBeInTheDocument()
  })
})
