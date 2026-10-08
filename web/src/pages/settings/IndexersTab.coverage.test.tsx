import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor, fireEvent, within } from '@testing-library/react'
import { useState } from 'react'
import { acceptConfirm } from '../../test-utils'
import type { Indexer, ProwlarrInstance } from '../../api/client'

// i18n: echo the key plus interpolated options so assertions are stable and do
// not depend on English copy.
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
    listIndexers: vi.fn(),
    addIndexer: vi.fn(),
    updateIndexer: vi.fn(),
    deleteIndexer: vi.fn(),
    testIndexer: vi.fn(),
    testIndexerConfig: vi.fn(),
    listProwlarr: vi.fn(),
    addProwlarr: vi.fn(),
    updateProwlarr: vi.fn(),
    deleteProwlarr: vi.fn(),
    testProwlarr: vi.fn(),
    syncProwlarr: vi.fn(),
  },
}))

import { api } from '../../api/client'
import IndexersTab from './IndexersTab'

const m = vi.mocked(api)

function makeIndexer(overrides: Partial<Indexer> = {}): Indexer {
  return {
    id: 1,
    name: 'Alpha',
    type: 'newznab',
    url: 'https://alpha.example/api',
    apiKey: '',
    apiKeyConfigured: true,
    categories: [7020],
    priority: 0,
    enabled: true,
    ...overrides,
  } as Indexer
}

function makeProwlarr(overrides: Partial<ProwlarrInstance> = {}): ProwlarrInstance {
  return {
    id: 5,
    name: 'Prowlarr',
    url: 'http://prowlarr:9696',
    apiKey: '',
    syncOnStartup: true,
    enabled: true,
    ...overrides,
  }
}

// The tab is controlled by SettingsPage; this harness owns the state the same
// way so updates made through the setters show up on screen.
function Harness({ indexers: initialIdx = [], prowlarr: initialP = [] }: { indexers?: Indexer[]; prowlarr?: ProwlarrInstance[] }) {
  const [indexers, setIndexers] = useState<Indexer[]>(initialIdx)
  const [prowlarr, setProwlarr] = useState<ProwlarrInstance[]>(initialP)
  return <IndexersTab indexers={indexers} setIndexers={setIndexers} prowlarrInstances={prowlarr} setProwlarrInstances={setProwlarr} />
}

const okResult = { ok: true, status: 200, categories: 4, bookSearch: true, latencyMs: 12, searchResults: 3 }

beforeEach(() => {
  vi.clearAllMocks()
  m.listIndexers.mockResolvedValue([])
  m.listProwlarr.mockResolvedValue([])
})

