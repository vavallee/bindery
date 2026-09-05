import { useEffect, useState } from 'react'

export type Theme = 'light' | 'dark'

const STORAGE_KEY = 'bindery.theme'

function readInitial(): Theme {
  if (typeof window === 'undefined') return 'dark'
  let saved: Theme | null = null
  try {
    saved = localStorage.getItem(STORAGE_KEY) as Theme | null
  } catch {
    // Private mode or blocked storage. Fall through to the OS preference
    // rather than throwing out of a useState initializer, which would take
    // the whole app down at boot.
  }
  if (saved === 'light' || saved === 'dark') return saved
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
}

function apply(theme: Theme) {
  if (typeof document === 'undefined') return
  document.documentElement.classList.toggle('dark', theme === 'dark')
}

/**
 * useTheme returns the current theme and a setter. It persists to
 * localStorage and toggles the `dark` class on <html>.
 *
 * The initial state is read synchronously, so the React tree's first render
 * already reflects the stored theme. The `dark` class is NOT applied that
 * early: it lands in the effect below, after the first paint. The inline
 * bootstrap in index.html is what sets the class before the browser paints,
 * and it has to keep agreeing with readInitial() above; theme.bootstrap.test.ts
 * fails if the two drift.
 */
export function useTheme() {
  const [theme, setThemeState] = useState<Theme>(readInitial)

  useEffect(() => {
    apply(theme)
  }, [theme])

  const setTheme = (next: Theme) => {
    try {
      localStorage.setItem(STORAGE_KEY, next)
    } catch {
      // ignore quota / privacy-mode errors
    }
    setThemeState(next)
  }

  return { theme, setTheme }
}
