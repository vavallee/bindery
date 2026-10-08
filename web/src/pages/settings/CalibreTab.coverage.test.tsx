import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor, within } from '@testing-library/react'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, opts?: unknown) =>
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

function seed(entries: Record<string, string>) {
  mocked.listSettings.mockResolvedValue(Object.entries(entries).map(([key, value]) => ({ key, value })))
}

// The mocked t returns keys, so the Push all block is found through its
// description key and the button is taken from that block.
function pushAllButton(): HTMLButtonElement {
  const desc = screen.getByText('settings.calibre.pushAll.description')
  const block = desc.closest('.pt-3') as HTMLElement
  return within(block).getByRole('button') as HTMLButtonElement
}

function inputByPlaceholder(placeholder: string): HTMLInputElement {
  return screen.getByPlaceholderText(placeholder) as HTMLInputElement
}

function saveButtonFor(input: HTMLInputElement): HTMLButtonElement {
  return within(input.parentElement as HTMLElement).getByRole('button') as HTMLButtonElement
}

const run = (id: number, status: string, extra: Record<string, unknown> = {}) => ({
  id,
  sourceId: 'calibre',
  libraryPath: '/calibre',
  status,
  dryRun: false,
  startedAt: '2026-09-01T10:00:00Z',
  ...extra,
})

const rollbackResult = (overrides: Record<string, unknown> = {}) => ({
  runId: 7,
  preview: true,
  applied: false,
  dryRun: false,
  status: 'completed',
  stats: { actionsPlanned: 2, entitiesDeleted: 1, provenanceUnlinked: 1, skipped: 0, failed: 0, filesAffected: 0 },
  actions: [
    { entityType: 'book', externalId: 'c-1', localId: 11, action: 'delete', displayName: 'Dune', reason: 'created by run' },
    { entityType: 'author', externalId: 'c-2', localId: 12, action: 'unlink', displayName: '', reason: '' },
  ],
  finishedAt: '',
  ...overrides,
})

