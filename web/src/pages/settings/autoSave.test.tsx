import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { SettingsToastHost } from './autoSave'
import { useAutoSave } from './useAutoSave'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, fallback?: unknown, opts?: Record<string, string>) => {
      if (typeof fallback !== 'string') return key
      return fallback.replace(/\{\{(\w+)\}\}/g, (_, k) => opts?.[k] ?? '')
    },
  }),
}))

function Probe({ request, value }: { request: () => Promise<unknown>; value?: string }) {
  const autoSave = useAutoSave()
  return <button onClick={() => { void autoSave('import.mode', request, value) }}>go</button>
}

describe('useAutoSave', () => {
  it('toasts a status message when the save succeeds', async () => {
    render(<><SettingsToastHost /><Probe request={() => Promise.resolve()} /></>)
    fireEvent.click(screen.getByText('go'))
    const toast = await screen.findByRole('status')
    expect(toast).toHaveTextContent('saved')
  })

  it('names the chosen value when one is given', async () => {
    render(<><SettingsToastHost /><Probe request={() => Promise.resolve()} value="External" /></>)
    fireEvent.click(screen.getByText('go'))
    expect(await screen.findByRole('status')).toHaveTextContent('set to External')
  })

  it('toasts an alert when the save fails and does not throw', async () => {
    const spy = vi.spyOn(console, 'error').mockImplementation(() => {})
    render(<><SettingsToastHost /><Probe request={() => Promise.reject(new Error('boom'))} /></>)
    fireEvent.click(screen.getByText('go'))
    expect(await screen.findByRole('alert')).toHaveTextContent("Couldn't save")
    spy.mockRestore()
  })

  it('still saves when no host is mounted', async () => {
    const request = vi.fn().mockResolvedValue(undefined)
    render(<Probe request={request} />)
    fireEvent.click(screen.getByText('go'))
    await waitFor(() => expect(request).toHaveBeenCalled())
  })
})
