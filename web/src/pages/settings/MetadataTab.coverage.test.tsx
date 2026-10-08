import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor, fireEvent, within } from '@testing-library/react'
import MetadataTab from './MetadataTab'
import { api } from '../../api/client'
import type { MetadataProfile } from '../../api/client'
import { acceptConfirm, cancelConfirm } from '../../test-utils'

// Keys are returned as-is so assertions do not depend on English copy.
vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string) => key,
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
      setSetting: vi.fn(),
      addMetadataProfile: vi.fn(),
      updateMetadataProfile: vi.fn(),
      deleteMetadataProfile: vi.fn(),
    },
  }
})

function seedSettings(entries: Record<string, string>) {
  vi.mocked(api.listSettings).mockResolvedValue(
    Object.entries(entries).map(([key, value]) => ({ key, value })) as Awaited<
      ReturnType<typeof api.listSettings>
    >,
  )
}

const profile: MetadataProfile = {
  id: 7,
  name: 'Strict',
  minPopularity: 3,
  minPages: 120,
  minEditionCount: 2,
  skipMissingDate: true,
  skipMissingIsbn: true,
  skipPartBooks: true,
  allowedLanguages: 'eng, ger,xyz',
  unknownLanguageBehavior: 'fail',
}

const loose: MetadataProfile = {
  id: 8,
  name: 'Loose',
  minPopularity: 0,
  minPages: 0,
  minEditionCount: 0,
  skipMissingDate: false,
  skipMissingIsbn: false,
  skipPartBooks: false,
  allowedLanguages: '',
  unknownLanguageBehavior: 'pass',
}

beforeEach(() => {
  vi.clearAllMocks()
  seedSettings({})
  vi.mocked(api.status).mockResolvedValue({
    version: 'dev', commit: 'unknown', buildDate: '', enhancedHardcoverApi: false, hardcoverTokenConfigured: false,
  })
  vi.mocked(api.listMetadataProfiles).mockResolvedValue([])
  vi.mocked(api.setSetting).mockResolvedValue(undefined)
  vi.mocked(api.addMetadataProfile).mockResolvedValue(profile)
  vi.mocked(api.updateMetadataProfile).mockResolvedValue(profile)
  vi.mocked(api.deleteMetadataProfile).mockResolvedValue(undefined)
})

