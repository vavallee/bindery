import { useCallback, useEffect, useRef, useState } from 'react'

export type ClipboardCopyStatus = 'idle' | 'copied' | 'manual'

// Keep copy buttons working on plain-HTTP LAN installs where the modern
// Clipboard API is unavailable, then fall back to a visible textarea.
function legacyCopyToClipboard(text: string): boolean {
  const textarea = document.createElement('textarea')
  textarea.value = text
  textarea.setAttribute('readonly', '')
  textarea.style.position = 'fixed'
  textarea.style.top = '-9999px'
  textarea.style.left = '-9999px'
  document.body.appendChild(textarea)
  textarea.focus()
  textarea.select()
  try {
    return document.execCommand('copy')
  } catch {
    return false
  } finally {
    document.body.removeChild(textarea)
  }
}

export function useClipboardCopy(resetDelayMs = 1500) {
  const [status, setStatus] = useState<ClipboardCopyStatus>('idle')
  const [manualText, setManualText] = useState('')
  const resetTimerRef = useRef<number | undefined>(undefined)

  const clearResetTimer = useCallback(() => {
    if (resetTimerRef.current !== undefined) {
      window.clearTimeout(resetTimerRef.current)
      resetTimerRef.current = undefined
    }
  }, [])

  useEffect(() => () => {
    clearResetTimer()
  }, [clearResetTimer])

  const markCopied = useCallback(() => {
    setManualText('')
    setStatus('copied')
    resetTimerRef.current = window.setTimeout(() => {
      setStatus('idle')
      resetTimerRef.current = undefined
    }, resetDelayMs)
  }, [resetDelayMs])

  const copy = useCallback(async (text: string) => {
    clearResetTimer()
    if (window.isSecureContext && navigator.clipboard?.writeText) {
      try {
        await navigator.clipboard.writeText(text)
        markCopied()
        return true
      } catch {
        // Try the legacy path before asking the user to copy manually.
      }
    }
    if (legacyCopyToClipboard(text)) {
      markCopied()
      return true
    }
    setManualText(text)
    setStatus('manual')
    return false
  }, [clearResetTimer, markCopied])

  const reset = useCallback(() => {
    clearResetTimer()
    setManualText('')
    setStatus('idle')
  }, [clearResetTimer])

  return { status, manualText, copy, reset }
}
