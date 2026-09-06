import { useId, useState, type AriaRole, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

export type AlertTier = 'info' | 'warning' | 'error'

// One notice component, three tiers, and the tier is the whole point: it has
// to mean something.
//
//   info     neutral, dismissible. What happened was configured to happen.
//            Worth saying, not worth colouring.
//   warning  amber, self clearing. Something is off that Bindery cannot fix
//            on its own, but nothing is blocked yet.
//   error    red, persistent. Blocked, or the user has to act before the
//            thing they asked for will happen.
//
// Before this existed every notice picked its own palette and amber won by
// default, so a refresh that skipped books exactly as the metadata profile
// told it to looked the same as an indexer with nowhere to send its grabs.
// When everything is amber, amber stops carrying information.
//
// `details` is the declutter valve, and it is a disclosure rather than a
// deletion. The long form is one button away and lands in the DOM the moment
// it is asked for, which is what lets an info alert be a single line without
// losing a word of what it used to say.

const tierCls: Record<AlertTier, string> = {
  info: 'border-slate-300 dark:border-zinc-700 bg-slate-100 dark:bg-zinc-900 text-slate-700 dark:text-zinc-300',
  warning: 'border-amber-300 dark:border-amber-700/60 bg-amber-50 dark:bg-amber-950/40 text-amber-900 dark:text-amber-200',
  error: 'border-red-300 dark:border-red-800/60 bg-red-50 dark:bg-red-950/40 text-red-900 dark:text-red-200',
}

const dismissHoverCls: Record<AlertTier, string> = {
  info: 'hover:bg-slate-200 dark:hover:bg-zinc-800',
  warning: 'hover:bg-amber-100 dark:hover:bg-amber-900/50',
  error: 'hover:bg-red-100 dark:hover:bg-red-900/50',
}

// An error is the only tier that interrupts a screen reader. The other two
// are usually on screen from the first paint, where an assertive live region
// is noise rather than help.
const defaultRole: Record<AlertTier, AriaRole> = {
  info: 'status',
  warning: 'status',
  error: 'alert',
}

export default function Alert({
  tier,
  title,
  children,
  details,
  detailsLabel,
  actions,
  onDismiss,
  dismissible = false,
  role,
  className = '',
  testId,
}: {
  tier: AlertTier
  /** The one liner. Rendered in medium weight above the body. */
  title?: ReactNode
  /** Body that is always on screen. */
  children?: ReactNode
  /** The long form, revealed by a toggle. Never removed, only folded away. */
  details?: ReactNode
  /** Overrides the "Show details" toggle wording. */
  detailsLabel?: string
  /** Controls rendered to the right of the message, before the dismiss button. */
  actions?: ReactNode
  /** Supplying this makes the alert dismissible and reports the dismissal. */
  onDismiss?: () => void
  /** Makes the alert dismissible with no callback, when the parent does not care. */
  dismissible?: boolean
  role?: AriaRole
  className?: string
  testId?: string
}) {
  const { t } = useTranslation()
  const [dismissed, setDismissed] = useState(false)
  const [open, setOpen] = useState(false)
  const detailsId = useId()

  if (dismissed) return null

  const canDismiss = dismissible || onDismiss !== undefined
  const dismiss = () => {
    setDismissed(true)
    onDismiss?.()
  }

  return (
    <div
      data-testid={testId}
      role={role ?? defaultRole[tier]}
      className={`px-3 py-2 rounded-lg border text-sm ${tierCls[tier]} ${className}`}
    >
      <div className="flex flex-wrap items-start gap-3">
        <div className="flex-1 min-w-[12rem]">
          {title && <div className="font-medium">{title}</div>}
          {children}
          {details && (
            <button
              type="button"
              onClick={() => setOpen(o => !o)}
              aria-expanded={open}
              aria-controls={detailsId}
              className="mt-1 block text-xs underline hover:no-underline"
            >
              {detailsLabel ??
                (open ? t('common.hideDetails', 'Hide details') : t('common.showDetails', 'Show details'))}
            </button>
          )}
          {details && open && (
            <div id={detailsId} className="mt-1">
              {details}
            </div>
          )}
        </div>
        {actions && <div className="flex flex-wrap items-center gap-2 shrink-0">{actions}</div>}
        {canDismiss && (
          <button
            type="button"
            onClick={dismiss}
            aria-label={t('common.dismiss', 'Dismiss')}
            className={`shrink-0 rounded-md px-2 py-1 leading-none transition-colors ${dismissHoverCls[tier]}`}
          >
            ✕
          </button>
        )}
      </div>
    </div>
  )
}
