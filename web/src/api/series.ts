import { request } from './core'
import type { Book } from './books'

export interface SeriesHardcoverLink {
  id: number
  seriesId: number
  hardcoverSeriesId: string
  hardcoverProviderId: string
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
  presentCount: number
  missingCount: number
}

export interface Series {
  id: number
  foreignSeriesId: string
  title: string
  description: string
  monitored: boolean
  books?: Array<{
    seriesId: number
    bookId: number
    positionInSeries: string
    primarySeries?: boolean
    book?: Book
  }>
  hardcoverLink?: SeriesHardcoverLink
}

export const seriesApi = {
  // Series
  listSeries: () => request<Series[]>('/series'),
  createSeries: (data: { title: string }) => request<Series>('/series', { method: 'POST', body: JSON.stringify(data) }),
  getSeries: (id: number) => request<Series>(`/series/${id}`),
  updateSeries: (id: number, data: { title: string }) => request<Series>(`/series/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
  monitorSeries: (id: number, monitored: boolean) => request<{ monitored: boolean }>(`/series/${id}`, { method: 'PATCH', body: JSON.stringify({ monitored }) }),
  deleteSeries: (id: number) => request<void>(`/series/${id}`, { method: 'DELETE' }),
  linkBookToSeries: (id: number, data: { bookId: number; positionInSeries: string; primarySeries: boolean }) =>
    request<Series>(`/series/${id}/books`, { method: 'POST', body: JSON.stringify(data) }),
  fillSeries: (id: number, book?: SeriesFillBookRequest) =>
    request<{ queued: number }>(`/series/${id}/fill`, {
      method: 'POST',
      ...(book ? { body: JSON.stringify(book) } : {}),
    }),
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
