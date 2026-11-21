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
import { expectFieldValuesWired } from '../../test/fieldValue'

const mocked = api as unknown as Record<string, ReturnType<typeof vi.fn>>

function seed(entries: Record<string, string>) {
  mocked.listSettings.mockResolvedValue(Object.entries(entries).map(([key, value]) => ({ key, value })))
}

const sub = (id: string) => document.getElementById(id)!

describe('CalibreTab current value subtitles (#3989)', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocked.calibreImportStatus.mockResolvedValue({ running: false })
    mocked.calibreSyncStatus.mockResolvedValue({ running: false })
    mocked.calibreRuns.mockResolvedValue([])
    mocked.calibreDeliveries.mockResolvedValue({ items: [], total: 0 })
    mocked.calibreDeliverySummary.mockResolvedValue({ pending: 0, delivered: 0, failed: 0, skipped: 0, mode: 'plugin', target: {} })
    mocked.testCalibre.mockResolvedValue({ ok: 'true', version: 'v0.8.0' })
    mocked.setSetting.mockResolvedValue(undefined)
  })

  it('shows the write mode by name, and the transport label in plugin mode', async () => {
    seed({ 'calibre.mode': 'plugin', 'calibre.plugin_transport': 'pull' })
    render(<CalibreTab />)
    await screen.findByTestId('calibre-transport')
    expect(sub('calibre-mode-value')).toHaveTextContent('Current: Calibre Bridge plugin')
    expect(sub('calibre-transport-value')).toHaveTextContent('Current: settings.calibre.transport.pull')
    expect(screen.getByRole('radiogroup', { name: 'Write integration' })).toHaveAccessibleDescription('Current: Calibre Bridge plugin')
  })

  it('follows a mode change and reverts it when the save fails', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    seed({ 'calibre.mode': 'off' })
    render(<CalibreTab />)
    await screen.findByText('Write integration')
    await waitFor(() => expect(sub('calibre-mode-value')).toHaveTextContent('Current: Off'))
    mocked.setSetting.mockRejectedValueOnce(new Error('boom'))
    fireEvent.click(screen.getByRole('radio', { name: /Calibre Bridge plugin/ }))
    await waitFor(() => expect(mocked.setSetting).toHaveBeenCalled())
    await waitFor(() => expect(sub('calibre-mode-value')).toHaveTextContent('Current: Off'))
    fireEvent.click(screen.getByRole('radio', { name: /Calibre Bridge plugin/ }))
    await waitFor(() => expect(sub('calibre-mode-value')).toHaveTextContent('Current: Calibre Bridge plugin'))
  })

  it('shows library import as Off then On after the toggle saves', async () => {
    seed({ 'calibre.mode': 'off' })
    render(<CalibreTab />)
    await screen.findByText('Library import')
    expect(sub('calibre-library-import-value')).toHaveTextContent('Current: Off')
    fireEvent.click(document.querySelector('[aria-describedby="calibre-library-import-value"]')!)
    await waitFor(() => expect(sub('calibre-library-import-value')).toHaveTextContent('Current: On'))
  })

  it('wires every subtitle on the tab to a control', async () => {
    seed({ 'calibre.mode': 'plugin', 'calibre.library_import_enabled': 'true' })
    render(<CalibreTab />)
    await screen.findByTestId('calibre-transport')
    expectFieldValuesWired(document.body)
  })
})
