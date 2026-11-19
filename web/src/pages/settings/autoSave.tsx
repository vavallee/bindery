// One feedback path for settings that save the moment they change (a button
// click, a select, a toggle). Fields with a Save button keep their inline
// Save, Saved, Error button (SaveButton); everything else used to save
// silently and log failures to the console, so a person had to reload the page
// to learn whether a change had stuck (#3991).
//
// useAutoSave() wraps the request, then shows a toast: "<Field> saved" on
// success, "Couldn't save <Field>" on failure. The field name comes from the
// same label the Settings search indexes, so the two cannot drift. Toasts
// render in <SettingsToastHost />, mounted once by the Settings page; without
// a host (a tab rendered on its own) the save still happens, only the toast
// is absent.
//
// The host is a stack, not one slot, so a failure is never overwritten by a
// later success. It stays on screen at least 8 seconds, pauses its timer while
// the pointer or keyboard focus is on it (WCAG 2.2.1), and has a Dismiss
// button. The polite live region is always in the page, so text inserted into
// it is announced.

import { useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { listeners, type Listener, type ToastMessage } from './toastBus'

const SUCCESS_MS = 4000
const ERROR_MS = 8000

function Toast({ msg, onDismiss }: { msg: ToastMessage; onDismiss: (id: number) => void }) {
  const { t } = useTranslation()
  const [paused, setPaused] = useState(false)
  const error = msg.kind === 'error'

  useEffect(() => {
    if (paused) return
    const timer = setTimeout(() => onDismiss(msg.id), error ? ERROR_MS : SUCCESS_MS)
    return () => clearTimeout(timer)
  }, [paused, error, msg.id, onDismiss])

  return (
    <div
      role={error ? 'alert' : undefined}
      data-testid="settings-toast"
      onMouseEnter={() => setPaused(true)}
      onMouseLeave={() => setPaused(false)}
      onFocus={() => setPaused(true)}
      onBlur={() => setPaused(false)}
      className={`pointer-events-auto flex items-center gap-3 px-4 py-2.5 text-white rounded-lg shadow-lg text-sm font-medium animate-fade-in ${error ? 'bg-red-600' : 'bg-emerald-700'}`}
    >
      <span>{msg.text}</span>
      <button
        type="button"
        onClick={() => onDismiss(msg.id)}
        aria-label={t('common.dismiss', 'Dismiss')}
        className="shrink-0 rounded px-1 leading-none text-white/90 hover:text-white focus-visible:outline-2 focus-visible:outline-white"
      >
        <span aria-hidden="true">×</span>
      </button>
    </div>
  )
}

export function SettingsToastHost() {
  const [toasts, setToasts] = useState<ToastMessage[]>([])

  useEffect(() => {
    const l: Listener = m => setToasts(prev => [...prev.slice(-4), m])
    listeners.add(l)
    return () => { listeners.delete(l) }
  }, [])

  const dismiss = useCallback((id: number) => setToasts(prev => prev.filter(m => m.id !== id)), [])

  return (
    <div
      role="status"
      aria-live="polite"
      className="fixed bottom-safe-6 right-safe-6 z-50 flex flex-col items-end gap-2 pointer-events-none"
    >
      {toasts.map(m => <Toast key={m.id} msg={m} onDismiss={dismiss} />)}
    </div>
  )
}
