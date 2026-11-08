import { useState } from 'react'
import { Link } from 'react-router'
import { useTranslation } from 'react-i18next'
import type { TFunction } from 'i18next'
import Alert from './Alert'
import { useNeedsSetup } from './useNeedsSetup'
import { useLibraryFolderReady } from './useLibraryFolderReady'

// dismissKey encodes WHICH incomplete state was dismissed, so dismissing
// "no indexer and no client" doesn't also swallow a later, different
// warning (e.g. the user adds an indexer, and the sharper "downloads will
// fail" state should get one fresh chance to show).
const STORAGE_KEY = 'bindery.setupBannerDismissed'

function signature(needsRoot: boolean, needsIndexer: boolean, needsClient: boolean): string {
  const base = `${needsIndexer ? 'indexer' : ''}+${needsClient ? 'client' : ''}`
  // Prefixed only when the root folder is missing, so a dismissal stored
  // before the root folder step existed still matches its state.
  return needsRoot ? `root+${base}` : base
}

// The message for a missing library folder, alone or with the other gaps.
// Only called when the root folder is missing.
function rootFolderMessage(t: TFunction, needsIndexer: boolean, needsClient: boolean): string {
  if (needsIndexer && needsClient) {
    return t('setupBanner.all', 'Finish setting up: add a root folder, an indexer and a download client so searches, downloads and imports work.')
  }
  if (needsIndexer) {
    return t('setupBanner.rootFolderIndexer', 'Finish setting up: add a root folder and an indexer so searches and imports work.')
  }
  if (needsClient) {
    return t('setupBanner.rootFolderClient', 'Finish setting up: add a root folder and a download client so downloads and imports work.')
  }
  return t('setupBanner.rootFolder', 'Finish setting up: add a root folder so imported books have a library folder to go to.')
}

// App-shell banner shown while the search→download pipeline is incomplete,
// or there is no library folder for imports (no root folder and no usable
// BINDERY_LIBRARY_DIR). The gaps are named in setup order: root folder,
// indexer, download client, and the action links the first one.
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
  // Mounted for admins only (App.tsx), who are the ones who can add one.
  // Unknown (null) counts as fine until answered, so nothing flashes.
  const needsRoot = useLibraryFolderReady(true) === false
  const [dismissed, setDismissed] = useState(() => {
    try {
      return localStorage.getItem(STORAGE_KEY)
    } catch {
      return null
    }
  })

  const sig = signature(needsRoot, needsIndexer, needsClient)
  if (!(needsAny || needsRoot) || dismissed === sig) return null

  const message = needsRoot
    ? rootFolderMessage(t, needsIndexer, needsClient)
    : needsIndexer && needsClient
    ? t('setupBanner.both', 'Finish setting up: add an indexer and a download client so searches and downloads work.')
    : needsClient
      ? t('gettingStarted.clientMissing', 'An indexer is configured but there is no download client — searches will find releases, but every grab will fail.')
      : t('gettingStarted.indexerMissing', 'A download client is configured but there is no indexer — searches have nowhere to look and will find nothing.')

  const target = needsRoot
    ? '/settings?tab=rootfolders'
    : needsIndexer ? '/settings?tab=indexers' : '/settings?tab=clients'
  const action = needsRoot
    ? t('gettingStarted.rootFolders', 'Set up Root Folders')
    : needsIndexer
      ? t('gettingStarted.indexers', 'Set up Indexers')
      : t('gettingStarted.downloadClients', 'Set up Download Clients')

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
            {action}
          </Link>
        }
        onDismiss={() => {
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
