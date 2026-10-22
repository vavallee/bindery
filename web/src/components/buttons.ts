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

// Solid emerald fill for primary actions, with its own text colour. btn.primary
// uses it, and so do the primary buttons that keep their own padding and shape:
// those used to set the colours themselves, and most set no text colour, so in
// the dark theme they took the page's zinc-100 at 3.3:1 (#3265). The colour of
// every primary button is set here and nowhere else. White on emerald-700 is
// 5.4:1, and the hover goes darker, to emerald-800 at 7.6:1, rather than
// lighter: emerald-600 and -500 left white at 3.7:1 and 2.5:1.
export const primaryFill = 'bg-emerald-700 hover:bg-emerald-800 text-white'

const base =
  'inline-flex items-center justify-center gap-1.5 rounded-md font-medium transition-colors ' +
  'focus-visible:outline-hidden focus-visible:ring-2 focus-visible:ring-offset-1 ' +
  'focus-visible:ring-offset-slate-100 dark:focus-visible:ring-offset-zinc-900 ' +
  'disabled:opacity-50 disabled:cursor-not-allowed'

export const btn = {
  // Primary action (grab, save, add an entity).
  primary: `${base} ${primaryFill} focus-visible:ring-emerald-500`,
  // Secondary action (filled neutral, lower weight than primary).
  secondary: `${base} bg-slate-200 dark:bg-zinc-800 hover:bg-slate-300 dark:hover:bg-zinc-700 text-slate-800 dark:text-zinc-200 focus-visible:ring-slate-400`,
  // Tertiary action (ghost, lowest emphasis — e.g. row "Refresh").
  ghost: `${base} text-fg-muted hover:text-slate-900 dark:hover:text-white hover:bg-slate-200/60 dark:hover:bg-zinc-800/60 focus-visible:ring-slate-400`,
  // Destructive action: ghost-danger button (outlined + tinted hover). AA-safe
  // in both themes. Pair with a confirmation at the call site — this is styling,
  // not a guard.
  danger: `${base} border border-red-300 dark:border-red-900/70 text-red-700 dark:text-red-300 hover:bg-red-50 dark:hover:bg-red-950/40 focus-visible:ring-red-500`,
  // Irreversible destructive action — solid red, the loudest control we have.
  // Reserve it for actions with no undo (delete a book and its files, delete an
  // author). A reversible destructive action gets `danger` above; when both
  // appear on one page, solid red must mark the one you can't take back.
  dangerSolid: `${base} bg-red-600 hover:bg-red-500 text-white focus-visible:ring-red-500`,
} as const

export const btnSize = {
  sm: 'px-2.5 py-1 text-xs',
  md: 'px-3 py-1.5 text-sm',
  lg: 'px-3 py-2 text-xs', // larger touch target (queue row actions)
} as const

// Solid dark grey fill for the neutral action buttons in the Settings tabs
// (Test connection, Scan Library, Refresh runs…). It sets its own text colour:
// those buttons used to take the page's, which in the light theme is dark
// slate on dark slate, 1.7:1 (#3241). Colour only, like the variants above;
// the call site keeps its own padding and shape.
export const slateFill = 'bg-slate-700 hover:bg-slate-600 text-white'

// Inline destructive link, for dense list rows where a bordered button would be
// too heavy. Properly paired for light + dark — use this instead of a bare
// `text-red-400`, which is invisible-to-low-contrast on light surfaces.
export const dangerLink =
  'text-red-700 dark:text-red-400 hover:text-red-800 dark:hover:text-red-300 transition-colors'
