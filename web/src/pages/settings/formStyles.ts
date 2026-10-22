// Shared form-control class strings for the Settings tabs.
//
// Previously copy-pasted ~6× inside SettingsPage.tsx (and ~10× across the app).
// Import these instead of declaring another local `const inputCls = '...'`.

export const inputCls =
  'w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm'

export const labelCls = 'block text-xs text-fg-muted mb-1'
