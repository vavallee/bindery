import { request } from './core'

export interface AudiobookScoring {
  // Preferred density per parsed codec token (m4b, m4a, mp3, flac, ogg), in
  // MiB/min. A codec left out gets no size-per-minute adjustment.
  codecTargets?: Record<string, number>
  // Band around the target that counts as a match. Omitted means 0.2.
  toleranceMiBPerMinute?: number
  // Score charged per MiB/min outside the band. 0 or omitted disables the
  // normalised term and keeps the flat size bonus.
  sizePerMinuteWeight?: number
  // Scales the log10(grabs+1) popularity term. Omitted keeps 10; 0 disables it.
  grabsWeight?: number
}

export interface QualityProfile {
  id: number
  name: string
  upgradeAllowed: boolean
  cutoff: string
  items: Array<{ quality: string; allowed: boolean }>
  audiobookScoring?: AudiobookScoring
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
