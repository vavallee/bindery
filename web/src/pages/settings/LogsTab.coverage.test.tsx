import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, waitFor, fireEvent, within } from '@testing-library/react'
import LogsTab from './LogsTab'
import { api } from '../../api/client'
import type { LogEntry } from '../../api/client'
import { acceptConfirm, cancelConfirm } from '../../test-utils'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, opts?: unknown) => {
      if (opts && typeof opts === 'object' && 'page' in opts) return `${key}:${(opts as { page: number }).page}`
      return key
    },
    i18n: { changeLanguage: vi.fn(), resolvedLanguage: 'en' },
  }),
}))
vi.mock('../../api/client', async importOriginal => {
  const actual = await importOriginal<typeof import('../../api/client')>()
  return {
    ...actual,
    api: {
      ...actual.api,
      getLogs: vi.fn(),
      getLogLevel: vi.fn(),
      setLogLevel: vi.fn(),
      listSettings: vi.fn(),
      setSetting: vi.fn(),
      listBackups: vi.fn(),
      createBackup: vi.fn(),
      deleteBackup: vi.fn(),
    },
  }
})

let alertSpy: ReturnType<typeof vi.spyOn>

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(api.getLogs).mockResolvedValue([])
  vi.mocked(api.getLogLevel).mockResolvedValue({ level: 'WARN' })
  vi.mocked(api.setLogLevel).mockResolvedValue(undefined as never)
  vi.mocked(api.listSettings).mockResolvedValue([])
  vi.mocked(api.setSetting).mockResolvedValue(undefined)
  vi.mocked(api.listBackups).mockResolvedValue([])
  vi.mocked(api.deleteBackup).mockResolvedValue(undefined as never)
  alertSpy = vi.spyOn(window, 'alert').mockImplementation(() => {})
})

afterEach(() => {
  alertSpy.mockRestore()
})

function lastGetLogsArgs() {
  const calls = vi.mocked(api.getLogs).mock.calls
  return calls[calls.length - 1][0]
}

function pauseAutoRefresh() {
  fireEvent.click(screen.getByRole('button', { name: /settings\.logs\.autoRefresh/ }))
}

describe('LogsTab log view', () => {
  it('shows the empty state and the runtime level from the server', async () => {
    render(<LogsTab />)
    expect(await screen.findByText('settings.logs.noEntries')).toBeInTheDocument()
    await waitFor(() => expect(screen.getByTitle('Controls which log levels are written to the database')).toHaveValue('warn'))
  })

  it('renders both DB and ring-buffer entry shapes, quoting attrs with spaces', async () => {
    const entries: LogEntry[] = [
      { id: 1, ts: '2026-02-03T04:05:06Z', level: 'ERROR', component: 'importer', message: 'move failed', fields: { path: '/a b', n: '3', q: 'say "hi"' } },
      { time: '2026-02-03T04:05:07Z', level: 'WARN', msg: 'slow indexer', attrs: { idx: 'nzb' } },
      { ts: '2026-02-03T04:05:07Z', level: 'DEBUG', message: 'debug line' },
      { ts: '2026-02-03T04:05:08Z', level: 'INFO', message: 'ok' },
    ]
    vi.mocked(api.getLogs).mockResolvedValue(entries)
    render(<LogsTab />)
    expect(await screen.findByText('move failed')).toBeInTheDocument()
    expect(screen.getByText('path="/a b" n=3 q="say \\"hi\\""')).toBeInTheDocument()
    expect(screen.getByText('slow indexer')).toBeInTheDocument()
    expect(screen.getByText('idx=nzb')).toBeInTheDocument()
    expect(screen.getByText('debug line')).toBeInTheDocument()
    expect(screen.getByText('importer')).toBeInTheDocument()
  })

  it('pages forward and back through a full page of results', async () => {
    const full: LogEntry[] = Array.from({ length: 200 }, (_, i) => ({ id: i + 1, ts: '2026-02-03T04:05:06Z', level: 'INFO', message: `m${i}` }))
    vi.mocked(api.getLogs).mockResolvedValue(full)
    render(<LogsTab />)
    await screen.findByText('m0')
    pauseAutoRefresh()
    const prev = screen.getByRole('button', { name: /common\.prev/ })
    const next = screen.getByRole('button', { name: /common\.next/ })
    expect(prev).toBeDisabled()
    expect(next).toBeEnabled()
    fireEvent.click(next)
    await waitFor(() => expect(lastGetLogsArgs()).toMatchObject({ limit: 200, offset: 200 }))
    expect(await screen.findByText('settings.logs.page:2')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: /common\.prev/ }))
    await waitFor(() => expect(lastGetLogsArgs()).toMatchObject({ offset: 0 }))
    expect(await screen.findByText('settings.logs.page:1')).toBeInTheDocument()
  })

  it('applies date range filters as RFC3339 and clears every filter', async () => {
    render(<LogsTab />)
    await screen.findByText('settings.logs.noEntries')
    pauseAutoRefresh()
    fireEvent.change(screen.getByLabelText('settings.logs.from'), { target: { value: '2026-08-12T14:03' } })
    fireEvent.change(screen.getByLabelText('settings.logs.to'), { target: { value: '2026-08-13T09:00' } })
    fireEvent.click(screen.getByRole('button', { name: 'WARN' }))
    await waitFor(() => expect(lastGetLogsArgs()).toMatchObject({
      level: 'warn',
      from: new Date('2026-08-12T14:03').toISOString(),
      to: new Date('2026-08-13T09:00').toISOString(),
    }))

    fireEvent.click(screen.getByRole('button', { name: 'settings.logs.clearFilters' }))
    await waitFor(() => expect(lastGetLogsArgs()).toEqual({ limit: 200, offset: 0 }))
    expect(screen.getByLabelText('settings.logs.from')).toHaveValue('')

    fireEvent.click(screen.getByRole('button', { name: 'ALL' }))
    await waitFor(() => expect(lastGetLogsArgs()).toMatchObject({ level: undefined }))
    const before = vi.mocked(api.getLogs).mock.calls.length
    fireEvent.click(screen.getByRole('button', { name: 'common.refresh' }))
    await waitFor(() => expect(api.getLogs).toHaveBeenCalledTimes(before + 1))
  })

  it('changes the runtime log level', async () => {
    render(<LogsTab />)
    const select = screen.getByTitle('Controls which log levels are written to the database')
    await waitFor(() => expect(select).toHaveValue('warn'))
    fireEvent.change(select, { target: { value: 'debug' } })
    await waitFor(() => expect(api.setLogLevel).toHaveBeenCalledWith('debug'))
    await waitFor(() => expect(select).toHaveValue('debug'))
  })

  it('toggles auto-refresh off and back on', async () => {
    render(<LogsTab />)
    const btn = screen.getByRole('button', { name: /settings\.logs\.autoRefresh/ })
    expect(btn.textContent).toContain('⏸')
    fireEvent.click(btn)
    expect(btn.textContent).toContain('▶')
    fireEvent.click(btn)
    expect(btn.textContent).toContain('⏸')
  })

  it('survives failed initial loads', async () => {
    const err = vi.spyOn(console, 'error').mockImplementation(() => {})
    vi.mocked(api.getLogLevel).mockRejectedValue(new Error('a'))
    vi.mocked(api.getLogs).mockRejectedValue(new Error('b'))
    vi.mocked(api.listSettings).mockRejectedValue(new Error('c'))
    vi.mocked(api.listBackups).mockRejectedValue(new Error('d'))
    render(<LogsTab />)
    await waitFor(() => expect(err.mock.calls.length).toBeGreaterThanOrEqual(4))
    expect(screen.getByText('settings.logs.noEntries')).toBeInTheDocument()
    err.mockRestore()
  })
})

