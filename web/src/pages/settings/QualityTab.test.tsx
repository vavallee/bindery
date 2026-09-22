import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor, fireEvent, within } from '@testing-library/react'

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

  it('renders existing profiles as best first chips per list', async () => {
    mockList.mockResolvedValueOnce([profile({
      items: [
        { quality: 'epub', allowed: true },
        { quality: 'mobi', allowed: true },
        { quality: 'm4b', allowed: true },
        { quality: 'pdf', allowed: false },
      ],
    })])
    render(<QualityTab />)
    await waitFor(() => {
      expect(screen.getByText('Ebook Preferred')).toBeInTheDocument()
    })
    // Numbering restarts per list: m4b is the only audiobook format, so it
    // is "1." in its own list rather than "3." in a flat one.
    expect(screen.getByText('1. m4b')).toBeInTheDocument()
    const ebookList = screen.getByRole('list', { name: 'settings.quality.ebookList' })
    expect(within(ebookList).getByText('1. epub')).toBeInTheDocument()
    expect(within(ebookList).getByText('2. mobi')).toBeInTheDocument()
    expect(within(ebookList).getByText('3. pdf')).toBeInTheDocument()
    const audioList = screen.getByRole('list', { name: 'settings.quality.audiobookList' })
    expect(within(audioList).getByText('1. m4b')).toBeInTheDocument()
    expect(screen.getAllByText('settings.quality.bestFirst').length).toBeGreaterThan(0)
  })

  it('summary sentence reflects state', async () => {
    mockList.mockResolvedValueOnce([profile({
      items: [
        { quality: 'epub', allowed: true },
        { quality: 'pdf', allowed: false },
        { quality: 'azw3', allowed: true },
      ],
    })])
    render(<QualityTab />)
    await waitFor(() => screen.getByText('Ebook Preferred'))
    fireEvent.click(screen.getByRole('button', { name: 'common.edit' }))
    // Ebook list: prefer epub, then azw3; never pdf.
    expect(screen.getByText('settings.quality.summaryPreferThen first=epub rest=azw3')).toBeInTheDocument()
    expect(screen.getByText('settings.quality.summaryNever formats=pdf')).toBeInTheDocument()
    // Audiobook list is empty: no opinion, any audiobook format is accepted.
    expect(screen.getByText('settings.quality.summaryNoOpinionAudiobook')).toBeInTheDocument()

    // Untick epub: azw3 is the only ticked one left, so no "then".
    fireEvent.click(screen.getByRole('checkbox', { name: 'epub' }))
    expect(screen.getByText('settings.quality.summaryPrefer first=azw3')).toBeInTheDocument()
    expect(screen.getByText('settings.quality.summaryNever formats=epub, pdf')).toBeInTheDocument()

    // Untick azw3 too: nothing ticked, so no ebook will be grabbed.
    fireEvent.click(screen.getByRole('checkbox', { name: 'azw3' }))
    expect(screen.getByText('settings.quality.summaryNoneAllowedEbook')).toBeInTheDocument()
  })

  it('move stays inside its own list', async () => {
    const mockUpdate = api.updateQualityProfile as ReturnType<typeof vi.fn>
    mockUpdate.mockResolvedValueOnce(profile())
    mockList.mockResolvedValue([profile({
      items: [
        { quality: 'epub', allowed: true },
        { quality: 'm4b', allowed: true },
        { quality: 'pdf', allowed: true },
      ],
    })])
    render(<QualityTab />)
    await waitFor(() => screen.getByText('Ebook Preferred'))
    fireEvent.click(screen.getByRole('button', { name: 'common.edit' }))
    // The first "move down" belongs to epub, the top of the ebook list. It
    // swaps with pdf, its neighbour in that list, not with m4b, its
    // neighbour in the stored order.
    fireEvent.click(screen.getAllByRole('button', { name: 'settings.quality.moveDown' })[0])
    fireEvent.click(screen.getByRole('button', { name: 'settings.quality.saveChanges' }))
    await waitFor(() => expect(mockUpdate).toHaveBeenCalled())
    const sent = mockUpdate.mock.calls[0][1] as QualityProfile
    expect(sent.items.map(i => i.quality)).toEqual(['pdf', 'epub', 'm4b'])
  })

  it('new profile seeds azw3 first', async () => {
    mockList.mockResolvedValueOnce([])
    render(<QualityTab />)
    await waitFor(() => screen.getByText('settings.quality.newProfile'))
    fireEvent.click(screen.getByText('settings.quality.newProfile'))
    const boxes = screen.getAllByRole('checkbox')
    expect(boxes.map(b => b.getAttribute('aria-label') ?? (b as HTMLInputElement).labels?.[0]?.textContent))
      .toEqual(['azw3', 'epub', 'mobi', 'pdf'])
    expect(boxes.every(b => (b as HTMLInputElement).checked)).toBe(true)
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
})
