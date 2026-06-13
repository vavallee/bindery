import { useState, useCallback } from 'react'

type SaveResult = 'idle' | 'saved' | 'error'

export function useSaveResult(): [SaveResult, (fn: () => Promise<unknown>) => Promise<void>] {
  const [result, setResult] = useState<SaveResult>('idle')

  const save = useCallback(async (fn: () => Promise<unknown>) => {
    setResult('idle')
    try {
      await fn()
      setResult('saved')
      setTimeout(() => setResult('idle'), 2000)
    } catch {
      setResult('error')
      setTimeout(() => setResult('idle'), 3000)
    }
  }, [])

  return [result, save]
}
