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
