import { request } from './core'

export interface QualityProfile {
  id: number
  name: string
  upgradeAllowed: boolean
  cutoff: string
  items: Array<{ quality: string; allowed: boolean }>
}

export interface MetadataProfile {
  id: number
  name: string
  minPopularity: number
  minPages: number
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

export interface CustomFormat {
  id: number
  name: string
  conditions: Array<{
    type: string
    pattern: string
    negate: boolean
    required: boolean
  }>
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

  // Metadata Profiles
  listMetadataProfiles: () => request<MetadataProfile[]>('/metadataprofile'),
  addMetadataProfile: (data: Partial<MetadataProfile>) => request<MetadataProfile>('/metadataprofile', { method: 'POST', body: JSON.stringify(data) }),
  updateMetadataProfile: (id: number, data: Partial<MetadataProfile>) => request<MetadataProfile>(`/metadataprofile/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
  deleteMetadataProfile: (id: number) => request<void>(`/metadataprofile/${id}`, { method: 'DELETE' }),

  // Delay Profiles
  listDelayProfiles: () => request<DelayProfile[]>('/delayprofile'),
  addDelayProfile: (data: Partial<DelayProfile>) => request<DelayProfile>('/delayprofile', { method: 'POST', body: JSON.stringify(data) }),
  deleteDelayProfile: (id: number) => request<void>(`/delayprofile/${id}`, { method: 'DELETE' }),

  // Custom Formats
  listCustomFormats: () => request<CustomFormat[]>('/customformat'),
  addCustomFormat: (data: Partial<CustomFormat>) => request<CustomFormat>('/customformat', { method: 'POST', body: JSON.stringify(data) }),
  deleteCustomFormat: (id: number) => request<void>(`/customformat/${id}`, { method: 'DELETE' }),

  // Root Folders
  listRootFolders: () => request<RootFolder[]>('/rootfolder'),
  addRootFolder: (path: string) => request<RootFolder>('/rootfolder', { method: 'POST', body: JSON.stringify({ path }) }),
  deleteRootFolder: (id: number) => request<void>(`/rootfolder/${id}`, { method: 'DELETE' }),
}
