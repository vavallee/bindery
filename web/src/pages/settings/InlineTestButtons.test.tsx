import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'

// i18n: echo the key (plus interpolated options) so assertions are stable.
vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, options?: Record<string, unknown> | string) => {
      if (typeof options === 'string') return options
      if (!options) return key
      let out = key
      for (const [k, v] of Object.entries(options)) {
        out += ` ${k}=${String(v)}`
      }
      return out
    },
  }),
}))

vi.mock('../../api/client', () => ({
  api: {
    testDownloadClientConfig: vi.fn(),
    addDownloadClient: vi.fn(),
    updateDownloadClient: vi.fn(),
    deleteDownloadClient: vi.fn(),
    testIndexerConfig: vi.fn(),
    testIndexer: vi.fn(),
    addIndexer: vi.fn(),
    updateIndexer: vi.fn(),
    deleteIndexer: vi.fn(),
  },
}))

import { api } from '../../api/client'
import ClientsTab from './ClientsTab'
import IndexersTab from './IndexersTab'

const testClient = api.testDownloadClientConfig as ReturnType<typeof vi.fn>
const testIndexer = api.testIndexerConfig as ReturnType<typeof vi.fn>
const testIndexerById = api.testIndexer as ReturnType<typeof vi.fn>
const updateIndexer = api.updateIndexer as ReturnType<typeof vi.fn>

const savedIndexer = {
  id: 7,
  name: 'Saved',
  type: 'newznab',
  url: 'https://idx.example/api',
  // The API redacts the key (#2212); the client only learns that one is set.
  apiKey: '',
  apiKeyConfigured: true,
  categories: [7020],
  priority: 0,
  enabled: true,
}

describe('ClientsTab inline Test button', () => {
  beforeEach(() => vi.clearAllMocks())

  it('tests the Add form with the current (unsaved) form values and shows success', async () => {
    testClient.mockResolvedValueOnce({ message: 'Connection verified' })
    render(<ClientsTab clients={[]} setClients={vi.fn()} />)

    // Open the Add form.
    fireEvent.click(screen.getByText('settings.clients.addButton'))

    // Type a host so the Test button enables and the value is sent.
    const host = screen.getByPlaceholderText('Host')
    fireEvent.change(host, { target: { value: '10.0.0.5' } })

    fireEvent.click(screen.getByText('common.test'))

    await waitFor(() => {
      expect(testClient).toHaveBeenCalledTimes(1)
    })
    // The unsaved host value is forwarded to the test-by-config endpoint.
    expect(testClient.mock.calls[0][0]).toMatchObject({ host: '10.0.0.5' })
    // Success feedback is rendered (reuses common.connOk).
    expect(await screen.findByText('common.connOk')).toBeInTheDocument()
  })

  it('renders an actionable error when the test fails', async () => {
    testClient.mockRejectedValueOnce(new Error('connection refused'))
    render(<ClientsTab clients={[]} setClients={vi.fn()} />)

    fireEvent.click(screen.getByText('settings.clients.addButton'))
    fireEvent.change(screen.getByPlaceholderText('Host'), { target: { value: '10.0.0.5' } })
    fireEvent.click(screen.getByText('common.test'))

    await waitFor(() => expect(testClient).toHaveBeenCalledTimes(1))
    // The backend error string is surfaced via common.connFail.
    expect(await screen.findByText('common.connFail error=connection refused')).toBeInTheDocument()
  })

  it('shows the path-visibility warning distinctly from a connection success (#1182)', async () => {
    // Connection succeeds, but Bindery can't see the completed-downloads path.
    testClient.mockResolvedValueOnce({
      message: 'Connection verified',
      pathVisibility: { status: 'warning', message: "Bindery can't read /downloads", path: '/downloads' },
    })
    render(<ClientsTab clients={[]} setClients={vi.fn()} />)

    fireEvent.click(screen.getByText('settings.clients.addButton'))
    fireEvent.change(screen.getByPlaceholderText('Host'), { target: { value: '10.0.0.5' } })
    fireEvent.click(screen.getByText('common.test'))

    await waitFor(() => expect(testClient).toHaveBeenCalledTimes(1))
    // Connection success still shown...
    expect(await screen.findByText('common.connOk')).toBeInTheDocument()
    // ...and the warning is surfaced separately (role=alert) with the backend message.
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent("Bindery can't read /downloads")
  })

  it('does not show a path-visibility warning when the path is visible', async () => {
    testClient.mockResolvedValueOnce({
      message: 'Connection verified',
      pathVisibility: { status: 'ok', message: 'Bindery can read /downloads', path: '/downloads' },
    })
    render(<ClientsTab clients={[]} setClients={vi.fn()} />)

    fireEvent.click(screen.getByText('settings.clients.addButton'))
    fireEvent.change(screen.getByPlaceholderText('Host'), { target: { value: '10.0.0.5' } })
    fireEvent.click(screen.getByText('common.test'))

    expect(await screen.findByText('common.connOk')).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })
})

