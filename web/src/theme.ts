import { useEffect, useState } from 'react'

export type Theme = 'light' | 'dark'

const STORAGE_KEY = 'bindery.theme'

function readInitial(): Theme {
  if (typeof window === 'undefined') return 'dark'
  const saved = localStorage.getItem(STORAGE_KEY) as Theme | null
  if (saved === 'light' || saved === 'dark') return saved
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
}

function apply(theme: Theme) {
  if (typeof document === 'undefined') return
  document.documentElement.classList.toggle('dark', theme === 'dark')
}

/**
 * useTheme — returns the current theme and a setter. Persists to
 * localStorage and toggles the `dark` class on <html>. The initial
 * state is read synchronously so the app's first render already
 * reflects the persisted/system theme.
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