describe('LogsTab retention and telemetry', () => {
  it('saves the log retention setting', async () => {
    vi.mocked(api.listSettings).mockResolvedValue([{ key: 'log.retention_days', value: '30' }] as Awaited<ReturnType<typeof api.listSettings>>)
    render(<LogsTab />)
    const input = await screen.findByDisplayValue('30')
    fireEvent.change(input, { target: { value: '7' } })
    fireEvent.click(screen.getByTestId('save-log-retention'))
    await waitFor(() => expect(api.setSetting).toHaveBeenCalledWith('log.retention_days', '7'))
  })

  it('surfaces a failed retention save', async () => {
    const err = vi.spyOn(console, 'error').mockImplementation(() => {})
    vi.mocked(api.setSetting).mockRejectedValue(new Error('nope'))
    vi.mocked(api.listSettings).mockResolvedValue([{ key: 'log.retention_days', value: '21' }] as Awaited<ReturnType<typeof api.listSettings>>)
    render(<LogsTab />)
    await screen.findByDisplayValue('21')
    fireEvent.click(screen.getByTestId('save-log-retention'))
    await waitFor(() => expect(api.setSetting).toHaveBeenCalledWith('log.retention_days', '21'))
    await waitFor(() => expect(screen.getByTestId('save-log-retention').textContent).toMatch(/error/i))
    err.mockRestore()
  })

  it('toggles telemetry', async () => {
    render(<LogsTab />)
    const toggle = await screen.findByRole('switch')
    expect(toggle).toHaveAttribute('aria-checked', 'true')
    fireEvent.click(toggle)
    await waitFor(() => expect(api.setSetting).toHaveBeenCalledWith('telemetry.enabled', 'false'))
    expect(toggle).toHaveAttribute('aria-checked', 'false')
    fireEvent.click(toggle)
    await waitFor(() => expect(api.setSetting).toHaveBeenCalledWith('telemetry.enabled', 'true'))
  })
})

