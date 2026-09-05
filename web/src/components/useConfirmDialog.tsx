import { useCallback, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import ConfirmDialog from './ConfirmDialog'

export interface ConfirmOptions {
  /** Heading. Say what is about to happen. */
  title: string
  /** Explanatory body. A plain string keeps its line breaks. */
  body: React.ReactNode
  /** Confirm button text. Defaults to common.confirm. */
  confirmLabel?: string
  /**
   * Set this to require an "I understand" tick before the confirm button
   * enables. Reserve it for the irreversible actions.
   */
  acknowledgeLabel?: string
}

/**
 * useConfirmDialog is the in-app replacement for window.confirm (#2359). It
 * returns an awaitable `confirm` plus the element to render, so a call site
 * keeps the shape it already had:
 *
 *   const { confirm, confirmDialog } = useConfirmDialog()
 *   ...
 *   if (!await confirm({ title, body })) return
 *   ...
 *   return <>{confirmDialog}...</>
 *
 * Only one prompt can be open at a time, which is what window.confirm gave us
 * as well. Unmounting with a prompt open leaves its promise unsettled, same as
 * navigating away from a native dialog.
 */
export function useConfirmDialog(): {
  confirm: (options: ConfirmOptions) => Promise<boolean>
  confirmDialog: React.ReactNode
} {
  const { t } = useTranslation()
  const [pending, setPending] = useState<ConfirmOptions | null>(null)
  const resolveRef = useRef<((value: boolean) => void) | null>(null)

  const confirm = useCallback((options: ConfirmOptions) => new Promise<boolean>(resolve => {
    // A second prompt while one is open would strand the first promise, so
    // decline it rather than leaving a caller awaiting forever.
    if (resolveRef.current) {
      resolve(false)
      return
    }
    resolveRef.current = resolve
    setPending(options)
  }), [])

  const settle = useCallback((value: boolean) => {
    const resolve = resolveRef.current
    resolveRef.current = null
    setPending(null)
    resolve?.(value)
  }, [])

  const confirmDialog = pending ? (
    <ConfirmDialog
      title={pending.title}
      // whitespace-pre-line keeps the blank lines and bullet lists the messages
      // carried over from window.confirm, which rendered \n itself.
      body={typeof pending.body === 'string'
        ? <p className="whitespace-pre-line">{pending.body}</p>
        : pending.body}
      acknowledgeLabel={pending.acknowledgeLabel}
      confirmLabel={pending.confirmLabel ?? t('common.confirm', 'Confirm')}
      onConfirm={() => settle(true)}
      onClose={() => settle(false)}
    />
  ) : null

  return { confirm, confirmDialog }
}
