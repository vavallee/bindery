import { request } from './core'

export type RequestKind = 'book' | 'author'
export type RequestStatus = 'pending' | 'approved' | 'declined'

// One request as the API returns it. username, the result ids and the
// created fields are present on the admin queue only.
export interface LibraryRequest {
  id: number
  kind: RequestKind
  foreignId: string
  mediaType: string
  title: string
  authorName: string
  status: RequestStatus
  declineReason?: string
  createdAt: string
  decidedAt?: string
  fulfilled: boolean
  booksTotal?: number
  booksImported?: number
  username?: string
  resultBookId?: number
  resultAuthorId?: number
  // Set when the request was made for its owner with forUser: by the API key,
  // or by the admin named in createdBy.
  createdVia?: 'api_key' | 'admin'
  createdBy?: string
}

export interface LibraryRequestList {
  items: LibraryRequest[]
  total: number
  limit: number
  offset: number
}

// A library book as a requester may see it: a projection with no paths and
// no links into the admin library pages.
export interface RequesterLibraryBook {
  id: number
  title: string
  authorName: string
  series?: string
  seriesPosition?: string
  coverUrl?: string
  status: string
  formats: string[]
}

export interface RequesterLibraryPage {
  items: RequesterLibraryBook[]
  total: number
  limit: number
  offset: number
}

// The admin's choices when approving. The profile, root folder and monitor
// fields apply to author requests; a book request uses searchOnAdd and
// mediaType.
export interface ApproveRequestChoices {
  qualityProfileId?: number | null
  metadataProfileId?: number | null
  rootFolderId?: number | null
  monitorMode?: string
  monitorLatestCount?: number
  monitorNewItems?: string
  searchOnAdd?: boolean
  mediaType?: string
}

function pageQuery(params: Record<string, string | number | undefined>): string {
  const qs = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== '') qs.set(k, String(v))
  }
  const s = qs.toString()
  return s ? `?${s}` : ''
}

export const requestsApi = {
  createRequest: (kind: RequestKind, foreignId: string, mediaType = '') =>
    request<LibraryRequest>('/requests', { method: 'POST', body: JSON.stringify({ kind, foreignId, mediaType }) }),
  listMyRequests: (params: { limit?: number; offset?: number } = {}) =>
    request<LibraryRequestList>(`/requests${pageQuery(params)}`),
  withdrawRequest: (id: number) => request<void>(`/requests/${id}`, { method: 'DELETE' }),
  requesterLibrary: (params: { search?: string; limit?: number; offset?: number } = {}) =>
    request<RequesterLibraryPage>(`/requests/library${pageQuery(params)}`),
  listRequestQueue: (params: { status?: RequestStatus | 'all'; limit?: number; offset?: number } = {}) =>
    request<LibraryRequestList>(`/requests/queue${pageQuery(params)}`),
  pendingRequestCount: () => request<{ count: number }>('/requests/pending-count'),
  approveRequest: (id: number, choices: ApproveRequestChoices) =>
    request<LibraryRequest>(`/requests/${id}/approve`, { method: 'POST', body: JSON.stringify(choices) }),
  declineRequest: (id: number, reason: string) =>
    request<LibraryRequest>(`/requests/${id}/decline`, { method: 'POST', body: JSON.stringify({ reason }) }),
}
