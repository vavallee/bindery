import { describe, it, expect, vi, beforeEach } from 'vitest'
import { useState } from 'react'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import ClientsTab from './ClientsTab'
import { addRemapRule } from './helpers'
import { api, type DownloadClient } from '../../api/client'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, opts?: unknown) => typeof opts === 'string' ? opts : key,
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
      testDownloadClient: vi.fn(),
    },
  }
})

function makeClient(overrides: Partial<DownloadClient> = {}): DownloadClient {
  return {
    id: 3,
    name: 'qBit',
    type: 'qbittorrent',
    host: 'qbit',
    port: 8080,
    apiKey: '',
    username: 'u',
    password: '',
    apiKeyConfigured: false,
    passwordConfigured: true,
    useSsl: false,
    urlBase: '',
    category: 'books',
    categoryAudiobook: '',
    pathRemap: '',
    enabled: true,
    ...overrides,
  }
}

function Harness({ initial }: { initial: DownloadClient[] }) {
  const [clients, setClients] = useState(initial)
  return <ClientsTab clients={clients} setClients={setClients} />
}

const warning = {
  message: 'Connected',
  pathVisibility: { status: 'warning' as const, message: 'cannot read it', path: '/downloads/books', remotePath: '/downloads/books' },
  remapCandidates: [
    { path: '/data/downloads', source: 'ebookDownloadDir' as const, before: '/downloads/books/Example Book', after: '/data/downloads/Example Book' },
    { path: '/books', source: 'library' as const, before: '/downloads/books/Example Book', after: '/books/Example Book' },
  ],
}

async function runTest() {
  fireEvent.click(screen.getByRole('button', { name: 'common.test' }))
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(api.updateDownloadClient).mockImplementation(async (id, data) => makeClient({ ...data, id }))
})

describe('path remap assistant', () => {
  it('appears for a warning on a supported client with a labelled chooser and preview', async () => {
    vi.mocked(api.testDownloadClient).mockResolvedValue(warning)
    render(<Harness initial={[makeClient()]} />)
    await runTest()

    expect(await screen.findByRole('region', { name: 'settings.clients.remapAssist.heading' })).toBeTruthy()
    expect(screen.getByText('/downloads/books', { selector: 'code' })).toBeTruthy()
    const chooser = screen.getByLabelText('settings.clients.remapAssist.chooseLabel') as HTMLSelectElement
    expect(chooser.value).toBe('/data/downloads')
    expect(screen.getByText('/data/downloads/Example Book')).toBeTruthy()

    fireEvent.change(chooser, { target: { value: '/books' } })
    expect(screen.getByText('/books/Example Book')).toBeTruthy()
    expect(screen.queryByText('/data/downloads/Example Book')).toBeNull()
  })

  it.each(['sabnzbd', 'transmission', 'deluge'])('never appears for %s, even if candidates arrive', async type => {
    vi.mocked(api.testDownloadClient).mockResolvedValue(warning)
    render(<Harness initial={[makeClient({ type })]} />)
    await runTest()

    await screen.findByText('cannot read it')
    expect(screen.queryByRole('region', { name: 'settings.clients.remapAssist.heading' })).toBeNull()
  })

  it('stays away when the folder is visible or the server sends no candidates', async () => {
    vi.mocked(api.testDownloadClient).mockResolvedValue({ message: 'ok', pathVisibility: { status: 'ok', remotePath: '/downloads/books' } })
    render(<Harness initial={[makeClient()]} />)
    await runTest()
    await waitFor(() => expect(api.testDownloadClient).toHaveBeenCalled())
    expect(screen.queryByRole('region', { name: 'settings.clients.remapAssist.heading' })).toBeNull()
  })

  it('saves through the update call, keeps existing rules, then re-runs Test', async () => {
    vi.mocked(api.testDownloadClient).mockResolvedValueOnce(warning)
    vi.mocked(api.testDownloadClient).mockResolvedValueOnce({ message: 'ok', pathVisibility: { status: 'ok', remotePath: '/downloads/books' } })
    render(<Harness initial={[makeClient({ pathRemap: '/seed:/other' })]} />)
    await runTest()

    fireEvent.click(await screen.findByRole('button', { name: 'settings.clients.remapAssist.apply' }))

    await waitFor(() => expect(api.testDownloadClient).toHaveBeenCalledTimes(2))
    expect(api.updateDownloadClient).toHaveBeenCalledTimes(1)
    // Only the field being changed goes over the wire: the server merges onto
    // the stored row, so no other field (or credential) is touched.
    expect(api.updateDownloadClient).toHaveBeenCalledWith(3, { pathRemap: '/seed:/other,/downloads/books:/data/downloads' })
    // The update happened before the second Test.
    const updateOrder = vi.mocked(api.updateDownloadClient).mock.invocationCallOrder[0]
    expect(updateOrder).toBeGreaterThan(vi.mocked(api.testDownloadClient).mock.invocationCallOrder[0])
    expect(updateOrder).toBeLessThan(vi.mocked(api.testDownloadClient).mock.invocationCallOrder[1])
    // Re-test came back visible, so the assistant is gone.
    await waitFor(() => expect(screen.queryByRole('region', { name: 'settings.clients.remapAssist.heading' })).toBeNull())
    // The Apply button unmounted with focus, so focus lands on the row's Test button.
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('button', { name: 'common.test' })))
  })

  it('replaces a rule with the same source instead of appending behind it', async () => {
    vi.mocked(api.testDownloadClient).mockResolvedValueOnce(warning)
    vi.mocked(api.testDownloadClient).mockResolvedValueOnce({ message: 'ok' })
    render(<Harness initial={[makeClient({ pathRemap: '/downloads/books:/wrong,/a:/b' })]} />)
    await runTest()
    fireEvent.click(await screen.findByRole('button', { name: 'settings.clients.remapAssist.apply' }))
    await waitFor(() => expect(api.updateDownloadClient).toHaveBeenCalledWith(3, { pathRemap: '/downloads/books:/data/downloads,/a:/b' }))
  })

  it('labels candidates by what they are', async () => {
    vi.mocked(api.testDownloadClient).mockResolvedValue({
      ...warning,
      remapCandidates: [
        { path: '/e', source: 'ebookDownloadDir', before: 'b', after: 'a' },
        { path: '/a', source: 'audiobookDownloadDir', before: 'b', after: 'a' },
        { path: '/l', source: 'library', before: 'b', after: 'a' },
      ],
    })
    render(<Harness initial={[makeClient()]} />)
    await runTest()
    const options = (await screen.findAllByRole('option')).map(o => o.textContent)
    expect(options).toEqual([
      '/e (settings.clients.remapAssist.source.ebookDownloadDir)',
      '/a (settings.clients.remapAssist.source.audiobookDownloadDir)',
      '/l (settings.clients.remapAssist.source.library)',
    ])
  })

  it.each([
    ['samePath', 'settings.clients.remapAssist.note.samePath'],
    ['unsupportedChars', 'settings.clients.remapAssist.note.unsupportedChars'],
  ] as const)('explains a %s note and offers no Apply', async (remapNote, text) => {
    vi.mocked(api.testDownloadClient).mockResolvedValue({ message: 'ok', pathVisibility: warning.pathVisibility, remapNote })
    render(<Harness initial={[makeClient()]} />)
    await runTest()

    expect(await screen.findByText(text)).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'settings.clients.remapAssist.apply' })).toBeNull()
    expect(screen.queryByRole('combobox')).toBeNull()
  })

  it('shows the error and does not re-test when the save fails', async () => {
    vi.mocked(api.testDownloadClient).mockResolvedValue(warning)
    vi.mocked(api.updateDownloadClient).mockRejectedValue(new Error('pair 1 is bad'))
    render(<Harness initial={[makeClient()]} />)
    await runTest()

    fireEvent.click(await screen.findByRole('button', { name: 'settings.clients.remapAssist.apply' }))
    await waitFor(() => expect(screen.getAllByRole('alert').some(a => a.textContent?.includes('settings.clients.remapAssist.failed'))).toBe(true))
    expect(api.testDownloadClient).toHaveBeenCalledTimes(1)
  })
})

