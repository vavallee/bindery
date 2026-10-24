// The small index behind the Settings search box (#3059): tab names, the
// section headings the tabs render, and, for admins, registered setting keys.
//
// Deliberately NOT here: per field labels and help text. That version needs an
// anchor on every field and a test that fails when a new setting forgets one,
// which is a much larger change. This slice only gets someone to the right tab
// (and to the right section heading on it).
//
// Each heading entry names the i18n key the tab renders it with, so the search
// matches the text the user actually sees in their language. A few headings are
// plain English literals in their tab today; those are indexed as literals,
// which is exactly what the page shows.

export type SettingsTabId =
  | 'indexers' | 'clients' | 'notifications' | 'quality' | 'metadata' | 'general'
  | 'import' | 'rootfolders' | 'logs' | 'blocklist' | 'calibre' | 'abs' | 'grimmory'
  | 'api-keys' | 'advanced' | 'about'

export interface HeadingEntry {
  tab: SettingsTabId
  /** i18n key the tab renders the heading with. */
  key?: string
  /** English default passed to t(), matching the tab's own call. */
  fallback?: string
  /** A heading the tab renders as a literal, not through t(). */
  literal?: string
  /** The tab only renders this section for admins, so a non admin must not be offered it. */
  adminOnly?: boolean
}

export const HEADINGS: HeadingEntry[] = [
  // General: Appearance and Security are visible to everyone. GeneralTab wraps
  // everything from file naming down in an isAdmin block, so those are
  // adminOnly: offering them to a non admin would be a dead end.
  { tab: 'general', key: 'settings.general.appearance' },
  { tab: 'general', key: 'settings.general.fileNaming', adminOnly: true },
  { tab: 'general', key: 'settings.general.downloads', adminOnly: true },
  { tab: 'general', key: 'settings.general.storage', adminOnly: true },
  { tab: 'general', key: 'settings.general.dbCheck', adminOnly: true },
  { tab: 'general', key: 'settings.general.library', adminOnly: true },
  { tab: 'general', key: 'settings.general.search', adminOnly: true },
  { tab: 'general', key: 'settings.general.hardcoverSync', fallback: 'Hardcover list sync', adminOnly: true },
  { tab: 'general', key: 'settings.general.discovery', adminOnly: true },
  { tab: 'general', key: 'settings.general.defaultLibraryLocation', adminOnly: true },
  { tab: 'general', key: 'settings.general.security' },
  { tab: 'indexers', key: 'settings.indexers.heading' },
  { tab: 'indexers', key: 'settings.prowlarr.heading' },
  { tab: 'clients', key: 'settings.clients.heading' },
  { tab: 'notifications', key: 'settings.notifications.heading' },
  { tab: 'quality', key: 'settings.quality.heading' },
  { tab: 'metadata', key: 'settings.metadata.heading' },
  { tab: 'metadata', key: 'settings.metadata.libraryDefaults' },
  { tab: 'metadata', key: 'settings.general.enrichersHeading', fallback: 'Enrichers' },
  { tab: 'metadata', key: 'settings.general.audiobookSourcesHeading', fallback: 'Audiobook sources' },
  { tab: 'rootfolders', key: 'settings.rootfolders.heading' },
  { tab: 'import', key: 'settings.import.csvHeading' },
  { tab: 'import', key: 'settings.import.readarrHeading' },
  { tab: 'import', key: 'settings.import.manualImportHeading' },
  { tab: 'import', key: 'settings.import.folderMovedHeading', fallback: 'Import from a folder' },
  { tab: 'import', key: 'settings.import.hardcoverHeading' },
  { tab: 'import', key: 'settings.import.goodreadsHeading' },
  { tab: 'calibre', literal: 'Calibre-Web-Automated (CWA)' },
  { tab: 'abs', key: 'settings.abs.libraries.heading' },
  { tab: 'grimmory', key: 'settings.grimmory.push.heading' },
  { tab: 'api-keys', key: 'settings.general.apiKeys' },
  { tab: 'api-keys', key: 'settings.general.opds' },
  { tab: 'blocklist', key: 'blocklist.title' },
  { tab: 'logs', key: 'settings.logs.heading' },
  { tab: 'logs', key: 'settings.general.logRetention' },
  { tab: 'logs', key: 'settings.general.backup' },
  { tab: 'logs', key: 'settings.general.telemetry' },
  { tab: 'advanced', key: 'settings.advanced.title', fallback: 'Advanced' },
  { tab: 'about', key: 'settings.about.title', fallback: 'About' },
]

