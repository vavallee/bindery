import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor, fireEvent, within } from '@testing-library/react'
import { useState } from 'react'
import type { DownloadClient, Indexer, ProwlarrInstance } from '../../api/client'

// i18n: echo the key plus interpolated options so assertions do not depend on
// English copy.
vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, options?: Record<string, unknown>) => {
      if (!options) return key
      let out = key
      for (const [k, v] of Object.entries(options)) out += ` ${k}=${String(v)}`
      return out
    },
  }),
}))

vi.mock('../../api/client', () => ({
  api: {
    addIndexer: vi.fn(),
    updateIndexer: vi.fn(),
    deleteIndexer: vi.fn(),
    testIndexer: vi.fn(),
    testIndexerConfig: vi.fn(),
    listDownloadClients: vi.fn(),
  },
}))

import { api } from '../../api/client'
import IndexersTab from './IndexersTab'

const m = vi.mocked(api)

function client(overrides: Partial<DownloadClient>): DownloadClient {
  return {
    id: 1, name: 'c', type: 'qbittorrent', host: 'h', port: 1, apiKey: '', username: '', password: '',
    apiKeyConfigured: false, passwordConfigured: false, useSsl: false, urlBase: '', category: '',
    enabled: true, ...overrides,
  }
}

const clients: DownloadClient[] = [
  client({ id: 1, name: 'qbit-public', type: 'qbittorrent' }),
  client({ id: 2, name: 'qbit-private', type: 'qbittorrent' }),
  client({ id: 3, name: 'qbit-off', type: 'qbittorrent', enabled: false }),
  client({ id: 4, name: 'sab', type: 'sabnzbd' }),
]

function makeIndexer(overrides: Partial<Indexer> = {}): Indexer {
  return {
    id: 1, name: 'TrackerX', type: 'torznab', url: 'https://t.example/api', apiKey: '', apiKeyConfigured: true,
    categories: [7020], priority: 0, enabled: true, ...overrides,
  } as Indexer
}

function Harness({ indexers: initial }: { indexers: Indexer[] }) {
  const [indexers, setIndexers] = useState<Indexer[]>(initial)
  const [prowlarr, setProwlarr] = useState<ProwlarrInstance[]>([])
  return <IndexersTab indexers={indexers} setIndexers={setIndexers} prowlarrInstances={prowlarr} setProwlarrInstances={setProwlarr} />
}

const label = 'settings.indexers.form.downloadClient'

beforeEach(() => {
  vi.clearAllMocks()
  m.listDownloadClients.mockResolvedValue(clients)
})

async function openEdit() {
  fireEvent.click(screen.getByText('common.edit'))
  return await screen.findByLabelText(label)
}