describe('MetadataTab library defaults', () => {
  it('shows the empty profile state', async () => {
    render(<MetadataTab />)
    expect(await screen.findByText('settings.metadata.empty')).toBeInTheDocument()
  })

  it('persists default media type, strict flag and monitor mode', async () => {
    render(<MetadataTab />)
    await waitFor(() => expect(api.listSettings).toHaveBeenCalled())
    const [, mediaType, monitorMode] = screen.getAllByRole('combobox')
    expect(mediaType).toHaveValue('ebook')
    expect(monitorMode).toHaveValue('all')

    const strict = screen.getByRole('checkbox')
    fireEvent.click(strict)
    await waitFor(() => expect(api.setSetting).toHaveBeenCalledWith('default.media_type_strict', 'true'))
    expect(strict).toBeChecked()

    fireEvent.change(mediaType, { target: { value: 'both' } })
    await waitFor(() => expect(api.setSetting).toHaveBeenCalledWith('default.media_type', 'both'))
    // The strict flag only makes sense when a single format is the default.
    expect(screen.queryByRole('checkbox')).not.toBeInTheDocument()

    fireEvent.change(monitorMode, { target: { value: 'latest' } })
    await waitFor(() => expect(api.setSetting).toHaveBeenCalledWith('author.default_monitor_mode', 'latest'))
    const count = screen.getByRole('spinbutton')
    expect(count).toHaveValue(1)

    // An invalid count is shown but never persisted.
    fireEvent.change(count, { target: { value: '0' } })
    expect(api.setSetting).not.toHaveBeenCalledWith('author.default_monitor_latest_count', '0')
    fireEvent.change(count, { target: { value: '3' } })
    await waitFor(() => expect(api.setSetting).toHaveBeenCalledWith('author.default_monitor_latest_count', '3'))
  })

  it('falls back to "all" for an unrecognised stored monitor mode', async () => {
    seedSettings({ 'author.default_monitor_mode': 'bogus' })
    render(<MetadataTab />)
    await waitFor(() => expect(api.listSettings).toHaveBeenCalled())
    await waitFor(() => expect(screen.getAllByRole('combobox')[2]).toHaveValue('all'))
  })

  it('toggles auto-grab off and recommendations on', async () => {
    seedSettings({ 'autoGrab.enabled': 'true', 'recommendations.enabled': 'false' })
    render(<MetadataTab />)
    await waitFor(() => expect(api.listSettings).toHaveBeenCalled())
    const [autoGrab, recs] = screen.getAllByRole('switch')
    expect(autoGrab).toHaveAttribute('aria-checked', 'true')
    expect(recs).toHaveAttribute('aria-checked', 'false')
    fireEvent.click(autoGrab)
    await waitFor(() => expect(api.setSetting).toHaveBeenCalledWith('autoGrab.enabled', 'false'))
    expect(autoGrab).toHaveAttribute('aria-checked', 'false')
    fireEvent.click(autoGrab)
    await waitFor(() => expect(api.setSetting).toHaveBeenCalledWith('autoGrab.enabled', 'true'))
    fireEvent.click(recs)
    await waitFor(() => expect(api.setSetting).toHaveBeenCalledWith('recommendations.enabled', 'true'))
    fireEvent.click(recs)
    await waitFor(() => expect(api.setSetting).toHaveBeenCalledWith('recommendations.enabled', 'false'))
  })

  it('keeps rendering when the initial loads fail', async () => {
    const err = vi.spyOn(console, 'error').mockImplementation(() => {})
    vi.mocked(api.listSettings).mockRejectedValue(new Error('settings'))
    vi.mocked(api.status).mockRejectedValue(new Error('status'))
    vi.mocked(api.listMetadataProfiles).mockRejectedValue(new Error('profiles'))
    render(<MetadataTab />)
    await waitFor(() => expect(err).toHaveBeenCalledTimes(3))
    expect(screen.getByText('settings.metadata.empty')).toBeInTheDocument()
    err.mockRestore()
  })
})

