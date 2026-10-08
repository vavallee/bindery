import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor, within } from '@testing-library/react'

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

const REASON = 'calibredb is not installed where Bindery runs (looked for "calibredb"). The official Bindery image does not include Calibre.'

function missing() {
  return Object.assign(new Error(REASON), { body: { error: REASON, code: 'calibredb_missing' } })
}

// #1940: calibredb mode on the official image (no calibredb) saved without a
// word and then dropped every import. The tab must say so on its own, and
// offer the Bridge plugin.
describe('CalibreTab with no calibredb', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocked.calibreImportStatus.mockResolvedValue({ running: false })
    mocked.calibreSyncStatus.mockResolvedValue({ running: false })
    mocked.calibreRuns.mockResolvedValue([])
    mocked.calibreDeliverySummary.mockResolvedValue({ pending: 0, delivered: 0, failed: 0, skipped: 0, mode: 'calibredb', target: {} })
    mocked.calibreDeliveries.mockResolvedValue({ items: [], total: 0 })
  })

  it('warns as soon as the tab opens in calibredb mode, without pressing Test', async () => {
    mocked.listSettings.mockResolvedValue([{ key: 'calibre.mode', value: 'calibredb' }])
    mocked.testCalibre.mockRejectedValue(missing())
    render(<CalibreTab />)

    expect(await screen.findByText('settings.calibre.calibredbMissing.title')).toBeInTheDocument()
    expect(screen.getByText(REASON)).toBeInTheDocument()
  })

  it('warns when calibredb mode is chosen and the save reports it missing', async () => {
    mocked.listSettings.mockResolvedValue([{ key: 'calibre.mode', value: 'off' }])
    // The save answers first; the probe is kept pending so the warning can
    // only have come from the save.
    mocked.testCalibre.mockReturnValue(new Promise(() => {}))
    mocked.setSetting.mockResolvedValue({ key: 'calibre.mode', value: 'calibredb', warning: REASON, warningCode: 'calibredb_missing' })
    render(<CalibreTab />)

    fireEvent.click(await screen.findByDisplayValue('calibredb'))

    expect(await screen.findByText('settings.calibre.calibredbMissing.title')).toBeInTheDocument()
    expect(mocked.setSetting).toHaveBeenCalledWith('calibre.mode', 'calibredb')
  })

  it('switches to the Bridge plugin from the warning', async () => {
    mocked.listSettings.mockResolvedValue([{ key: 'calibre.mode', value: 'calibredb' }])
    mocked.testCalibre.mockRejectedValue(missing())
    mocked.setSetting.mockResolvedValue({ key: 'calibre.mode', value: 'plugin' })
    render(<CalibreTab />)

    fireEvent.click(await screen.findByText('settings.calibre.calibredbMissing.switchToBridge'))

    await waitFor(() => expect(mocked.setSetting).toHaveBeenCalledWith('calibre.mode', 'plugin'))
    await waitFor(() => expect(screen.queryByText('settings.calibre.calibredbMissing.title')).not.toBeInTheDocument())
  })

  it('says nothing when calibredb fails for another reason', async () => {
    mocked.listSettings.mockResolvedValue([{ key: 'calibre.mode', value: 'calibredb' }])
    mocked.testCalibre.mockRejectedValue(Object.assign(new Error('calibredb --version: exit status 1'), { body: { error: 'x' } }))
    render(<CalibreTab />)

    await waitFor(() => expect(mocked.testCalibre).toHaveBeenCalled())
    expect(screen.queryByText('settings.calibre.calibredbMissing.title')).not.toBeInTheDocument()
  })

  it('clears the warning when Test connection then reaches calibredb', async () => {
    mocked.listSettings.mockResolvedValue([{ key: 'calibre.mode', value: 'calibredb' }])
    mocked.testCalibre.mockRejectedValueOnce(missing())
    render(<CalibreTab />)
    expect(await screen.findByText('settings.calibre.calibredbMissing.title')).toBeInTheDocument()

    mocked.testCalibre.mockResolvedValueOnce({ ok: 'true', version: 'calibredb (calibre 8.0)' })
    fireEvent.click(screen.getByText('Test connection'))

    await waitFor(() => expect(screen.queryByText('settings.calibre.calibredbMissing.title')).not.toBeInTheDocument())
  })

  it('sets and clears the warning from the binary path save', async () => {
    mocked.listSettings.mockResolvedValue([{ key: 'calibre.mode', value: 'calibredb' }])
    // The probe on open finds calibredb fine; only the saves speak here.
    mocked.testCalibre.mockResolvedValue({ ok: 'true', version: 'calibredb (calibre 8.0)' })
    render(<CalibreTab />)
    const binary = await screen.findByPlaceholderText('/usr/bin/calibredb') as HTMLInputElement
    await waitFor(() => expect(mocked.testCalibre).toHaveBeenCalled())
    const save = within(binary.parentElement as HTMLElement).getByRole('button')

    const cannotRun = 'calibredb exists but can\'t run in this image ("/opt/calibre/calibredb": no such file or directory).'
    mocked.setSetting.mockResolvedValueOnce({ key: 'calibre.binary_path', value: '/opt/calibre/calibredb', warning: cannotRun, warningCode: 'calibredb_missing' })
    fireEvent.change(binary, { target: { value: '/opt/calibre/calibredb' } })
    fireEvent.click(save)
    expect(await screen.findByText(cannotRun)).toBeInTheDocument()

    mocked.setSetting.mockResolvedValueOnce({ key: 'calibre.binary_path', value: '/usr/bin/calibredb' })
    fireEvent.change(binary, { target: { value: '/usr/bin/calibredb' } })
    fireEvent.click(save)
    await waitFor(() => expect(screen.queryByText('settings.calibre.calibredbMissing.title')).not.toBeInTheDocument())
  })
})
