// Field level entries for the Settings search (#3059). One row per static
// setting a tab renders, in tab order. A row names the i18n keys the tab shows
// the label and help text with, so search matches what the user reads in their
// language, and the setting key, so a key quoted in docs or a support thread
// finds its field.
//
// The setting key doubles as the anchor id. The tab puts `id="<key>"` on the
// field's wrapper, which gives a stable deep link such as
// /settings?tab=metadata#metadata.primary_provider. settingsFieldIndex.test.ts
// reads the tab sources and fails when a row has no anchor in its tab, or when
// a tab saves a setting key that has no row here, so a new setting cannot be
// forgotten.
//
// Rows the user creates (indexers, clients, notification targets) are not
// indexed: the tab that holds them is.
import type { SettingsTabId } from './settingsSearchIndex'

export interface FieldEntry {
  /** The stored setting key, which is also the anchor id. */
  key: string
  tab: SettingsTabId
  /** i18n key of the label the tab renders. */
  label: string
  /** i18n key of the help text the tab renders, when it has one. */
  help?: string
  /** The tab only renders this field for admins. */
  adminOnly?: boolean
}

const G = 'settings.general.'
const C = 'settings.calibre.'

export const FIELDS: FieldEntry[] = [
  // General (everything below Appearance and Security is admin only).
  { key: 'import.mode', tab: 'general', adminOnly: true, label: G + 'importMode.label', help: G + 'importMode.hint' },
  { key: 'import.audiobook.mode', tab: 'general', adminOnly: true, label: G + 'audiobookImportMode', help: G + 'audiobookImportModeHint' },
  { key: 'import.audiobook.flatten_multi_disc', tab: 'general', adminOnly: true, label: G + 'flattenMultiDisc', help: G + 'flattenMultiDiscHint' },
  { key: 'import.ebook.content_check', tab: 'general', adminOnly: true, label: G + 'ebookContentCheck', help: G + 'ebookContentCheckHint' },
  { key: 'import.audiobook.content_check', tab: 'general', adminOnly: true, label: G + 'audiobookContentCheck', help: G + 'audiobookContentCheckHint' },
  { key: 'import.drop_folder', tab: 'general', adminOnly: true, label: G + 'dropFolder', help: G + 'dropFolderHint' },
  { key: 'import.audiobook.drop_folder', tab: 'general', adminOnly: true, label: G + 'audiobookDropFolder', help: G + 'audiobookDropFolderHint' },
  { key: 'import.drop_layout', tab: 'general', adminOnly: true, label: G + 'dropLayout' },
  { key: 'import.drop_link_mode', tab: 'general', adminOnly: true, label: G + 'dropLinkMode' },
  { key: 'import.source_folders', tab: 'general', adminOnly: true, label: G + 'importSources', help: G + 'importSourcesHint' },
  { key: 'import.write_opf_sidecar', tab: 'general', adminOnly: true, label: G + 'writeOPFSidecar', help: G + 'writeOPFSidecarHint' },
  { key: 'naming.bookTemplate', tab: 'general', adminOnly: true, label: G + 'bookTemplate' },
  { key: 'naming_template_audiobook', tab: 'general', adminOnly: true, label: G + 'audiobookTemplate', help: G + 'audiobookTemplateHint' },
  { key: 'naming.audiobook_file_template', tab: 'general', adminOnly: true, label: G + 'audiobookFileTemplate', help: G + 'audiobookFileTemplateHint' },
  { key: 'search.preferredLanguage', tab: 'general', adminOnly: true, label: G + 'preferredLanguage', help: G + 'preferredLanguageHint' },
  { key: 'search.rejectWords', tab: 'general', adminOnly: true, label: G + 'rejectWords', help: G + 'rejectWordsHint' },
  { key: 'search.interval', tab: 'general', adminOnly: true, label: G + 'searchIntervalLabel', help: G + 'searchIntervalHint' },
  { key: 'hardcover.sync_interval', tab: 'general', adminOnly: true, label: G + 'hardcoverSyncIntervalLabel', help: G + 'hardcoverSyncIntervalHint' },
  { key: 'library.scan_interval', tab: 'general', adminOnly: true, label: G + 'libraryScanIntervalLabel', help: G + 'libraryScanIntervalHint' },
  { key: 'authors.discovery.interval', tab: 'general', adminOnly: true, label: G + 'discoveryIntervalLabel', help: G + 'discoveryIntervalHint' },
  { key: 'series.monitoring.interval', tab: 'general', adminOnly: true, label: G + 'seriesCheckIntervalLabel', help: G + 'seriesCheckIntervalHint' },

  // Metadata
  { key: 'metadata.primary_provider', tab: 'metadata', label: G + 'metadataProviderLabel', help: G + 'metadataProviderHint' },
  { key: 'default.media_type', tab: 'metadata', label: G + 'defaultMediaTypeLabel', help: G + 'defaultMediaTypeHint' },
  { key: 'default.media_type_strict', tab: 'metadata', label: G + 'strictMediaTypeLabel', help: G + 'strictMediaTypeHint' },
  { key: 'author.default_monitor_mode', tab: 'metadata', label: G + 'defaultMonitorModeLabel', help: G + 'defaultMonitorModeHint' },
  { key: 'author.default_monitor_latest_count', tab: 'metadata', label: G + 'defaultMonitorLatestCountLabel' },
  { key: 'autoGrab.enabled', tab: 'metadata', label: G + 'autoGrabLabel', help: G + 'autoGrabHint' },
  { key: 'recommendations.enabled', tab: 'metadata', label: G + 'recommendationsLabel', help: G + 'recommendationsHint' },
  { key: 'metadata.author_enrichment.wikidata', tab: 'metadata', label: G + 'authorWikidataLabel', help: G + 'authorWikidataHint' },
  { key: 'metadata.enrichers_disabled', tab: 'metadata', label: G + 'enrichersHeading' },

  // Root folders
  { key: 'library.defaultRootFolderId', tab: 'rootfolders', label: 'settings.rootfolders.defaultLabel', help: 'settings.rootfolders.defaultHint' },
  { key: 'library.defaultAudiobookRootFolderId', tab: 'rootfolders', label: 'settings.rootfolders.audiobookDefaultLabel', help: 'settings.rootfolders.audiobookDefaultHint' },

  // Calibre
  { key: 'calibre.library_path', tab: 'calibre', label: C + 'fields.libraryPath', help: C + 'fields.libraryPathHint' },
  { key: 'calibre.mode', tab: 'calibre', label: C + 'mode.heading' },
  { key: 'calibre.binary_path', tab: 'calibre', label: C + 'fields.binaryPath', help: C + 'fields.binaryPathHint' },
  { key: 'calibre.plugin_transport', tab: 'calibre', label: C + 'transport.label' },
  { key: 'calibre.plugin_url', tab: 'calibre', label: C + 'fields.pluginUrl', help: C + 'fields.pluginUrlHint' },
  { key: 'calibre.plugin_api_key', tab: 'calibre', label: C + 'fields.apiKey', help: C + 'fields.apiKeyHint' },
  { key: 'calibre.push_path_remap', tab: 'calibre', label: C + 'fields.pushRemap', help: C + 'fields.pushRemapHint' },
  { key: 'calibre.library_import_enabled', tab: 'calibre', label: C + 'import.heading', help: C + 'import.hint' },
  { key: 'calibre.sync_on_startup', tab: 'calibre', label: C + 'import.syncOnStartup', help: C + 'import.syncOnStartupHint' },
  { key: 'cwa.ingest_path', tab: 'calibre', label: C + 'cwa.ingestPath', help: C + 'cwa.ingestPathHint' },

  // API keys
  { key: 'googlebooks.apiKey', tab: 'api-keys', label: G + 'googleBooksKey' },
  { key: 'hardcover.api_token', tab: 'api-keys', label: G + 'hardcoverApiToken' },
  { key: 'hardcover.enhanced_series_enabled', tab: 'api-keys', label: G + 'enhancedHardcoverSeries', help: G + 'enhancedHardcoverSeriesHint' },

  // Logs
  { key: 'log.retention_days', tab: 'logs', label: G + 'logRetentionLabel', help: G + 'logRetentionHint' },
  { key: 'telemetry.enabled', tab: 'logs', label: G + 'telemetryLabel', help: G + 'telemetryHint' },
]

/** The field a URL hash names, if it is one. */
export function fieldForAnchor(anchor: string): FieldEntry | undefined {
  return FIELDS.find(f => f.key === anchor)
}
