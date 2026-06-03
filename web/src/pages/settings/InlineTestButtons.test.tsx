import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'

// i18n: echo the key (plus interpolated options) so assertions are stable.
vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, options?: Record<string, unknown>) => {
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
