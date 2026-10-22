import { useEffect, useRef } from 'react'

// One selection checkbox for every list. Books, Authors and Queue drew theirs
// as circles (`rounded-full`), which reads as a radio button: pick one. These
// controls are multi-select, so they are square everywhere, the shape the
// Author detail table already used.
//
// `indeterminate` is a DOM property with no attribute, so it is set through a
// ref; a header "select all" shows it when part of the page is selected.
export const checkboxClass = 'rounded border-slate-400 dark:border-zinc-600'

export default function Checkbox({
  checked,
  onChange,
  label,
  indeterminate = false,
  title,
  disabled = false,
  className = '',
}: {
  checked: boolean
  onChange: (checked: boolean) => void
  /** Accessible name. Every list checkbox needs one: a column of unnamed boxes is unusable with a screen reader. */
  label: string
  indeterminate?: boolean
  title?: string
  disabled?: boolean
  className?: string
}) {
  const ref = useRef<HTMLInputElement>(null)
  useEffect(() => {
    if (ref.current) ref.current.indeterminate = indeterminate
  }, [indeterminate])
  return (
    <input
      ref={ref}
      type="checkbox"
      checked={checked}
      disabled={disabled}
      onChange={e => onChange(e.target.checked)}
      aria-label={label}
      title={title ?? label}
      className={`${checkboxClass} ${className}`}
    />
  )
}
