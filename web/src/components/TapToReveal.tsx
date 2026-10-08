import { useId, useState } from 'react'

// Short text whose fuller form used to live only in a title tooltip, such as
// "6m ago" over the exact time. A tooltip needs a hover, so a touch screen
// never showed it. The text is a button now: a tap or a keyboard press shows
// the detail inline after the label, and a second one hides it again. Mouse
// users still get the tooltip on hover.
export default function TapToReveal({ label, detail, className = '' }: {
  label: string
  detail: string
  className?: string
}) {
  const [shown, setShown] = useState(false)
  const detailId = useId()
  return (
    <button
      type="button"
      title={detail}
      aria-expanded={shown}
      aria-controls={shown ? detailId : undefined}
      onClick={() => setShown(s => !s)}
      className={`text-left cursor-help touch-manipulation ${className}`}
    >
      {label}
      {shown && <span id={detailId}> ({detail})</span>}
    </button>
  )
}
