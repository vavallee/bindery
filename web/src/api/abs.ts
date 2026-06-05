import { request } from './core'
import type { PaginatedResponse } from './common'

export interface ABSConfig {
  featureEnabled: boolean
  baseUrl: string
  label: string
  enabled: boolean
  libraryId: string
  libraryIds?: string[]
  pathRemap: string
  apiKeyConfigured: boolean
}

export interface ABSLibraryFolder {
  id: string
  fullPath: string
}

export interface ABSLibrary {
  id: string
  name: string
  mediaType: string
  icon: string
  provider: string
  folders: ABSLibraryFolder[]
}

export interface ABSTestResult {
  message: string
  username: string
  userType: string
  defaultLibraryId: string
  serverVersion: string
  source: string
}

export interface ABSImportStats {
  librariesScanned: number
  pagesScanned: number
  itemsSeen: number
  itemsNormalized: number
  itemsDetailFetched: number
  authorsCreated: number
  authorsLinked: number
  booksCreated: number
  booksLinked: number
  booksUpdated: number
  seriesCreated: number
  seriesLinked: number
  editionsAdded: number
  ownedMarked: number
  pendingManual: number
  reviewQueued: number
  metadataMatched: number
  metadataRelinked: number
  metadataConflicts: number
  metadataAutoResolved: number
  skipped: number
  failed: number
}

export interface ABSImportItemResult {
  itemId: string
  title: string
  outcome: string
  message?: string
  matchedBy?: string
  authorId?: number
  bookId?: number
  seriesCount?: number
}

export interface ABSImportProgress {
  running: boolean
  runId?: number
  dryRun?: boolean
  startedAt?: string
  finishedAt?: string
  processed: number
  message?: string
  error?: string
  resumedFromCheckpoint?: boolean
  checkpoint?: {
    libraryId: string
    page: number
    lastItemId?: string
    pageSize: number
    updatedAt: string
  }
  stats?: ABSImportStats
  results?: ABSImportItemResult[]
}

export interface ABSImportRunSummary {
  dryRun: boolean
  resumedFromCheckpoint: boolean
  checkpoint?: {
    libraryId: string
    page: number
    lastItemId?: string
    pageSize: number
    updatedAt: string
  }
  stats: ABSImportStats
  error?: string
}

export interface ABSImportRun {
  id: number
  sourceId: string
  sourceLabel: string
  baseUrl: string
  libraryId: string
  status: string
  dryRun: boolean
  startedAt: string
  finishedAt?: string
  source: {
    sourceId: string
    label: string
    baseUrl: string
    libraryId: string
    libraryIds?: string[]
    pathRemap?: string
    enabled: boolean
    dryRun: boolean
  }
  checkpoint?: {
    libraryId: string
    page: number
    lastItemId?: string
    pageSize: number
    updatedAt: string
  }
  summary: ABSImportRunSummary
}

export interface ABSRollbackAction {
  entityType: string
  externalId: string
  displayName?: string
  localId: number
  outcome: string
  action: string
  reason?: string
}

export interface ABSRollbackResult {
  runId: number
  preview: boolean
  dryRun: boolean
  status: string
  stats: {
    actionsPlanned: number
    entitiesDeleted: number
    provenanceUnlinked: number
    skipped: number
    failed: number
  }
  actions: ABSRollbackAction[]
  finishedAt: string
}

export interface ABSReviewItem {
  id: number
  sourceId: string
  libraryId: string
  itemId: string
  title: string
  primaryAuthor: string
  asin: string
  mediaType: string
  reviewReason: 'unmatched_author' | 'ambiguous_author' | 'unmatched_book' | 'ambiguous_book'
  payloadJson: string
  resolvedAuthorForeignId?: string
  resolvedAuthorName?: string
  resolvedBookForeignId?: string
  resolvedBookTitle?: string
  editedTitle?: string
  fileMappingFound: boolean
  fileMappingMessage?: string
  latestRunId?: number | null
  status: 'pending' | 'approved' | 'dismissed'
  createdAt: string
  updatedAt: string
}

