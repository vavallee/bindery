import type { ReactNode } from 'react'

// The section + heading + bordered-card pattern BookDetailPage had repeated
// inline for File, Audiobook, History and Danger zone. AuthorDetailPage's Books
// section used neither, so the two pages disagreed about what a section looks
// like. Extracted so they agree, and so the padding/border/surface choice lives
// in one place.

export function Card({
  className = '',
  tone = 'default',
  children,
}: {
  className?: string
  /** `danger` is reserved for genuinely irreversible actions. */
  tone?: 'default' | 'danger'
  children: ReactNode
}) {
  const toneCls =
    tone === 'danger'
      ? 'border-rose-200 dark:border-rose-900 bg-rose-50 dark:bg-rose-950/30'
      : 'border-slate-200 dark:border-zinc-800 bg-slate-100 dark:bg-zinc-900'
  return <div className={`rounded-lg border ${toneCls} ${className}`}>{children}</div>
}

export default function Section({
  title,
  tone = 'default',
  actions,
  bare = false,
  className = 'mt-8',
  cardClassName = 'p-4',
  children,
}: {
  title: ReactNode
  tone?: 'default' | 'danger'
  /** Controls rendered on the heading row, right-aligned. */
  actions?: ReactNode
  /**
   * Render children directly instead of inside a Card. For sections that
   * supply their own container — a book grid, or a stack of nested groups.
   */
  bare?: boolean
  className?: string
  cardClassName?: string
  children: ReactNode
}) {
  return (
    <section className={className}>
      <div className="mb-3 flex items-center justify-between gap-3">
        <h3
          className={`text-base font-semibold ${
            tone === 'danger'
              ? 'text-rose-700 dark:text-rose-400'
              : 'text-slate-800 dark:text-zinc-200'
          }`}
        >
          {title}
        </h3>
        {actions && <div className="flex items-center gap-3">{actions}</div>}
      </div>
      {bare ? children : <Card tone={tone} className={cardClassName}>{children}</Card>}
    </section>
  )
}
