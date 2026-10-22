import { useTranslation } from 'react-i18next'
import MoreMenu, { type MoreMenuItem } from './MoreMenu'
import { BELOW_SM, useMediaQuery } from './useMediaQuery'

interface BulkActionBase {
  label: string
  variant?: 'default' | 'caution' | 'danger'
}

/**
 * A command, or a choice: `choices` renders as a small menu ("Set format" ▸
 * Ebook / Audiobook / Both) instead of one button per value, and in the
 * overflow menu each choice becomes its own item, prefixed with the label
 * ("Set format: Ebook"), so a choice's label names only its value. An action
 * is one or the other, never both.
 */
export type BulkAction = BulkActionBase & (
  | { onClick: () => void; choices?: never }
  | { choices: { label: string; onClick: () => void }[]; onClick?: never }
)

interface BulkActionBarProps {
  count: number
  actions: BulkAction[]
  onClear: () => void
  busy?: boolean
}

// How many actions stay inline. Everything past this goes behind a More
// menu, so the bar is a short row of the common actions rather than a flat
// run of every label: Authors had nine buttons in one row, with Delete beside
// Clear. Callers list their primary actions first. On a phone the bar keeps
// to one row, so it holds two and Clear moves into the menu too: with every
// button inline it wrapped to four or five rows and covered the list rows
// and pagination it acts on.
const DESKTOP_INLINE_ACTIONS = 4
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

const menuItems = (action: BulkAction): MoreMenuItem[] =>
  action.choices
    ? action.choices.map(c => ({ label: `${action.label}: ${c.label}`, onSelect: c.onClick }))
    : [{
        label: action.label,
        onSelect: action.onClick,
        danger: action.variant === 'danger',
        caution: action.variant === 'caution',
      }]

/**
 * Sticky footer shown whenever one or more items are selected on a list page.
 * Renders nothing when count === 0 so callers don't need conditional wrapping.
 *
 * Destructive actions never sit inline: Delete goes to the end of the More
 * menu, where reaching it is a deliberate second step, and the caller's
 * confirmation names how many items it will touch.
 */
export default function BulkActionBar({ count, actions, onClear, busy = false }: BulkActionBarProps) {
  const { t } = useTranslation()
  const narrow = useMediaQuery(BELOW_SM)
  if (count === 0) return null

  const limit = narrow ? PHONE_INLINE_ACTIONS : DESKTOP_INLINE_ACTIONS
  const safe = actions.filter(a => a.variant !== 'danger')
  const danger = actions.filter(a => a.variant === 'danger')
  const inline = safe.slice(0, limit)
  const overflow = [...safe.slice(limit), ...danger]
  const clearInMenu = narrow && overflow.length > 0

  return (
    <div data-testid="bulk-action-bar" className="fixed bottom-0 left-0 right-0 z-50 flex items-center justify-between gap-2 sm:gap-4 px-safe-3 sm:px-safe-6 pt-3 pb-safe-3 bg-slate-800 dark:bg-zinc-950 border-t border-slate-600 dark:border-zinc-700 shadow-xl">
      <span className="text-sm font-medium text-white min-w-0 truncate sm:shrink-0">
        {t('bulkActionBar.selected', { count })}
      </span>
      <div className={`flex items-center gap-2 justify-end ${narrow ? 'shrink-0' : 'flex-wrap pointer-coarse:gap-y-5'}`}>
        {inline.map(action => action.choices ? (
          <MoreMenu
            key={action.label}
            label={action.label}
            ariaLabel={action.label}
            placement="above"
            disabled={busy}
            buttonClassName={`${actionClass(action.variant)} inline-flex items-center gap-1`}
            items={action.choices.map(c => ({ label: c.label, onSelect: c.onClick }))}
          />
        ) : (
          <button
            key={action.label}
            onClick={action.onClick}
            disabled={busy}
            className={actionClass(action.variant)}
          >
            {action.label}
          </button>
        ))}
        {overflow.length > 0 && (
          <MoreMenu
            label={t('common.more')}
            ariaLabel={t('bulkActionBar.more', 'More bulk actions')}
            placement="above"
            disabled={busy}
            buttonClassName={`${actionClass('default')} inline-flex items-center gap-1`}
            items={[
              ...overflow.flatMap(menuItems),
              ...(clearInMenu ? [{ label: t('bulkActionBar.clear'), onSelect: onClear }] : []),
            ]}
          />
        )}
        {!clearInMenu && (
          <button onClick={onClear} disabled={busy} className={clearClass}>
            {t('bulkActionBar.clear')}
          </button>
        )}
      </div>
    </div>
  )
}
