import { useCallback, useSyncExternalStore } from 'react'

// Breakpoints as media queries, matching Tailwind's defaults. Use these for a
// layout that has to change its markup below a breakpoint (a select instead
// of a sidebar, stacked blocks instead of a table). For anything a class can
// do, prefer the sm: and md: variants: they need no JavaScript.
export const BELOW_SM = '(width < 40rem)'
export const BELOW_MD = '(width < 48rem)'

/**
 * Whether `query` matches now, kept current as the viewport changes. Reads as
 * false where matchMedia is missing (jsdom, very old browsers), which is the
 * wide layout every page was built for first.
 */
export function useMediaQuery(query: string): boolean {
  const subscribe = useCallback((onChange: () => void) => {
    if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return () => {}
    const list = window.matchMedia(query)
    list.addEventListener('change', onChange)
    return () => list.removeEventListener('change', onChange)
  }, [query])
  const snapshot = () =>
    typeof window !== 'undefined' && typeof window.matchMedia === 'function' && window.matchMedia(query).matches
  return useSyncExternalStore(subscribe, snapshot, () => false)
}
