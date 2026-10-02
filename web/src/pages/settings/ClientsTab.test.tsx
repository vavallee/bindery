import { describe, it, expect, vi, beforeEach } from 'vitest'
import { useState } from 'react'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import ClientsTab from './ClientsTab'
import { api, type DiagnoseResult, type DownloadClient } from '../../api/client'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, fallback?: unknown) => typeof fallback === 'string' ? fallback : key,
    i18n: { changeLanguage: vi.fn() },
  }),
}))

vi.mock('../../api/client', async importOriginal => {
  const actual = await importOriginal<typeof import('../../api/client')>()
  return {
    ...actual,
    api: {
      ...actual.api,
      updateDownloadClient: vi.fn(),
      deleteDownloadClient: vi.fn(),
      testDownloadClient: vi.fn(),
      testDownloadClientConfig: vi.fn(),
      diagnoseDownloadClient: vi.fn(),
    },
  }
})

function makeClient(overrides: Partial<DownloadClient> = {}): DownloadClient {
  return {
    id: 7,
    name: 'SABnzbd',
    type: 'sabnzbd',
    host: 'sabnzbd',
    port: 8080,
    // The API blanks both credentials and reports presence separately (#2213).
    apiKey: '',
    username: '',
    password: '',
    apiKeyConfigured: true,
    passwordConfigured: false,
    useSsl: false,
    urlBase: '',
    category: 'books',
    categoryAudiobook: '',
    pathRemap: '',
    enabled: true,
    priority: 0,
    enabledForBooks: true,
    enabledForAudiobooks: true,
    ...overrides,
  }
}

function Harness({ initial }: { initial: DownloadClient[] }) {
  const [clients, setClients] = useState(initial)
  return <ClientsTab clients={clients} setClients={setClients} />
}

function renderTab(initial: DownloadClient[]) {
  render(<Harness initial={initial} />)
}

function openEditForm() {
  fireEvent.click(screen.getByRole('button', { name: 'common.edit' }))
}

function save() {
  fireEvent.click(screen.getByRole('button', { name: 'common.save' }))
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(api.updateDownloadClient).mockImplementation(async (id, data) => makeClient({ ...data, id }))
  vi.mocked(api.testDownloadClientConfig).mockResolvedValue({ message: 'Connection verified' })
})

