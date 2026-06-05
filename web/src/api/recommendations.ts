import { request } from './core'

export interface Recommendation {
  id: number
  userId: number
  foreignId: string
  recType: string
  title: string
  authorName: string
  authorId?: number
  imageUrl: string
  description: string
  genres: string[]
  rating: number
  ratingsCount: number
  releaseDate?: string
  language: string
  mediaType: string
  score: number
  reason: string
  seriesId?: number
  seriesPos: string
  dismissed: boolean
  batchId: string
  createdAt: string
}

export const recommendationsApi = {
  // Recommendations
  listRecommendations: (params?: { type?: string; limit?: number; offset?: number }) => {
    const q = new URLSearchParams()
    if (params?.type) q.set('type', params.type)
    if (params?.limit) q.set('limit', String(params.limit))
    if (params?.offset) q.set('offset', String(params.offset))
    const qs = q.toString()
    return request<Recommendation[]>(`/recommendations${qs ? '?' + qs : ''}`)
  },
  dismissRecommendation: (id: number) => request<void>(`/recommendations/${id}/dismiss`, { method: 'POST' }),
  addRecommendation: (id: number) => request<void>(`/recommendations/${id}/add`, { method: 'POST' }),
  refreshRecommendations: () => request<void>('/recommendations/refresh', { method: 'POST' }),
  clearRecommendationDismissals: () => request<void>('/recommendations/dismissals', { method: 'DELETE' }),
  listAuthorExclusions: () => request<string[]>('/recommendations/exclude-author'),
  addAuthorExclusion: (authorName: string) => request<void>('/recommendations/exclude-author', { method: 'POST', body: JSON.stringify({ authorName }) }),
  removeAuthorExclusion: (authorName: string) => request<void>(`/recommendations/exclude-author/${encodeURIComponent(authorName)}`, { method: 'DELETE' }),
}
