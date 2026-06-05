// Barrel for the Bindery web API client.
//
// This file used to be a ~1,580-line god-module holding the shared fetch core,
// the single `api` object (every endpoint method), and every request/response
// type. It has been split by domain into sibling modules under web/src/api/
// (books.ts, authors.ts, series.ts, queue.ts, settings.ts, …) plus a private
// core.ts (base-url + CSRF + request()/uploadFile() + ApiError).
//
// This module re-assembles the original public surface so the ~70 existing
// importers keep working unchanged: it re-exports every type and constant, and
// composes the single `api` object from each domain's partial slice. The set of
// exported symbols here is identical to before the split.

import { systemApi } from './system'
import { authApi } from './auth'
import { booksApi } from './books'
import { authorsApi } from './authors'
import { bulkApi } from './bulk'
import { indexersApi } from './indexers'
import { downloadClientsApi } from './downloadclients'
import { libraryApi } from './library'
import { queueApi } from './queue'
import { historyApi } from './history'
import { notificationsApi } from './notifications'
import { profilesApi } from './profiles'
import { seriesApi } from './series'
import { settingsApi } from './settings'
import { calibreApi } from './calibre'
import { grimmoryApi } from './grimmory'
import { absApi } from './abs'
import { importListsApi } from './importlists'
import { recommendationsApi } from './recommendations'

// Shared core: public constant, error class, helpers, and CSRF init.
export { ApiError, BINDERY_BASE, isNoDownloadClientError, initCSRF } from './core'

// Re-export every domain module's types so
// `import { Book, Author, ImportList, … } from '../api/client'` still resolves.
// `export type *` deliberately re-exports only the type declarations and NOT
// each module's runtime `*Api` slice (those stay private composition inputs),
// keeping this barrel's public surface identical to the pre-split monolith.
export type * from './common'
export type * from './system'
export type * from './auth'
export type * from './books'
export type * from './authors'
export type * from './bulk'
export type * from './indexers'
export type * from './downloadclients'
export type * from './library'
export type * from './queue'
export type * from './history'
export type * from './notifications'
export type * from './profiles'
export type * from './series'
export type * from './settings'
export type * from './calibre'
export type * from './grimmory'
export type * from './abs'
export type * from './importlists'
export type * from './recommendations'

// The single `api` object, composed from each domain's slice. Key order mirrors
// the original file's section ordering; all method names are unique across
// domains, so the merged object is identical to the previous monolith.
export const api = {
  ...systemApi,
  ...authApi,
  // Metadata search + addBook + Books live in booksApi; Authors in authorsApi.
  ...booksApi,
  ...authorsApi,
  // Wanted + bulk actions.
  ...bulkApi,
  ...indexersApi,
  ...downloadClientsApi,
  ...libraryApi,
  ...queueApi,
  // History + blocklist.
  ...historyApi,
  ...notificationsApi,
  // Quality / metadata / delay profiles, custom formats, root folders.
  ...profilesApi,
  ...seriesApi,
  // Settings + backup.
  ...settingsApi,
  ...calibreApi,
  ...grimmoryApi,
  ...absApi,
  ...importListsApi,
  ...recommendationsApi,
}
