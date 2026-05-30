import { useEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'

interface Props {
  text: string
  className?: string
}

export default function ClipboardManualFallback({ text, className = '' }: Props) {
  const { t } = useTranslation()
  const textareaRef = useRef<HTMLTextAreaElement>(null)

  useEffect(() => {
    textareaRef.current?.select()
  }, [text])

  return (
    <div className={`rounded border border-amber-200 dark:border-amber-900/70 bg-amber-50 dark:bg-amber-950/30 p-3 text-amber-900 dark:text-amber-200 ${className}`}>
      <p role="status" className="mb-2 text-xs">
        {t('common.clipboardBlocked', 'Clipboard access is blocked. Copy the selected text below.')}
      </p>
      <textarea
        ref={textareaRef}
        readOnly
        aria-label={t('common.clipboardTextLabel', 'Text to copy')}
        value={text}
        onFocus={e => e.currentTarget.select()}
        className="h-32 w-full resize-y rounded border border-amber-200 dark:border-amber-900/70 bg-white dark:bg-zinc-950 p-2 font-mono text-xs text-slate-700 dark:text-zinc-200"
      />
    </div>
  )
}