describe('IndexersTab inline Test button', () => {
  beforeEach(() => vi.clearAllMocks())

  it('tests the Add form with the current (unsaved) form values and shows success', async () => {
    testIndexer.mockResolvedValueOnce({
      ok: true, status: 200, categories: 3, bookSearch: true, latencyMs: 42, searchResults: 5,
    })
    render(<IndexersTab indexers={[]} setIndexers={vi.fn()} prowlarrInstances={[]} setProwlarrInstances={vi.fn()} />)

    fireEvent.click(screen.getByText('settings.indexers.addButton'))

    const url = screen.getByPlaceholderText('settings.indexers.form.urlPlaceholderExample')
    fireEvent.change(url, { target: { value: 'https://idx.example/api' } })

    fireEvent.click(screen.getByText('common.test'))

    await waitFor(() => expect(testIndexer).toHaveBeenCalledTimes(1))
    expect(testIndexer.mock.calls[0][0]).toMatchObject({ url: 'https://idx.example/api' })
    // Success banner uses the testOk key with interpolated probe values.
    expect(await screen.findByText(/settings\.indexers\.testOk/)).toBeInTheDocument()
  })

  it('renders an actionable error when the test fails', async () => {
    testIndexer.mockRejectedValueOnce(new Error('HTTP 401'))
    render(<IndexersTab indexers={[]} setIndexers={vi.fn()} prowlarrInstances={[]} setProwlarrInstances={vi.fn()} />)

    fireEvent.click(screen.getByText('settings.indexers.addButton'))
    fireEvent.change(screen.getByPlaceholderText('settings.indexers.form.urlPlaceholderExample'), { target: { value: 'https://idx.example/api' } })
    fireEvent.click(screen.getByText('common.test'))

    await waitFor(() => expect(testIndexer).toHaveBeenCalledTimes(1))
    expect(await screen.findByText('settings.indexers.testFail error=HTTP 401')).toBeInTheDocument()
  })
})

// The edit form no longer holds the saved key (#2212), so a blank field means
// "keep the stored one". Both the Save payload and the Test button have to
// respect that: sending a blank key would wipe it, and probing with a blank key
// would report a failure the user cannot act on.
describe('IndexersTab edit form with a write-only API key', () => {
  beforeEach(() => vi.clearAllMocks())

  const openEditForm = () => {
    render(<IndexersTab indexers={[savedIndexer]} setIndexers={vi.fn()} prowlarrInstances={[]} setProwlarrInstances={vi.fn()} />)
    fireEvent.click(screen.getByText('common.edit'))
  }

  it('starts blank rather than seeding the saved key', () => {
    openEditForm()
    expect(screen.getByPlaceholderText('••••••••')).toHaveValue('')
    expect(screen.getByText('settings.indexers.form.apiKeyEditHint')).toBeInTheDocument()
  })

  it('omits apiKey from the save payload when the field is left blank', async () => {
    updateIndexer.mockResolvedValueOnce(savedIndexer)
    openEditForm()

    fireEvent.click(screen.getByText('common.save'))

    await waitFor(() => expect(updateIndexer).toHaveBeenCalledTimes(1))
    expect(updateIndexer.mock.calls[0][0]).toBe(7)
    expect(updateIndexer.mock.calls[0][1]).not.toHaveProperty('apiKey')
  })

  it('sends the typed key when the user enters a new one', async () => {
    updateIndexer.mockResolvedValueOnce(savedIndexer)
    openEditForm()

    fireEvent.change(screen.getByPlaceholderText('••••••••'), { target: { value: 'rotated' } })
    fireEvent.click(screen.getByText('common.save'))

    await waitFor(() => expect(updateIndexer).toHaveBeenCalledTimes(1))
    expect(updateIndexer.mock.calls[0][1]).toMatchObject({ apiKey: 'rotated' })
  })

  it('tests by id (using the stored key) when the field is blank', async () => {
    testIndexerById.mockResolvedValueOnce({
      ok: true, status: 200, categories: 3, bookSearch: true, latencyMs: 42, searchResults: 5,
    })
    openEditForm()

    // The saved row carries its own Test button; the edit form's is the last.
    fireEvent.click(screen.getAllByText('common.test').at(-1)!)

    await waitFor(() => expect(testIndexerById).toHaveBeenCalledTimes(1))
    expect(testIndexerById).toHaveBeenCalledWith(7)
    expect(testIndexer).not.toHaveBeenCalled()
    expect((await screen.findAllByText(/settings\.indexers\.testOk/)).length).toBeGreaterThan(0)
  })

  it('tests inline with the typed key so it can be validated before saving', async () => {
    testIndexer.mockResolvedValueOnce({
      ok: true, status: 200, categories: 3, bookSearch: true, latencyMs: 42, searchResults: 5,
    })
    openEditForm()

    fireEvent.change(screen.getByPlaceholderText('••••••••'), { target: { value: 'rotated' } })
    fireEvent.click(screen.getAllByText('common.test').at(-1)!)

    await waitFor(() => expect(testIndexer).toHaveBeenCalledTimes(1))
    expect(testIndexer.mock.calls[0][0]).toMatchObject({ apiKey: 'rotated' })
    expect(testIndexerById).not.toHaveBeenCalled()
  })
})
