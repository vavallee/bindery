import { useEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import Alert from './Alert'

interface Props {
  text: string
  className?: string
}

// Info tier, not warning. A browser refusing the clipboard over plain HTTP is
// the expected behaviour of a non secure context, and the remedy is the
// textarea directly below the sentence, already focused and selected. There is
// nothing to chase and nothing broken, so an amber box was overstating it and
// tinting the text people were about to copy.
//
// The alert supplies the live region, so the sentence no longer carries its
// own role="status" — nesting one inside the other announced it twice.
export default function ClipboardManualFallback({ text, className = '' }: Props) {
  const { t } = useTranslation()
  const textareaRef = useRef<HTMLTextAreaElement>(null)

  useEffect(() => {
    textareaRef.current?.select()
  }, [text])

  return (
    <Alert tier="info" className={`p-3 ${className}`}>
      <p className="mb-2 text-xs">
        {t('common.clipboardBlocked', 'Clipboard access is blocked. Copy the selected text below.')}
      </p>
      <textarea
        ref={textareaRef}
        readOnly
        aria-label={t('common.clipboardTextLabel', 'Text to copy')}
        value={text}
        onFocus={e => e.currentTarget.select()}
        className="h-32 w-full resize-y rounded border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-950 p-2 font-mono text-xs text-slate-700 dark:text-zinc-200"
      />
    </Alert>
  )
}
