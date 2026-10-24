import type { ReactNode } from 'react'

// Two different situations that pages used to render the same way:
//   - "empty": there is nothing here yet. Say what fills it and how.
//   - "no-match": there is data, the current search or filter hides all of
//     it. Say so, and offer the way back, so it is not mistaken for an empty
//     library.
// The caller supplies the words; this keeps the shape and spacing the same on
// every page.
export default function EmptyState({
  variant = 'empty',
  title,
  hint,
  action,
  children,
}: {
  variant?: 'empty' | 'no-match'
  title: string
  hint?: ReactNode
  /** One button or link, e.g. Clear filters or Add author. */
  action?: ReactNode
  /** Extra guidance above the title (setup checklists). */
  children?: ReactNode
}) {
  return (
    <div className="text-center py-16 text-fg-muted" data-empty-state={variant}>
      {children}
      <p className={variant === 'empty' ? 'text-lg font-medium text-slate-700 dark:text-zinc-300' : 'font-medium'}>{title}</p>
      {hint && <p className="text-sm mt-1 max-w-md mx-auto">{hint}</p>}
      {action && <div className="mt-4 flex justify-center">{action}</div>}
    </div>
  )
}
