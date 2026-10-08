import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, opts?: Record<string, unknown>) =>
      opts && typeof opts === 'object' ? `${key} ${JSON.stringify(opts)}` : key,
  }),
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

function settings(transport?: string) {
  const list = [
    { key: 'calibre.mode', value: 'plugin' },
    { key: 'calibre.plugin_url', value: 'http://calibre:8099' },
  ]
  if (transport) list.push({ key: 'calibre.plugin_transport', value: transport })
  return list
}

// The pull transport (#2833): the plugin connects to Bindery, so the tab
// hides what only push needs and reports the last check in instead of
// probing a plugin URL.
describe('CalibreTab transport', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocked.calibreImportStatus.mockResolvedValue({ running: false })
    mocked.calibreSyncStatus.mockResolvedValue({ running: false })
    mocked.calibreRuns.mockResolvedValue([])
    mocked.calibreDeliveries.mockResolvedValue({ items: [], total: 0 })
    mocked.testCalibre.mockResolvedValue({ ok: 'true', version: 'v0.8.0' })
    mocked.setSetting.mockResolvedValue(undefined)
  })

  it('shows the push fields by default', async () => {
    mocked.listSettings.mockResolvedValue(settings())
    mocked.calibreDeliverySummary.mockResolvedValue({ pending: 0, delivered: 0, failed: 0, skipped: 0, mode: 'plugin', target: {} })
    render(<CalibreTab />)
    expect(await screen.findByText('Plugin URL')).toBeInTheDocument()
    expect(screen.getByText('Push path remap')).toBeInTheDocument()
    expect(screen.getByDisplayValue('push')).toBeChecked()
  })

  it('in pull hides the plugin URL and remap and reports the last check in', async () => {
    mocked.listSettings.mockResolvedValue(settings('pull'))
    mocked.calibreDeliverySummary.mockResolvedValue({
      pending: 2, delivered: 0, failed: 0, skipped: 0, mode: 'plugin', target: {}, transport: 'pull',
      pull: { lastSeen: '2026-09-27T10:00:00Z', pluginVersion: '0.8.0' },
    })
    render(<CalibreTab />)
    await screen.findByTestId('calibre-transport')
    expect(screen.getByDisplayValue('pull')).toBeChecked()
    expect(screen.queryByText('Plugin URL')).not.toBeInTheDocument()
    expect(screen.queryByText('Push path remap')).not.toBeInTheDocument()
    expect(screen.getByText('settings.calibre.transport.apiKeyPullHelp')).toBeInTheDocument()

    fireEvent.click(screen.getByText('Test connection'))
    expect(await screen.findByText(/settings\.calibre\.transport\.testLastCheckInVersion .*"version":"0\.8\.0"/)).toBeInTheDocument()
    // Nothing probes a plugin URL in pull.
    expect(mocked.testCalibre).not.toHaveBeenCalled()
    // Push all works without a plugin URL.
    expect(screen.getByRole('button', { name: 'settings.calibre.pushAll.label' })).toBeEnabled()
    // The queue panel shows the check in rather than reachability.
    expect(await screen.findByTestId('calibre-delivery-reachability')).toHaveTextContent('settings.calibre.deliveries.lastCheckIn')
  })

  it('says so when the plugin has never checked in', async () => {
    mocked.listSettings.mockResolvedValue(settings('pull'))
    mocked.calibreDeliverySummary.mockResolvedValue({
      pending: 0, delivered: 0, failed: 0, skipped: 0, mode: 'plugin', target: {}, transport: 'pull', pull: {},
    })
    render(<CalibreTab />)
    fireEvent.click(await screen.findByText('Test connection'))
    expect(await screen.findByText('settings.calibre.transport.testNeverCheckedIn')).toBeInTheDocument()
  })

  it('saves the transport choice', async () => {
    mocked.listSettings.mockResolvedValue(settings())
    mocked.calibreDeliverySummary.mockResolvedValue({ pending: 0, delivered: 0, failed: 0, skipped: 0, mode: 'plugin', target: {} })
    render(<CalibreTab />)
    fireEvent.click(await screen.findByDisplayValue('pull'))
    await waitFor(() => expect(mocked.setSetting).toHaveBeenCalledWith('calibre.plugin_transport', 'pull'))
    expect(screen.queryByText('Plugin URL')).not.toBeInTheDocument()
  })
})
