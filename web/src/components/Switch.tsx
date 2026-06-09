import type { ReactNode } from 'react'

interface SwitchProps {
  checked: boolean
  onChange: () => void
  /** Accessible name (and tooltip, unless `title` is given). */
  label: string
  /** Optional visible text rendered next to the switch. */
  children?: ReactNode
  title?: string
  className?: string
}

// A toggle switch (sliding knob), used for controls like "Monitored" that the
// user can flip — distinct from a read-only status badge (see bookStatus). The
// sliding affordance is the whole point: a badge and a toggle must not look
// alike, or users can't tell which pills they can click. Track/knob sizing
// matches the original inline switch on the Series page.
export default function Switch({ checked, onChange, label, children, title, className = '' }: SwitchProps) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      title={title ?? label}
      onClick={onChange}
      className={`group inline-flex items-center gap-2 focus-visible:outline-none ${className}`}
    >
      <span
        className={`relative w-9 h-5 rounded-full transition-colors flex-shrink-0 group-focus-visible:ring-2 group-focus-visible:ring-emerald-500 group-focus-visible:ring-offset-1 group-focus-visible:ring-offset-slate-100 dark:group-focus-visible:ring-offset-zinc-900 ${checked ? 'bg-emerald-600' : 'bg-slate-300 dark:bg-zinc-700'}`}
      >
        <span className={`absolute top-0.5 left-0.5 w-4 h-4 bg-white rounded-full transition-transform ${checked ? 'translate-x-4' : ''}`} />
      </span>
      {children != null && <span className="text-xs font-medium text-slate-700 dark:text-zinc-300">{children}</span>}
    </button>
  )
}
