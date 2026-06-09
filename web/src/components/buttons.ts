// Shared button vocabulary. Call sites used to invent their own class strings
// per button, which drifted in two ways: emphasis was inconsistent, and most
// destructive actions were a bare `text-red-400 hover:text-red-300` with no
// light-mode pairing — red-400 on a light surface fails WCAG AA. These variants
// carry their own slate(light)/zinc(dark) pairing so danger is AA-safe in both
// themes and primary/secondary/tertiary emphasis is consistent everywhere.
//
// Colour semantics (see ui-design): emerald = primary, red = destructive,
// slate/zinc = neutral.
//
// Variants are colour/emphasis only — compose a size from `btnSize` so call
// sites that need a larger touch target don't end up with two conflicting
// padding utilities on one element: `className={`${btn.danger} ${btnSize.sm}`}`.

const base =
  'inline-flex items-center justify-center gap-1.5 rounded-md font-medium transition-colors ' +
  'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-offset-1 ' +
  'focus-visible:ring-offset-slate-100 dark:focus-visible:ring-offset-zinc-900 ' +
  'disabled:opacity-50 disabled:cursor-not-allowed'

export const btn = {
  // Primary action (grab, save, add an entity).
  primary: `${base} bg-emerald-600 hover:bg-emerald-500 text-white focus-visible:ring-emerald-500`,
  // Secondary action (filled neutral, lower weight than primary).
  secondary: `${base} bg-slate-200 dark:bg-zinc-800 hover:bg-slate-300 dark:hover:bg-zinc-700 text-slate-800 dark:text-zinc-200 focus-visible:ring-slate-400`,
  // Tertiary action (ghost, lowest emphasis — e.g. row "Refresh").
  ghost: `${base} text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white hover:bg-slate-200/60 dark:hover:bg-zinc-800/60 focus-visible:ring-slate-400`,
  // Destructive action: ghost-danger button (outlined + tinted hover). AA-safe
  // in both themes. Pair with a confirmation at the call site — this is styling,
  // not a guard.
  danger: `${base} border border-red-300 dark:border-red-900/70 text-red-700 dark:text-red-300 hover:bg-red-50 dark:hover:bg-red-950/40 focus-visible:ring-red-500`,
} as const

export const btnSize = {
  sm: 'px-2.5 py-1 text-xs',
  md: 'px-3 py-1.5 text-sm',
  lg: 'px-3 py-2 text-xs', // larger touch target (queue row actions)
} as const

// Inline destructive link, for dense list rows where a bordered button would be
// too heavy. Properly paired for light + dark — use this instead of a bare
// `text-red-400`, which is invisible-to-low-contrast on light surfaces.
export const dangerLink =
  'text-red-700 dark:text-red-400 hover:text-red-800 dark:hover:text-red-300 transition-colors'