describe('MetadataTab profiles', () => {
  it('lists profiles with their filters and language labels', async () => {
    vi.mocked(api.listMetadataProfiles).mockResolvedValue([profile, loose])
    render(<MetadataTab />)
    expect(await screen.findByText('Strict')).toBeInTheDocument()
    expect(screen.getByText('120')).toBeInTheDocument()
    expect(screen.getByText('English, German, xyz')).toBeInTheDocument()
    expect(screen.getByText('settings.metadata.skipMissingDate')).toBeInTheDocument()
    expect(screen.getByText('settings.metadata.skipMissingIsbn')).toBeInTheDocument()
    expect(screen.getByText('settings.metadata.skipPartBooks')).toBeInTheDocument()
    // The loose profile shows "none" for zero thresholds and "any" for no languages.
    expect(screen.getAllByText('none')).toHaveLength(2)
    expect(screen.getByText('any')).toBeInTheDocument()
  })

  it('creates a profile with the chosen fields', async () => {
    render(<MetadataTab />)
    await screen.findByText('settings.metadata.empty')
    fireEvent.click(screen.getByRole('button', { name: 'settings.metadata.newProfile' }))

    const form = screen.getByRole('button', { name: 'settings.metadata.createProfile' }).closest('form')!
    const f = within(form)
    fireEvent.change(f.getByPlaceholderText('settings.metadata.formNamePlaceholder'), { target: { value: '  Mine  ' } })
    fireEvent.click(f.getByRole('button', { name: 'English' })) // off
    fireEvent.click(f.getByRole('button', { name: 'French' })) // on
    fireEvent.click(f.getByRole('button', { name: 'Japanese' })) // on
    fireEvent.change(f.getByRole('combobox'), { target: { value: 'fail' } })
    const [pages, editions] = f.getAllByRole('spinbutton')
    fireEvent.change(pages, { target: { value: '50' } })
    expect(f.queryByText('settings.metadata.formMinEditionCountHint')).not.toBeInTheDocument()
    fireEvent.change(editions, { target: { value: '4' } })
    expect(f.getByText('settings.metadata.formMinEditionCountHint')).toBeInTheDocument()
    const [date, isbn, parts] = f.getAllByRole('checkbox')
    fireEvent.click(date)
    fireEvent.click(isbn)
    fireEvent.click(parts)

    fireEvent.submit(form)
    await waitFor(() => expect(api.addMetadataProfile).toHaveBeenCalledWith({
      name: 'Mine',
      minPopularity: 0,
      minPages: 50,
      minEditionCount: 4,
      skipMissingDate: true,
      skipMissingIsbn: true,
      skipPartBooks: true,
      allowedLanguages: 'fre,jpn',
      unknownLanguageBehavior: 'fail',
    }))
    await waitFor(() => expect(screen.queryByRole('button', { name: 'settings.metadata.createProfile' })).not.toBeInTheDocument())
    expect(api.listMetadataProfiles).toHaveBeenCalledTimes(2)
  })

  it('shows the server error when creating fails, and a fallback for non-Error rejections', async () => {
    vi.mocked(api.addMetadataProfile).mockRejectedValueOnce(new Error('name taken'))
    vi.mocked(api.addMetadataProfile).mockRejectedValueOnce('weird')
    render(<MetadataTab />)
    await screen.findByText('settings.metadata.empty')
    fireEvent.click(screen.getByRole('button', { name: 'settings.metadata.newProfile' }))
    const form = screen.getByRole('button', { name: 'settings.metadata.createProfile' }).closest('form')!
    fireEvent.change(within(form).getByPlaceholderText('settings.metadata.formNamePlaceholder'), { target: { value: 'X' } })
    fireEvent.submit(form)
    expect(await screen.findByText('name taken')).toBeInTheDocument()
    fireEvent.submit(form)
    expect(await screen.findByText('settings.metadata.saveFail')).toBeInTheDocument()
    fireEvent.click(within(form).getByRole('button', { name: 'common.cancel' }))
    expect(screen.queryByRole('button', { name: 'settings.metadata.createProfile' })).not.toBeInTheDocument()
  })

  it('edits an existing profile and round-trips minPopularity', async () => {
    vi.mocked(api.listMetadataProfiles).mockResolvedValue([profile])
    render(<MetadataTab />)
    await screen.findByText('Strict')
    fireEvent.click(screen.getByRole('button', { name: 'common.edit' }))
    const form = screen.getByRole('button', { name: 'settings.metadata.saveChanges' }).closest('form')!
    const f = within(form)
    expect(f.getByDisplayValue('Strict')).toBeInTheDocument()
    expect(f.getByRole('combobox')).toHaveValue('fail')
    fireEvent.click(f.getByRole('button', { name: 'German' })) // drop ger
    fireEvent.submit(form)
    await waitFor(() => expect(api.updateMetadataProfile).toHaveBeenCalledWith(7, expect.objectContaining({
      name: 'Strict',
      minPopularity: 3,
      minPages: 120,
      allowedLanguages: 'eng,xyz',
      unknownLanguageBehavior: 'fail',
    })))
    await waitFor(() => expect(screen.queryByRole('button', { name: 'settings.metadata.saveChanges' })).not.toBeInTheDocument())
  })

  it('cancels editing without saving', async () => {
    vi.mocked(api.listMetadataProfiles).mockResolvedValue([profile])
    render(<MetadataTab />)
    await screen.findByText('Strict')
    fireEvent.click(screen.getByRole('button', { name: 'common.edit' }))
    fireEvent.click(screen.getByRole('button', { name: 'common.cancel' }))
    expect(await screen.findByText('Strict')).toBeInTheDocument()
    expect(api.updateMetadataProfile).not.toHaveBeenCalled()
  })

  it('deletes a profile only after confirmation', async () => {
    vi.mocked(api.listMetadataProfiles).mockResolvedValue([profile])
    render(<MetadataTab />)
    await screen.findByText('Strict')
    fireEvent.click(screen.getByRole('button', { name: 'common.delete' }))
    await cancelConfirm()
    expect(api.deleteMetadataProfile).not.toHaveBeenCalled()

    fireEvent.click(screen.getByRole('button', { name: 'common.delete' }))
    await acceptConfirm()
    await waitFor(() => expect(api.deleteMetadataProfile).toHaveBeenCalledWith(7))
    await waitFor(() => expect(api.listMetadataProfiles).toHaveBeenCalledTimes(2))
  })
})
