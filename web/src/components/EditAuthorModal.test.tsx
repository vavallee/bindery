import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import EditAuthorModal from './EditAuthorModal'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (_key: string, fallback?: string) => fallback ?? _key,
  }),
}))

vi.mock('../api/client', () => ({
  api: {
    listQualityProfiles: vi.fn(),
    listMetadataProfiles: vi.fn(),
    listRootFolders: vi.fn(),
    updateAuthor: vi.fn(),
  },
}))

import { api } from '../api/client'
import type { Author, MetadataProfile, QualityProfile, RootFolder } from '../api/client'

const baseAuthor: Author = {
  id: 42,
  foreignAuthorId: 'OL123A',
  authorName: 'Brandon Sanderson',
  sortName: 'Sanderson, Brandon',
  description: '',
  imageUrl: '',
  disambiguation: '',
  ratingsCount: 0,
  averageRating: 0,
  monitored: true,
  qualityProfileId: 1,
  metadataProfileId: 10,
  rootFolderId: 100,
}

const qualityProfiles: QualityProfile[] = [
  { id: 1, name: 'Any', upgradeAllowed: true, cutoff: 'EPUB', items: [] },
  { id: 2, name: 'EPUB Only', upgradeAllowed: false, cutoff: 'EPUB', items: [] },
]

const metadataProfiles: MetadataProfile[] = [
  { id: 10, name: 'Standard', minPopularity: 0, minPages: 0, skipMissingDate: false, skipMissingIsbn: false, skipPartBooks: false, allowedLanguages: 'eng', unknownLanguageBehavior: 'pass' },
  { id: 11, name: 'English Only', minPopularity: 0, minPages: 0, skipMissingDate: false, skipMissingIsbn: false, skipPartBooks: false, allowedLanguages: 'eng', unknownLanguageBehavior: 'fail' },
]

const rootFolders: RootFolder[] = [
  { id: 100, path: '/library/ebooks', freeSpace: 0, createdAt: '' },
  { id: 101, path: '/library/audiobooks', freeSpace: 0, createdAt: '' },
]

describe('EditAuthorModal', () => {
  const onClose = vi.fn()
  const onSaved = vi.fn()

  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(api.listQualityProfiles).mockResolvedValue(qualityProfiles)
    vi.mocked(api.listMetadataProfiles).mockResolvedValue(metadataProfiles)
    vi.mocked(api.listRootFolders).mockResolvedValue(rootFolders)
    vi.mocked(api.updateAuthor).mockImplementation(async (_id, patch) => ({
      ...baseAuthor,
      ...patch,
    }))
  })

  async function getSelects() {
    // Wait for profiles to load — once they do all three selects render.
    await screen.findByRole('option', { name: 'Any' })
    const selects = screen.getAllByRole('combobox') as HTMLSelectElement[]
    expect(selects).toHaveLength(3)
    const [quality, metadata, root] = selects
    return { quality, metadata, root }
  }

  it('opens with the current author values prefilled', async () => {
    render(<EditAuthorModal author={baseAuthor} onClose={onClose} onSaved={onSaved} />)

    const { quality, metadata, root } = await getSelects()
    expect(quality.value).toBe('1')
    expect(metadata.value).toBe('10')
    expect(root.value).toBe('100')
  })

  it('only sends the fields that actually changed', async () => {
    render(<EditAuthorModal author={baseAuthor} onClose={onClose} onSaved={onSaved} />)

    const { quality } = await getSelects()
    fireEvent.change(quality, { target: { value: '2' } })

    fireEvent.click(screen.getByRole('button', { name: /^save$/i }))

    await waitFor(() => expect(api.updateAuthor).toHaveBeenCalledTimes(1))
    expect(api.updateAuthor).toHaveBeenCalledWith(42, { qualityProfileId: 2 })
    // Unchanged fields must not be in the patch.
    const callArg = vi.mocked(api.updateAuthor).mock.calls[0][1] as Record<string, unknown>
    expect('metadataProfileId' in callArg).toBe(false)
    expect('rootFolderId' in callArg).toBe(false)
  })

  it('passes the updated author to onSaved after a successful save', async () => {
    render(<EditAuthorModal author={baseAuthor} onClose={onClose} onSaved={onSaved} />)

    const { root } = await getSelects()
    fireEvent.change(root, { target: { value: '101' } })

    fireEvent.click(screen.getByRole('button', { name: /^save$/i }))

    await waitFor(() => expect(onSaved).toHaveBeenCalledTimes(1))
    const passed = onSaved.mock.calls[0][0] as Author
    expect(passed.rootFolderId).toBe(101)
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('closes on cancel without calling updateAuthor', async () => {
    render(<EditAuthorModal author={baseAuthor} onClose={onClose} onSaved={onSaved} />)

    await getSelects()
    fireEvent.click(screen.getByRole('button', { name: /^cancel$/i }))

    expect(onClose).toHaveBeenCalledTimes(1)
    expect(api.updateAuthor).not.toHaveBeenCalled()
    expect(onSaved).not.toHaveBeenCalled()
  })

  it('skips the API call when nothing changed and just closes', async () => {
    render(<EditAuthorModal author={baseAuthor} onClose={onClose} onSaved={onSaved} />)

    await getSelects()
    fireEvent.click(screen.getByRole('button', { name: /^save$/i }))

    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1))
    expect(api.updateAuthor).not.toHaveBeenCalled()
    expect(onSaved).not.toHaveBeenCalled()
  })

  it('shows an error message when save fails', async () => {
    vi.mocked(api.updateAuthor).mockRejectedValue(new Error('HTTP 500: server error'))

    render(<EditAuthorModal author={baseAuthor} onClose={onClose} onSaved={onSaved} />)

    const { metadata } = await getSelects()
    fireEvent.change(metadata, { target: { value: '11' } })
    fireEvent.click(screen.getByRole('button', { name: /^save$/i }))

    await waitFor(() => expect(screen.getByText(/HTTP 500/i)).toBeInTheDocument())
    expect(onSaved).not.toHaveBeenCalled()
    expect(onClose).not.toHaveBeenCalled()
  })
})
