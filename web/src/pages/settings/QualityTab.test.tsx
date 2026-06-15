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

  it('renders existing profiles with cutoff and format chips', async () => {
    mockList.mockResolvedValueOnce([profile()])
    render(<QualityTab />)
    await waitFor(() => {
      expect(screen.getByText('Ebook Preferred')).toBeInTheDocument()
    })
    // Each item is rendered as a worst→best ranked chip ("1. pdf"); "epub"
    // also appears separately as the cutoff value, so match it loosely.
    expect(screen.getByText('1. pdf')).toBeInTheDocument()
    expect(screen.getByText('2. mobi')).toBeInTheDocument()
    expect(screen.getByText('3. epub')).toBeInTheDocument()
    expect(screen.getAllByText('epub', { exact: false }).length).toBeGreaterThan(1)
    // Cutoff label rendered.
    expect(screen.getByText('settings.quality.cutoff', { exact: false })).toBeInTheDocument()
  })

  it('opens the editor form when "New Profile" is clicked', async () => {
    mockList.mockResolvedValueOnce([])
    render(<QualityTab />)
    await waitFor(() => screen.getByText('settings.quality.newProfile'))
    fireEvent.click(screen.getByText('settings.quality.newProfile'))
    // Form heading-equivalent: the name label appears in the form.
    expect(screen.getByText('settings.quality.formName')).toBeInTheDocument()
    expect(screen.getByText('settings.quality.formPreference')).toBeInTheDocument()
    expect(screen.getByText('settings.quality.formCutoff')).toBeInTheDocument()
  })
})
