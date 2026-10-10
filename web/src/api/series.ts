import { request } from './core'
import type { Book } from './books'
import type { MediaType } from './authors'

export interface SeriesHardcoverLink {
  id: number
  seriesId: number
  hardcoverSeriesId: string
  hardcoverProviderId: string
  // Hardcover's public URL slug. Empty on links stored before migration 080
  // and on series Hardcover reports without one; hardcoverSeriesUrl returns
  // null in that case rather than building a URL that 404s (#1708).
  hardcoverSlug?: string
  hardcoverTitle: string
  hardcoverAuthorName: string
  hardcoverBookCount: number
  confidence: number
  linkedBy: 'auto' | 'manual' | string
  linkedAt: string
  createdAt: string
  updatedAt: string
}

export interface SeriesHardcoverSearchResult {
  foreignId: string
  providerId: string
  slug?: string
  title: string
  authorName: string
  bookCount: number
  readersCount: number
  books: string[]
  confidence?: number
}

export interface SeriesHardcoverAutoResponse {
  linked: boolean
  link?: SeriesHardcoverLink
  candidates: SeriesHardcoverSearchResult[]
  reason?: string
}

export interface SeriesFillBookRequest {
  foreignBookId?: string
  providerId?: string
  position?: string
  mediaType?: MediaType
}

export interface SeriesHardcoverDiffBook {
  foreignBookId: string
  providerId: string
  title: string
  subtitle?: string
  position: string
  imageUrl?: string
  authorName?: string
  releaseDate?: string
  usersCount?: number
  localBookId?: number
  localTitle?: string
  localStatus?: string
  matchConfidence?: number
}

export interface SeriesHardcoverDiff {
  seriesId: number
  link: SeriesHardcoverLink
  present: SeriesHardcoverDiffBook[]
  missing: SeriesHardcoverDiffBook[]
  localOnly: SeriesHardcoverDiffBook[]
  uncertain: SeriesHardcoverDiffBook[]
  // covered is a split edition of a book already in Present (#2524) — e.g. a
  // "Part 1"/"Part 2" catalogue row for a novel you already own whole. Its
  // localBookId/localTitle point at that whole work, not at a row of its own.
  // Never counted in missingCount, never offered an add button.
  covered: SeriesHardcoverDiffBook[]
  presentCount: number
  missingCount: number
}

export interface Series {
  id: number
  foreignSeriesId: string
  title: string
  description: string
  monitored: boolean
  // Genre override (#1709). genreOverrideSet distinguishes "no override" from
  // "override set to no genres" — the latter locks future books to an empty
  // genre list, so genreOverride being absent is not enough to tell them apart.
  genreOverride?: string[]
  genreOverrideSet?: boolean
  books?: Array<{
    seriesId: number
    bookId: number
    positionInSeries: string
    primarySeries?: boolean
    book?: Book
  }>
  hardcoverLink?: SeriesHardcoverLink
  // Books in this series that are split edition parts of a whole the library
  // already has (#3048). Fill and the wanted search skip them.
  splitEditionPartBookIds?: number[]
}

// What POST /series/{id}/fill reports. The skipped counts are absent on a
// single book add, which is never screened.
export interface SeriesFillResult {
  queued: number
  skippedByProfile?: number
  skippedSplitParts?: number
}

// What a series merge does (#2554): POST /series/{id}/merge returns it, as a
// preview with dryRun or as what was applied.
export interface SeriesMergeBook {
  bookId: number
  title: string
  position: string
  primary: boolean
}

export interface SeriesMergeSource {
  id: number
  title: string
  foreignSeriesId: string
  moved: SeriesMergeBook[]
  kept: SeriesMergeBook[]
  conflicts: Array<{ bookId: number; title: string; targetPosition: string; sourcePosition: string }>
}

export interface SeriesMergePlan {
  targetId: number
  title: string
  aliases: string[]
  sources: SeriesMergeSource[]
  hardcoverLinkFrom: number
  genreOverrideFrom: number
  monitored: boolean
}

