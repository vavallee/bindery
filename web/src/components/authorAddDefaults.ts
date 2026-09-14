import { api } from '../api/client'
import type { AuthorMonitorMode, MediaType, MetadataProfile, RootFolder } from '../api/client'

// What the author confirm step in AddToLibraryModal needs before an add can
// be posted. Kept out of AddAuthorConfirm.tsx so that file exports only its
// component, which is what React fast refresh requires.

export const DEFAULT_MONITOR_MODE: AuthorMonitorMode = 'all'
export const DEFAULT_MONITOR_LATEST_COUNT = 1

function isAuthorMonitorMode(value: string): value is AuthorMonitorMode {
  return value === 'all' || value === 'future' || value === 'latest' || value === 'none'
}

export interface AuthorAddDefaults {
  profiles: MetadataProfile[]
  rootFolders: RootFolder[]
  // The root folder to preselect, or null when there are none.
  rootFolderId: number | null
  mediaType: MediaType
  monitorMode: AuthorMonitorMode
  monitorLatestCount: number
}

// loadAuthorAddDefaults gathers what the author confirm step needs before an
// add can be posted. Every lookup degrades to its default on failure, so the
// step is usable on an install with nothing configured.
export async function loadAuthorAddDefaults(): Promise<AuthorAddDefaults> {
  const [profiles, rootFolders, defaultRootSetting, mediaTypeSetting, monitorModeSetting, latestCountSetting] = await Promise.all([
    api.listMetadataProfiles().catch((err: unknown) => { console.error(err); return [] as MetadataProfile[] }),
    api.listRootFolders().catch((err: unknown) => { console.error(err); return [] as RootFolder[] }),
    api.getSetting('library.defaultRootFolderId').catch(() => null),
    api.getSetting('default.media_type').catch(() => null),
    api.getSetting('author.default_monitor_mode').catch(() => null),
    api.getSetting('author.default_monitor_latest_count').catch(() => null),
  ])
  // Seed the root-folder picker from the install default rather than from
  // list position (#2166). The value here is posted as an explicit
  // per-author rootFolderId, and the scanner resolves an author's own
  // root_folder_id ahead of library.defaultRootFolderId, so seeding from
  // rfs[0] did not merely preselect the wrong entry, it made the setting
  // unreachable for every author added through this dialog. Invisible until
  // a second root folder exists, which is how it reached #2165: adding
  // /downloads as a root put it first in the list. Fall back to the first
  // folder when the setting is unset, unparseable, or still names a root
  // folder that has since been deleted.
  const defaultRootId = Number(defaultRootSetting?.value)
  const preferredRoot = rootFolders.find(rf => rf.id === defaultRootId)
  const rootFolderId = rootFolders.length === 0 ? null : (preferredRoot ? preferredRoot.id : rootFolders[0].id)
  const mediaValue = mediaTypeSetting?.value
  const mediaType: MediaType = mediaValue === 'ebook' || mediaValue === 'audiobook' || mediaValue === 'both' ? mediaValue : 'ebook'
  const modeValue = monitorModeSetting?.value ?? ''
  const monitorMode = isAuthorMonitorMode(modeValue) ? modeValue : DEFAULT_MONITOR_MODE
  const latest = Number(latestCountSetting?.value)
  const monitorLatestCount = Number.isInteger(latest) && latest > 0 ? latest : DEFAULT_MONITOR_LATEST_COUNT
  return { profiles, rootFolders, rootFolderId, mediaType, monitorMode, monitorLatestCount }
}
