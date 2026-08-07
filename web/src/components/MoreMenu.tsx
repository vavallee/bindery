import { useEffect, useId, useRef, useState } from 'react'
import { btn, btnSize } from './buttons'

// Overflow menu for actions that shouldn't spend room in a button row.
//
// Both detail pages had grown action rows wide enough to wrap, which buried the
// primary action among rarely-used ones. This holds the long tail behind one
// control.
//
// Dismissal: Escape and outside-click both close and both return focus to the
// trigger, so keyboard users aren't stranded at the top of the document. Arrow
// keys move between items and Home/End jump to the ends, matching the WAI-ARIA
// menu-button pattern.

export interface MoreMenuItem {
  label: string
  onSelect: () => void
  /** Renders the item in the destructive vocabulary. */
  danger?: boolean
  disabled?: boolean
  title?: string
}

export default function MoreMenu({
  items,
  label,
  className = '',
  buttonClassName = `${btn.secondary} ${btnSize.md}`,
}: {
  items: MoreMenuItem[]
  /** Accessible name for the trigger, e.g. "More actions". */
  label: string
  className?: string
  buttonClassName?: string
}) {
  const [open, setOpen] = useState(false)
  const [activeIndex, setActiveIndex] = useState(-1)
  const rootRef = useRef<HTMLDivElement>(null)
  const triggerRef = useRef<HTMLButtonElement>(null)
  const itemRefs = useRef<Array<HTMLButtonElement | null>>([])
  const menuId = useId()

  const enabledIndexes = items.map((it, i) => (it.disabled ? -1 : i)).filter(i => i >= 0)

  const close = (returnFocus = true) => {
    setOpen(false)
    setActiveIndex(-1)
    if (returnFocus) triggerRef.current?.focus()
  }

  // Outside click. Pointerdown rather than click so the menu closes on press,
  // matching native menus; the listener only exists while open.
  useEffect(() => {
    if (!open) return
    const onPointerDown = (e: PointerEvent) => {
      if (!rootRef.current?.contains(e.target as Node)) close(false)
    }
    document.addEventListener('pointerdown', onPointerDown)
    return () => document.removeEventListener('pointerdown', onPointerDown)
  }, [open])

  // Escape closes from anywhere, including when focus has left the menu.
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

  useEffect(() => {
    if (open && activeIndex >= 0) itemRefs.current[activeIndex]?.focus()
  }, [open, activeIndex])

  const openAt = (where: 'first' | 'last') => {
    if (enabledIndexes.length === 0) return
    setOpen(true)
    setActiveIndex(where === 'first' ? enabledIndexes[0] : enabledIndexes[enabledIndexes.length - 1])
  }

  const move = (delta: number) => {
    if (enabledIndexes.length === 0) return
    const pos = enabledIndexes.indexOf(activeIndex)
    const next = pos === -1 ? 0 : (pos + delta + enabledIndexes.length) % enabledIndexes.length
    setActiveIndex(enabledIndexes[next])
  }

  const onTriggerKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'ArrowDown' || e.key === 'Enter' || e.key === ' ') {
      e.preventDefault()
      openAt('first')
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      openAt('last')
    }
  }

  const onMenuKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      move(1)
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      move(-1)
    } else if (e.key === 'Home') {
      e.preventDefault()
      setActiveIndex(enabledIndexes[0])
    } else if (e.key === 'End') {
      e.preventDefault()
      setActiveIndex(enabledIndexes[enabledIndexes.length - 1])
    } else if (e.key === 'Tab') {
      // Tabbing out of a menu dismisses it, but focus must land where the user
      // is going rather than snapping back to the trigger.
      close(false)
    }
  }

  const select = (item: MoreMenuItem) => {
    if (item.disabled) return
    close()
    item.onSelect()
  }

  return (
    <div ref={rootRef} className={`relative ${className}`}>
      <button
        ref={triggerRef}
        type="button"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? menuId : undefined}
        onClick={() => (open ? close(false) : openAt('first'))}
        onKeyDown={onTriggerKeyDown}
        className={buttonClassName}
      >
        {label}
        <span aria-hidden>▾</span>
      </button>

      {open && (
        <div
          id={menuId}
          role="menu"
          aria-label={label}
          onKeyDown={onMenuKeyDown}
          className="absolute right-0 z-20 mt-1 min-w-44 rounded-md border border-slate-300 dark:border-zinc-700 bg-slate-50 dark:bg-zinc-900 py-1"
        >
          {items.map((item, i) => (
            <button
              key={item.label}
              ref={el => { itemRefs.current[i] = el }}
              type="button"
              role="menuitem"
              disabled={item.disabled}
              title={item.title}
              tabIndex={i === activeIndex ? 0 : -1}
              onClick={() => select(item)}
              className={`block w-full px-3 py-1.5 text-left text-sm disabled:opacity-40 disabled:cursor-not-allowed focus-visible:outline-none focus:bg-slate-200 dark:focus:bg-zinc-800 ${
                item.danger
                  ? 'text-red-700 dark:text-red-400 hover:bg-red-50 dark:hover:bg-red-950/40'
                  : 'text-slate-700 dark:text-zinc-300 hover:bg-slate-200 dark:hover:bg-zinc-800'
              }`}
            >
              {item.label}
            </button>
          ))}
        </div>
      )}
    </div>
  )
}
