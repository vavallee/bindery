import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import ApiKeysTab from './ApiKeysTab'
import { api } from '../../api/client'
import { expectFieldValuesWired } from '../../test/fieldValue'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, fallback?: unknown, opts?: Record<string, string>) => {
      if (typeof fallback !== 'string') return key
      return fallback.replace(/\{\{(\w+)\}\}/g, (_, k) => opts?.[k] ?? '')
    },
    i18n: { changeLanguage: vi.fn() },
  }),
}))
vi.mock('../../api/client', async importOriginal => {
  const actual = await importOriginal<typeof import('../../api/client')>()
  return { ...actual, api: { ...actual.api, listSettings: vi.fn(), status: vi.fn(), setSetting: vi.fn() } }
})

function status(over: Record<string, unknown>) {
  vi.mocked(api.status).mockResolvedValue({ version: 'dev', commit: 'x', buildDate: '', enhancedHardcoverApi: false, ...over } as Awaited<ReturnType<typeof api.status>>)
}

beforeEach(() => {
  vi.mocked(api.listSettings).mockResolvedValue([])
  status({ hardcoverTokenConfigured: false })
  vi.mocked(api.setSetting).mockReset().mockResolvedValue(undefined as never)
})

describe('ApiKeysTab current value subtitles (#3989)', () => {
  it('describes the token input with its existing Not set badge, once, with no second line', async () => {
    render(<ApiKeysTab />)
    await screen.findByText('Hardcover API Token')
    const input = screen.getByPlaceholderText('Paste a Hardcover API token')
    expect(input).toHaveAccessibleDescription('No token configured')
    // Only the enhanced series line exists; a Current line for the token must not return.
    expect(document.querySelectorAll('[data-testid="field-value"]').length).toBe(1)
    expect(document.querySelector('[data-testid="field-value"]')).toHaveAttribute('id', 'enhanced-hardcover-series-value')
    expect(screen.queryByText(/^Current: (Set|Not set)$/)).toBeNull()
  })

  it('shows the configured badge once and no Current line for the token', async () => {
    status({ hardcoverTokenConfigured: true })
    render(<ApiKeysTab />)
    await screen.findByText('Token configured')
    expect(screen.getAllByText('Token configured')).toHaveLength(1)
    expect(screen.queryByText(/^Current: (Set|Not set)$/)).toBeNull()
    expect(document.querySelectorAll('[data-testid="field-value"]').length).toBe(1)
  })

  it('shows a configured token only as the badge and never the token or a typed value', async () => {
    status({ hardcoverTokenConfigured: true })
    vi.mocked(api.listSettings).mockResolvedValue([{ key: 'hardcover.api_token', value: 'hc-secret-token' }] as Awaited<ReturnType<typeof api.listSettings>>)
    render(<ApiKeysTab />)
    const input = await screen.findByPlaceholderText(/Saved token is hidden/)
    await waitFor(() => expect(input).toHaveAccessibleDescription('Token configured'))
    fireEvent.change(input, { target: { value: 'typed-new-token' } })
    expect(document.body.textContent).not.toContain('hc-secret-token')
    expect(document.body.textContent).not.toContain('typed-new-token')
  })

  it('follows the enhanced series toggle and reverts it when the save fails', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    render(<ApiKeysTab />)
    await screen.findByText('Hardcover API Token')
    const sub = () => document.getElementById('enhanced-hardcover-series-value')!
    expect(sub()).toHaveTextContent('Current: Off')
    vi.mocked(api.setSetting).mockRejectedValueOnce(new Error('boom'))
    fireEvent.click(document.querySelector('[aria-describedby="enhanced-hardcover-series-value"]')!)
    await waitFor(() => expect(api.setSetting).toHaveBeenCalled())
    await waitFor(() => expect(sub()).toHaveTextContent('Current: Off'))
    fireEvent.click(document.querySelector('[aria-describedby="enhanced-hardcover-series-value"]')!)
    await waitFor(() => expect(sub()).toHaveTextContent('Current: On'))
  })

  it('wires every subtitle on the tab to a control', async () => {
    render(<ApiKeysTab />)
    await screen.findByText('Hardcover API Token')
    expectFieldValuesWired(document.body)
  })
})
