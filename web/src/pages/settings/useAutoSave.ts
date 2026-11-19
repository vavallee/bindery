// See autoSave.tsx for the toast host and the reasoning (#3991).
import { useCallback } from 'react'
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
   * Put the control back on what the server still holds. Called when the save
   * fails, because the handlers update their state before the request so the
   * control answers at once.
   */
  revert?: () => void
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
  return useCallback(async <T,>(key: string, request: () => Promise<T>, options: AutoSaveOptions = {}): Promise<T | undefined> => {
    const { value, rethrow = false, revert } = options
    const entry = FIELDS.find(f => f.key === key)
    const name = options.name ?? (entry ? t(entry.label) : t('settings.toast.genericName', 'Setting'))
    try {
      const result = await request()
      publish('success', value
        ? t('settings.toast.set', '{{name}} set to {{value}}', { name, value })
        : t('settings.toast.saved', '{{name}} saved', { name }))
      return result
    } catch (err) {
      console.error(err)
      revert?.()
      publish('error', t('settings.toast.failed', "Couldn't save {{name}}", { name }))
      if (rethrow) throw err
      return undefined
    }
  }, [t])
}
