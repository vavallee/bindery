import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import AdvancedTab from './AdvancedTab'
import { api, SettingDescriptor } from '../../api/client'
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
vi.mock('../../auth/AuthContext', () => ({
  useAuth: () => ({ isAdmin: true, role: 'admin', loading: false }),
}))
vi.mock('../../api/client', async importOriginal => {
  const actual = await importOriginal<typeof import('../../api/client')>()
  return {
    ...actual,
    api: { ...actual.api, listSettingDescriptors: vi.fn(), listSettings: vi.fn(), setSetting: vi.fn(), deleteSetting: vi.fn() },
  }
})

function descriptor(over: Partial<SettingDescriptor> & { key: string }): SettingDescriptor {
  return {
    type: 'string', default: '', description: 'd', restartRequired: false, state: 'active',
    secret: false, adminOnly: false, writable: true, ...over,
  }
}

function seed(descriptors: SettingDescriptor[], stored: Record<string, string> = {}) {
  vi.mocked(api.listSettingDescriptors).mockResolvedValue(descriptors)
  vi.mocked(api.listSettings).mockResolvedValue(Object.entries(stored).map(([key, value]) => ({ key, value })))
}

beforeEach(() => {
  vi.mocked(api.setSetting).mockReset().mockResolvedValue(undefined)
  vi.mocked(api.deleteSetting).mockReset().mockResolvedValue(undefined)
})

describe('AdvancedTab current value subtitles (#3989)', () => {
  it('shows Default (x) for an unstored key and the stored value once one is saved', async () => {
    seed([descriptor({ key: 'search.interval', default: '12h' })])
    render(<AdvancedTab />)
    const input = await screen.findByLabelText('search.interval')
    const sub = () => document.getElementById(input.getAttribute('aria-describedby')!)!
    expect(sub()).toHaveTextContent('Current: Default (12h)')
    fireEvent.change(input, { target: { value: '6h' } })
    // Typing is a draft, not the saved value.
    expect(sub()).toHaveTextContent('Current: Default (12h)')
    fireEvent.click(screen.getByRole('button', { name: 'Save search.interval' }))
    await waitFor(() => expect(sub()).toHaveTextContent('Current: 6h'))
  })

  it('keeps the stored value when a save fails', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    seed([descriptor({ key: 'search.interval', default: '12h' })], { 'search.interval': '24h' })
    vi.mocked(api.setSetting).mockRejectedValue(new Error('boom'))
    render(<AdvancedTab />)
    const input = await screen.findByLabelText('search.interval')
    const sub = () => document.getElementById(input.getAttribute('aria-describedby')!)!
    expect(sub()).toHaveTextContent('Current: 24h')
    fireEvent.change(input, { target: { value: '6h' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save search.interval' }))
    await waitFor(() => expect(api.setSetting).toHaveBeenCalled())
    expect(sub()).toHaveTextContent('Current: 24h')
  })

  it('shows On or Off for a bool and Not set when there is no default', async () => {
    seed([
      descriptor({ key: 'calibre.sync_on_startup', type: 'bool', default: 'false' }),
      descriptor({ key: 'some.path' }),
    ])
    render(<AdvancedTab />)
    const toggle = await screen.findByRole('switch', { name: 'calibre.sync_on_startup' })
    expect(document.getElementById(toggle.getAttribute('aria-describedby')!)).toHaveTextContent('Current: Default (Off)')
    const path = screen.getByLabelText('some.path')
    expect(document.getElementById(path.getAttribute('aria-describedby')!)).toHaveTextContent('Current: Not set')
  })

  it('shows no subtitle for a secret key, so its value cannot appear', async () => {
    seed([descriptor({ key: 'x.token', secret: true })], { 'x.token': 'super-secret-value' })
    render(<AdvancedTab />)
    await screen.findByTestId('setting-row-x.token')
    expect(screen.queryByTestId('field-value')).toBeNull()
    expect(document.body.textContent).not.toContain('super-secret-value')
  })

  it('never shows credentials embedded in a saved URL', async () => {
    seed([descriptor({ key: 'x.base_url' })], { 'x.base_url': 'http://admin:hunter2@calibre:8099/lib' })
    render(<AdvancedTab />)
    const input = await screen.findByLabelText('x.base_url')
    const el = document.getElementById(input.getAttribute('aria-describedby')!)!
    expect(el).toHaveTextContent('Current: http://calibre:8099/lib')
    expect(el).not.toHaveAttribute('title')
    expect(el.outerHTML).not.toContain('hunter2')
  })

  it('wires every subtitle on the tab to a control', async () => {
    seed([descriptor({ key: 'a.b', default: '1' }), descriptor({ key: 'a.c', type: 'bool', default: 'true' })])
    render(<AdvancedTab />)
    await screen.findByLabelText('a.b')
    expectFieldValuesWired(document.body)
  })
})