describe('LogsTab backups', () => {
  const now = Date.now()
  const iso = (msAgo: number) => new Date(now - msAgo).toISOString()

  it('lists existing backups with relative ages', async () => {
    vi.mocked(api.listBackups).mockResolvedValue([
      { name: 'a.db', size: 1024, modTime: iso(10_000) },
      { name: 'b.db', size: 2048, modTime: iso(5 * 60_000) },
      { name: 'c.db', size: 0, modTime: iso(3 * 3600_000) },
      { name: 'd.db', size: 10, modTime: iso(4 * 86400_000) },
      { name: 'e.db', size: 10, modTime: new Date(now + 3 * 3600_000).toISOString() },
      { name: 'f.db', size: 10, modTime: new Date(now + 5 * 60_000).toISOString() },
      { name: 'g.db', size: 10, modTime: new Date(now + 3 * 86400_000).toISOString() },
      { name: 'h.db', size: 10, modTime: new Date(now + 20_000).toISOString() },
      { name: 'i.db', size: 10, modTime: 'not a date' },
    ])
    render(<LogsTab />)
    expect(await screen.findByText('a.db')).toBeInTheDocument()
    const row = (name: string) => screen.getByText(name).closest('li')!
    expect(row('a.db').textContent).toContain('just now')
    expect(row('b.db').textContent).toContain('5m ago')
    expect(row('c.db').textContent).toContain('3h ago')
    expect(row('d.db').textContent).toContain('4d ago')
    expect(row('e.db').textContent).toMatch(/in [23]h/)
    expect(row('f.db').textContent).toMatch(/in [45]m/)
    expect(row('g.db').textContent).toMatch(/in [23]d/)
    expect(row('h.db').textContent).toContain('in a moment')
  })

  it('creates a labelled backup and adds it to the list', async () => {
    vi.mocked(api.createBackup).mockResolvedValue({ name: 'labelled.db', size: 100, modTime: new Date().toISOString() })
    render(<LogsTab />)
    await screen.findByText('settings.logs.noEntries')
    fireEvent.change(screen.getByPlaceholderText('settings.general.backupLabelPlaceholder'), { target: { value: '  pre-upgrade ' } })
    fireEvent.click(screen.getByRole('button', { name: 'settings.general.backupButton' }))
    await waitFor(() => expect(api.createBackup).toHaveBeenCalledWith('pre-upgrade'))
    expect(await screen.findByText('labelled.db')).toBeInTheDocument()
    expect(alertSpy).toHaveBeenCalledWith('Backup created: labelled.db')
    expect(screen.getByPlaceholderText('settings.general.backupLabelPlaceholder')).toHaveValue('')
  })

  it('sends no label for a blank one and reports a failure', async () => {
    vi.mocked(api.createBackup).mockRejectedValueOnce(new Error('disk full'))
    vi.mocked(api.createBackup).mockRejectedValueOnce('odd')
    render(<LogsTab />)
    await screen.findByText('settings.logs.noEntries')
    fireEvent.click(screen.getByRole('button', { name: 'settings.general.backupButton' }))
    await waitFor(() => expect(alertSpy).toHaveBeenCalledWith('Backup failed: disk full'))
    expect(api.createBackup).toHaveBeenCalledWith(undefined)
    await waitFor(() => expect(screen.getByRole('button', { name: 'settings.general.backupButton' })).toBeEnabled())
    fireEvent.click(screen.getByRole('button', { name: 'settings.general.backupButton' }))
    await waitFor(() => expect(alertSpy).toHaveBeenCalledWith('Backup failed: Unknown error'))
  })

  it('deletes a backup only after confirmation', async () => {
    vi.mocked(api.listBackups).mockResolvedValue([{ name: 'old.db', size: 1, modTime: iso(1000) }])
    render(<LogsTab />)
    const row = (await screen.findByText('old.db')).closest('li')!
    fireEvent.click(within(row).getByRole('button'))
    await cancelConfirm()
    expect(api.deleteBackup).not.toHaveBeenCalled()

    fireEvent.click(within(row).getByRole('button'))
    await acceptConfirm()
    await waitFor(() => expect(api.deleteBackup).toHaveBeenCalledWith('old.db'))
    await waitFor(() => expect(screen.queryByText('old.db')).not.toBeInTheDocument())
  })

  it('reports a failed delete and keeps the backup', async () => {
    vi.mocked(api.listBackups).mockResolvedValue([{ name: 'old.db', size: 1, modTime: iso(1000) }])
    vi.mocked(api.deleteBackup).mockRejectedValueOnce(new Error('locked'))
    vi.mocked(api.deleteBackup).mockRejectedValueOnce(42)
    render(<LogsTab />)
    const row = (await screen.findByText('old.db')).closest('li')!
    fireEvent.click(within(row).getByRole('button'))
    await acceptConfirm()
    await waitFor(() => expect(alertSpy).toHaveBeenCalledWith('Delete failed: locked'))
    expect(screen.getByText('old.db')).toBeInTheDocument()
    await waitFor(() => expect(within(row).getByRole('button')).toBeEnabled())
    fireEvent.click(within(row).getByRole('button'))
    await acceptConfirm()
    await waitFor(() => expect(alertSpy).toHaveBeenCalledWith('Delete failed: Unknown error'))
  })
})
