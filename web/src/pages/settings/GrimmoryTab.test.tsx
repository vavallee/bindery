import { describe, it, expect, vi, beforeEach } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'

vi.mock('../../api/client', () => ({
  api: {
    grimmoryConfig: vi.fn(),
    grimmorySyncStatus: vi.fn(),
    grimmorySetConfig: vi.fn(),
    grimmoryTest: vi.fn(),
    grimmorySync: vi.fn(),
  },
}))

import { api, GrimmoryConfig, GrimmorySyncStatus } from '../../api/client'
import GrimmoryTab from './GrimmoryTab'

const mocked = api as unknown as Record<keyof typeof api, ReturnType<typeof vi.fn>>

const config: GrimmoryConfig = { enabled: true, baseUrl: 'http://grimmory.local', apiKeyConfigured: true, username: '', passwordConfigured: false }

function finishedSync(overrides: Partial<GrimmorySyncStatus> = {}): GrimmorySyncStatus {
  return {
    running: false,
    startedAt: '2026-10-20T10:00:00Z',
    finishedAt: '2026-10-20T10:01:00Z',
    message: 'Finished',
    stats: { total: 7, processed: 7, pushed: 1, alreadyPushed: 0, failed: 6 },
    errors: [],
    totalPushedFiles: 1,
    ...overrides,
  }
}

beforeEach(() => {
  vi.resetAllMocks()
  mocked.grimmoryConfig.mockResolvedValue(config)
  mocked.grimmorySyncStatus.mockResolvedValue(null)
})

// Every failure the tab reports is shown in the error colour, so these cover
// each place it lands: save, test, starting a push, and a finished push.
describe('GrimmoryTab failures', () => {
  it('shows a failed save', async () => {
    mocked.grimmorySetConfig.mockRejectedValue(new Error('base URL is not reachable'))
    render(<GrimmoryTab />)
    fireEvent.click(await screen.findByRole('button', { name: 'Save' }))
    // red-700 on light: red-500 and red-600 are under 4.5:1 on light cards (#3084).
    expect(await screen.findByText('base URL is not reachable')).toHaveClass('text-red-700', 'dark:text-red-400')
  })

  it('shows a test that reached Grimmory but failed, and one that could not run', async () => {
    mocked.grimmoryTest.mockResolvedValueOnce({ ok: false, message: 'API key rejected' })
    mocked.grimmoryTest.mockRejectedValueOnce(new Error('network down'))
    render(<GrimmoryTab />)
    const testButton = await screen.findByRole('button', { name: 'Test Connection' })
    fireEvent.click(testButton)
    expect(await screen.findByText('API key rejected')).toBeInTheDocument()
    fireEvent.click(testButton)
    expect(await screen.findByText('network down')).toBeInTheDocument()
  })

  it('shows a push that would not start', async () => {
    mocked.grimmorySync.mockRejectedValue(new Error('a push is already running'))
    render(<GrimmoryTab />)
    fireEvent.click(await screen.findByRole('button', { name: 'Push all to Grimmory' }))
    expect(await screen.findByText('a push is already running')).toBeInTheDocument()
  })

  it('lists the errors of a finished push, the first five and a count of the rest', async () => {
    const errors = Array.from({ length: 6 }, (_, i) => ({ bookId: i + 1, title: `Fjellvinden ${i + 1}`, path: `/books/${i + 1}.epub`, reason: 'upload refused' }))
    mocked.grimmorySyncStatus.mockResolvedValue(finishedSync({ error: 'Grimmory went away', errors }))
    render(<GrimmoryTab />)
    expect(await screen.findByText('Grimmory went away')).toBeInTheDocument()
    expect(screen.getByText('Fjellvinden 5: upload refused')).toBeInTheDocument()
    expect(screen.queryByText('Fjellvinden 6: upload refused')).toBeNull()
    expect(screen.getByText('…and 1 more')).toBeInTheDocument()
  })
})
