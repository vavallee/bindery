import { useTheme } from '../theme'

export default function ThemeToggle() {
  const { theme, setTheme } = useTheme()
  const isDark = theme === 'dark'

  return (
    <button
      type="button"
      onClick={() => setTheme(isDark ? 'light' : 'dark')}
      aria-label={`Switch to ${isDark ? 'light' : 'dark'} mode`}
      aria-pressed={isDark}
      className={`relative inline-flex h-7 w-12 shrink-0 items-center rounded-full border transition-colors ${
        isDark
          ? 'bg-zinc-700 border-zinc-600'
          : 'bg-slate-200 border-slate-300'
      }`}
    >
      <span
        className={`absolute left-1 top-0.5 h-5 w-5 rounded-full shadow-sm transition-transform ${
          isDark ? 'translate-x-5 bg-zinc-900' : 'translate-x-0 bg-white'
        }`}
      />
      <span
        aria-hidden
        className={`absolute left-1.5 top-1 text-[11px] transition-opacity ${
          isDark ? 'opacity-30' : 'opacity-80'
        }`}
      >
        ☀
      </span>
      <span
        aria-hidden
        className={`absolute right-1.5 top-1 text-[11px] transition-opacity ${
          isDark ? 'opacity-80' : 'opacity-30'
        }`}
      >
        🌙
      </span>
    </button>
  )
}
