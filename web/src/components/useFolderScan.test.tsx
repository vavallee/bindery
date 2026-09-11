import { describe, it, expect, vi, beforeEach } from 'vitest'
import { act, render, screen, fireEvent } from '@testing-library/react'
import { useFolderScan } from './useFolderScan'

vi.mock('../api/client', () => ({
  api: { scanFolder: vi.fn() },
}))

import { api } from '../api/client'
const mockScan = api.scanFolder as ReturnType<typeof vi.fn>

// Harness renders the hook's state as text/attributes so assertions don't
// need to reach into React internals, and exposes its actions as buttons.
function Harness({ onItems }: { onItems?: (items: { path: string }[]) => void }) {
  const fs = useFolderScan(onItems as never)
  return (
    <div>
      <input aria-label="path" value={fs.path} onChange={e => fs.setPath(e.target.value)} />
      <button onClick={fs.scan}>scan</button>
      <button onClick={() => fs.toggleShowImported(!fs.showImported)}>toggle</button>
      <button onClick={fs.clear}>clear</button>
      <div data-testid="scanning">{String(fs.scanning)}</div>
      <div data-testid="truncated">{String(fs.truncated)}</div>
      <div data-testid="showImported">{String(fs.showImported)}</div>
      <div data-testid="error">{fs.error ?? ''}</div>
      <div data-testid="items">{fs.items ? fs.items.map(i => i.path).join(',') : 'null'}</div>
    </div>
  )
}

describe('useFolderScan', () => {
  beforeEach(() => vi.clearAllMocks())

  it('does nothing when scanning an empty path', () => {
    render(<Harness />)
    fireEvent.click(screen.getByText('scan'))
    expect(mockScan).not.toHaveBeenCalled()
  })

  it('scans with includeImported=false by default and reports items/truncated', async () => {
    mockScan.mockResolvedValue({ items: [{ path: '/dl/a.epub' }], truncated: false })
    render(<Harness />)
    fireEvent.change(screen.getByLabelText('path'), { target: { value: '/dl' } })
    await act(async () => { fireEvent.click(screen.getByText('scan')) })

    expect(mockScan).toHaveBeenCalledWith('/dl', { includeImported: false })
    expect(screen.getByTestId('items').textContent).toBe('/dl/a.epub')
    expect(screen.getByTestId('truncated').textContent).toBe('false')
  })

  it('invokes onItems with the fetched items on a successful scan', async () => {
    mockScan.mockResolvedValue({ items: [{ path: '/dl/a.epub' }], truncated: false })
    const onItems = vi.fn()
    render(<Harness onItems={onItems} />)
    fireEvent.change(screen.getByLabelText('path'), { target: { value: '/dl' } })
    await act(async () => { fireEvent.click(screen.getByText('scan')) })

    expect(onItems).toHaveBeenCalledWith([{ path: '/dl/a.epub' }])
  })

  it('sets error and leaves items null when the scan fails', async () => {
    mockScan.mockRejectedValue(new Error('boom'))
    render(<Harness />)
    fireEvent.change(screen.getByLabelText('path'), { target: { value: '/dl' } })
    await act(async () => { fireEvent.click(screen.getByText('scan')) })

    expect(screen.getByTestId('error').textContent).toBe('boom')
    expect(screen.getByTestId('items').textContent).toBe('null')
  })

  it('toggling on re-scans with includeImported=true only once items exist', async () => {
    render(<Harness />)
    fireEvent.change(screen.getByLabelText('path'), { target: { value: '/dl' } })

    // No prior scan yet: toggling just flips the flag, no fetch.
    fireEvent.click(screen.getByText('toggle'))
    expect(screen.getByTestId('showImported').textContent).toBe('true')
    expect(mockScan).not.toHaveBeenCalled()

    mockScan.mockResolvedValue({ items: [{ path: '/dl/a.epub' }], truncated: false })
    await act(async () => { fireEvent.click(screen.getByText('scan')) })
    expect(mockScan).toHaveBeenCalledWith('/dl', { includeImported: true })

    // Now that items exist, toggling again re-scans.
    mockScan.mockClear()
    mockScan.mockResolvedValue({ items: [], truncated: false })
    await act(async () => { fireEvent.click(screen.getByText('toggle')) })
    expect(mockScan).toHaveBeenCalledWith('/dl', { includeImported: false })
  })

  // The bug this hook fixes: a stale truncated=true from a previous scan must
  // not survive into a later scan that isn't truncated (#2480).
  it('clears a stale truncated flag once a later scan reports untruncated', async () => {
    mockScan.mockResolvedValue({ items: [], truncated: true })
    render(<Harness />)
    fireEvent.change(screen.getByLabelText('path'), { target: { value: '/dl' } })
    await act(async () => { fireEvent.click(screen.getByText('scan')) })
    expect(screen.getByTestId('truncated').textContent).toBe('true')

    mockScan.mockResolvedValue({ items: [], truncated: false })
    await act(async () => { fireEvent.click(screen.getByText('scan')) })
    expect(screen.getByTestId('truncated').textContent).toBe('false')
  })

  it('clear() drops items, truncated, and error together', async () => {
    mockScan.mockResolvedValue({ items: [{ path: '/dl/a.epub' }], truncated: true })
    render(<Harness />)
    fireEvent.change(screen.getByLabelText('path'), { target: { value: '/dl' } })
    await act(async () => { fireEvent.click(screen.getByText('scan')) })
    expect(screen.getByTestId('items').textContent).toBe('/dl/a.epub')

    fireEvent.click(screen.getByText('clear'))
    expect(screen.getByTestId('items').textContent).toBe('null')
    expect(screen.getByTestId('truncated').textContent).toBe('false')
    expect(screen.getByTestId('error').textContent).toBe('')
  })
})
