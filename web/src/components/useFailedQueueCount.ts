import { useEffect, useState } from 'react'
import { api } from '../api/client'
import { isFailed } from './downloadStatus'

// The count behind the Activity nav badge: queue rows that failed and need a
// retry or a removal. Import had the only count in the nav while a queue of
// failed downloads, which is more urgent, showed nothing. Read once per app
// load for an admin, then updated from the event QueuePage fires each time it
// loads the queue (it polls while open). Nothing else polls.

const EVENT = 'bindery:queue-failed-changed'

export function notifyFailedQueueChanged(failed: number) {
  window.dispatchEvent(new CustomEvent<number>(EVENT, { detail: failed }))
}

export function useFailedQueueCount(enabled: boolean): number {
  const [count, setCount] = useState(0)

  useEffect(() => {
    if (!enabled) return
    let cancelled = false
    api.listQueue()
      .then(q => { if (!cancelled) setCount((q.items ?? []).filter(it => isFailed(it.status)).length) })
      .catch(() => { /* the badge is a convenience; no count shows nothing */ })
    const onChange = (e: Event) => setCount((e as CustomEvent<number>).detail)
    window.addEventListener(EVENT, onChange)
    return () => {
      cancelled = true
      window.removeEventListener(EVENT, onChange)
    }
  }, [enabled])

  return enabled ? count : 0
}
