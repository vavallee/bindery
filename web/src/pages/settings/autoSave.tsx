// One feedback path for settings that save the moment they change (a button
// click, a select, a toggle). Fields with a Save button keep their inline
// Save, Saved, Error button (SaveButton); everything else used to save
// silently and log failures to the console, so a person had to reload the page
// to learn whether a change had stuck (#3991).
//
// useAutoSave() wraps the request, then shows a toast: "<Field> saved" on
// success, "Couldn't save <Field>" on failure. The field name comes from the
// same label the Settings search indexes, so the two cannot drift. The toast
// renders in <SettingsToastHost />, mounted once by the Settings page; without
// a host (a tab rendered on its own) the save still happens, only the toast
// is absent.

import { useEffect, useState } from 'react'
import { listeners, type Listener, type ToastMessage } from './toastBus'

export function SettingsToastHost() {
  const [msg, setMsg] = useState<ToastMessage | null>(null)

  useEffect(() => {
    const l: Listener = m => setMsg(m)
    listeners.add(l)
    return () => { listeners.delete(l) }
  }, [])

  useEffect(() => {
    if (!msg) return
    const timer = setTimeout(() => setMsg(null), msg.kind === 'error' ? 5000 : 2500)
    return () => clearTimeout(timer)
  }, [msg])

  if (!msg) return null
  const error = msg.kind === 'error'
  return (
    <div
      key={msg.id}
      role={error ? 'alert' : 'status'}
      data-testid="settings-toast"
      className={`fixed bottom-safe-6 right-safe-6 z-50 px-4 py-2.5 text-white rounded-lg shadow-lg text-sm font-medium animate-fade-in ${error ? 'bg-red-600' : 'bg-emerald-700'}`}
    >
      {msg.text}
    </div>
  )
}