export const seriesApi = {
  // Series
  listSeries: () => request<Series[]>('/series'),
  createSeries: (data: { title: string }) => request<Series>('/series', { method: 'POST', body: JSON.stringify(data) }),
  getSeries: (id: number) => request<Series>(`/series/${id}`),
  updateSeries: (id: number, data: { title: string }) => request<Series>(`/series/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
  monitorSeries: (id: number, monitored: boolean) => request<{ monitored: boolean }>(`/series/${id}`, { method: 'PATCH', body: JSON.stringify({ monitored }) }),
  deleteSeries: (id: number) => request<void>(`/series/${id}`, { method: 'DELETE' }),
  mergeSeries: (id: number, data: { sourceIds: number[]; title?: string; dryRun?: boolean }) =>
    request<SeriesMergePlan>(`/series/${id}/merge`, { method: 'POST', body: JSON.stringify(data) }),
  linkBookToSeries: (id: number, data: { bookId: number; positionInSeries: string; primarySeries: boolean }) =>
    request<Series>(`/series/${id}/books`, { method: 'POST', body: JSON.stringify(data) }),
  // #2525: a book can sit in several series, and only one of them names its
  // files. These two are the only way to say which, and to leave a series the
  // book should never have been filed under.
  removeBookFromSeries: (id: number, bookId: number) =>
    request<void>(`/series/${id}/books/${bookId}`, { method: 'DELETE' }),
  setPrimarySeriesForBook: (id: number, bookId: number) =>
    request<Series>(`/series/${id}/books/${bookId}/primary`, { method: 'PUT' }),
  fillSeries: (id: number, book?: SeriesFillBookRequest) =>
    request<SeriesFillResult>(`/series/${id}/fill`, {
      method: 'POST',
      ...(book ? { body: JSON.stringify(book) } : {}),
    }),
  // Fill every missing book at once, optionally targeting a media type. Unlike
  // fillSeries(book), this carries no book selector — the backend expands the
  // whole Hardcover catalog — so the media type travels in its own body.
  fillSeriesAll: (id: number, mediaType?: MediaType) =>
    request<SeriesFillResult>(`/series/${id}/fill`, {
      method: 'POST',
      ...(mediaType ? { body: JSON.stringify({ mediaType }) } : {}),
    }),
  // Unmonitor the split edition parts of books already in the series (#3048).
  // Nothing is deleted.
  unmonitorSeriesSplitParts: (id: number) =>
    request<{ unmonitored: number }>(`/series/${id}/split-parts/unmonitor`, { method: 'POST' }),
  // Genre override (#1446, #1709): set + lock the genre list on every current
  // and subsequently added book in the series.
  applySeriesGenres: (id: number, genres: string[]) =>
    request<{ updated: number }>(`/series/${id}/genres`, { method: 'PUT', body: JSON.stringify({ genres }) }),
  // Drop the override so later books take their genres from metadata again.
  // Genres already written onto existing books stay put and stay locked.
  clearSeriesGenres: (id: number) =>
    request<{ cleared: boolean }>(`/series/${id}/genres`, { method: 'DELETE' }),
  searchHardcoverSeries: (term: string, limit = 10) =>
    request<SeriesHardcoverSearchResult[]>(`/series/hardcover/search?term=${encodeURIComponent(term)}&limit=${limit}`),
  getSeriesHardcoverLink: (id: number) => request<SeriesHardcoverLink>(`/series/${id}/hardcover-link`),
  autoLinkSeriesHardcover: (id: number) =>
    request<SeriesHardcoverAutoResponse>(`/series/${id}/hardcover-link/auto`, { method: 'POST' }),
  linkSeriesHardcover: (id: number, result: SeriesHardcoverSearchResult) =>
    request<SeriesHardcoverLink>(`/series/${id}/hardcover-link`, {
      method: 'PUT',
      body: JSON.stringify(result),
    }),
  unlinkSeriesHardcover: (id: number) => request<{ success: boolean }>(`/series/${id}/hardcover-link`, { method: 'DELETE' }),
  getSeriesHardcoverDiff: (id: number) => request<SeriesHardcoverDiff>(`/series/${id}/hardcover-diff`),
}
