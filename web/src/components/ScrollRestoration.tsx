import { useEffect, useLayoutEffect, useRef } from 'react'
import { useLocation, useNavigationType } from 'react-router'

const STORAGE_KEY = 'bindery.scroll'
// Lists load their rows after the route renders, so the page is too short to
// reach the saved offset at first. Keep trying for this long.
const RESTORE_WINDOW_MS = 2500
const RESTORE_INTERVAL_MS = 50
// Bound the stored map so a long session does not grow it forever.
const MAX_ENTRIES = 100

function sessionStore(): Storage | null {
  try {
    return window.sessionStorage
  } catch {
    return null
  }
}

function readPositions(): Map<string, number> {
  const map = new Map<string, number>()
  try {
    const raw = sessionStore()?.getItem(STORAGE_KEY)
    if (!raw) return map
    const parsed: unknown = JSON.parse(raw)
    if (parsed && typeof parsed === 'object') {
      for (const [k, v] of Object.entries(parsed as Record<string, unknown>)) {
        if (typeof v === 'number' && Number.isFinite(v)) map.set(k, v)
      }
    }
  } catch {
    // Scroll memory is a convenience; a broken entry just means no restore.
  }
  return map
}

function writePositions(map: Map<string, number>) {
  try {
    const entries = [...map.entries()].slice(-MAX_ENTRIES)
    sessionStore()?.setItem(STORAGE_KEY, JSON.stringify(Object.fromEntries(entries)))
  } catch {
    // Unavailable storage only loses restore across reloads.
  }
}

// The first entry of a tab has no key of its own ('default'), and neither does
// a full page load from the address bar, so qualify it with the URL to avoid
// restoring one page's offset onto another.
function entryKey(location: { key: string; pathname: string; search: string }): string {
  return location.key === 'default' ? `default:${location.pathname}${location.search}` : location.key
}

function maxScrollY(): number {
  const doc = document.documentElement
  return Math.max(doc.scrollHeight, document.body?.scrollHeight ?? 0) - window.innerHeight
}

/**
 * ScrollRestoration does for BrowserRouter what the data routers' component of
 * the same name does (#3052):
 *
 *  - a PUSH to a different pathname starts at the top, so a book opened from
 *    far down the list does not open already scrolled to the list's offset;
 *  - a POP (back, forward, the Android back gesture, the iOS swipe) returns to
 *    where that history entry was left, keyed by location.key and kept in
 *    sessionStorage so it survives a reload too;
 *  - a PUSH that changes the list's `page` param also starts at the top;
 *  - a REPLACE to a different pathname starts at the top as well: that is
 *    how a navigation from inside an open modal arrives (useModal);
 *  - a REPLACE within the page and other query-only pushes (filters, sort,
 *    a modal's own history entry) leave the scroll
 *    alone, and a location with a hash is left to the browser's own anchor
 *    handling. A REPLACE of the location being restored does not cancel the
 *    restore.
 *
 * Restoring waits for the page to grow tall enough, because a list fetches its
 * rows after mounting, and gives up as soon as the user scrolls themselves.
 */
export default function ScrollRestoration() {
  const location = useLocation()
  const navigationType = useNavigationType()
  const positionsRef = useRef<Map<string, number> | null>(null)
  const keyRef = useRef(entryKey(location))
  const pathnameRef = useRef<string | null>(null)
  const searchRef = useRef<string | null>(null)

  const positions = () => {
    if (!positionsRef.current) positionsRef.current = readPositions()
    return positionsRef.current
  }

  // Take scroll restoration away from the browser, which would otherwise race
  // this component on back/forward.
  useEffect(() => {
    if (!('scrollRestoration' in window.history)) return
    const previous = window.history.scrollRestoration
    window.history.scrollRestoration = 'manual'
    return () => { window.history.scrollRestoration = previous }
  }, [])

  // Record the offset of whichever entry is current on every scroll. Reading it
  // at navigation time instead would be too late: the new route has already
  // rendered and the browser has clamped scrollY to the shorter page.
  useEffect(() => {
    let flushTimer: ReturnType<typeof setTimeout> | undefined
    const flush = () => {
      flushTimer = undefined
      writePositions(positions())
    }
    const onScroll = () => {
      const map = positions()
      map.delete(keyRef.current)
      map.set(keyRef.current, window.scrollY)
      if (flushTimer === undefined) flushTimer = setTimeout(flush, 200)
    }
    window.addEventListener('scroll', onScroll, { passive: true })
    window.addEventListener('pagehide', flush)
    return () => {
      window.removeEventListener('scroll', onScroll)
      window.removeEventListener('pagehide', flush)
      if (flushTimer !== undefined) {
        clearTimeout(flushTimer)
        flush()
      }
    }
  }, [])

  // The restore in flight, if any, and the location it is restoring. It lives
  // outside the effect so a REPLACE of the same location (a page tidying its
  // own URL while rows load) does not cancel it through effect cleanup.
  const restoreRef = useRef<{ stop: () => void; pathname: string; search: string } | null>(null)
  useEffect(() => () => restoreRef.current?.stop(), [])

  useLayoutEffect(() => {
    const key = entryKey(location)
    keyRef.current = key
    const previousPathname = pathnameRef.current
    const previousSearch = searchRef.current
    pathnameRef.current = location.pathname
    searchRef.current = location.search

    const inFlight = restoreRef.current
    if (inFlight) {
      if (navigationType === 'REPLACE' && inFlight.pathname === location.pathname && inFlight.search === location.search) return
      inFlight.stop()
    }

    if (navigationType === 'POP') {
      const target = positions().get(key)
      if (target === undefined) return

      let cancelled = false
      const deadline = Date.now() + RESTORE_WINDOW_MS
      let timer: ReturnType<typeof setTimeout> | undefined
      const stop = () => {
        cancelled = true
        if (timer !== undefined) clearTimeout(timer)
        window.removeEventListener('wheel', stop)
        window.removeEventListener('touchmove', stop)
        window.removeEventListener('keydown', stop)
        if (restoreRef.current?.stop === stop) restoreRef.current = null
      }
      const attempt = () => {
        if (cancelled) return
        const reachable = maxScrollY() >= target - 1
        if (reachable || Date.now() >= deadline) {
          window.scrollTo(0, target)
          stop()
          return
        }
        // Get as close as the page allows for now, then try again once more
        // rows have rendered.
        window.scrollTo(0, target)
        timer = setTimeout(attempt, RESTORE_INTERVAL_MS)
      }
      restoreRef.current = { stop, pathname: location.pathname, search: location.search }
      window.addEventListener('wheel', stop, { passive: true })
      window.addEventListener('touchmove', stop, { passive: true })
      window.addEventListener('keydown', stop)
      attempt()
      return
    }

    if (location.hash || previousPathname === null) return
    // A navigation made from inside an open modal replaces the modal's
    // history entry (useModal), so a new page can arrive as a REPLACE too.
    if (navigationType === 'REPLACE') {
      if (previousPathname !== location.pathname) window.scrollTo(0, 0)
      return
    }
    if (navigationType !== 'PUSH') return
    // A new page starts at the top, and so does a new page of a list:
    // Pagination sits below the rows, so page 2 would otherwise open at its
    // bottom.
    const pageParam = (search: string | null) => new URLSearchParams(search ?? '').get('page')
    if (previousPathname !== location.pathname || pageParam(previousSearch) !== pageParam(location.search)) {
      window.scrollTo(0, 0)
    }
    // positions() only reads a ref; it does not need to be a dependency.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [location.key, location.pathname, location.search, location.hash, navigationType])

  return null
}
