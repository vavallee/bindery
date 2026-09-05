import { useEffect, useId, useRef, useState, type ReactNode } from 'react'
import { btn, btnSize } from './buttons'

// Filter and sort controls behind one disclosure.
//
// Both list pages had grown two rows of pills: Books packed sort, media type
// and monitored into a single wrapping row, Authors carried five sort pills
// plus a monitored row. That is a lot of permanent furniture above a list, and
// it wraps badly at narrow widths.
//
// The constraint this is built under: decluttering must not cost the
// discoverability of a control people actually reach for. So the trigger
// carries a count when filters are set, and the caller renders the active ones
// as removable chips beside it. A filter you have applied is never invisible.
//
// Dismissal mirrors MoreMenu: Escape and outside pointerdown both close and
// return focus to the trigger.

export interface FilterOption<T extends string> {
  value: T
  label: ReactNode
  /** Distinct accessible name when `label` carries an emoji or other decoration. */
  ariaLabel?: string
}

/**
 * One single-select facet. A radio group rather than a menu: these set state
 * that persists, they are not commands, and exactly one value is always active.
 */
export function FilterGroup<T extends string>({
  label,
  value,
  onChange,
  options,
}: {
  label: string
  value: T
  onChange: (next: T) => void
  options: FilterOption<T>[]
}) {
  return (
    <div role="radiogroup" aria-label={label} className="px-1 py-1.5">
      <p className="px-2 pb-1 text-[11px] font-semibold uppercase tracking-wide text-fg-muted">{label}</p>
      <div className="flex flex-col">
        {options.map(opt => {
          const active = opt.value === value
          return (
            <button
              key={opt.value}
              type="button"
              role="radio"
              aria-checked={active}
              aria-label={opt.ariaLabel}
              onClick={() => onChange(opt.value)}
              className={`flex items-center gap-2 px-2 py-1.5 rounded text-left text-sm transition-colors ${
                active
                  ? 'text-slate-900 dark:text-white font-medium'
                  : 'text-slate-700 dark:text-zinc-300 hover:bg-slate-200 dark:hover:bg-zinc-700'
              }`}
            >
              <span aria-hidden="true" className={`w-3 text-emerald-600 dark:text-emerald-400 ${active ? '' : 'opacity-0'}`}>
                ✓
              </span>
              <span className="truncate">{opt.label}</span>
            </button>
          )
        })}
      </div>
    </div>
  )
}

export default function FilterPopover({
  label,
  ariaLabel,
  activeCount = 0,
  children,
}: {
  label: string
  ariaLabel?: string
  /** Shown on the trigger so an applied filter is visible without opening it. */
  activeCount?: number
  children: ReactNode
}) {
  const [open, setOpen] = useState(false)
  const rootRef = useRef<HTMLDivElement>(null)
  const triggerRef = useRef<HTMLButtonElement>(null)
  const panelId = useId()

  const close = (returnFocus = true) => {
    setOpen(false)
    if (returnFocus) triggerRef.current?.focus()
  }

  useEffect(() => {
    if (!open) return
    const onPointerDown = (e: PointerEvent) => {
      if (!rootRef.current?.contains(e.target as Node)) close(false)
    }
    document.addEventListener('pointerdown', onPointerDown)
    return () => document.removeEventListener('pointerdown', onPointerDown)
  }, [open])

  useEffect(() => {
    if (!open) return
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.stopPropagation()
        close()
      }
    }
    document.addEventListener('keydown', onKeyDown)
    return () => document.removeEventListener('keydown', onKeyDown)
  }, [open])

  return (
    <div className="relative" ref={rootRef}>
      <button
        ref={triggerRef}
        type="button"
        aria-haspopup="true"
        aria-expanded={open}
        aria-controls={open ? panelId : undefined}
        aria-label={ariaLabel}
        onClick={() => setOpen(o => !o)}
        className={`${btn.secondary} ${btnSize.sm}`}
      >
        {label}
        {activeCount > 0 && (
          <span className="ml-1 inline-flex items-center justify-center min-w-[1.25rem] px-1 rounded-full bg-emerald-600 text-white text-[11px] font-semibold">
            {activeCount}
          </span>
        )}
        <span aria-hidden="true" className="text-[10px] opacity-70">▾</span>
      </button>

      {open && (
        <div
          id={panelId}
          className="absolute left-0 top-full mt-1 z-20 min-w-[13rem] max-h-[70vh] overflow-y-auto rounded-lg border border-slate-200 dark:border-zinc-700 bg-white dark:bg-zinc-800 shadow-lg divide-y divide-slate-200 dark:divide-zinc-700"
        >
          {children}
        </div>
      )}
    </div>
  )
}
