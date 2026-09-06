import { useState } from 'react'
import { Link } from 'react-router'
import { useTranslation } from 'react-i18next'
import Alert from './Alert'
import { useNeedsSetup } from './useNeedsSetup'

// dismissKey encodes WHICH incomplete state was dismissed, so dismissing
// "no indexer and no client" doesn't also swallow a later, different
// warning (e.g. the user adds an indexer, and the sharper "downloads will
// fail" state should get one fresh chance to show).
const STORAGE_KEY = 'bindery.setupBannerDismissed'

function signature(needsIndexer: boolean, needsClient: boolean): string {
  return `${needsIndexer ? 'indexer' : ''}+${needsClient ? 'client' : ''}`
}

// App-shell banner shown while the search→download pipeline is incomplete.
// Exists because the GettingStartedGuidance card only renders inside the
// Authors/Books EMPTY states — a user who imports an existing library first
// (the common Readarr-migrant path) has non-empty pages and previously got
// no setup guidance anywhere. Dismissible; the dismissal is remembered per
// missing-state signature in localStorage.
//
// Warning tier rather than info: nothing the user asks for will work until
// this is dealt with. It clears itself the moment useNeedsSetup stops
// reporting a gap, which is what a warning is supposed to do.
export default function SetupBanner() {
  const { t } = useTranslation()
  const { needsIndexer, needsClient, needsAny } = useNeedsSetup()
  const [dismissed, setDismissed] = useState(() => {
    try {
      return localStorage.getItem(STORAGE_KEY)
    } catch {
      return null
    }
  })

  if (!needsAny || dismissed === signature(needsIndexer, needsClient)) return null

  const message = needsIndexer && needsClient
    ? t('setupBanner.both', 'Finish setting up: add an indexer and a download client so searches and downloads work.')
    : needsClient
      ? t('gettingStarted.clientMissing', 'An indexer is configured but there is no download client — searches will find releases, but every grab will fail.')
      : t('gettingStarted.indexerMissing', 'A download client is configured but there is no indexer — searches have nowhere to look and will find nothing.')

  const target = needsIndexer ? '/settings?tab=indexers' : '/settings?tab=clients'

  return (
    <div className="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 pt-4">
      <Alert
        tier="warning"
        className="px-4 py-3"
        actions={
          <Link
            to={target}
            className="px-3 py-1.5 rounded-md bg-amber-600 hover:bg-amber-500 text-white font-medium transition-colors"
          >
            {needsIndexer ? t('gettingStarted.indexers') : t('gettingStarted.downloadClients')}
          </Link>
        }
        onDismiss={() => {
          const sig = signature(needsIndexer, needsClient)
          try {
            localStorage.setItem(STORAGE_KEY, sig)
          } catch {
            // localStorage unavailable (private mode) — dismiss for this render only.
          }
          setDismissed(sig)
        }}
      >
        <span>{message}</span>
      </Alert>
    </div>
  )
}
