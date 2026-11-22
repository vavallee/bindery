import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import MetadataTab from './MetadataTab'
import { api, MetadataProfile } from '../../api/client'
import { acceptConfirm } from '../../test-utils'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, fallback?: unknown) => (typeof fallback === 'string' ? fallback : key),
    i18n: { changeLanguage: vi.fn() },
  }),
}))
vi.mock('../../api/client', async importOriginal => {
  const actual = await importOriginal<typeof import('../../api/client')>()
  return {
    ...actual,
    api: {
      ...actual.api,
      listSettings: vi.fn(),
      listMetadataProfiles: vi.fn(),
      deleteMetadataProfile: vi.fn(),
      status: vi.fn(),
      setSetting: vi.fn(),
    },
  }
})

const profile = (id: number, name: string): MetadataProfile => ({
  id, name, minPopularity: 0, minPages: 0, minEditionCount: 0, skipMissingDate: false,
  skipMissingIsbn: false, skipPartBooks: false, allowedLanguages: 'eng', unknownLanguageBehavior: 'pass',
} as MetadataProfile)

beforeEach(() => {
  vi.mocked(api.listSettings).mockResolvedValue([])
  vi.mocked(api.status).mockResolvedValue({
    version: 'dev', commit: 'unknown', buildDate: '', enhancedHardcoverApi: false, hardcoverTokenConfigured: false,
  })
  vi.mocked(api.listMetadataProfiles).mockResolvedValue([profile(2, 'Deutsch'), profile(3, 'Deutsch + English')])
  vi.mocked(api.setSetting).mockReset()
  vi.mocked(api.setSetting).mockResolvedValue(undefined)
})

// #4132: the install default for new authors, and the fallback for an author
// whose profile was deleted.
describe('Default metadata profile', () => {
  it('shows the first profile when nothing is stored, and saves a change', async () => {
    render(<MetadataTab />)
    const select = await screen.findByLabelText('Default metadata profile for new authors') as HTMLSelectElement
    await waitFor(() => expect(select.value).toBe('2'))
    fireEvent.change(select, { target: { value: '3' } })
    await waitFor(() => expect(api.setSetting).toHaveBeenCalledWith('author.default_metadata_profile_id', '3'))
    expect(select.value).toBe('3')
  })

  it('shows the stored default, and ignores one naming a deleted profile', async () => {
    vi.mocked(api.listSettings).mockResolvedValue([{ key: 'author.default_metadata_profile_id', value: '3' }] as Awaited<ReturnType<typeof api.listSettings>>)
    const { unmount } = render(<MetadataTab />)
    let select = await screen.findByLabelText('Default metadata profile for new authors') as HTMLSelectElement
    await waitFor(() => expect(select.value).toBe('3'))
    unmount()
    vi.mocked(api.listSettings).mockResolvedValue([{ key: 'author.default_metadata_profile_id', value: '1' }] as Awaited<ReturnType<typeof api.listSettings>>)
    render(<MetadataTab />)
    select = await screen.findByLabelText('Default metadata profile for new authors') as HTMLSelectElement
    await waitFor(() => expect(select.value).toBe('2'))
  })

  it('puts the select back and says why when the save is refused', async () => {
    vi.mocked(api.setSetting).mockRejectedValue(new Error('metadata profile 9 does not exist'))
    render(<MetadataTab />)
    const select = await screen.findByLabelText('Default metadata profile for new authors') as HTMLSelectElement
    await waitFor(() => expect(select.value).toBe('2'))
    fireEvent.change(select, { target: { value: '3' } })
    expect(await screen.findByRole('alert')).toHaveTextContent('metadata profile 9 does not exist')
    expect(select.value).toBe('2')
  })

  it('shows the 409 message when a profile that authors use cannot be deleted', async () => {
    vi.mocked(api.deleteMetadataProfile).mockRejectedValue(new Error('in use by 3 authors: A, B, C'))
    render(<MetadataTab />)
    fireEvent.click((await screen.findAllByRole('button', { name: 'common.delete' }))[0])
    await acceptConfirm()
    expect(await screen.findByText('in use by 3 authors: A, B, C')).toBeInTheDocument()
  })

  it('falls back to generic messages when the server error has no message', async () => {
    vi.mocked(api.setSetting).mockRejectedValue('nope')
    vi.mocked(api.deleteMetadataProfile).mockRejectedValue('nope')
    render(<MetadataTab />)
    const select = await screen.findByLabelText('Default metadata profile for new authors') as HTMLSelectElement
    await waitFor(() => expect(select.value).toBe('2'))
    fireEvent.change(select, { target: { value: '3' } })
    expect(await screen.findByText('Could not save the default metadata profile')).toBeInTheDocument()
    fireEvent.click(screen.getAllByRole('button', { name: 'common.delete' })[0])
    await acceptConfirm()
    expect(await screen.findByText('Could not delete the metadata profile')).toBeInTheDocument()
    expect(api.listMetadataProfiles).toHaveBeenCalledTimes(1) // a refused delete does not reload
  })

  it('hides the picker when there is only one profile to choose', async () => {
    vi.mocked(api.listMetadataProfiles).mockResolvedValue([profile(2, 'Deutsch')])
    render(<MetadataTab />)
    await screen.findByText('Deutsch')
    expect(screen.queryByLabelText('Default metadata profile for new authors')).toBeNull()
  })
})
