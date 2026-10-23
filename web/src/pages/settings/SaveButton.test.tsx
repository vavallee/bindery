import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import SaveButton from './SaveButton'
import { primaryFill } from '../../components/buttons'
import { useSaveResult } from './useSaveResult'

// Harness that wires the button to the real hook, so these assertions cover the
// pair the way a settings tab uses it rather than the button in isolation.
function Harness({ save }: { save: () => Promise<unknown> }) {
  const [result, run] = useSaveResult()
  return <SaveButton result={result} saving={false} onClick={() => run(save)} testId="save" />
}

describe('SaveButton', () => {
  it('confirms a successful save, then returns to idle', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    render(<Harness save={() => Promise.resolve()} />)

    expect(screen.getByTestId('save')).toHaveTextContent(/^common\.save$/)

    fireEvent.click(screen.getByTestId('save'))
    await waitFor(() => expect(screen.getByTestId('save')).toHaveTextContent('Saved ✓'))
    // Saved keeps the primary fill and its white text (#3265).
    expect(screen.getByTestId('save').className).toContain(primaryFill)

    vi.advanceTimersByTime(2000)
    await waitFor(() => expect(screen.getByTestId('save')).toHaveTextContent(/^common\.save$/))
    vi.useRealTimers()
  })

  // The whole point of #1668: a rejected save must not read as success. The
  // three settings tabs used to swallow the error in saveSetting, which would
  // have made this button lie.
  it('reports a failed save as an error, not as saved', async () => {
    render(<Harness save={() => Promise.reject(new Error('nope'))} />)

    fireEvent.click(screen.getByTestId('save'))
    await waitFor(() => expect(screen.getByTestId('save')).toHaveTextContent('Error'))
    expect(screen.getByTestId('save').className).toContain('bg-red-600')
  })

  it('shows the in-flight label and blocks re-entry while saving', () => {
    const onClick = vi.fn()
    render(<SaveButton result="idle" saving onClick={onClick} testId="save" />)

    const btn = screen.getByTestId('save')
    expect(btn).toHaveTextContent('common.saving')
    expect(btn).toBeDisabled()
    fireEvent.click(btn)
    expect(onClick).not.toHaveBeenCalled()
  })

  // A fast round trip must still show the confirmation rather than flashing
  // back to "Save" because `saving` had already flipped false.
  it('keeps the saved label visible even once saving has cleared', () => {
    render(<SaveButton result="saved" saving={false} onClick={() => {}} testId="save" />)
    expect(screen.getByTestId('save')).toHaveTextContent('Saved ✓')
  })

  it('honours a field-level disabled guard', () => {
    const onClick = vi.fn()
    render(<SaveButton result="idle" saving={false} disabled onClick={onClick} testId="save" />)
    fireEvent.click(screen.getByTestId('save'))
    expect(onClick).not.toHaveBeenCalled()
  })
})
