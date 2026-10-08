import { useTranslation } from 'react-i18next'
import MoreMenu from './MoreMenu'
import { BELOW_SM, useMediaQuery } from './useMediaQuery'

export interface BulkAction {
  label: string
  onClick: () => void
  variant?: 'default' | 'caution' | 'danger'
}

interface BulkActionBarProps {
  count: number
  actions: BulkAction[]
  onClear: () => void
  busy?: boolean
}

// How many actions stay inline on a phone. The rest, and Clear, go behind a
// More menu, so the bar keeps to one row: with every button inline it wrapped
// to four or five rows and covered the list rows and pagination it acts on.
// Callers list their primary actions first.
const PHONE_INLINE_ACTIONS = 2

const actionClass = (variant: BulkAction['variant']) =>
  `touch-target px-3 py-1.5 rounded text-xs font-medium transition-colors disabled:opacity-50 disabled:cursor-not-allowed ${
    variant === 'danger'
      ? 'bg-red-600 hover:bg-red-500 text-white'
      : variant === 'caution'
        ? 'bg-amber-600 hover:bg-amber-500 text-white'
        : 'bg-slate-600 hover:bg-slate-500 dark:bg-zinc-700 dark:hover:bg-zinc-600 text-white'
  }`

const clearClass = 'touch-target px-3 py-1.5 rounded text-xs font-medium text-slate-400 hover:text-white transition-colors disabled:opacity-50 disabled:cursor-not-allowed'

/**
 * Sticky footer shown whenever one or more items are selected on a list page.
 * Renders nothing when count === 0 so callers don't need conditional wrapping.
 */
export default function BulkActionBar({ count, actions, onClear, busy = false }: BulkActionBarProps) {
  const { t } = useTranslation()
  const narrow = useMediaQuery(BELOW_SM)
  if (count === 0) return null

  const collapse = narrow && actions.length > PHONE_INLINE_ACTIONS
  const inline = collapse ? actions.slice(0, PHONE_INLINE_ACTIONS) : actions
  const overflow = collapse ? actions.slice(PHONE_INLINE_ACTIONS) : []

  return (
    <div className="fixed bottom-0 left-0 right-0 z-50 flex items-center justify-between gap-2 sm:gap-4 px-safe-3 sm:px-safe-6 pt-3 pb-safe-3 bg-slate-800 dark:bg-zinc-950 border-t border-slate-600 dark:border-zinc-700 shadow-xl">
      <span className="text-sm font-medium text-white min-w-0 truncate sm:shrink-0">
        {t('bulkActionBar.selected', { count })}
      </span>
      <div className={`flex items-center gap-2 justify-end ${collapse ? 'shrink-0' : 'flex-wrap pointer-coarse:gap-y-5'}`}>
        {inline.map((action) => (
          <button
            key={action.label}
            onClick={action.onClick}
            disabled={busy}
            className={actionClass(action.variant)}
          >
            {action.label}
          </button>
        ))}
        {collapse ? (
          <MoreMenu
            label={t('common.more')}
            placement="above"
            disabled={busy}
            buttonClassName={`${actionClass('default')} inline-flex items-center gap-1`}
            items={[
              ...overflow.map(action => ({
                label: action.label,
                onSelect: action.onClick,
                danger: action.variant === 'danger',
                caution: action.variant === 'caution',
              })),
              { label: t('bulkActionBar.clear'), onSelect: onClear },
            ]}
          />
        ) : (
          <button onClick={onClear} disabled={busy} className={clearClass}>
            {t('bulkActionBar.clear')}
          </button>
        )}
      </div>
    </div>
  )
}
