export interface BulkAction {
  label: string
  onClick: () => void
  variant?: 'default' | 'danger'
}

interface BulkActionBarProps {
  count: number
  actions: BulkAction[]
  onClear: () => void
  busy?: boolean
}

/**
 * Sticky footer shown whenever one or more items are selected on a list page.
 * Renders nothing when count === 0 so callers don't need conditional wrapping.
 */
export default function BulkActionBar({ count, actions, onClear, busy = false }: BulkActionBarProps) {
  if (count === 0) return null

  return (
    <div className="fixed bottom-0 left-0 right-0 z-50 flex items-center justify-between gap-4 px-6 py-3 bg-slate-800 dark:bg-zinc-950 border-t border-slate-600 dark:border-zinc-700 shadow-xl">
      <span className="text-sm font-medium text-white shrink-0">
        {count} selected
      </span>
      <div className="flex items-center gap-2 flex-wrap justify-end">
        {actions.map((action) => (
          <button
            key={action.label}
            onClick={action.onClick}
            disabled={busy}
            className={`px-3 py-1.5 rounded text-xs font-medium transition-colors disabled:opacity-50 disabled:cursor-not-allowed ${
              action.variant === 'danger'
                ? 'bg-red-600 hover:bg-red-500 text-white'
                : 'bg-slate-600 hover:bg-slate-500 dark:bg-zinc-700 dark:hover:bg-zinc-600 text-white'
            }`}
          >
            {action.label}
          </button>
        ))}
        <button
          onClick={onClear}
          disabled={busy}
          className="px-3 py-1.5 rounded text-xs font-medium text-slate-400 hover:text-white transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
        >
          Clear
        </button>
      </div>
    </div>
  )
}