describe('download client edit form credentials', () => {
  it('never seeds the credential field from the client', () => {
    renderTab([makeClient({ apiKeyConfigured: true })])
    openEditForm()

    const credential = screen.getByLabelText('settings.clients.apiKeyEditLabel') as HTMLInputElement
    expect(credential.value).toBe('')
    expect(credential.placeholder).toBe('••••••••')
    expect(screen.getByText('settings.clients.credentialKeepHint')).toBeInTheDocument()
  })

  it('says so when no credential is stored yet', () => {
    renderTab([makeClient({ apiKeyConfigured: false })])
    openEditForm()

    const credential = screen.getByLabelText('settings.clients.apiKeyEditLabel') as HTMLInputElement
    expect(credential.placeholder).toBe('API Key')
    expect(screen.getByText('settings.clients.credentialUnsetHint')).toBeInTheDocument()
  })

  it('omits the credential when the user leaves it blank', async () => {
    renderTab([makeClient()])
    openEditForm()
    fireEvent.change(screen.getByPlaceholderText('Name'), { target: { value: 'Renamed' } })
    save()

    await waitFor(() => expect(api.updateDownloadClient).toHaveBeenCalled())
    const [, payload] = vi.mocked(api.updateDownloadClient).mock.calls[0]
    expect(payload).not.toHaveProperty('apiKey')
    expect(payload).not.toHaveProperty('password')
    expect(payload.clearPassword).toBe(true)
    expect(payload.name).toBe('Renamed')
  })

  it('sends the typed credential when the user enters one', async () => {
    renderTab([makeClient()])
    openEditForm()
    fireEvent.change(screen.getByLabelText('settings.clients.apiKeyEditLabel'), { target: { value: 'rotated-key' } })
    save()

    await waitFor(() => expect(api.updateDownloadClient).toHaveBeenCalled())
    const [, payload] = vi.mocked(api.updateDownloadClient).mock.calls[0]
    expect(payload.apiKey).toBe('rotated-key')
    expect(payload.clearPassword).toBe(true)
    expect(payload).not.toHaveProperty('clearApiKey')
  })

  // The regression this guards: an empty string used to be how the form
  // dropped the credential it no longer needed. Under the write-only contract
  // an empty string means "keep", so a type switch that did not send the clear
  // flag would leave the old secret in the row.
  it('clears the abandoned API key when switching to a password client', async () => {
    renderTab([makeClient({ type: 'sabnzbd', apiKeyConfigured: true })])
    openEditForm()
    fireEvent.change(screen.getByRole('combobox'), { target: { value: 'qbittorrent' } })
    fireEvent.change(screen.getByLabelText('settings.clients.passwordEditLabel'), { target: { value: 'qbit-pass' } })
    save()

    await waitFor(() => expect(api.updateDownloadClient).toHaveBeenCalled())
    const [, payload] = vi.mocked(api.updateDownloadClient).mock.calls[0]
    expect(payload.clearApiKey).toBe(true)
    expect(payload.password).toBe('qbit-pass')
    expect(payload).not.toHaveProperty('apiKey')
    expect(payload).not.toHaveProperty('clearPassword')
  })

  it('clears the abandoned password when switching to an API-key client', async () => {
    renderTab([makeClient({ type: 'qbittorrent', name: 'qBit', apiKeyConfigured: false, passwordConfigured: true })])
    openEditForm()
    fireEvent.change(screen.getByRole('combobox'), { target: { value: 'sabnzbd' } })
    fireEvent.change(screen.getByLabelText('settings.clients.apiKeyEditLabel'), { target: { value: 'sab-key' } })
    save()

    await waitFor(() => expect(api.updateDownloadClient).toHaveBeenCalled())
    const [, payload] = vi.mocked(api.updateDownloadClient).mock.calls[0]
    expect(payload.clearPassword).toBe(true)
    expect(payload.apiKey).toBe('sab-key')
    expect(payload).not.toHaveProperty('password')
    expect(payload).not.toHaveProperty('clearApiKey')
  })

  // A type switch abandons the stored credential, so the field must stop
  // claiming there is one to keep.
  it('stops offering to keep the credential after a type switch', () => {
    renderTab([makeClient({ type: 'sabnzbd', apiKeyConfigured: true })])
    openEditForm()
    fireEvent.change(screen.getByRole('combobox'), { target: { value: 'qbittorrent' } })

    const credential = screen.getByLabelText('settings.clients.passwordEditLabel') as HTMLInputElement
    expect(credential.placeholder).toBe('Password')
    expect(screen.getByText('settings.clients.credentialUnsetHint')).toBeInTheDocument()
  })

  // The backend fills a blank credential in from the saved row, but only when
  // the probe still points at that row's host, so the id has to travel with it.
  it('sends the saved client id with a config test', async () => {
    renderTab([makeClient()])
    openEditForm()
    // The client row has a Test button of its own; the form's is the later one.
    const testButtons = screen.getAllByRole('button', { name: 'common.test' })
    fireEvent.click(testButtons[testButtons.length - 1])

    await waitFor(() => expect(api.testDownloadClientConfig).toHaveBeenCalled())
    const [payload] = vi.mocked(api.testDownloadClientConfig).mock.calls[0]
    expect(payload.id).toBe(7)
    expect(payload).not.toHaveProperty('apiKey')
  })
})