describe('addRemapRule', () => {
  it('builds remote:local, appends, and does not repeat a rule', () => {
    expect(addRemapRule('', '/downloads', '/data')).toBe('/downloads:/data')
    expect(addRemapRule('/a:/b', '/downloads', '/data')).toBe('/a:/b,/downloads:/data')
    expect(addRemapRule('/downloads:/data', '/downloads', '/data')).toBe('/downloads:/data')
  })

  it('replaces a rule whose source matches, never appending behind it (the first equal prefix wins in pathmap)', () => {
    expect(addRemapRule('/data/dl:/wrong', '/data/dl', '/right')).toBe('/data/dl:/right')
    expect(addRemapRule('/x:/y,/data/dl:/wrong,/z:/w', '/data/dl', '/right')).toBe('/x:/y,/data/dl:/right,/z:/w')
    expect(addRemapRule('/data/dl/:/wrong', '/data/dl', '/right')).toBe('/data/dl:/right')
    // POSIX sources are case sensitive: a different case is a different rule.
    expect(addRemapRule('/Data/dl:/keep', '/data/dl', '/right')).toBe('/Data/dl:/keep,/data/dl:/right')
  })

  it('collides Windows variants of the same source', () => {
    expect(addRemapRule('S:\\Downloads:/wrong', 's:/downloads', '/right')).toBe('s:/downloads:/right')
    expect(addRemapRule('s:/downloads/:/wrong', 'S:\\Downloads', '/right')).toBe('S:\\Downloads:/right')
    expect(addRemapRule('\\\\NAS\\Books:/wrong', '//nas/books', '/right')).toBe('//nas/books:/right')
    expect(addRemapRule('S:\\Downloads:/wrong,/a:/b', 'S:/downloads', '/right')).toBe('S:/downloads:/right,/a:/b')
    expect(addRemapRule('T:\\Downloads:/other', 'S:/downloads', '/right')).toBe('T:\\Downloads:/other,S:/downloads:/right')
  })

  it('removes further rules with the same source while replacing', () => {
    expect(addRemapRule('/d:/1,/d:/2', '/d', '/3')).toBe('/d:/3')
  })

  it('keeps Windows drive and share paths intact on the client side', () => {
    expect(addRemapRule('', 'S:/Downloads/books', '/data/downloads')).toBe('S:/Downloads/books:/data/downloads')
    expect(addRemapRule('', '//nas/books/dl', '/data')).toBe('//nas/books/dl:/data')
  })
})