describe('IndexersTab download client pin', () => {
  it('lists only enabled clients of the indexer protocol', async () => {
    render(<Harness indexers={[makeIndexer()]} />)
    const select = await openEdit()
    const names = within(select).getAllByRole('option').map(o => o.textContent)
    expect(names).toEqual(['settings.indexers.form.downloadClientDefault', 'qbit-public', 'qbit-private'])
  })

  it('lists usenet clients for a usenet indexer', async () => {
    render(<Harness indexers={[makeIndexer({ type: 'newznab' })]} />)
    const select = await openEdit()
    const names = within(select).getAllByRole('option').map(o => o.textContent)
    expect(names).toEqual(['settings.indexers.form.downloadClientDefault', 'sab'])
  })

  it('sends the chosen client id on save', async () => {
    m.updateIndexer.mockImplementation(async (_id, p) => ({ ...makeIndexer(), ...p }) as Indexer)
    render(<Harness indexers={[makeIndexer()]} />)
    const select = await openEdit()
    fireEvent.change(select, { target: { value: '2' } })
    fireEvent.click(screen.getByText('common.save'))
    await waitFor(() => expect(m.updateIndexer).toHaveBeenCalledWith(1, expect.objectContaining({ downloadClientId: 2 })))
  })

  it('sends null when the pin is cleared back to Default', async () => {
    m.updateIndexer.mockImplementation(async (_id, p) => ({ ...makeIndexer(), ...p }) as Indexer)
    render(<Harness indexers={[makeIndexer({ downloadClientId: 2 })]} />)
    const select = await openEdit()
    expect((select as HTMLSelectElement).value).toBe('2')
    fireEvent.change(select, { target: { value: '' } })
    fireEvent.click(screen.getByText('common.save'))
    await waitFor(() => expect(m.updateIndexer).toHaveBeenCalledWith(1, expect.objectContaining({ downloadClientId: null })))
  })

  it('leaves an unpinned indexer unpinned on save', async () => {
    m.updateIndexer.mockImplementation(async (_id, p) => ({ ...makeIndexer(), ...p }) as Indexer)
    render(<Harness indexers={[makeIndexer()]} />)
    await openEdit()
    fireEvent.click(screen.getByText('common.save'))
    await waitFor(() => expect(m.updateIndexer).toHaveBeenCalledWith(1, expect.objectContaining({ downloadClientId: null })))
  })

  it('drops a pin the new indexer type cannot use', async () => {
    m.updateIndexer.mockImplementation(async (_id, p) => ({ ...makeIndexer(), ...p }) as Indexer)
    render(<Harness indexers={[makeIndexer({ downloadClientId: 2 })]} />)
    await openEdit()
    fireEvent.change(screen.getByDisplayValue('settings.indexers.form.typeTorznabTorrent'), { target: { value: 'newznab' } })
    fireEvent.click(screen.getByText('common.save'))
    await waitFor(() => expect(m.updateIndexer).toHaveBeenCalledWith(1, expect.objectContaining({ type: 'newznab', downloadClientId: null })))
  })

  it('warns on the row when the pinned client is disabled, and keeps it selectable as disabled', async () => {
    render(<Harness indexers={[makeIndexer({ downloadClientId: 3 })]} />)
    expect(await screen.findByText('settings.indexers.pinnedClientDisabled name=qbit-off')).toBeInTheDocument()
    const select = await openEdit()
    const names = within(select).getAllByRole('option').map(o => o.textContent)
    expect(names).toContain('settings.indexers.form.downloadClientDisabledOption name=qbit-off')
  })

  it('warns when the pinned client no longer exists', async () => {
    render(<Harness indexers={[makeIndexer({ downloadClientId: 99 })]} />)
    expect(await screen.findByText('settings.indexers.pinnedClientMissing')).toBeInTheDocument()
  })

  it('shows no warning for an enabled pin or no pin', async () => {
    render(<Harness indexers={[makeIndexer({ id: 1, downloadClientId: 2 }), makeIndexer({ id: 2, name: 'Plain', url: 'https://p/api' })]} />)
    await waitFor(() => expect(m.listDownloadClients).toHaveBeenCalled())
    await waitFor(() => expect(screen.queryByText(/pinnedClient/)).not.toBeInTheDocument())
  })

  it('does not warn about a missing client while the list is still loading', () => {
    m.listDownloadClients.mockReturnValue(new Promise(() => {}))
    render(<Harness indexers={[makeIndexer({ downloadClientId: 2 })]} />)
    expect(screen.queryByText('settings.indexers.pinnedClientMissing')).not.toBeInTheDocument()
  })

  it('keeps a wrong protocol pin visible and warns about it on the row', async () => {
    // The usenet client sab (id 4) pinned to a torrent indexer.
    render(<Harness indexers={[makeIndexer({ downloadClientId: 4 })]} />)
    expect(await screen.findByText('settings.indexers.pinnedClientWrongProtocol name=sab')).toBeInTheDocument()
    const select = await openEdit()
    expect((select as HTMLSelectElement).value).toBe('4')
    const names = within(select).getAllByRole('option').map(o => o.textContent)
    expect(names).toContain('settings.indexers.form.downloadClientWrongProtocolOption name=sab')
  })

  it('shows no pin warning at all when the client list fails to load', async () => {
    m.listDownloadClients.mockRejectedValue(new Error('boom'))
    render(<Harness indexers={[makeIndexer({ downloadClientId: 2 }), makeIndexer({ id: 2, name: 'Other', url: 'https://o/api', downloadClientId: 99 })]} />)
    await waitFor(() => expect(m.listDownloadClients).toHaveBeenCalled())
    // Let the rejection settle before asserting nothing appeared.
    await new Promise(r => setTimeout(r, 20))
    expect(screen.queryByText(/pinnedClient/)).not.toBeInTheDocument()
  })

  it('sends the pin when adding an indexer', async () => {
    m.addIndexer.mockImplementation(async p => ({ ...makeIndexer(), ...p, id: 9 }) as Indexer)
    render(<Harness indexers={[]} />)
    fireEvent.click(screen.getByText('settings.indexers.addButton'))
    fireEvent.change(screen.getByPlaceholderText('settings.indexers.form.namePlaceholderExample'), { target: { value: 'New' } })
    fireEvent.change(screen.getByPlaceholderText('settings.indexers.form.urlPlaceholderExample'), { target: { value: 'https://n/api' } })
    fireEvent.change(screen.getByDisplayValue('settings.indexers.form.typeNewznab'), { target: { value: 'torznab' } })
    const select = await screen.findByLabelText(label)
    await waitFor(() => expect(within(select).getAllByRole('option').length).toBeGreaterThan(1))
    fireEvent.change(select, { target: { value: '1' } })
    fireEvent.click(screen.getByText('common.save'))
    await waitFor(() => expect(m.addIndexer).toHaveBeenCalledWith(expect.objectContaining({ downloadClientId: 1 })))
  })
})
