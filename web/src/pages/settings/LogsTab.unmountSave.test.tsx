import { describe, it, expect, vi } from 'vitest'
import { render, screen, waitFor, fireEvent, act } from '@testing-library/react'
import LogsTab from './LogsTab'
import { SettingsToastHost } from './autoSave'
import { api } from '../../api/client'

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
      listSettings: vi.fn().mockResolvedValue([]),
      listBackups: vi.fn().mockResolvedValue([]),
    },
  }
})

describe('LogsTab level save finishing after unmount (#3991)', () => {
  it('still toasts and writes no state or React warning', async () => {
    const errors = vi.spyOn(console, 'error').mockImplementation(() => {})
    let resolve!: () => void
    vi.mocked(api.setLogLevel).mockReturnValue(new Promise<void>(res => { resolve = res }) as never)
    const host = render(<SettingsToastHost />)
    const tab = render(<LogsTab />)
    const select = await screen.findByLabelText('Runtime level')
    await waitFor(() => expect(select).toHaveValue('info'))
    fireEvent.change(select, { target: { value: 'debug' } })
    tab.unmount()
    await act(async () => { resolve() })
    expect(await screen.findByText(/Runtime level set to DEBUG/)).toBeInTheDocument()
    expect(errors).not.toHaveBeenCalled()
    host.unmount()
    errors.mockRestore()
  })
})
