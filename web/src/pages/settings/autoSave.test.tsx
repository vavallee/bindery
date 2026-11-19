import { describe, it, expect, vi, afterEach } from 'vitest'
import { act, render, screen, fireEvent, waitFor } from '@testing-library/react'
import { SettingsToastHost } from './autoSave'
import { useAutoSave, type AutoSaveOptions } from './useAutoSave'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, fallback?: unknown, opts?: Record<string, string>) => {
      if (typeof fallback !== 'string') return key
      return fallback.replace(/\{\{(\w+)\}\}/g, (_, k) => opts?.[k] ?? '')
    },
  }),
}))

function Probe({ request, options }: { request: () => Promise<unknown>; options?: AutoSaveOptions }) {
  const autoSave = useAutoSave()
  return <button onClick={() => { void autoSave('import.mode', request, options) }}>go</button>
}

afterEach(() => { vi.useRealTimers() })

describe('useAutoSave', () => {
  it('keeps the live region in the page and announces a success in it', async () => {
    render(<><SettingsToastHost /><Probe request={() => Promise.resolve()} /></>)
    const region = screen.getByRole('status')
    expect(region).toHaveAttribute('aria-live', 'polite')
    expect(region).toBeEmptyDOMElement()
    fireEvent.click(screen.getByText('go'))
    expect(await screen.findByText(/saved/)).toBeInTheDocument()
    expect(region).toHaveTextContent('saved')
  })

  it('names the chosen value when one is given', async () => {
    render(<><SettingsToastHost /><Probe request={() => Promise.resolve()} options={{ value: 'External' }} /></>)
    fireEvent.click(screen.getByText('go'))
    expect(await screen.findByText(/set to External/)).toBeInTheDocument()
  })

  it('toasts an alert and calls revert when the save fails', async () => {
    const spy = vi.spyOn(console, 'error').mockImplementation(() => {})
    const revert = vi.fn()
    render(<><SettingsToastHost /><Probe request={() => Promise.reject(new Error('boom'))} options={{ revert }} /></>)
    fireEvent.click(screen.getByText('go'))
    expect(await screen.findByRole('alert')).toHaveTextContent("Couldn't save")
    expect(revert).toHaveBeenCalledTimes(1)
    spy.mockRestore()
  })

  it('does not call revert when the save works', async () => {
    const revert = vi.fn()
    render(<><SettingsToastHost /><Probe request={() => Promise.resolve()} options={{ revert }} /></>)
    fireEvent.click(screen.getByText('go'))
    await screen.findByText(/saved/)
    expect(revert).not.toHaveBeenCalled()
  })

  it('still saves when no host is mounted', async () => {
    const request = vi.fn().mockResolvedValue(undefined)
    render(<Probe request={request} />)
    fireEvent.click(screen.getByText('go'))
    await waitFor(() => expect(request).toHaveBeenCalled())
  })

  it('stacks toasts so a later success does not hide an earlier failure', async () => {
    const spy = vi.spyOn(console, 'error').mockImplementation(() => {})
    let fail = true
    render(<><SettingsToastHost /><Probe request={() => (fail ? Promise.reject(new Error('x')) : Promise.resolve())} /></>)
    fireEvent.click(screen.getByText('go'))
    await screen.findByRole('alert')
    fail = false
    fireEvent.click(screen.getByText('go'))
    await screen.findByText(/saved/)
    expect(screen.getAllByTestId('settings-toast')).toHaveLength(2)
    expect(screen.getByRole('alert')).toBeInTheDocument()
    spy.mockRestore()
  })

  it('dismisses a toast with its button', async () => {
    render(<><SettingsToastHost /><Probe request={() => Promise.resolve()} /></>)
    fireEvent.click(screen.getByText('go'))
    await screen.findByText(/saved/)
    fireEvent.click(screen.getByRole('button', { name: 'Dismiss' }))
    expect(screen.queryByTestId('settings-toast')).toBeNull()
  })

  it('keeps an error at least 8 seconds and pauses the timer while hovered', async () => {
    vi.useFakeTimers()
    const spy = vi.spyOn(console, 'error').mockImplementation(() => {})
    render(<><SettingsToastHost /><Probe request={() => Promise.reject(new Error('x'))} /></>)
    fireEvent.click(screen.getByText('go'))
    await act(async () => { await vi.advanceTimersByTimeAsync(0) })
    const toast = screen.getByTestId('settings-toast')
    await act(async () => { await vi.advanceTimersByTimeAsync(7500) })
    expect(screen.queryByTestId('settings-toast')).not.toBeNull()
    fireEvent.mouseEnter(toast)
    await act(async () => { await vi.advanceTimersByTimeAsync(20000) })
    expect(screen.queryByTestId('settings-toast')).not.toBeNull()
    fireEvent.mouseLeave(toast)
    await act(async () => { await vi.advanceTimersByTimeAsync(8100) })
    expect(screen.queryByTestId('settings-toast')).toBeNull()
    spy.mockRestore()
  })
})
