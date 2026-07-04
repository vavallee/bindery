import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'

// i18n: t(key) and t(key, 'default string') both render the bare key so
// assertions stay stable; t(key, {opts}) is unused here.
vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, options?: unknown) => {
      if (!options || typeof options !== 'object') return key
      return key
    },
  }),
}))

vi.mock('../../api/client', () => ({
  api: {
    status: vi.fn(),
  },
}))

import { api, SystemStatus } from '../../api/client'
import AboutTab from './AboutTab'

const mockStatus = api.status as ReturnType<typeof vi.fn>

function status(overrides: Partial<SystemStatus> = {}): SystemStatus {
  return {
    version: '1.22.1',
    commit: '9ecd99e',
    buildDate: '2026-06-01T00:00:00Z',
    enhancedHardcoverApi: false,
    hardcoverTokenConfigured: false,
    ...overrides,
  }
}

describe('AboutTab', () => {
  beforeEach(() => vi.clearAllMocks())

  it('renders the release version, commit, and build date from /system/status', async () => {
    mockStatus.mockResolvedValue(status())
    render(<AboutTab />)

    // Tagged build → "vX.Y.Z" label linking to the tag-specific release.
    const link = await screen.findByText('v1.22.1')
    expect(link).toBeInTheDocument()
    expect(link).toHaveAttribute('href', 'https://github.com/vavallee/bindery/releases/tag/v1.22.1')

    expect(screen.getByText('9ecd99e')).toBeInTheDocument()
    expect(screen.getByText('2026-06-01T00:00:00Z')).toBeInTheDocument()
  })

  it('shows the raw sha and the releases index for an untagged build', async () => {
    mockStatus.mockResolvedValue(status({ version: 'sha-9ecd99e' }))
    render(<AboutTab />)

    const link = await screen.findByText('sha-9ecd99e')
    expect(link).toHaveAttribute('href', 'https://github.com/vavallee/bindery/releases')
  })

  it('renders a load error when /system/status fails', async () => {
    mockStatus.mockRejectedValue(new Error('boom'))
    render(<AboutTab />)

    await waitFor(() =>
      expect(screen.getByText('settings.about.loadError')).toBeInTheDocument(),
    )
  })
})
