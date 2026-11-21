import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, fallback?: unknown, opts?: Record<string, string>) => {
      if (typeof fallback === 'string') return fallback.replace(/\{\{(\w+)\}\}/g, (_, k) => opts?.[k] ?? '')
      return key
    },
  }),
}))

vi.mock('../../api/client', () => ({
  api: {
    listQualityProfiles: vi.fn(),
    addQualityProfile: vi.fn(),
    updateQualityProfile: vi.fn(),
    deleteQualityProfile: vi.fn(),
    getSetting: vi.fn(),
    setSetting: vi.fn(),
    countAuthorsWithoutQualityProfile: vi.fn(),
    applyQualityProfileToAuthorsWithout: vi.fn(),
  },
}))

import { api, QualityProfile } from '../../api/client'
import QualityTab from './QualityTab'
import { expectFieldValuesWired } from '../../test/fieldValue'

const profiles = [
  { id: 1, name: 'Ebook Preferred', upgradeAllowed: true, cutoff: 'epub', items: [{ quality: 'epub', allowed: true }] },
  { id: 2, name: 'Audio Only', upgradeAllowed: true, cutoff: 'm4b', items: [{ quality: 'm4b', allowed: true }] },
] as QualityProfile[]

const sub = () => document.querySelector('[data-testid="field-value"]') as HTMLElement

describe('QualityTab current value subtitles (#3989)', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(api.listQualityProfiles).mockResolvedValue(profiles)
    vi.mocked(api.countAuthorsWithoutQualityProfile).mockResolvedValue({ count: 0 })
    vi.mocked(api.getSetting).mockRejectedValue(new Error('404'))
    vi.mocked(api.setSetting).mockResolvedValue(undefined as never)
  })

  it('shows Not set with no default, then the profile name after a save', async () => {
    render(<QualityTab />)
    const select = await screen.findByRole('combobox')
    expect(sub()).toHaveTextContent('Current: Not set')
    fireEvent.change(select, { target: { value: '2' } })
    await waitFor(() => expect(sub()).toHaveTextContent('Current: Audio Only'))
    expect(select).toHaveAccessibleDescription('Current: Audio Only')
  })

  it('goes back to the saved default when the save fails', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    vi.mocked(api.getSetting).mockResolvedValue({ key: 'author.default_quality_profile_id', value: '1' } as Awaited<ReturnType<typeof api.getSetting>>)
    vi.mocked(api.setSetting).mockRejectedValue(new Error('boom'))
    render(<QualityTab />)
    const select = await screen.findByRole('combobox')
    await waitFor(() => expect(sub()).toHaveTextContent('Current: Ebook Preferred'))
    fireEvent.change(select, { target: { value: '2' } })
    await waitFor(() => expect(api.setSetting).toHaveBeenCalled())
    await waitFor(() => expect(sub()).toHaveTextContent('Current: Ebook Preferred'))
  })

  it('wires every subtitle on the tab to a control', async () => {
    render(<QualityTab />)
    await screen.findByRole('combobox')
    expectFieldValuesWired(document.body)
  })
})
