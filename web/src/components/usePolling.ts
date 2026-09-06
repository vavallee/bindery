import { useEffect, useRef } from 'react'

/**
 * usePolling runs `fn` every `intervalMs` while the tab is visible (#2360).
 *
 * Two things it does that a bare setInterval in an effect did not:
 *
 *  - It stops on `document.hidden` and restarts on `visibilitychange`, with one
 *    catch-up call the moment the tab comes back, so a queue left open in a
 *    background window overnight stops asking the server for news nobody is
 *    reading. Everything here polls a self-hosted box, so the point is noise and
 *    battery rather than cost.
 *  - It keeps `fn` in a ref, so an inline arrow that closes over changing state
 *    does not tear the timer down and restart the interval on every render. The
 *    Wanted page restarted its 5 s window on every hover.
 *
 * `fn` is NOT called on mount. Pages do their first load in their own effect,
 * where the loading state lives.
 *
 * Pass `enabled: false` to suspend polling entirely, e.g. behind a user-facing
 * "auto refresh" toggle.
 */
export function usePolling(fn: () => void, intervalMs: number, enabled = true): void {
  const saved = useRef(fn)
  useEffect(() => { saved.current = fn })

  useEffect(() => {
    if (!enabled) return
    let timer: ReturnType<typeof setInterval> | null = null
    const tick = () => saved.current()
    const start = () => { if (timer === null) timer = setInterval(tick, intervalMs) }
    const stop = () => {
      if (timer !== null) {
        clearInterval(timer)
        timer = null
      }
    }
    const onVisibilityChange = () => {
      if (document.hidden) {
        stop()
        return
      }
      // Catch up first, so coming back to the tab shows current data rather
      // than data up to intervalMs stale.
      tick()
      start()
    }

    if (!document.hidden) start()
    document.addEventListener('visibilitychange', onVisibilityChange)
    return () => {
      stop()
      document.removeEventListener('visibilitychange', onVisibilityChange)
    }
  }, [intervalMs, enabled])
}
