// The channel between useAutoSave (publishes) and SettingsToastHost (shows).
// A plain module level set, so a tab rendered on its own with no host mounted
// just publishes to nobody (#3991).

export interface ToastMessage {
  id: number
  kind: 'success' | 'error'
  text: string
}

export type Listener = (m: ToastMessage) => void
export const listeners = new Set<Listener>()
let nextId = 1

export function publish(kind: ToastMessage['kind'], text: string) {
  const m = { id: nextId++, kind, text }
  listeners.forEach(l => l(m))
}

// Whole class strings, text and fill together, so the contrast test (which
// reads a text colour and a fill from one string) measures what is rendered.
// White on emerald-700 and on red-600 is at least 4.5:1. No animation class:
// the toast appears and goes without motion, so prefers-reduced-motion needs
// no handling here; if a transition is added it must be a motion-safe: one.
export const TOAST_FILL: Record<ToastMessage['kind'], string> = {
  success: 'bg-success-fill text-on-fill',
  error: 'bg-danger-fill text-on-fill',
}
