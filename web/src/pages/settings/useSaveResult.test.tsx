import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { renderHook, act } from '@testing-library/react'
import { useSaveResult } from './useSaveResult'

describe('useSaveResult', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.runOnlyPendingTimers()
    vi.useRealTimers()
  })

  it('transitions idle → saved → idle on success', async () => {
    const { result } = renderHook(() => useSaveResult())
    expect(result.current[0]).toBe('idle')

    await act(async () => {
      await result.current[1](() => Promise.resolve())
    })
    expect(result.current[0]).toBe('saved')

    act(() => {
      vi.advanceTimersByTime(2000)
    })
    expect(result.current[0]).toBe('idle')
  })

  it('transitions idle → error → idle on failure', async () => {
    const { result } = renderHook(() => useSaveResult())

    await act(async () => {
      await result.current[1](() => Promise.reject(new Error('boom')))
    })
    expect(result.current[0]).toBe('error')

    // Error stays visible for 3s, not 2s.
    act(() => {
      vi.advanceTimersByTime(2000)
    })
    expect(result.current[0]).toBe('error')

    act(() => {
      vi.advanceTimersByTime(1000)
    })
    expect(result.current[0]).toBe('idle')
  })

  it('cancels a previously scheduled idle reset when save() is re-invoked', async () => {
    const { result } = renderHook(() => useSaveResult())

    await act(async () => {
      await result.current[1](() => Promise.resolve())
    })
    expect(result.current[0]).toBe('saved')

    // Re-save before the first 2s reset fires; the old timer must be cancelled
    // so it can't clobber the new 'saved' state.
    act(() => {
      vi.advanceTimersByTime(1000)
    })
    await act(async () => {
      await result.current[1](() => Promise.resolve())
    })
    expect(result.current[0]).toBe('saved')

    // The original timer (scheduled 1s ago) would have fired here if it were
    // still pending; it must not, so we remain 'saved'.
    act(() => {
      vi.advanceTimersByTime(1000)
    })
    expect(result.current[0]).toBe('saved')

    // The new timer fires at its own 2s mark.
    act(() => {
      vi.advanceTimersByTime(1000)
    })
    expect(result.current[0]).toBe('idle')
  })

  it('clears pending timers on unmount (no setState after unmount)', async () => {
    const clearSpy = vi.spyOn(globalThis, 'clearTimeout')
    const { result, unmount } = renderHook(() => useSaveResult())

    await act(async () => {
      await result.current[1](() => Promise.resolve())
    })
    expect(result.current[0]).toBe('saved')

    // Unmount with a pending reset timer; cleanup must clear it.
    unmount()
    expect(clearSpy).toHaveBeenCalled()

    // Advancing past the reset window must not throw or warn (timer gone).
    expect(() => {
      vi.advanceTimersByTime(3000)
    }).not.toThrow()

    clearSpy.mockRestore()
  })
})