describe('CalibreTab coverage', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    seed({})
    mocked.setSetting.mockResolvedValue(undefined)
    mocked.calibreImportStatus.mockResolvedValue({ running: false })
    mocked.calibreSyncStatus.mockResolvedValue({ running: false })
    mocked.calibreRuns.mockResolvedValue([])
    mocked.calibreDeliverySummary.mockResolvedValue({ pending: 0, delivered: 0, failed: 0, skipped: 0, mode: 'plugin', target: {} })
    mocked.calibreDeliveries.mockResolvedValue({ items: [], total: 0 })
    mocked.testCalibre.mockResolvedValue({ ok: 'true', version: 'v1' })
  })

  it('shows a loading state, then the off mode with no connection test', async () => {
    let resolve!: (v: unknown) => void
    mocked.listSettings.mockReturnValue(new Promise(r => { resolve = r }))
    render(<CalibreTab />)
    expect(screen.getByText(/Loading/)).toBeInTheDocument()
    resolve([])
    expect(await screen.findByDisplayValue('off')).toBeChecked()
    expect(screen.queryByText('Test connection')).not.toBeInTheDocument()
    expect(screen.queryByTestId('calibre-delivery-panel')).not.toBeInTheDocument()
    expect(mocked.testCalibre).not.toHaveBeenCalled()
  })

  it('renders a legacy enabled install as calibredb and saves the mode change', async () => {
    seed({ 'calibre.enabled': 'true' })
    render(<CalibreTab />)
    expect(await screen.findByDisplayValue('calibredb')).toBeChecked()
    expect(screen.getByText('Binary path (optional)')).toBeInTheDocument()

    fireEvent.click(screen.getByDisplayValue('plugin'))
    await waitFor(() => expect(mocked.setSetting).toHaveBeenCalledWith('calibre.mode', 'plugin'))
    expect(screen.getByTestId('calibre-transport')).toBeInTheDocument()
    expect(screen.queryByText('Binary path (optional)')).not.toBeInTheDocument()
  })

  it('tests calibredb and reports success and failure', async () => {
    seed({ 'calibre.mode': 'calibredb' })
    render(<CalibreTab />)
    // calibredb mode probes once on its own when the tab opens (#1940).
    await waitFor(() => expect(mocked.testCalibre).toHaveBeenCalledTimes(1))
    mocked.testCalibre.mockResolvedValueOnce({ ok: 'true', version: '', message: 'calibredb 7.0' })
    fireEvent.click(await screen.findByText('Test connection'))
    expect(await screen.findByText('✓ calibredb reachable — calibredb 7.0')).toBeInTheDocument()

    mocked.testCalibre.mockResolvedValueOnce({ ok: 'true' })
    fireEvent.click(screen.getByText('Test connection'))
    expect(await screen.findByText('✓ calibredb reachable')).toBeInTheDocument()

    mocked.testCalibre.mockRejectedValueOnce(new Error('not on PATH'))
    fireEvent.click(screen.getByText('Test connection'))
    expect(await screen.findByText('✗ calibredb unreachable — not on PATH')).toBeInTheDocument()
    expect(screen.queryByTestId('calibre-test-warning')).not.toBeInTheDocument()
  })

  it('saves the binary path and the library path, and shows a save failure', async () => {
    seed({ 'calibre.mode': 'calibredb' })
    render(<CalibreTab />)
    const binary = await screen.findByPlaceholderText('/usr/bin/calibredb') as HTMLInputElement
    fireEvent.change(binary, { target: { value: '/opt/calibre/calibredb' } })
    fireEvent.click(saveButtonFor(binary))
    await waitFor(() => expect(mocked.setSetting).toHaveBeenCalledWith('calibre.binary_path', '/opt/calibre/calibredb'))

    mocked.setSetting.mockRejectedValueOnce(new Error('path does not exist'))
    const library = inputByPlaceholder('/data/calibre-library')
    fireEvent.change(library, { target: { value: '/nope' } })
    fireEvent.click(saveButtonFor(library))
    expect(await screen.findByText('path does not exist')).toBeInTheDocument()
    expect(mocked.setSetting).toHaveBeenCalledWith('calibre.library_path', '/nope')
  })

  it('saves the plugin URL, API key, remap and CWA path', async () => {
    seed({ 'calibre.mode': 'plugin' })
    render(<CalibreTab />)
    const url = await screen.findByPlaceholderText('http://calibre.default.svc:8099') as HTMLInputElement
    // No plugin URL yet: the bridge is not probed and Push all is blocked.
    expect(mocked.testCalibre).not.toHaveBeenCalled()
    expect(pushAllButton()).toBeDisabled()
    expect(pushAllButton()).toHaveAttribute('title', 'settings.calibre.pushAll.needPluginUrl')

    fireEvent.change(url, { target: { value: 'http://calibre:8099' } })
    fireEvent.click(saveButtonFor(url))
    await waitFor(() => expect(mocked.setSetting).toHaveBeenCalledWith('calibre.plugin_url', 'http://calibre:8099'))

    const key = inputByPlaceholder('plugin api key')
    fireEvent.change(key, { target: { value: 's3cret' } })
    fireEvent.click(saveButtonFor(key))
    await waitFor(() => expect(mocked.setSetting).toHaveBeenCalledWith('calibre.plugin_api_key', 's3cret'))

    const remap = inputByPlaceholder('/books:/mnt/user/media/books')
    fireEvent.change(remap, { target: { value: '/books:/mnt/books' } })
    fireEvent.click(saveButtonFor(remap))
    await waitFor(() => expect(mocked.setSetting).toHaveBeenCalledWith('calibre.push_path_remap', '/books:/mnt/books'))

    const cwa = inputByPlaceholder('/cwa-book-ingest')
    fireEvent.change(cwa, { target: { value: '/ingest' } })
    fireEvent.click(saveButtonFor(cwa))
    await waitFor(() => expect(mocked.setSetting).toHaveBeenCalledWith('cwa.ingest_path', '/ingest'))
  })

  it('warns when the bridge is unreachable and a manual test clears it', async () => {
    seed({ 'calibre.mode': 'plugin', 'calibre.plugin_url': 'http://calibre:8099' })
    mocked.testCalibre.mockRejectedValueOnce(new Error('refused'))
    render(<CalibreTab />)
    expect(await screen.findByText('settings.calibre.pushAll.bridgeUnreachable')).toBeInTheDocument()

    mocked.testCalibre.mockResolvedValueOnce({ ok: 'true', version: '' , message: 'plugin reachable' })
    fireEvent.click(screen.getByText('Test connection'))
    expect(await screen.findByText('✓ Plugin reachable — plugin reachable')).toBeInTheDocument()
    expect(screen.queryByText('settings.calibre.pushAll.bridgeUnreachable')).not.toBeInTheDocument()
  })

  it('reports a failed check-in lookup in pull mode', async () => {
    seed({ 'calibre.mode': 'plugin', 'calibre.plugin_transport': 'pull' })
    render(<CalibreTab />)
    await screen.findByTestId('calibre-transport')
    mocked.calibreDeliverySummary.mockRejectedValueOnce(new Error('offline'))
    fireEvent.click(screen.getByText('Test connection'))
    expect(await screen.findByText(/settings\.calibre\.transport\.testFailed .*offline/)).toBeInTheDocument()
  })

  it('reports the last check in without a plugin version', async () => {
    seed({ 'calibre.mode': 'plugin', 'calibre.plugin_transport': 'pull' })
    mocked.calibreDeliverySummary.mockResolvedValue({
      pending: 0, delivered: 0, failed: 0, skipped: 0, mode: 'plugin', target: {}, transport: 'pull',
      pull: { lastSeen: '2026-09-27T10:00:00Z' },
    })
    render(<CalibreTab />)
    fireEvent.click(await screen.findByText('Test connection'))
    expect(await screen.findByText(/settings\.calibre\.transport\.testLastCheckIn \{"time"/)).toBeInTheDocument()
  })

  it('starts Push all, shows the modal, and closes it', async () => {
    seed({ 'calibre.mode': 'plugin', 'calibre.plugin_url': 'http://calibre:8099' })
    mocked.calibreSyncStart.mockResolvedValue({
      running: false,
      finishedAt: '2026-09-27T12:00:00Z',
      message: 'done',
      stats: { total: 2, processed: 2, pushed: 1, alreadyInCalibre: 1, failed: 0, skipped: 0 },
      errors: [],
      skips: [],
    })
    render(<CalibreTab />)
    await screen.findByTestId('calibre-transport')
    await waitFor(() => expect(pushAllButton()).toBeEnabled())
    fireEvent.click(pushAllButton())
    const dialog = await screen.findByRole('dialog')
    expect(mocked.calibreSyncStart).toHaveBeenCalledTimes(1)
    expect(await within(dialog).findByText(/settings\.calibre\.pushAll\.done \{"pushed":1,"already":1/)).toBeInTheDocument()
    // The header X carries the same name; the footer button is the last.
    fireEvent.click(within(dialog).getAllByRole('button', { name: 'settings.calibre.pushAll.close' }).at(-1)!)
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  })

  it('shows why Push all failed to start', async () => {
    seed({ 'calibre.mode': 'plugin', 'calibre.plugin_url': 'http://calibre:8099' })
    mocked.calibreSyncStart.mockRejectedValue(new Error('already running'))
    render(<CalibreTab />)
    await screen.findByTestId('calibre-transport')
    fireEvent.click(pushAllButton())
    const dialog = await screen.findByRole('dialog')
    expect(await within(dialog).findByText('already running')).toBeInTheDocument()
  })

  it('toggles library import and sync on startup', async () => {
    render(<CalibreTab />)
    const toggle = await screen.findByTitle('Enable library import')
    expect(screen.queryByText('Sync on startup')).not.toBeInTheDocument()
    fireEvent.click(toggle)
    await waitFor(() => expect(mocked.setSetting).toHaveBeenCalledWith('calibre.library_import_enabled', 'true'))
    expect(screen.getByText('Sync on startup')).toBeInTheDocument()
    expect(screen.getByText('Never imported.')).toBeInTheDocument()
    expect(screen.getByText('settings.calibre.runs.empty')).toBeInTheDocument()
    // No library path: the import cannot start.
    expect(screen.getByRole('button', { name: 'Import library' })).toBeDisabled()

    fireEvent.click(screen.getByTitle('Enable'))
    await waitFor(() => expect(mocked.setSetting).toHaveBeenCalledWith('calibre.sync_on_startup', 'true'))
    fireEvent.click(screen.getByTitle('Disable'))
    await waitFor(() => expect(mocked.setSetting).toHaveBeenCalledWith('calibre.sync_on_startup', 'false'))
    fireEvent.click(screen.getByTitle('Disable library import'))
    await waitFor(() => expect(mocked.setSetting).toHaveBeenCalledWith('calibre.library_import_enabled', 'false'))
    expect(screen.queryByText('Sync on startup')).not.toBeInTheDocument()
  })

  it('starts a library import and shows its progress', async () => {
    seed({
      'calibre.library_import_enabled': 'true',
      'calibre.library_path': '/calibre',
      'calibre.last_import_at': '2026-09-01T10:00:00Z',
    })
    mocked.calibreImportStart.mockResolvedValue({ running: true, processed: 5, total: 10, message: 'Reading metadata.db' })
    render(<CalibreTab />)
    expect(await screen.findByText(/Last import:/)).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Import library' }))
    expect(await screen.findByText('Reading metadata.db')).toBeInTheDocument()
    expect(screen.getByText('5 / 10')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Importing…' })).toBeDisabled()
  })

  it('shows an import that failed to start', async () => {
    seed({ 'calibre.library_import_enabled': 'true', 'calibre.library_path': '/calibre' })
    mocked.calibreImportStart.mockRejectedValue(new Error('metadata.db not found'))
    render(<CalibreTab />)
    fireEvent.click(await screen.findByRole('button', { name: 'Import library' }))
    expect(await screen.findByText('metadata.db not found')).toBeInTheDocument()
  })

  it('shows the result of a finished import and a failed one', async () => {
    seed({ 'calibre.library_import_enabled': 'true', 'calibre.library_path': '/calibre' })
    mocked.calibreImportStatus.mockResolvedValueOnce({
      running: false, processed: 3, total: 3,
      stats: { authorsAdded: 2, booksAdded: 3, editionsAdded: 4, duplicatesMerged: 1, skipped: 0 },
    })
    const { unmount } = render(<CalibreTab />)
    expect(await screen.findByText(/Import complete/)).toBeInTheDocument()
    // A finished import refreshes the run list.
    await waitFor(() => expect(mocked.calibreRuns.mock.calls.length).toBeGreaterThanOrEqual(2))
    unmount()

    mocked.calibreImportStatus.mockResolvedValueOnce({ running: false, processed: 0, total: 0, error: 'disk full' })
    render(<CalibreTab />)
    expect(await screen.findByText('Import failed: disk full')).toBeInTheDocument()
  })

  it('lists recent runs and applies a rollback', async () => {
    seed({ 'calibre.library_import_enabled': 'true', 'calibre.library_path': '/calibre' })
    mocked.calibreRuns.mockResolvedValue([
      run(7, 'completed'),
      run(6, 'running'),
      run(5, 'rolled_back', { libraryPath: '' }),
      run(4, 'failed'),
      run(3, 'weird'),
    ])
    mocked.calibreRunRollbackPreview.mockResolvedValue(rollbackResult({
      stats: { actionsPlanned: 2, entitiesDeleted: 1, provenanceUnlinked: 1, skipped: 0, failed: 0, filesAffected: 3 },
    }))
    mocked.calibreRunRollback.mockResolvedValue(rollbackResult({ applied: true, preview: false }))
    render(<CalibreTab />)

    expect(await screen.findByText(/settings\.calibre\.runs\.runLabel \{"runId":7,"status":"settings\.calibre\.runs\.statusCompleted"\}/)).toBeInTheDocument()
    expect(screen.getByText(/"runId":6,"status":"settings\.calibre\.runs\.statusRunning"/)).toBeInTheDocument()
    expect(screen.getByText(/"runId":4,"status":"settings\.calibre\.runs\.statusFailed"/)).toBeInTheDocument()
    expect(screen.getByText(/"runId":3,"status":"weird"/)).toBeInTheDocument()

    const rollbackButtons = screen.getAllByRole('button', { name: 'settings.calibre.runs.rollback' })
    // Completed, failed and unknown runs can roll back; the running one cannot.
    expect(rollbackButtons).toHaveLength(4)
    expect(rollbackButtons[1]).toBeDisabled()
    expect(screen.getByRole('button', { name: 'settings.calibre.runs.rolledBack' })).toBeDisabled()

    mocked.calibreRuns.mockClear()
    fireEvent.click(rollbackButtons[0])
    const dialog = await screen.findByRole('dialog')
    expect(mocked.calibreRunRollbackPreview).toHaveBeenCalledWith(7)
    expect(await within(dialog).findByText('Dune')).toBeInTheDocument()
    expect(within(dialog).getByText('c-2')).toBeInTheDocument()
    expect(within(dialog).getByText('settings.calibre.runs.filesOnDiskWarning')).toBeInTheDocument()

    fireEvent.click(within(dialog).getByRole('button', { name: 'settings.calibre.runs.applyRollback' }))
    expect(await within(dialog).findByText(/settings\.calibre\.runs\.appliedSummary/)).toBeInTheDocument()
    expect(mocked.calibreRunRollback).toHaveBeenCalledWith(7)
    expect(mocked.calibreRuns).toHaveBeenCalled()
    expect(within(dialog).queryByRole('button', { name: 'settings.calibre.runs.applyRollback' })).not.toBeInTheDocument()

    fireEvent.click(within(dialog).getByRole('button', { name: 'settings.calibre.runs.cancel' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  })

  it('blocks a rollback whose preview failed, and shows an apply failure', async () => {
    seed({ 'calibre.library_import_enabled': 'true', 'calibre.library_path': '/calibre' })
    mocked.calibreRuns.mockResolvedValue([run(7, 'completed'), run(8, 'completed')])
    mocked.calibreRunRollbackPreview.mockRejectedValueOnce(new Error('run not found'))
    render(<CalibreTab />)

    const buttons = await screen.findAllByRole('button', { name: 'settings.calibre.runs.rollback' })
    fireEvent.click(buttons[0])
    let dialog = await screen.findByRole('dialog')
    expect(await within(dialog).findByText(/settings\.calibre\.runs\.error .*run not found/)).toBeInTheDocument()
    expect(within(dialog).getByRole('button', { name: 'settings.calibre.runs.applyRollback' })).toBeDisabled()
    fireEvent.click(within(dialog).getByRole('button', { name: 'common.close' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())

    // A preview with nothing to undo keeps Apply disabled (see
    // CalibreRollbackModal.empty.test.tsx), so the apply failure needs one.
    mocked.calibreRunRollbackPreview.mockResolvedValueOnce(rollbackResult({
      actions: [{ entityType: 'book', externalId: 'c-9', localId: 19, action: 'delete', displayName: 'Emma', reason: '' }],
      runId: 8,
    }))
    mocked.calibreRunRollback.mockRejectedValueOnce('locked')
    fireEvent.click(screen.getAllByRole('button', { name: 'settings.calibre.runs.rollback' })[1])
    dialog = await screen.findByRole('dialog')
    expect(await within(dialog).findByText('Emma')).toBeInTheDocument()
    fireEvent.click(within(dialog).getByRole('button', { name: 'settings.calibre.runs.applyRollback' }))
    expect(await within(dialog).findByText(/settings\.calibre\.runs\.error .*locked/)).toBeInTheDocument()
    expect(mocked.calibreRunRollback).toHaveBeenCalledWith(8)
  })

  it('refreshes the run list on demand', async () => {
    seed({ 'calibre.library_import_enabled': 'true' })
    render(<CalibreTab />)
    const refresh = await screen.findByRole('button', { name: 'settings.calibre.runs.refresh' })
    await waitFor(() => expect(mocked.calibreRuns).toHaveBeenCalled())
    const before = mocked.calibreRuns.mock.calls.length
    fireEvent.click(refresh)
    await waitFor(() => expect(mocked.calibreRuns.mock.calls.length).toBe(before + 1))
    expect(mocked.calibreRuns).toHaveBeenLastCalledWith(10)
  })
})
