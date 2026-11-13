import { request } from './core'

export interface QualityProfile {
  id: number
  name: string
  upgradeAllowed: boolean
  cutoff: string
  // Keep the file an upgrade replaced (false, the default) or delete it once
  // the new one is verified (#1357). Absent on an older server.
  removeReplaced?: boolean
  // Preference order, best first within each media type. The server derives
  // each entry's media type from its token, so ebook and audiobook entries
  // share this one array and only the order inside each kind matters (#2733).
  items: Array<{ quality: string; allowed: boolean }>
  // Audiobook narrator and edition preferences (#1358). Both empty means off.
  preferredNarrators?: string[]
  editionPreference?: '' | 'prefer_unabridged' | 'require_unabridged'
}

export interface MetadataProfile {
  id: number
  name: string
  minPopularity: number
  minPages: number
  minEditionCount: number
  skipMissingDate: boolean
  skipMissingIsbn: boolean
  skipPartBooks: boolean
  allowedLanguages: string
  unknownLanguageBehavior: 'pass' | 'fail'
}

export interface DelayProfile {
  id: number
  usenetDelay: number
  torrentDelay: number
  preferredProtocol: string
  enableUsenet: boolean
  enableTorrent: boolean
  order: number
}

export interface RootFolder {
  id: number
  path: string
  freeSpace: number
  createdAt: string
}

export const profilesApi = {
  // Quality Profiles
  listQualityProfiles: () => request<QualityProfile[]>('/qualityprofile'),
  getQualityProfile: (id: number) => request<QualityProfile>(`/qualityprofile/${id}`),
  addQualityProfile: (data: Partial<QualityProfile>) =>
    request<QualityProfile>('/qualityprofile', { method: 'POST', body: JSON.stringify(data) }),
  updateQualityProfile: (id: number, data: Partial<QualityProfile>) =>
    request<QualityProfile>(`/qualityprofile/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
  deleteQualityProfile: (id: number) =>
    request<void>(`/qualityprofile/${id}`, { method: 'DELETE' }),
  // Authors with no quality profile accept every format at grab and import
  // (#3831). Admin only; both cover the caller's own and unowned authors.
  countAuthorsWithoutQualityProfile: () =>
    request<{ count: number }>('/authors/without-quality-profile'),
  applyQualityProfileToAuthorsWithout: (qualityProfileId: number) =>
    request<{ updated: number }>('/authors/without-quality-profile', { method: 'POST', body: JSON.stringify({ qualityProfileId }) }),

  // Metadata Profiles
  listMetadataProfiles: () => request<MetadataProfile[]>('/metadataprofile'),
  addMetadataProfile: (data: Partial<MetadataProfile>) => request<MetadataProfile>('/metadataprofile', { method: 'POST', body: JSON.stringify(data) }),
  updateMetadataProfile: (id: number, data: Partial<MetadataProfile>) => request<MetadataProfile>(`/metadataprofile/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
  deleteMetadataProfile: (id: number) => request<void>(`/metadataprofile/${id}`, { method: 'DELETE' }),

  // Delay Profiles
  listDelayProfiles: () => request<DelayProfile[]>('/delayprofile'),
  addDelayProfile: (data: Partial<DelayProfile>) => request<DelayProfile>('/delayprofile', { method: 'POST', body: JSON.stringify(data) }),
  deleteDelayProfile: (id: number) => request<void>(`/delayprofile/${id}`, { method: 'DELETE' }),

  // Root Folders
  listRootFolders: () => request<RootFolder[]>('/rootfolder'),
  addRootFolder: (path: string) => request<RootFolder>('/rootfolder', { method: 'POST', body: JSON.stringify({ path }) }),
  deleteRootFolder: (id: number) => request<void>(`/rootfolder/${id}`, { method: 'DELETE' }),
}