describe('download client add form', () => {
  it('still sends the credential it collected', async () => {
    const addDownloadClient = vi.spyOn(api, 'addDownloadClient').mockImplementation(async data => makeClient({ ...data, id: 99 }))
    renderTab([])

    fireEvent.click(screen.getByRole('button', { name: 'settings.clients.addButton' }))
    fireEvent.change(screen.getByPlaceholderText('Host'), { target: { value: 'sabnzbd' } })
    fireEvent.change(screen.getByPlaceholderText('API Key'), { target: { value: 'fresh-key' } })
    save()

    await waitFor(() => expect(addDownloadClient).toHaveBeenCalled())
    expect(addDownloadClient.mock.calls[0][0]).toMatchObject({ apiKey: 'fresh-key', password: '' })
    addDownloadClient.mockRestore()
  })
})

function makeDiagnosis(overrides: Partial<DiagnoseResult> = {}): DiagnoseResult {
  return {
    clientType: 'qbittorrent',
    checks: [
      { code: 'connect', status: 'pass', message: 'Connected to qBittorrent.' },
      { code: 'local_path', status: 'fail', message: 'Bindery would look outside every folder.', fix: 'Add a path remap.' },
      { code: 'hardlinks', status: 'skipped', message: 'Skipped because an earlier check failed.' },
      { code: 'indexer_reach', status: 'unknown', message: 'Bindery cannot test indexer reach.', fix: 'Check the client network.' },
    ],
    paths: [{ clientPath: '/torrents/books', source: 'the category save path', remapRule: 'none', localPath: '/torrents/books' }],
    hardlinks: [],
    primaryFix: 'Add a path remap.',
    ...overrides,
  }
}

describe('download client diagnose', () => {
  it('runs for the saved client and shows the fix, the checklist and the paths inline', async () => {
    vi.mocked(api.diagnoseDownloadClient).mockResolvedValue(makeDiagnosis())
    renderTab([makeClient({ id: 7, type: 'qbittorrent' })])

    fireEvent.click(screen.getByRole('button', { name: 'settings.clients.diagnose.button' }))

    const panel = await screen.findByRole('region', { name: 'settings.clients.diagnose.heading' })
    expect(api.diagnoseDownloadClient).toHaveBeenCalledWith(7)
    expect(screen.getByRole('alert')).toHaveTextContent('Add a path remap.')
    expect(panel).toHaveTextContent('Bindery would look outside every folder.')
    expect(panel).toHaveTextContent('Bindery cannot test indexer reach.')
    expect(panel).toHaveTextContent('/torrents/books')

    fireEvent.click(screen.getByRole('button', { name: 'settings.clients.diagnose.close' }))
    expect(screen.queryByRole('region', { name: 'settings.clients.diagnose.heading' })).not.toBeInTheDocument()
  })

  it('says so when nothing needs fixing and lists hardlinks per library folder', async () => {
    vi.mocked(api.diagnoseDownloadClient).mockResolvedValue(makeDiagnosis({
      checks: [{ code: 'connect', status: 'pass', message: 'Connected.' }],
      primaryFix: '',
      hardlinks: [
        { downloadPath: '/downloads', root: '/books', result: 'yes', linkable: true },
        { downloadPath: '/downloads', root: '/audiobooks', result: 'no', linkable: false, reason: 'different filesystems' },
      ],
    }))
    renderTab([makeClient()])
    fireEvent.click(screen.getByRole('button', { name: 'settings.clients.diagnose.button' }))

    expect(await screen.findByText('settings.clients.diagnose.allClear')).toBeInTheDocument()
    const table = screen.getByRole('table', { name: 'settings.clients.diagnose.hardlinksCaption' })
    expect(table).toHaveTextContent('/audiobooks')
    expect(table).toHaveTextContent('different filesystems')
  })

  it('shows an error when the request fails', async () => {
    vi.mocked(api.diagnoseDownloadClient).mockRejectedValue(new Error('boom'))
    renderTab([makeClient()])
    fireEvent.click(screen.getByRole('button', { name: 'settings.clients.diagnose.button' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('settings.clients.diagnose.failed')
  })
})
