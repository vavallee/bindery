import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import MetadataTab from './MetadataTab'
import { api } from '../../api/client'
import type { MetadataProfile } from '../../api/profiles'

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
      status: vi.fn(),
      updateMetadataProfile: vi.fn(),
      addMetadataProfile: vi.fn(),
    },
  }
})

function profile(allowedLanguages: string): MetadataProfile {
  return {
    id: 1, name: 'Standard', minPopularity: 0, minPages: 0, minEditionCount: 0,
    skipMissingDate: false, skipMissingIsbn: false, skipPartBooks: false,
    allowedLanguages, unknownLanguageBehavior: 'pass',
  }
}

function pressedLanguages() {
  return ['English', 'French', 'Norwegian']
    .filter(name => screen.getByRole('button', { name }).getAttribute('aria-pressed') === 'true')
}

beforeEach(() => {
  vi.mocked(api.listSettings).mockResolvedValue([])
  vi.mocked(api.status).mockResolvedValue({
    version: 'dev', commit: 'unknown', buildDate: '', enhancedHardcoverApi: false, hardcoverTokenConfigured: false,
  })
  vi.mocked(api.updateMetadataProfile).mockReset()
  vi.mocked(api.addMetadataProfile).mockReset()
  vi.mocked(api.updateMetadataProfile).mockResolvedValue(profile(''))
})

// #3212: an existing profile that allows any language opened with English
// ticked, so the form showed a filter that was not applied, and saving it
// unchanged applied it.
describe('Metadata profile language selection', () => {
  it('opens an any-language profile with nothing ticked and saves it unchanged', async () => {
    vi.mocked(api.listMetadataProfiles).mockResolvedValue([profile('')])
    render(<MetadataTab />)
    fireEvent.click(await screen.findByRole('button', { name: 'common.edit' }))
    expect(pressedLanguages()).toEqual([])

    fireEvent.click(screen.getByRole('button', { name: 'settings.metadata.saveChanges' }))
    await waitFor(() => expect(api.updateMetadataProfile).toHaveBeenCalled())
    expect(vi.mocked(api.updateMetadataProfile).mock.calls[0][1].allowedLanguages).toBe('')
  })

  it('opens a profile with exactly the languages it stores', async () => {
    vi.mocked(api.listMetadataProfiles).mockResolvedValue([profile('nor,fre')])
    render(<MetadataTab />)
    fireEvent.click(await screen.findByRole('button', { name: 'common.edit' }))
    expect(pressedLanguages()).toEqual(['French', 'Norwegian'])
  })

  // #3240: the profile form's labels name their fields.
  it('names the profile fields by their labels', async () => {
    vi.mocked(api.listMetadataProfiles).mockResolvedValue([profile('eng')])
    render(<MetadataTab />)
    fireEvent.click(await screen.findByRole('button', { name: 'common.edit' }))
    expect(screen.getByLabelText('settings.metadata.formMinPages').tagName).toBe('INPUT')
    expect(screen.getByLabelText('settings.metadata.formUnknownLanguage').tagName).toBe('SELECT')
  })

  // #3293: Create turns an empty list into English, so a new profile with no
  // language ticked has to say "any" itself.
  it('saves a new profile with no language ticked as any language', async () => {
    vi.mocked(api.listMetadataProfiles).mockResolvedValue([])
    vi.mocked(api.addMetadataProfile).mockResolvedValue(profile('any'))
    render(<MetadataTab />)
    fireEvent.click(await screen.findByRole('button', { name: 'settings.metadata.newProfile' }))
    fireEvent.change(screen.getByLabelText('settings.metadata.formName'), { target: { value: 'Anything' } })
    fireEvent.click(screen.getByRole('button', { name: 'English' }))
    expect(pressedLanguages()).toEqual([])
    fireEvent.click(screen.getByRole('button', { name: 'settings.metadata.createProfile' }))
    await waitFor(() => expect(api.addMetadataProfile).toHaveBeenCalled())
    expect(vi.mocked(api.addMetadataProfile).mock.calls[0][0].allowedLanguages).toBe('any')
  })

  it('still sends the ticked languages when a new profile has some', async () => {
    vi.mocked(api.listMetadataProfiles).mockResolvedValue([])
    vi.mocked(api.addMetadataProfile).mockResolvedValue(profile('eng,fre'))
    render(<MetadataTab />)
    fireEvent.click(await screen.findByRole('button', { name: 'settings.metadata.newProfile' }))
    fireEvent.change(screen.getByLabelText('settings.metadata.formName'), { target: { value: 'Two' } })
    fireEvent.click(screen.getByRole('button', { name: 'French' }))
    fireEvent.click(screen.getByRole('button', { name: 'settings.metadata.createProfile' }))
    await waitFor(() => expect(api.addMetadataProfile).toHaveBeenCalled())
    expect(vi.mocked(api.addMetadataProfile).mock.calls[0][0].allowedLanguages).toBe('eng,fre')
  })

  it('still defaults a new profile to English', async () => {
    vi.mocked(api.listMetadataProfiles).mockResolvedValue([])
    render(<MetadataTab />)
    fireEvent.click(await screen.findByRole('button', { name: 'settings.metadata.newProfile' }))
    expect(pressedLanguages()).toEqual(['English'])
  })
})
