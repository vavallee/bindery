import { describe, expect, it, vi } from 'vitest'
import { act, renderHook } from '@testing-library/react'
import { api } from '../api/client'
import { notifyFailedQueueChanged, useFailedQueueCount } from './useFailedQueueCount'

vi.mock('../api/client', () => ({
  api: { failedQueueCount: vi.fn() },
}))

describe('useFailedQueueCount', () => {
  // A fetch sent before QueuePage reported the cleared queue must not put
  // the old count back when it answers.
  it('keeps the newer count from the page over an older fetch answer', async () => {
    let answer: (v: { failed: number }) => void = () => {}
    vi.mocked(api.failedQueueCount).mockReturnValue(new Promise(r => { answer = r }))
    const { result } = renderHook(() => useFailedQueueCount(true))
    act(() => notifyFailedQueueChanged(0))
    await act(async () => { answer({ failed: 29 }) })
    expect(result.current).toBe(0)
  })

  it('shows the fetched count when nothing newer came in', async () => {
    vi.mocked(api.failedQueueCount).mockResolvedValue({ failed: 3 })
    const { result } = renderHook(() => useFailedQueueCount(true))
    await act(async () => {})
    expect(result.current).toBe(3)
  })

  it('reads nothing and shows nothing when disabled', () => {
    vi.mocked(api.failedQueueCount).mockClear()
    const { result } = renderHook(() => useFailedQueueCount(false))
    expect(result.current).toBe(0)
    expect(api.failedQueueCount).not.toHaveBeenCalled()
  })
})