export type SearchResult =
  | { kind: 'tab'; id: string; tab: SettingsTabId; tabLabel: string }
  | { kind: 'heading'; id: string; tab: SettingsTabId; tabLabel: string; heading: string }
  | { kind: 'key'; id: string; tab: SettingsTabId; tabLabel: string; settingKey: string }

export const MAX_RESULTS = 8

export interface SearchOutcome {
  /** At most MAX_RESULTS, best first. */
  results: SearchResult[]
  /** How many matched before the cap. */
  total: number
}

export interface SearchInput {
  query: string
  /** The tabs the viewer can open, in display order. */
  tabs: { id: SettingsTabId; label: string }[]
  t: (key: string, fallback?: string) => string
  isAdmin: boolean
  /** Registered setting keys; only passed for admins, who can open Advanced. */
  settingKeys?: string[]
}

function norm(s: string): string {
  return s.normalize('NFKD').replace(/\p{M}/gu, '').toLowerCase().replace(/\s+/g, ' ').trim()
}

// Rank 0 is a prefix match, 1 a word prefix, 2 anywhere else, -1 no match.
function rank(haystack: string, needle: string): number {
  const h = norm(haystack)
  if (h.startsWith(needle)) return 0
  if (h.includes(' ' + needle)) return 1
  return h.includes(needle) ? 2 : -1
}

export function searchSettings({ query, tabs, t, isAdmin, settingKeys = [] }: SearchInput): SearchOutcome {
  const needle = norm(query)
  if (needle === '') return { results: [], total: 0 }
  const tabLabels = new Map(tabs.map(x => [x.id, x.label]))
  const scored: { r: SearchResult; rank: number; order: number }[] = []
  let order = 0

  for (const { id, label } of tabs) {
    const k = rank(label, needle)
    if (k >= 0) scored.push({ r: { kind: 'tab', id: `tab:${id}`, tab: id, tabLabel: label }, rank: k, order: order++ })
  }
  for (const h of HEADINGS) {
    const tabLabel = tabLabels.get(h.tab)
    if (!tabLabel) continue // the viewer cannot open that tab
    if (h.adminOnly && !isAdmin) continue // the tab hides this section from them
    const heading = h.literal ?? t(h.key as string, h.fallback)
    if (norm(heading) === norm(tabLabel)) continue // the tab result already covers it
    const k = rank(heading, needle)
    if (k >= 0) scored.push({ r: { kind: 'heading', id: `h:${h.tab}:${h.key ?? h.literal}`, tab: h.tab, tabLabel, heading }, rank: k, order: order++ })
  }
  const advancedLabel = tabLabels.get('advanced')
  if (advancedLabel) {
    for (const settingKey of settingKeys) {
      const k = rank(settingKey, needle)
      if (k >= 0) scored.push({ r: { kind: 'key', id: `k:${settingKey}`, tab: 'advanced', tabLabel: advancedLabel, settingKey }, rank: k + 3, order: order++ })
    }
  }
  // Tab and heading matches outrank key matches, so a short query still lists
  // the tabs first. Within a rank, keep tab order then index order.
  scored.sort((a, b) => a.rank - b.rank || a.order - b.order)
  return { results: scored.slice(0, MAX_RESULTS).map(s => s.r), total: scored.length }
}
