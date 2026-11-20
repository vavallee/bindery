import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import LogsTab from './LogsTab'
import { api } from '../../api/client'
import { expectFieldValuesWired } from '../../test/fieldValue'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, fallback?: unknown, opts?: Record<string, string>) => {
      if (typeof fallback !== 'string') return key
      return fallback.replace(/\{\{(\w+)\}\}/g, (_, k) => opts?.[k] ?? '')
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
      getLogs: vi.fn().mockResolvedValue([]),
      getLogLevel: vi.fn().mockResolvedValue({ level: 'INFO' }),
      setLogLevel: vi.fn(),
      listSettings: vi.fn(),
      listBackups: vi.fn().mockResolvedValue([]),
      setSetting: vi.fn(),
    },
  }
})

beforeEach(() => {
  vi.mocked(api.listSettings).mockResolvedValue([])
  vi.mocked(api.setSetting).mockReset().mockResolvedValue(undefined as never)
  vi.mocked(api.setLogLevel).mockReset().mockResolvedValue(undefined as never)
})

describe('LogsTab current value subtitles (#3989)', () => {
  it('shows the runtime level and moves it only after the save lands', async () => {
    render(<LogsTab />)
    const select = await screen.findByLabelText('Runtime level')
    await waitFor(() => expect(document.getElementById(select.getAttribute('aria-describedby')!)).toHaveTextContent('Current: INFO'))
    fireEvent.change(select, { target: { value: 'debug' } })
    await waitFor(() => expect(document.getElementById(select.getAttribute('aria-describedby')!)).toHaveTextContent('Current: DEBUG'))
  })

  it('keeps the old runtime level when the save fails', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    vi.mocked(api.setLogLevel).mockRejectedValue(new Error('boom'))
    render(<LogsTab />)
    const select = await screen.findByLabelText('Runtime level')
    await waitFor(() => expect(document.getElementById(select.getAttribute('aria-describedby')!)).toHaveTextContent('Current: INFO'))
    fireEvent.change(select, { target: { value: 'debug' } })
    await waitFor(() => expect(api.setLogLevel).toHaveBeenCalled())
    expect(document.getElementById(select.getAttribute('aria-describedby')!)).toHaveTextContent('Current: INFO')
  })

  it('shows telemetry as On by default and Off after the toggle saves', async () => {
    render(<LogsTab />)
    await screen.findByLabelText('Runtime level')
    const sub = () => document.getElementById('telemetry-enabled-value')!
    await waitFor(() => expect(sub()).toHaveTextContent('Current: On'))
    fireEvent.click(document.querySelector('[aria-describedby="telemetry-enabled-value"]')!)
    await waitFor(() => expect(sub()).toHaveTextContent('Current: Off'))
    expect(api.setSetting).toHaveBeenCalledWith('telemetry.enabled', 'false')
  })

  it('reverts telemetry when the save fails', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    vi.mocked(api.setSetting).mockRejectedValue(new Error('boom'))
    render(<LogsTab />)
    await screen.findByLabelText('Runtime level')
    fireEvent.click(document.querySelector('[aria-describedby="telemetry-enabled-value"]')!)
    await waitFor(() => expect(api.setSetting).toHaveBeenCalled())
    await waitFor(() => expect(document.getElementById('telemetry-enabled-value')).toHaveTextContent('Current: On'))
  })

  it('wires every subtitle on the tab to a control', async () => {
    render(<LogsTab />)
    await screen.findByLabelText('Runtime level')
    expectFieldValuesWired(document.body)
  })
})