describe('IndexersTab indexer rows', () => {
  it('shows the empty state with no indexers', () => {
    render(<Harness />)
    expect(screen.getByText('settings.indexers.empty')).toBeInTheDocument()
  })

  it('toggles an indexer off through updateIndexer and reflects the result', async () => {
    const idx = makeIndexer()
    m.updateIndexer.mockResolvedValue({ ...idx, enabled: false })
    render(<Harness indexers={[idx]} />)

    const toggle = screen.getByRole('switch')
    expect(toggle).toHaveAttribute('title', 'common.disable')
    fireEvent.click(toggle)

    await waitFor(() => expect(m.updateIndexer).toHaveBeenCalledWith(1, expect.objectContaining({ id: 1, enabled: false })))
    await waitFor(() => expect(screen.getByRole('switch')).toHaveAttribute('aria-checked', 'false'))
    expect(screen.getByRole('switch')).toHaveAttribute('title', 'common.enable')
  })

  it('asks before deleting, and only deletes on yes', async () => {
    m.deleteIndexer.mockResolvedValue(undefined as never)
    render(<Harness indexers={[makeIndexer()]} />)

    fireEvent.click(screen.getByText('common.delete'))
    fireEvent.click(screen.getByText('common.no'))
    expect(m.deleteIndexer).not.toHaveBeenCalled()
    expect(screen.getByText('Alpha')).toBeInTheDocument()

    fireEvent.click(screen.getByText('common.delete'))
    fireEvent.click(screen.getByText('common.yes'))
    await waitFor(() => expect(m.deleteIndexer).toHaveBeenCalledWith(1))
    expect(await screen.findByText('settings.indexers.empty')).toBeInTheDocument()
  })

  it('shows daily query usage, and the reached state once the cap is spent', () => {
    render(
      <Harness
        indexers={[
          makeIndexer({ id: 1, name: 'Under', dailyQueryLimit: 10, dailyQueriesUsed: 3 }),
          makeIndexer({ id: 2, name: 'Spent', dailyQueryLimit: 5, dailyQueriesUsed: 5 }),
          makeIndexer({ id: 3, name: 'Uncapped', dailyQueryLimit: 0 }),
        ]}
      />,
    )
    expect(screen.getByText('settings.indexers.quotaUsage used=3 limit=10')).toBeInTheDocument()
    expect(screen.getByText('settings.indexers.quotaReached limit=5')).toBeInTheDocument()
    expect(screen.queryByText(/limit=0/)).not.toBeInTheDocument()
  })

  it('prefers a live cooldown over the stored error, and separates auth failures from other failures', () => {
    render(
      <Harness
        indexers={[
          makeIndexer({ id: 1, name: 'Cooling', cooldownUntil: '2026-10-05T12:00:00Z', cooldownReason: 'rate limited', lastError: 'rate limited' }),
          makeIndexer({ id: 2, name: 'Auth', lastError: 'bad key', lastErrorCode: 100, lastFailureAt: '2026-10-05T10:00:00Z' }),
          makeIndexer({ id: 3, name: 'Down', lastError: 'timeout', lastErrorCode: 500 }),
        ]}
      />,
    )
    expect(screen.getByText(/^settings\.indexers\.cooldown .*error=rate limited$/)).toBeInTheDocument()
    expect(screen.queryByText(/healthFail error=rate limited/)).not.toBeInTheDocument()
    expect(screen.getByText(/^settings\.indexers\.healthAuthFail error=bad key \(/)).toBeInTheDocument()
    expect(screen.getByText('settings.indexers.healthFail error=timeout')).toBeInTheDocument()
  })

  it.each([
    ['ok with results', okResult, 'settings.indexers.testOk status=200 categories=4 latency=12 results=3'],
    [
      'ok with zero results warns and appends the search error',
      { ...okResult, searchResults: 0, searchError: 'no book search' },
      'settings.indexers.testWarn status=200 categories=4 latency=12 — no book search',
    ],
    ['a failed probe', { ...okResult, ok: false, error: 'HTTP 401' }, 'settings.indexers.testFail error=HTTP 401'],
    ['a failed probe with no error', { ...okResult, ok: false }, 'settings.indexers.testFail error=Unknown error'],
  ])('renders a saved-row test result: %s', async (_name, result, expected) => {
    m.testIndexer.mockResolvedValue(result)
    render(<Harness indexers={[makeIndexer()]} />)
    fireEvent.click(screen.getByText('common.test'))
    await waitFor(() => expect(m.testIndexer).toHaveBeenCalledWith(1))
    expect(await screen.findByText(expected)).toBeInTheDocument()
  })

  it('renders a thrown saved-row test as a failure', async () => {
    m.testIndexer.mockRejectedValue(new Error('network down'))
    render(<Harness indexers={[makeIndexer()]} />)
    fireEvent.click(screen.getByText('common.test'))
    expect(await screen.findByText('settings.indexers.testFail error=network down')).toBeInTheDocument()
  })
})

describe('IndexersTab edit indexer form', () => {
  it('saves every edited field and omits a blank API key so the stored key is kept', async () => {
    const idx = makeIndexer({ seedRatio: 1.5, seedRatioSource: 'prowlarr', seedTimeSource: 'prowlarr', seedTimeMinutes: 60 })
    m.updateIndexer.mockImplementation(async (_id, payload) => ({ ...idx, ...payload }) as Indexer)
    render(<Harness indexers={[idx]} />)

    fireEvent.click(screen.getByText('common.edit'))
    expect(screen.getByText('settings.indexers.form.seedRatioFromProwlarr')).toBeInTheDocument()
    expect(screen.getByText('settings.indexers.form.seedTimeFromProwlarr')).toBeInTheDocument()

    fireEvent.change(screen.getByPlaceholderText('settings.indexers.form.namePlaceholder'), { target: { value: 'Renamed' } })
    fireEvent.change(screen.getByDisplayValue('settings.indexers.form.typeNewznabUsenet'), { target: { value: 'torznab' } })
    fireEvent.change(screen.getByPlaceholderText('settings.indexers.form.urlPlaceholder'), { target: { value: 'https://beta.example/api' } })
    fireEvent.change(screen.getByPlaceholderText('settings.indexers.form.categoriesPlaceholder'), { target: { value: '7020, 3030' } })
    fireEvent.click(screen.getByLabelText('settings.indexers.form.includeParentCategories'))
    fireEvent.change(screen.getByPlaceholderText('0'), { target: { value: '5' } })
    fireEvent.click(screen.getByLabelText('settings.indexers.form.seedRatioUnlimited'))
    fireEvent.change(screen.getByLabelText('settings.indexers.form.inactiveSeedTime'), { target: { value: '30.9' } })
    fireEvent.blur(screen.getByLabelText('settings.indexers.form.inactiveSeedTime'))
    expect(screen.getByLabelText('settings.indexers.form.inactiveSeedTime')).toHaveValue(30)
    fireEvent.change(screen.getByLabelText('settings.indexers.form.seedTime'), { target: { value: '' } })
    fireEvent.click(screen.getByLabelText('settings.indexers.form.freeleechOnly'))
    const limit = screen.getByPlaceholderText('settings.indexers.form.dailyQueryLimitPlaceholder')
    fireEvent.change(limit, { target: { value: '7.8' } })
    fireEvent.blur(limit)
    expect(limit).toHaveValue(7)

    fireEvent.click(screen.getByText('common.save'))
    await waitFor(() => expect(m.updateIndexer).toHaveBeenCalledTimes(1))
    const [id, payload] = m.updateIndexer.mock.calls[0]
    expect(id).toBe(1)
    expect(payload).toMatchObject({
      name: 'Renamed',
      type: 'torznab',
      url: 'https://beta.example/api',
      categories: [7020, 3030],
      includeParentCategories: true,
      priority: 5,
      seedRatio: -1,
      seedTimeMinutes: null,
      inactiveSeedTimeMinutes: 30,
      freeleechOnly: true,
      dailyQueryLimit: 7,
    })
    expect(payload).not.toHaveProperty('apiKey')
    // The form closes and the row shows the saved name.
    expect(await screen.findByText('Renamed')).toBeInTheDocument()
    expect(screen.queryByText('settings.indexers.form.apiKeyEditHint')).not.toBeInTheDocument()
  })

  it('sends a typed API key, and turning unlimited back off clears the ratio', async () => {
    const idx = makeIndexer({ seedRatio: -1 })
    m.updateIndexer.mockResolvedValue(idx)
    render(<Harness indexers={[idx]} />)
    fireEvent.click(screen.getByText('common.edit'))

    const unlimited = screen.getByLabelText('settings.indexers.form.seedRatioUnlimited')
    expect(unlimited).toBeChecked()
    fireEvent.click(unlimited)
    const ratio = screen.getByPlaceholderText('settings.indexers.form.seedRatioPlaceholder')
    expect(ratio).not.toBeDisabled()
    fireEvent.change(ratio, { target: { value: '2' } })
    fireEvent.change(ratio, { target: { value: '' } })

    fireEvent.change(screen.getByPlaceholderText('••••••••'), { target: { value: 'new-key' } })
    fireEvent.click(screen.getByText('common.save'))
    await waitFor(() => expect(m.updateIndexer).toHaveBeenCalled())
    expect(m.updateIndexer.mock.calls[0][1]).toMatchObject({ apiKey: 'new-key', seedRatio: null })
  })

  it('tests by id with a blank key, and by config once a key is typed', async () => {
    m.testIndexer.mockResolvedValue(okResult)
    m.testIndexerConfig.mockRejectedValue(new Error('bad key'))
    render(<Harness indexers={[makeIndexer()]} />)
    fireEvent.click(screen.getByText('common.edit'))

    // Two Test buttons now: the row's and the form's. The form's is last.
    const formTest = () => screen.getAllByText('common.test').at(-1)!
    fireEvent.click(formTest())
    await waitFor(() => expect(m.testIndexer).toHaveBeenCalledWith(1))
    expect(m.testIndexerConfig).not.toHaveBeenCalled()
    expect(await screen.findByText(/settings\.indexers\.testOk/)).toBeInTheDocument()

    fireEvent.change(screen.getByPlaceholderText('••••••••'), { target: { value: 'typed' } })
    fireEvent.click(formTest())
    await waitFor(() =>
      expect(m.testIndexerConfig).toHaveBeenCalledWith({
        name: 'Alpha',
        type: 'newznab',
        url: 'https://alpha.example/api',
        apiKey: 'typed',
        categories: [7020],
      }),
    )
    expect(await screen.findByText('settings.indexers.testFail error=bad key')).toBeInTheDocument()

    fireEvent.click(screen.getByText('common.cancel'))
    expect(screen.queryByPlaceholderText('••••••••')).not.toBeInTheDocument()
  })
})

describe('IndexersTab add indexer form', () => {
  it('adds an indexer with the entered fields', async () => {
    m.addIndexer.mockImplementation(async data => ({ ...makeIndexer({ id: 9 }), ...data }) as Indexer)
    render(<Harness />)
    fireEvent.click(screen.getByText('settings.indexers.addButton'))

    fireEvent.change(screen.getByPlaceholderText('settings.indexers.form.namePlaceholderExample'), { target: { value: 'Gamma' } })
    fireEvent.change(screen.getByDisplayValue('settings.indexers.form.typeNewznab'), { target: { value: 'torznab' } })
    fireEvent.change(screen.getByPlaceholderText('settings.indexers.form.urlPlaceholderExample'), { target: { value: 'https://gamma/api' } })
    fireEvent.change(screen.getByPlaceholderText('settings.indexers.form.apiKey'), { target: { value: 'k' } })
    fireEvent.change(screen.getByPlaceholderText('settings.indexers.form.seedRatioPlaceholder'), { target: { value: '1.2' } })
    fireEvent.change(screen.getByLabelText('settings.indexers.form.seedTime'), { target: { value: '120' } })

    fireEvent.click(screen.getByText('common.save'))
    await waitFor(() =>
      expect(m.addIndexer).toHaveBeenCalledWith(
        expect.objectContaining({
          name: 'Gamma',
          type: 'torznab',
          url: 'https://gamma/api',
          apiKey: 'k',
          categories: [7020],
          enabled: true,
          seedRatio: 1.2,
          seedTimeMinutes: 120,
          dailyQueryLimit: null,
        }),
      ),
    )
    expect(await screen.findByText('Gamma')).toBeInTheDocument()
  })

  it('keeps Test disabled until a URL is entered and shows a thrown test as a failure', async () => {
    m.testIndexerConfig.mockRejectedValue('nope')
    render(<Harness />)
    fireEvent.click(screen.getByText('settings.indexers.addButton'))
    const test = screen.getByText('common.test')
    expect(test).toBeDisabled()
    fireEvent.change(screen.getByPlaceholderText('settings.indexers.form.urlPlaceholderExample'), { target: { value: 'https://x/api' } })
    expect(test).not.toBeDisabled()
    fireEvent.click(test)
    expect(await screen.findByText('settings.indexers.testFail error=Request failed')).toBeInTheDocument()

    fireEvent.click(screen.getByText('common.cancel'))
    expect(screen.queryByPlaceholderText('settings.indexers.form.urlPlaceholderExample')).not.toBeInTheDocument()
  })
})

describe('IndexersTab Prowlarr', () => {
  it('shows last sync with the count of indexers it owns, and hides Add once one exists', () => {
    render(
      <Harness
        indexers={[makeIndexer({ id: 1, prowlarrInstanceId: 5 }), makeIndexer({ id: 2, name: 'Manual' })]}
        prowlarr={[makeProwlarr({ lastSyncAt: '2026-10-01T00:00:00Z' })]}
      />,
    )
    expect(screen.getByText(/^settings\.prowlarr\.lastSynced .*count=1$/)).toBeInTheDocument()
    expect(screen.queryByText('settings.prowlarr.addButton')).not.toBeInTheDocument()
  })

  it.each([
    ['connected', () => m.testProwlarr.mockResolvedValue({ ok: 'true', version: '1.2.3' }), 'settings.prowlarr.connectedVersion version=1.2.3'],
    ['refused', () => m.testProwlarr.mockResolvedValue({ ok: 'false', error: 'refused' }), 'settings.prowlarr.connFailed error=refused'],
    ['thrown', () => m.testProwlarr.mockRejectedValue(new Error('boom')), 'boom'],
    ['thrown non-error', () => m.testProwlarr.mockRejectedValue('x'), 'settings.prowlarr.connFailedGeneric'],
  ])('reports a %s connection test', async (_name, arrange, expected) => {
    arrange()
    render(<Harness prowlarr={[makeProwlarr()]} />)
    fireEvent.click(screen.getByText('settings.prowlarr.test'))
    await waitFor(() => expect(m.testProwlarr).toHaveBeenCalledWith(5))
    expect(await screen.findByText(expected)).toBeInTheDocument()
  })

  it('syncs, reports the counts and reloads indexers and instances', async () => {
    m.syncProwlarr.mockResolvedValue({ added: 2, updated: 1, removed: 0 })
    m.listIndexers.mockResolvedValue([makeIndexer({ id: 11, name: 'Synced', prowlarrInstanceId: 5 })])
    m.listProwlarr.mockResolvedValue([makeProwlarr({ name: 'Prowlarr reloaded' })])
    render(<Harness prowlarr={[makeProwlarr()]} />)

    fireEvent.click(screen.getByText('settings.prowlarr.syncNow'))
    expect(await screen.findByText('settings.prowlarr.synced added=2 updated=1 removed=0')).toBeInTheDocument()
    expect(await screen.findByText('Synced')).toBeInTheDocument()
    expect(await screen.findByText('Prowlarr reloaded')).toBeInTheDocument()
  })

  it('reports a failed sync', async () => {
    m.syncProwlarr.mockRejectedValue(new Error('timeout'))
    render(<Harness prowlarr={[makeProwlarr()]} />)
    fireEvent.click(screen.getByText('settings.prowlarr.syncNow'))
    expect(await screen.findByText('settings.prowlarr.syncFailed error=timeout')).toBeInTheDocument()
    expect(m.listIndexers).not.toHaveBeenCalled()
  })

  it('deletes an instance after confirmation and reloads indexers', async () => {
    m.deleteProwlarr.mockResolvedValue(undefined as never)
    render(<Harness prowlarr={[makeProwlarr()]} />)
    fireEvent.click(screen.getByText('settings.prowlarr.delete'))
    await acceptConfirm()
    await waitFor(() => expect(m.deleteProwlarr).toHaveBeenCalledWith(5))
    await waitFor(() => expect(m.listIndexers).toHaveBeenCalled())
    // With no instance left the Add button is offered again.
    expect(await screen.findByText('settings.prowlarr.addButton')).toBeInTheDocument()
  })

  it('edits an instance, warns on a URL change and keeps the stored key when blank', async () => {
    m.updateProwlarr.mockImplementation(async (id, payload) => ({ ...makeProwlarr(), ...payload, id }) as ProwlarrInstance)
    render(<Harness prowlarr={[makeProwlarr()]} />)
    fireEvent.click(screen.getByText('settings.prowlarr.edit'))

    expect(screen.queryByText('settings.prowlarr.urlChangeWarning')).not.toBeInTheDocument()
    fireEvent.change(screen.getByDisplayValue('http://prowlarr:9696'), { target: { value: 'http://prowlarr2:9696' } })
    expect(screen.getByText('settings.prowlarr.urlChangeWarning')).toBeInTheDocument()
    fireEvent.change(screen.getByDisplayValue('Prowlarr'), { target: { value: 'Main' } })
    fireEvent.click(screen.getAllByRole('switch').at(-1)!)

    fireEvent.click(screen.getByText('common.save'))
    await waitFor(() =>
      expect(m.updateProwlarr).toHaveBeenCalledWith(5, { name: 'Main', url: 'http://prowlarr2:9696', syncOnStartup: false, enabled: true }),
    )
    expect(await screen.findByText('Main')).toBeInTheDocument()
    await waitFor(() => expect(m.listIndexers).toHaveBeenCalled())
  })

  it('sends a typed key on edit and shows a save error', async () => {
    m.updateProwlarr.mockRejectedValue(new Error('invalid key'))
    render(<Harness prowlarr={[makeProwlarr()]} />)
    fireEvent.click(screen.getByText('settings.prowlarr.edit'))
    fireEvent.change(screen.getByPlaceholderText('••••••••'), { target: { value: 'secret' } })
    fireEvent.click(screen.getByText('common.save'))
    expect(await screen.findByText('invalid key')).toBeInTheDocument()
    expect(m.updateProwlarr.mock.calls[0][1]).toMatchObject({ apiKey: 'secret' })

    fireEvent.click(screen.getByText('common.cancel'))
    expect(screen.queryByText('invalid key')).not.toBeInTheDocument()
  })

  it('adds an instance and syncs it straight away', async () => {
    const added = makeProwlarr({ id: 8 })
    m.addProwlarr.mockResolvedValue(added)
    m.syncProwlarr.mockResolvedValue({ added: 1, updated: 0, removed: 0 })
    m.listProwlarr.mockResolvedValue([{ ...added, lastSyncAt: '2026-10-05T00:00:00Z' }])
    render(<Harness />)

    fireEvent.click(screen.getByText('settings.prowlarr.addButton'))
    const save = screen.getByText('settings.prowlarr.saveAndSync')
    expect(save).toBeDisabled()
    fireEvent.change(screen.getByPlaceholderText('settings.prowlarr.urlPlaceholder'), { target: { value: 'http://p:9696' } })
    fireEvent.change(screen.getByPlaceholderText('settings.prowlarr.apiKeyPlaceholder'), { target: { value: 'k' } })
    fireEvent.click(screen.getByRole('switch'))
    fireEvent.click(save)

    await waitFor(() =>
      expect(m.addProwlarr).toHaveBeenCalledWith({ name: 'Prowlarr', url: 'http://p:9696', apiKey: 'k', syncOnStartup: false, enabled: true }),
    )
    await waitFor(() => expect(m.syncProwlarr).toHaveBeenCalledWith(8))
    expect(await screen.findByText(/settings\.prowlarr\.lastSynced/)).toBeInTheDocument()
  })

  it('still adds the instance when the first sync fails', async () => {
    m.addProwlarr.mockResolvedValue(makeProwlarr({ id: 8, name: 'Fresh' }))
    m.syncProwlarr.mockRejectedValue(new Error('sync failed'))
    render(<Harness />)
    fireEvent.click(screen.getByText('settings.prowlarr.addButton'))
    fireEvent.change(screen.getByPlaceholderText('settings.prowlarr.urlPlaceholder'), { target: { value: 'http://p:9696' } })
    fireEvent.change(screen.getByPlaceholderText('settings.prowlarr.apiKeyPlaceholder'), { target: { value: 'k' } })
    fireEvent.click(screen.getByText('settings.prowlarr.saveAndSync'))
    expect(await screen.findByText('Fresh')).toBeInTheDocument()
  })

  it('shows the add error and keeps the form open', async () => {
    m.addProwlarr.mockRejectedValue(new Error('unreachable'))
    render(<Harness />)
    fireEvent.click(screen.getByText('settings.prowlarr.addButton'))
    fireEvent.change(screen.getByPlaceholderText('settings.prowlarr.urlPlaceholder'), { target: { value: 'http://p:9696' } })
    fireEvent.change(screen.getByPlaceholderText('settings.prowlarr.apiKeyPlaceholder'), { target: { value: 'k' } })
    fireEvent.click(screen.getByText('settings.prowlarr.saveAndSync'))
    expect(await screen.findByText('unreachable')).toBeInTheDocument()
    expect(m.syncProwlarr).not.toHaveBeenCalled()

    const form = screen.getByPlaceholderText('settings.prowlarr.urlPlaceholder').closest('div')!.parentElement!
    fireEvent.click(within(form).getByText('common.cancel'))
    expect(screen.queryByPlaceholderText('settings.prowlarr.urlPlaceholder')).not.toBeInTheDocument()
  })
})
