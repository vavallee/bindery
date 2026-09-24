import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'

// i18n: return the key so assertions are stable.
vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, options?: Record<string, unknown>) => {
      if (!options) return key
      let out = key
      for (const [k, v] of Object.entries(options)) {
        out += ` ${k}=${String(v)}`
      }
      return out
    },
  }),
}))

vi.mock('../../api/client', () => ({
  api: {
    listQualityProfiles: vi.fn(),
    addQualityProfile: vi.fn(),
    updateQualityProfile: vi.fn(),
    deleteQualityProfile: vi.fn(),
  },
}))

import { api, QualityProfile } from '../../api/client'
import QualityTab from './QualityTab'

const mockList = api.listQualityProfiles as ReturnType<typeof vi.fn>

function profile(overrides: Partial<QualityProfile> = {}): QualityProfile {
  return {
    id: 1,
    name: 'Ebook Preferred',
    upgradeAllowed: true,
    cutoff: 'epub',
    items: [
      { quality: 'pdf', allowed: false },
      { quality: 'mobi', allowed: true },
      { quality: 'epub', allowed: true },
    ],
    ...overrides,
  }
}

describe('QualityTab', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('renders the empty state when no profiles exist', async () => {
    mockList.mockResolvedValueOnce([])
    render(<QualityTab />)
    await waitFor(() => {
      expect(screen.getByText('settings.quality.empty')).toBeInTheDocument()
    })
    expect(screen.getByText('settings.quality.newProfile')).toBeInTheDocument()
  })

  it('renders existing profiles as worst→best format chips', async () => {
    mockList.mockResolvedValueOnce([profile()])
    render(<QualityTab />)
    await waitFor(() => {
      expect(screen.getByText('Ebook Preferred')).toBeInTheDocument()
    })
    // Each item is rendered as a worst→best ranked chip ("1. pdf").
    expect(screen.getByText('1. pdf')).toBeInTheDocument()
    expect(screen.getByText('2. mobi')).toBeInTheDocument()
    expect(screen.getByText('3. epub')).toBeInTheDocument()
  })

  // #2373: cutoff and "upgrades allowed" were removed from the UI because
  // nothing read either one. The row must not advertise them any more.
  it('does not show a cutoff or an upgrades-allowed badge', async () => {
    mockList.mockResolvedValueOnce([profile()])
    render(<QualityTab />)
    await waitFor(() => {
      expect(screen.getByText('Ebook Preferred')).toBeInTheDocument()
    })
    expect(screen.queryByText('settings.quality.cutoff', { exact: false })).not.toBeInTheDocument()
    expect(screen.queryByText('settings.quality.upgradesAllowed')).not.toBeInTheDocument()
  })

  it('opens the editor form when "New Profile" is clicked', async () => {
    mockList.mockResolvedValueOnce([])
    render(<QualityTab />)
    await waitFor(() => screen.getByText('settings.quality.newProfile'))
    fireEvent.click(screen.getByText('settings.quality.newProfile'))
    // Form heading-equivalent: the name label appears in the form.
    expect(screen.getByText('settings.quality.formName')).toBeInTheDocument()
    expect(screen.getByText('settings.quality.formPreference')).toBeInTheDocument()
    // No cutoff select and no upgrade checkbox since #2373.
    expect(screen.queryByText('settings.quality.formCutoff')).not.toBeInTheDocument()
    expect(screen.queryByText('settings.quality.formUpgradeAllowed')).not.toBeInTheDocument()
  })

  it('offers every release format the parser recognises (#1700)', async () => {
    mockList.mockResolvedValueOnce([])
    render(<QualityTab />)
    await waitFor(() => screen.getByText('settings.quality.newProfile'))
    fireEvent.click(screen.getByText('settings.quality.newProfile'))
    // A new profile still seeds only the four mainstream ebook containers.
    for (const seeded of ['pdf', 'mobi', 'epub', 'azw3']) {
      expect(screen.getByText(seeded)).toBeInTheDocument()
    }
    // Everything else ParseRelease can emit is one "+ Add" chip away. Before
    // #1700 nine of these had no path into the allow-list at all.
    const chips = [
      'txt', 'rtf', 'lit', 'djvu', 'cbr', 'cbz', 'fb2', 'azw',
      'ogg', 'mp3', 'm4a', 'm4b', 'flac',
    ]
    for (const f of chips) {
      expect(screen.getByText(`+ ${f}`)).toBeInTheDocument()
    }
  })

  it('adds ogg via its chip and badges it as an audiobook format', async () => {
    mockList.mockResolvedValueOnce([])
    render(<QualityTab />)
    await waitFor(() => screen.getByText('settings.quality.newProfile'))
    fireEvent.click(screen.getByText('settings.quality.newProfile'))
    fireEvent.click(screen.getByText('+ ogg'))
    expect(screen.queryByText('+ ogg')).not.toBeInTheDocument()
    expect(screen.getByText('ogg')).toBeInTheDocument()
    expect(screen.getByText('common.audiobook')).toBeInTheDocument()
  })

  // #2740: the scoring block is optional and must round-trip through the
  // editor, including a grabs weight the user deliberately set.
  it('round-trips the audiobook scoring block through the editor', async () => {
    mockList.mockResolvedValueOnce([profile({
      items: [{ quality: 'm4b', allowed: true }],
      audiobookScoring: {
        codecTargets: { m4b: 1 },
        toleranceMiBPerMinute: 0.2,
        sizePerMinuteWeight: 10,
        grabsWeight: 4,
      },
    })])
    const mockUpdate = api.updateQualityProfile as ReturnType<typeof vi.fn>
    mockUpdate.mockResolvedValueOnce({})
    render(<QualityTab />)
    await waitFor(() => screen.getByText('Ebook Preferred'))
    fireEvent.click(screen.getByText('common.edit'))
    expect(screen.getByLabelText('m4b')).toHaveValue(1)
    fireEvent.click(screen.getByText('settings.quality.saveChanges'))
    await waitFor(() => expect(mockUpdate).toHaveBeenCalled())
    const scoring = mockUpdate.mock.calls[0][1].audiobookScoring
    expect(scoring.codecTargets).toEqual({ m4b: 1 })
    expect(scoring.sizePerMinuteWeight).toBe(10)
    expect(scoring.grabsWeight).toBe(4)
  })

  // A profile with no scoring block must post none, so the server stores NULL
  // and keeps the historical ranking rather than switching to zero weights.
  it('omits the scoring block when it is switched off', async () => {
    mockList.mockResolvedValueOnce([profile({ items: [{ quality: 'epub', allowed: true }] })])
    const mockUpdate = api.updateQualityProfile as ReturnType<typeof vi.fn>
    mockUpdate.mockResolvedValueOnce({})
    render(<QualityTab />)
    await waitFor(() => screen.getByText('Ebook Preferred'))
    fireEvent.click(screen.getByText('common.edit'))
    fireEvent.click(screen.getByText('settings.quality.saveChanges'))
    await waitFor(() => expect(mockUpdate).toHaveBeenCalled())
    expect(mockUpdate.mock.calls[0][1].audiobookScoring).toBeUndefined()
  })
})
