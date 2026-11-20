// See autoSave.tsx for the toast host and the reasoning (#3991).
import { useCallback, useEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { FIELDS } from './settingsFieldIndex'
import { publish } from './toastBus'

export interface AutoSaveOptions {
  /** Field name for a setting the search index does not list. */
  name?: string
  /** Human readable new value ("External"), so the toast says what was chosen. */
  value?: string
  /** Keep the error flowing to a caller that also shows it inline. */
  rethrow?: boolean
  /**
   * The value the server held when this change was made, and the value being
   * saved. The hook keeps the last value known to be saved per key from these,
   * so a failed save restores that rather than whatever the render that made
   * the call happened to hold.
   */
  previous?: string
  next?: string
  /**
   * Put the control back on the last saved value (undefined: the key was
   * never stored). Called when the save fails and no newer change to the same
   * key has been made since, because the handlers update their state before
   * the request so the control answers at once.
   */
  revert?: (saved: string | undefined) => void
}

interface KeyState {
  /** Sequence number of the latest change issued for the key. */
  seq: number
  /** Changes issued and not yet settled. */
  pending: number
  /** Last value known saved, and the sequence number of the change that set it. */
  saved: string | undefined
  savedSeq: number
}

/** True until the calling component unmounts; guard state writes after an await. */
export function useMounted() {
  const mounted = useRef(true)
  useEffect(() => {
    mounted.current = true
    return () => { mounted.current = false }
  }, [])
  return mounted
}

/**
 * Restores one key of a settings map to what it held before an optimistic
 * update: removed again when it was absent, so a default that reads
 * `settings[key] ?? 'auto'` still applies.
 */
export function restoreSetting(
  set: (fn: (prev: Record<string, string>) => Record<string, string>) => void,
  key: string,
  previous: string | undefined,
) {
  set(s => {
    const next = { ...s }
    if (previous === undefined) delete next[key]
    else next[key] = previous
    return next
  })
}

/**
 * Returns autoSave(key, request, options?). It awaits the request, toasts the
 * outcome and resolves to the request's result, or undefined when it failed
 * (after calling options.revert).
 */
export function useAutoSave() {
  const { t } = useTranslation()
  const mounted = useMounted()
  const keys = useRef(new Map<string, KeyState>())
  return useCallback(async <T,>(key: string, request: () => Promise<T>, options: AutoSaveOptions = {}): Promise<T | undefined> => {
    const { value, rethrow = false, revert } = options
    let st = keys.current.get(key)
    if (!st) { st = { seq: 0, pending: 0, saved: undefined, savedSeq: 0 }; keys.current.set(key, st) }
    // Nothing in flight: what the caller saw is what the server holds.
    if (st.pending === 0) st.saved = options.previous
    st.pending++
    const mySeq = ++st.seq
    const entry = FIELDS.find(f => f.key === key)
    const name = options.name ?? (entry ? t(entry.label) : t('settings.toast.genericName', 'Setting'))
    try {
      const result = await request()
      st.pending--
      if (mySeq > st.savedSeq && options.next !== undefined) { st.saved = options.next; st.savedSeq = mySeq }
      publish('success', value
        ? t('settings.toast.set', '{{name}} set to {{value}}', { name, value })
        : t('settings.toast.saved', '{{name}} saved', { name }))
      return result
    } catch (err) {
      console.error(err)
      st.pending--
      // A newer change to this key owns the control now; undoing this one would
      // write an older value over it. Only the latest change reverts.
      if (mySeq === st.seq && mounted.current) revert?.(st.saved)
      publish('error', t('settings.toast.failed', "Couldn't save {{name}}", { name }))
      if (rethrow) throw err
      return undefined
    }
  }, [t, mounted])
}