export interface ABSMetadataConflict {
  id: number
  sourceId: string
  libraryId: string
  itemId: string
  entityType: string
  localId: number
  entityName: string
  fieldName: string
  fieldLabel: string
  absValue: string
  upstreamValue: string
  appliedSource: 'abs' | 'upstream' | ''
  appliedValue: string
  preferredSource: 'abs' | 'upstream' | ''
  authorRelinkEligible: boolean
  resolutionStatus: 'unresolved' | 'resolved'
  updatedAt: string
}

export const absApi = {
  // Audiobookshelf
  absConfig: () => request<ABSConfig>('/abs/config'),
  absSetConfig: (data: { baseUrl: string; label: string; enabled: boolean; libraryId: string; libraryIds?: string[]; pathRemap: string; apiKey?: string }) =>
    request<ABSConfig>('/abs/config', { method: 'PUT', body: JSON.stringify(data) }),
  absTest: (data?: { baseUrl?: string; apiKey?: string }) =>
    request<ABSTestResult>('/abs/test', { method: 'POST', body: JSON.stringify(data ?? {}) }),
  absLibraries: (data?: { baseUrl?: string; apiKey?: string }) =>
    request<ABSLibrary[]>('/abs/libraries', { method: 'POST', body: JSON.stringify(data ?? {}) }),
  absImportStart: (data?: { dryRun?: boolean }) =>
    request<ABSImportProgress>('/abs/import', { method: 'POST', body: JSON.stringify(data ?? {}) }),
  absImportStatus: () => request<ABSImportProgress>('/abs/import/status'),
  absImportRuns: () => request<ABSImportRun[]>('/abs/import/runs'),
  absImportRollbackPreview: (runId: number) => request<ABSRollbackResult>(`/abs/import/runs/${runId}/rollback/preview`, { method: 'POST' }),
  absImportRollback: (runId: number) => request<ABSRollbackResult>(`/abs/import/runs/${runId}/rollback`, { method: 'POST' }),
  absReviewItems: (params?: { limit?: number; offset?: number }) => {
    const q = new URLSearchParams()
    if (params?.limit) q.set('limit', String(params.limit))
    if (params?.offset) q.set('offset', String(params.offset))
    return request<PaginatedResponse<ABSReviewItem>>(`/abs/review${q.toString() ? `?${q.toString()}` : ''}`)
  },
  approveAbsReviewItem: (id: number) => request<ABSReviewItem>(`/abs/review/${id}/approve`, { method: 'POST' }),
  resolveAbsReviewAuthor: (id: number, data: { foreignAuthorId: string; authorName: string; applyTo?: 'same_author' }) =>
    request<{ updated: number }>(`/abs/review/${id}/resolve-author`, { method: 'POST', body: JSON.stringify(data) }),
  resolveAbsReviewBook: (id: number, data: { foreignBookId: string; title: string; editedTitle?: string }) =>
    request<ABSReviewItem>(`/abs/review/${id}/resolve-book`, { method: 'POST', body: JSON.stringify(data) }),
  dismissAbsReviewItem: (id: number) => request<ABSReviewItem>(`/abs/review/${id}/dismiss`, { method: 'POST' }),
  dismissAbsReviewRun: (runId: number) =>
    request<{ dismissed: number }>(`/abs/review/dismiss-run/${runId}`, { method: 'POST' }),
  absConflicts: (params?: { limit?: number; offset?: number }) => {
    const q = new URLSearchParams()
    if (params?.limit) q.set('limit', String(params.limit))
    if (params?.offset) q.set('offset', String(params.offset))
    return request<PaginatedResponse<ABSMetadataConflict>>(`/abs/conflicts${q.toString() ? `?${q.toString()}` : ''}`)
  },
  resolveAbsConflict: (id: number, source: 'abs' | 'upstream') =>
    request<ABSMetadataConflict>(`/abs/conflicts/${id}/resolve`, { method: 'POST', body: JSON.stringify({ source }) }),
}
