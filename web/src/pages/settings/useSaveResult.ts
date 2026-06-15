import { useCallback, useEffect, useRef, useState } from 'react'

type SaveResult = 'idle' | 'saved' | 'error'

export function useSaveResult(): [SaveResult, (fn: () => Promise<unknown>) => Promise<void>] {
  const [result, setResult] = useState<SaveResult>('idle')
  // Holds the pending "reset to idle" timer so we can cancel a previously
  // scheduled reset when save() is re-invoked (rapid re-clicks) and clear it
  // on unmount — otherwise stacked timers fire setState after the component is
  // gone (React warns) and an old reset can clobber a newer saved/error state.
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  const clearTimer = () => {
    if (timerRef.current !== null) {
      clearTimeout(timerRef.current)
      timerRef.current = null
    }
  }

  const save = useCallback(async (fn: () => Promise<unknown>) => {
    clearTimer()
    setResult('idle')
    try {
      await fn()
      setResult('saved')
      timerRef.current = setTimeout(() => setResult('idle'), 2000)
    } catch {
      setResult('error')
      timerRef.current = setTimeout(() => setResult('idle'), 3000)
    }
  }, [])

  useEffect(() => clearTimer, [])

  return [result, save]
}
