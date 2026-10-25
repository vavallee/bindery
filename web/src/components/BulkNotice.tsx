import { useTranslation } from 'react-i18next'

/**
 * An inline banner for something a bulk action needs to tell the user that is
 * not an exception: today, "nothing was searched because automatic grabbing is
 * off" (#2669).
 *
 * The Books and Authors pages had no place at all to say such a thing. Their
 * only failure affordance was `alert()` in a catch block, which never fired
 * here because the server answers 200 with a per id reason. A banner rather
 * than an alert so it does not block the page, and `role="alert"` so a screen
 * reader announces it without the user having to go looking.
 *
 * It stays until dismissed or until the next bulk action replaces it. Nothing
 * auto hides it, because the message asks the user to go and change a setting.
 */
export default function BulkNotice({ message, onDismiss }: { message: string | null; onDismiss: () => void }) {
  const { t } = useTranslation()
  if (!message) return null
  return (
    <div
      role="alert"
      className="mb-3 flex items-start gap-3 px-3 py-2 rounded-md text-sm bg-amber-50 dark:bg-amber-950/40 border border-amber-300 dark:border-amber-700/60 text-amber-900 dark:text-amber-200"
    >
      <span className="flex-1">{message}</span>
      <button
        type="button"
        onClick={onDismiss}
        aria-label={t('common.dismiss', 'Dismiss')}
        className="shrink-0 px-1 leading-none text-amber-700 dark:text-amber-300 hover:text-amber-900 dark:hover:text-amber-100 cursor-pointer"
      >
        ×
      </button>
    </div>
  )
}
