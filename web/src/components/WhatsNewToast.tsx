import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { isReleaseVersion, releaseHref } from '../util/version'

const STORAGE_KEY = 'bindery.lastSeenVersion'

// Closes the loop the update badge opens: badge → user upgrades → this
// confirms the upgrade landed and points at what changed. Without it the
// only feedback for upgrading is the badge disappearing.
//
// Fires once per version, on the first load after the running version
// changes. Deliberately silent on a first-ever load (no stored version):
// a fresh install has nothing to catch up on, and greeting it with
// "updated to X" would be a lie. Release builds only — a sha/dev build
// switching hashes is not an upgrade worth announcing.
export default function WhatsNewToast({ version }: { version: string }) {
  const { t } = useTranslation()
  const [show, setShow] = useState(false)

  useEffect(() => {
    if (!version || !isReleaseVersion(version)) return
    let previous: string | null
    try {
      previous = localStorage.getItem(STORAGE_KEY)
      localStorage.setItem(STORAGE_KEY, version)
    } catch {
      // localStorage unavailable (private mode): skip rather than showing
      // the toast on every load, which we could not suppress.
      return
    }
    if (previous && previous !== version) setShow(true)
  }, [version])

  if (!show) return null

  return (
    <div
      role="status"
      className="fixed bottom-4 right-4 z-50 max-w-sm flex items-start gap-3 px-4 py-3 rounded-lg shadow-lg border border-emerald-300 dark:border-emerald-700/60 bg-emerald-50 dark:bg-emerald-950/70 text-sm text-emerald-900 dark:text-emerald-200"
    >
      <span className="flex-1">
        {t('whatsNew.updated', 'Updated to v{{version}}.', { version })}{' '}
        <a
          href={releaseHref(version)}
          target="_blank"
          rel="noopener noreferrer"
          className="font-medium underline hover:no-underline"
        >
          {t('whatsNew.link', "See what's new")}
        </a>
      </span>
      <button
        onClick={() => setShow(false)}
        aria-label={t('common.dismiss', 'Dismiss')}
        className="px-1 rounded hover:bg-emerald-100 dark:hover:bg-emerald-900/50 transition-colors"
      >
        ✕
      </button>
    </div>
  )
}
