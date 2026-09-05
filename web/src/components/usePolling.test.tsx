import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act, render } from '@testing-library/react'
import { useState } from 'react'
import { usePolling } from './usePolling'

// jsdom's document.hidden is derived from visibilityState, which is read-only,
// so drive it through a redefined property and dispatch the real event.
let hidden = false
function setHidden(value: boolean) {
  hidden = value
  act(() => { document.dispatchEvent(new Event('visibilitychange')) })
}

beforeEach(() => {
  vi.useFakeTimers()
  hidden = false
  Object.defineProperty(document, 'hidden', { configurable: true, get: () => hidden })
})

afterEach(() => {
  vi.useRealTimers()
})

function Harness({ fn, enabled = true }: { fn: () => void; enabled?: boolean }) {
  usePolling(fn, 5000, enabled)
  return null
}

describe('usePolling', () => {
  it('does not call fn on mount, then calls it every interval', () => {
    const fn = vi.fn()
    render(<Harness fn={fn} />)
    expect(fn).not.toHaveBeenCalled()

    act(() => { vi.advanceTimersByTime(5000) })
    expect(fn).toHaveBeenCalledTimes(1)
    act(() => { vi.advanceTimersByTime(10000) })
    expect(fn).toHaveBeenCalledTimes(3)
  })

  it('stops while the tab is hidden and catches up when it comes back', () => {
    const fn = vi.fn()
    render(<Harness fn={fn} />)

    act(() => { vi.advanceTimersByTime(5000) })
    expect(fn).toHaveBeenCalledTimes(1)

    setHidden(true)
    act(() => { vi.advanceTimersByTime(60000) })
    expect(fn).toHaveBeenCalledTimes(1)

    // One immediate catch-up call, then the interval resumes.
    setHidden(false)
    expect(fn).toHaveBeenCalledTimes(2)
    act(() => { vi.advanceTimersByTime(5000) })
    expect(fn).toHaveBeenCalledTimes(3)
  })

  it('does not start at all when the tab is already hidden', () => {
    hidden = true
    const fn = vi.fn()
    render(<Harness fn={fn} />)
    act(() => { vi.advanceTimersByTime(20000) })
    expect(fn).not.toHaveBeenCalled()
  })

  // The bug this hook exists for: WantedPage listed hover and grab state in the
  // effect's deps, so every hover cleared the interval and restarted the 5 s
  // window, and a busy user could go a long time without a refresh (#2360).
  it('keeps one timer running when unrelated state changes', () => {
    const fn = vi.fn()
    let bump: () => void = () => {}

    function Rerenderer() {
      const [n, setN] = useState(0)
      bump = () => setN(x => x + 1)
      // A fresh closure every render, exactly like an inline arrow on a page.
      usePolling(() => fn(n), 5000)
      return null
    }

    render(<Rerenderer />)
    act(() => { vi.advanceTimersByTime(4000) })
    act(() => { bump() })
    act(() => { bump() })
    // Restarting the timer on each render would leave 1000ms still to run.
    act(() => { vi.advanceTimersByTime(1000) })
    expect(fn).toHaveBeenCalledTimes(1)
    // And it calls the latest closure, not the one captured on mount.
    expect(fn).toHaveBeenLastCalledWith(2)
  })

  it('runs nothing while disabled and starts once enabled', () => {
    const fn = vi.fn()
    const { rerender } = render(<Harness fn={fn} enabled={false} />)
    act(() => { vi.advanceTimersByTime(20000) })
    expect(fn).not.toHaveBeenCalled()

    rerender(<Harness fn={fn} enabled />)
    act(() => { vi.advanceTimersByTime(5000) })
    expect(fn).toHaveBeenCalledTimes(1)
  })

  it('clears the timer and the listener on unmount', () => {
    const fn = vi.fn()
    const remove = vi.spyOn(document, 'removeEventListener')
    const { unmount } = render(<Harness fn={fn} />)
    unmount()

    act(() => { vi.advanceTimersByTime(20000) })
    expect(fn).not.toHaveBeenCalled()
    expect(remove).toHaveBeenCalledWith('visibilitychange', expect.any(Function))
    remove.mockRestore()
  })
})
