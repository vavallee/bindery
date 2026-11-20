import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import MetadataTab from './MetadataTab'
import { SettingsToastHost } from './autoSave'
import { api } from '../../api/client'

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
  return {
    ...actual,
    api: {
      ...actual.api,
      listSettings: vi.fn(),
      listMetadataProfiles: vi.fn(),
      status: vi.fn(),
      setSetting: vi.fn(),
    },
  }
})

const KEY = 'author.default_monitor_latest_count'

beforeEach(() => {
  vi.mocked(api.listSettings).mockResolvedValue([
    { key: 'author.default_monitor_mode', value: 'latest' },
    { key: KEY, value: '2' },
  ] as Awaited<ReturnType<typeof api.listSettings>>)
  vi.mocked(api.listMetadataProfiles).mockResolvedValue([])
  vi.mocked(api.status).mockResolvedValue({ hardcoverTokenConfigured: false } as Awaited<ReturnType<typeof api.status>>)
  vi.mocked(api.setSetting).mockReset()
  vi.mocked(api.setSetting).mockResolvedValue(undefined as never)
})

async function field() {
  const input = await screen.findByRole('spinbutton')
  await waitFor(() => expect(input).toHaveValue(2))
  return input
}

describe('Latest book count autosave (#3991)', () => {
  it('typing 10 produces one save and one toast, on blur', async () => {
    render(<><SettingsToastHost /><MetadataTab /></>)
    const input = await field()
    fireEvent.change(input, { target: { value: '1' } })
    fireEvent.change(input, { target: { value: '10' } })
    expect(api.setSetting).not.toHaveBeenCalled()
    fireEvent.blur(input)
    await waitFor(() => expect(api.setSetting).toHaveBeenCalledTimes(1))
    expect(api.setSetting).toHaveBeenCalledWith(KEY, '10')
    await screen.findByText(/saved/)
    expect(screen.getAllByTestId('settings-toast')).toHaveLength(1)
  })

  it('saves on Enter, and not again on the blur that follows', async () => {
    render(<><SettingsToastHost /><MetadataTab /></>)
    const input = await field()
    fireEvent.change(input, { target: { value: '5' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    await waitFor(() => expect(api.setSetting).toHaveBeenCalledWith(KEY, '5'))
    await screen.findByText(/saved/)
    fireEvent.blur(input)
    expect(api.setSetting).toHaveBeenCalledTimes(1)
  })

  it('shows a validation error and does not save an invalid value', async () => {
    render(<MetadataTab />)
    const input = await field()
    fireEvent.change(input, { target: { value: '0' } })
    expect(screen.getByRole('alert')).toHaveTextContent('whole number')
    expect(input).toHaveAttribute('aria-invalid', 'true')
    fireEvent.blur(input)
    expect(api.setSetting).not.toHaveBeenCalled()
  })

  it('does not save when nothing changed', async () => {
    render(<MetadataTab />)
    const input = await field()
    fireEvent.blur(input)
    expect(api.setSetting).not.toHaveBeenCalled()
  })

  it('puts the saved value back when the save fails', async () => {
    const spy = vi.spyOn(console, 'error').mockImplementation(() => {})
    vi.mocked(api.setSetting).mockRejectedValue(new Error('boom'))
    render(<><SettingsToastHost /><MetadataTab /></>)
    const input = await field()
    fireEvent.change(input, { target: { value: '7' } })
    fireEvent.blur(input)
    expect(await screen.findByRole('alert')).toHaveTextContent("Couldn't save")
    await waitFor(() => expect(input).toHaveValue(2))
    // The failed value is not remembered as saved: retrying sends it again.
    fireEvent.change(input, { target: { value: '7' } })
    fireEvent.blur(input)
    await waitFor(() => expect(api.setSetting).toHaveBeenCalledTimes(2))
    spy.mockRestore()
  })
})
