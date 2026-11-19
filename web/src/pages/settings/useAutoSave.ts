// See autoSave.tsx for the toast host and the reasoning (#3991).
import { useCallback } from 'react'
import { useTranslation } from 'react-i18next'
import { FIELDS } from './settingsFieldIndex'
import { publish } from './toastBus'

/**
 * Returns autoSave(key, request, value?). It awaits the request, toasts the
 * outcome and resolves to the request's result, or undefined when it failed.
 * `rethrow` keeps the error flowing to a caller that also shows it inline.
 * `value` is the human readable new value ("External"), for choices where the
 * toast should say what was chosen.
 */
export function useAutoSave() {
  const { t } = useTranslation()
  return useCallback(async <T,>(key: string, request: () => Promise<T>, value?: string, rethrow = false): Promise<T | undefined> => {
    const entry = FIELDS.find(f => f.key === key)
    const name = entry ? t(entry.label) : t('settings.toast.genericName', 'Setting')
    try {
      const result = await request()
      publish('success', value
        ? t('settings.toast.set', '{{name}} set to {{value}}', { name, value })
        : t('settings.toast.saved', '{{name}} saved', { name }))
      return result
    } catch (err) {
      console.error(err)
      publish('error', t('settings.toast.failed', "Couldn't save {{name}}", { name }))
      if (rethrow) throw err
      return undefined
    }
  }, [t])
}
