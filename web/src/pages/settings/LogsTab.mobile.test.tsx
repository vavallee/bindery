import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import LogsTab from './LogsTab'
import { api } from '../../api/client'
import type { LogEntry } from '../../api/client'
import { mockMatchMedia } from '../../test-utils'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string) => key,
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
      listSettings: vi.fn(),
      listBackups: vi.fn(),
    },
  }
})

const entries: LogEntry[] = [
  { id: 1, ts: '2026-02-03T04:05:06Z', level: 'ERROR', component: 'importer', message: 'move failed', fields: { path: '/a b' } },
  { id: 2, ts: '2026-02-03T04:05:07Z', level: 'INFO', message: 'ok' },
]

let restore: () => void = () => {}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(api.getLogs).mockResolvedValue(entries)
  vi.mocked(api.getLogLevel).mockResolvedValue({ level: 'INFO' })
  vi.mocked(api.listSettings).mockResolvedValue([])
  vi.mocked(api.listBackups).mockResolvedValue([])
})

afterEach(() => restore())

// The five column fixed table gave the message column what was left after
// time, level, component and a 40% attrs column: about nothing on a phone.
describe('LogsTab on a phone', () => {
  it('renders each entry as a stacked block below sm', async () => {
    restore = mockMatchMedia(q => q.includes('40rem'))
    render(<LogsTab />)
    expect(await screen.findByText('move failed')).toBeInTheDocument()
    expect(screen.queryByRole('table')).not.toBeInTheDocument()
    const blocks = screen.getAllByTestId('log-entry')
    expect(blocks).toHaveLength(2)
    expect(blocks[0]).toHaveTextContent('ERROR')
    expect(blocks[0]).toHaveTextContent('importer')
    expect(blocks[0]).toHaveTextContent('path="/a b"')
  })

  it('keeps the table from sm up', async () => {
    restore = mockMatchMedia(false)
    render(<LogsTab />)
    expect(await screen.findByText('move failed')).toBeInTheDocument()
    expect(screen.getByRole('table')).toBeInTheDocument()
    expect(screen.queryByTestId('log-entry')).not.toBeInTheDocument()
  })

  it('caps the log pane in dynamic viewport units', async () => {
    render(<LogsTab />)
    const message = await screen.findByText('move failed')
    const pane = message.closest('.overflow-auto') as HTMLElement
    expect(pane.className).toContain('max-h-[60dvh]')
    expect(pane.className).not.toContain('max-h-[60vh]')
  })

  // The level pills and the backup row could not wrap, so together they
  // widened a phone page to about 406px.
  it('lets the level filter and the backup row wrap', async () => {
    render(<LogsTab />)
    await screen.findByText('move failed')
    expect(screen.getByTestId('log-level-filter').className).toContain('flex-wrap')
    const backup = screen.getByTestId('backup-create-row')
    expect(backup.className).toContain('flex-col')
    expect(backup.className).toContain('sm:flex-row')
    const controls = screen.getByPlaceholderText('settings.general.backupLabelPlaceholder').parentElement!
    expect(controls.className).toContain('flex-wrap')
    expect(controls.className.split(/\s+/)).not.toContain('flex-shrink-0')
  })
})
