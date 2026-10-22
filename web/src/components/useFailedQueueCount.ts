import { useCallback, useEffect, useRef, useState } from 'react'
import { api } from '../api/client'
import { usePolling } from './usePolling'

// The count behind the Activity nav badge: queue rows that failed and need a
// retry or a removal. Import had the only count in the nav while a queue of
// failed downloads, which is more urgent, showed nothing. For anyone who sees
// the queue (every role but requester) it is read on app load and then once a
// minute while the tab is visible, from /queue/failed-count, which reads the
// downloads table and asks no download client. QueuePage also fires an event
// each time it loads the queue, so the badge follows the open page without
// waiting for the next minute.

const EVENT = 'bindery:queue-failed-changed'

export function notifyFailedQueueChanged(failed: number) {
  window.dispatchEvent(new CustomEvent<number>(EVENT, { detail: failed }))
}

export function useFailedQueueCount(enabled: boolean): number {
  const [count, setCount] = useState(0)
  // Every write takes a new number, and a fetch keeps its answer only if no
  // write came after it was sent: a minute-old poll answering after QueuePage
  // reported the cleared queue would put the old count back.
  const seq = useRef(0)

  const refresh = useCallback(() => {
    const mine = ++seq.current
    api.failedQueueCount()
      .then(r => { if (mine === seq.current) setCount(r.failed) })
      .catch(() => { /* the badge is a convenience; no count shows nothing */ })
  }, [])
  usePolling(refresh, 60_000, enabled)

  useEffect(() => {
    if (!enabled) return
    refresh()
    const onChange = (e: Event) => {
      seq.current++
      setCount((e as CustomEvent<number>).detail)
    }
    window.addEventListener(EVENT, onChange)
    return () => {
      // An answer still in flight is dropped after unmount or a disable.
      seq.current++
      window.removeEventListener(EVENT, onChange)
    }
  }, [enabled, refresh])

  return enabled ? count : 0
}
