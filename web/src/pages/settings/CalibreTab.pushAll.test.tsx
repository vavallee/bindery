import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string, fallback?: unknown) => (typeof fallback === 'string' ? fallback : key) }),
}))

vi.mock('../../api/client', () => ({
  api: {
    listSettings: vi.fn(),
    setSetting: vi.fn(),
    testCalibre: vi.fn(),
    calibreImportStatus: vi.fn(),
    calibreSyncStatus: vi.fn(),
    calibreRuns: vi.fn(),
    calibreImportStart: vi.fn(),
    calibreSyncStart: vi.fn(),
    calibreRunRollback: vi.fn(),
    calibreRunRollbackPreview: vi.fn(),
    calibreDeliverySummary: vi.fn(),
    calibreDeliveries: vi.fn(),
  },
}))

import { api } from '../../api/client'
import CalibreTab from './CalibreTab'

const mocked = api as unknown as Record<string, ReturnType<typeof vi.fn>>

const waiting = {
  running: true,
  queueing: false,
  startedAt: '2026-09-27T12:00:00Z',
  message: '3 waiting for Calibre',
  stats: { total: 3, processed: 0, pushed: 0, alreadyInCalibre: 0, failed: 0, skipped: 0 },
  errors: [],
  skips: [],
}

// Push all on the delivery queue (#2832): a run whose books are waiting for a
// closed Calibre is still "running", possibly for days. That must neither
// lock the button nor pop the modal open on every visit to the tab.
describe('CalibreTab Push all on the queue', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocked.listSettings.mockResolvedValue([
      { key: 'calibre.mode', value: 'plugin' },
      { key: 'calibre.plugin_url', value: 'http://calibre:8099' },
    ])
    mocked.testCalibre.mockRejectedValue(new Error('connection refused'))
    mocked.calibreImportStatus.mockResolvedValue({ running: false })
    mocked.calibreRuns.mockResolvedValue([])
    mocked.calibreDeliverySummary.mockResolvedValue({
      pending: 3, delivered: 0, failed: 0, skipped: 0, mode: 'plugin', target: { reachable: false },
    })
    mocked.calibreDeliveries.mockResolvedValue({ items: [], total: 0 })
  })

  it('keeps Push all available while books wait for an unreachable Calibre', async () => {
    mocked.calibreSyncStatus.mockResolvedValue(waiting)
    render(<CalibreTab />)
    const button = await screen.findByRole('button', { name: 'settings.calibre.pushAll.label' })
    await waitFor(() => expect(mocked.calibreSyncStatus).toHaveBeenCalled())
    await waitFor(() => expect(mocked.testCalibre).toHaveBeenCalled())
    expect(button).toBeEnabled()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(await screen.findByTestId('calibre-delivery-panel')).toBeInTheDocument()
  })

  it('reopens the modal and locks the button while Push all is still queueing', async () => {
    mocked.calibreSyncStatus.mockResolvedValue({ ...waiting, queueing: true, message: 'queueing books for Calibre…' })
    render(<CalibreTab />)
    expect(await screen.findByRole('dialog')).toHaveTextContent('queueing books for Calibre')
    expect(screen.getByRole('button', { name: 'settings.calibre.pushAll.queueing' })).toBeDisabled()
  })
})
