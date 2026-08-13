import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import LogsTab from './LogsTab'
import { api } from '../../api/client'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, fallback?: unknown) => (typeof fallback === 'string' ? fallback : key),
    i18n: { changeLanguage: vi.fn(), resolvedLanguage: 'en' },
  }),
}))
// Only the network calls are stubbed — logExportURL stays real so the test
// covers the query string the download link actually produces.
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

beforeEach(() => {
  vi.mocked(api.getLogs).mockResolvedValue([])
  vi.mocked(api.getLogLevel).mockResolvedValue({ level: 'INFO' })
  vi.mocked(api.listSettings).mockResolvedValue([])
  vi.mocked(api.listBackups).mockResolvedValue([])
})

function downloadLink(): HTMLAnchorElement {
  return screen.getByTestId('download-logs') as HTMLAnchorElement
}

describe('LogsTab log download (#1903)', () => {
  it('offers an unfiltered export link by default', async () => {
    render(<LogsTab />)
    await waitFor(() => expect(api.getLogs).toHaveBeenCalled())

    const link = downloadLink()
    expect(link.getAttribute('href')).toBe('/api/v1/system/logs/export')
    // A real download, not a navigation.
    expect(link.hasAttribute('download')).toBe(true)
  })

  it('carries the applied filters into the export URL', async () => {
    render(<LogsTab />)
    await waitFor(() => expect(api.getLogs).toHaveBeenCalled())

    // The level pill, not the same-named runtime-level <option>.
    fireEvent.click(screen.getByRole('button', { name: 'ERROR' }))
    fireEvent.change(screen.getByPlaceholderText('settings.logs.componentPlaceholder'), { target: { value: 'importer' } })
    fireEvent.change(screen.getByPlaceholderText('settings.logs.searchPlaceholder'), { target: { value: 'hardlink' } })

    await waitFor(() => {
      const href = downloadLink().getAttribute('href') ?? ''
      expect(href).toContain('level=error')
      expect(href).toContain('component=importer')
      expect(href).toContain('q=hardlink')
    })
  })

  it('sends the date range as RFC3339 to both the list and the export', async () => {
    render(<LogsTab />)
    await waitFor(() => expect(api.getLogs).toHaveBeenCalled())

    // <input type="datetime-local"> yields a zone-less value the API rejects;
    // it has to be converted before it reaches either request.
    fireEvent.change(screen.getByLabelText('settings.logs.from'), { target: { value: '2026-08-12T14:03' } })
    const expected = new Date('2026-08-12T14:03').toISOString()

    await waitFor(() => expect(downloadLink().getAttribute('href')).toContain(`from=${encodeURIComponent(expected)}`))

    vi.mocked(api.getLogs).mockClear()
    fireEvent.click(screen.getByText('common.search'))
    await waitFor(() => expect(api.getLogs).toHaveBeenCalledWith(expect.objectContaining({ from: expected })))
  })

  it('states the export cap next to the log output', async () => {
    render(<LogsTab />)
    await waitFor(() => expect(api.getLogs).toHaveBeenCalled())
    // Truncation is allowed; silent truncation is not.
    expect(screen.getByText(/settings\.logs\.downloadNote/)).toBeTruthy()
  })
})
