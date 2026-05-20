import { useState } from 'react'
import { useTranslation } from 'react-i18next'

interface Props {
  title: string
  /** Explanatory body. A string, or arbitrary nodes for emphasis/markup. */
  body: React.ReactNode
  /** Label for the checkbox the user must tick before confirming. */
  acknowledgeLabel: string
  /** Confirm button text. */
  confirmLabel: string
  /** Confirm button text while the action is in flight. */
  confirmingLabel?: string
  confirming?: boolean
  onConfirm: () => void
  onClose: () => void
}

/**
 * A guarded confirmation modal: the confirm button stays disabled until the
 * user ticks an "I understand" checkbox. Built on the shared modal shell.
 */
export default function ConfirmDialog({
  title,
  body,
  acknowledgeLabel,
  confirmLabel,
  confirmingLabel,
  confirming = false,
  onConfirm,
  onClose,
}: Props) {
  const { t } = useTranslation()
  const [acknowledged, setAcknowledged] = useState(false)

  return (
    <div
      className="fixed inset-0 bg-black/60 flex items-center justify-center p-4 z-50"
      onClick={onClose}
    >
      <div
        className="bg-slate-100 dark:bg-zinc-900 border border-slate-300 dark:border-zinc-700 rounded-lg w-full max-w-md shadow-2xl max-h-[90vh] flex flex-col"
        onClick={e => e.stopPropagation()}
      >
        <div className="p-4 border-b border-slate-200 dark:border-zinc-800">
          <h3 className="text-lg font-semibold text-slate-800 dark:text-zinc-200">{title}</h3>
        </div>
        <div className="p-4 flex-1 overflow-y-auto space-y-4">
          <div className="text-sm text-slate-600 dark:text-zinc-400 leading-relaxed">{body}</div>
          <label className="flex items-start gap-2 text-sm text-slate-700 dark:text-zinc-300 cursor-pointer">
            <input
              type="checkbox"
              checked={acknowledged}
              onChange={e => setAcknowledged(e.target.checked)}
              disabled={confirming}
              className="mt-0.5 rounded border-slate-300 dark:border-zinc-700 text-emerald-600 focus:ring-emerald-500"
            />
            <span>{acknowledgeLabel}</span>
          </label>
        </div>
        <div className="p-4 border-t border-slate-200 dark:border-zinc-800 flex justify-end gap-2">
          <button
            type="button"
            onClick={onClose}
            disabled={confirming}
            className="px-3 py-1.5 text-sm font-medium rounded bg-slate-200 dark:bg-zinc-800 text-slate-700 dark:text-zinc-300 hover:bg-slate-300 dark:hover:bg-zinc-700 disabled:opacity-50"
          >
            {t('common.cancel')}
          </button>
          <button
            type="button"
            onClick={onConfirm}
            disabled={!acknowledged || confirming}
            className="px-3 py-1.5 text-sm font-medium rounded bg-red-600 hover:bg-red-500 text-white disabled:opacity-50"
          >
            {confirming ? confirmingLabel ?? confirmLabel : confirmLabel}
          </button>
        </div>
      </div>
    </div>
  )
}
