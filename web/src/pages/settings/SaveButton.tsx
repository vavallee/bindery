// SaveButton renders the save-confirmation state shared by the inline Settings
// fields: "Save" → "Saving…" while in flight → a green "Saved ✓" for two
// seconds → back to idle, or a red "Error" when the request failed.
//
// It exists because that ternary chain was hand-rolled at each call site and
// only some of the inline save buttons ever grew one, so saving the Book Naming
// Template gave no feedback at all while saving the Calibre library path a tab
// away did (#1668). Pair it with useSaveResult: the hook owns the state and the
// reset timers, this owns what that state looks like.
//
// Modal submit buttons deliberately don't use it — the dialog closes on save,
// so there is nowhere for the confirmation to live.

import { useTranslation } from 'react-i18next'
import { SaveResult } from './useSaveResult'
import { primaryFill } from '../../components/buttons'

export interface SaveButtonProps {
  result: SaveResult
  /** True while this field's request is in flight. */
  saving: boolean
  onClick: () => void
  /** Field-level guard (validation errors, empty required input). */
  disabled?: boolean
  /** Size/spacing only — the colour is owned by the component. */
  className?: string
  ariaLabel?: string
  testId?: string
}

export default function SaveButton({
  result,
  saving,
  onClick,
  disabled = false,
  className = 'px-3 py-2 text-xs',
  ariaLabel,
  testId,
}: SaveButtonProps) {
  const { t } = useTranslation()

  // Saved keeps the primary fill, so the button doesn't jump to a lighter
  // green with the page's text colour on it (#3265); the label carries the
  // confirmation.
  const colour = result === 'error' ? 'bg-red-600' : primaryFill

  // The saved/error states outrank "Saving…" so a fast round trip still shows
  // the confirmation rather than flashing straight back to "Save".
  const label =
    result === 'saved'
      ? t('common.saved', 'Saved ✓')
      : result === 'error'
        ? t('common.saveError', 'Error')
        : saving
          ? t('common.saving')
          : t('common.save')

  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled || saving}
      aria-label={ariaLabel}
      data-testid={testId}
      className={`rounded font-medium disabled:opacity-50 transition-colors ${colour} ${className}`}
    >
      {label}
    </button>
  )
}
