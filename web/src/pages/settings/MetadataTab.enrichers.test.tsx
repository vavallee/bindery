import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import MetadataTab from './MetadataTab'
import { api } from '../../api/client'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, fallback?: unknown) => (typeof fallback === 'string' ? fallback : key),
    i18n: { changeLanguage: vi.fn() },
  }),
}))
vi.mock('../../api/client', async importOriginal => {
  const actual = await importOriginal<typeof import('../../api/client')>()
  return {
    ...actual,
    api: {
      ...actual.api,
      listSettings: vi.fn(),
      listMetadataProfiles: vi.fn(),
      status: vi.fn(),
      setSetting: vi.fn(),
    },
  }
})

const KEY = 'metadata.enrichers_disabled'

function seedSettings(entries: Record<string, string>) {
  vi.mocked(api.listSettings).mockResolvedValue(
    Object.entries(entries).map(([key, value]) => ({ key, value })) as Awaited<
      ReturnType<typeof api.listSettings>
    >,
  )
}

async function box(name: string) {
  return (await screen.findByRole('checkbox', { name })) as HTMLInputElement
}

beforeEach(() => {
  seedSettings({})
  vi.mocked(api.status).mockResolvedValue({
    version: 'dev',
    commit: 'unknown',
    buildDate: '',
    enhancedHardcoverApi: false,
    hardcoverTokenConfigured: false,
  })
  vi.mocked(api.listMetadataProfiles).mockResolvedValue([])
  vi.mocked(api.setSetting).mockReset()
  vi.mocked(api.setSetting).mockResolvedValue(undefined)
})

// #1890 and #2015: each enricher and the two audiobook sources has a box,
// checked while the source is on, backed by one comma separated setting.
describe('Metadata sources that can be turned off', () => {
  it('shows every source on when nothing is stored, with the primary locked', async () => {
    render(<MetadataTab />)
    for (const name of ['Hardcover', 'Google Books', 'DNB (Deutsche Nationalbibliothek)', 'Audible catalogue', 'audnex']) {
      expect((await box(name)).checked).toBe(true)
    }
    const ol = await box('OpenLibrary')
    expect(ol.checked).toBe(true)
    expect(ol.disabled).toBe(true)
    expect(screen.getByText('This is the primary provider, so it cannot be turned off.')).toBeTruthy()
    expect((await box('DNB (Deutsche Nationalbibliothek)')).disabled).toBe(false)
  })

  it('reflects the stored list on load', async () => {
    seedSettings({ [KEY]: 'dnb,audnex' })
    render(<MetadataTab />)
    await waitFor(async () => expect((await box('DNB (Deutsche Nationalbibliothek)')).checked).toBe(false))
    expect((await box('audnex')).checked).toBe(false)
    expect((await box('Hardcover')).checked).toBe(true)
    expect((await box('Audible catalogue')).checked).toBe(true)
  })

  it('saves the comma list when a box is cleared, and removes the name when it is ticked again', async () => {
    render(<MetadataTab />)
    fireEvent.click(await box('DNB (Deutsche Nationalbibliothek)'))
    await waitFor(() => expect(api.setSetting).toHaveBeenLastCalledWith(KEY, 'dnb'))
    fireEvent.click(await box('Audible catalogue'))
    await waitFor(() => expect(api.setSetting).toHaveBeenLastCalledWith(KEY, 'dnb,audible'))
    fireEvent.click(await box('DNB (Deutsche Nationalbibliothek)'))
    await waitFor(() => expect(api.setSetting).toHaveBeenLastCalledWith(KEY, 'audible'))
    fireEvent.click(await box('Audible catalogue'))
    await waitFor(() => expect(api.setSetting).toHaveBeenLastCalledWith(KEY, ''))
  })

  it('saves names in a fixed order whatever order they were cleared in', async () => {
    render(<MetadataTab />)
    fireEvent.click(await box('audnex'))
    await waitFor(() => expect(api.setSetting).toHaveBeenLastCalledWith(KEY, 'audnex'))
    fireEvent.click(await box('DNB (Deutsche Nationalbibliothek)'))
    await waitFor(() => expect(api.setSetting).toHaveBeenLastCalledWith(KEY, 'dnb,audnex'))
  })

  it('treats a hardcover primary with no token as OpenLibrary, as the server does', async () => {
    seedSettings({ 'metadata.primary_provider': 'hardcover' })
    render(<MetadataTab />)
    expect((await box('OpenLibrary')).disabled).toBe(true)
    expect((await box('Hardcover')).disabled).toBe(false)
  })

  it('puts the box back and says so when the save fails', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    vi.mocked(api.setSetting).mockRejectedValueOnce(new Error('400'))
    render(<MetadataTab />)
    fireEvent.click(await box('DNB (Deutsche Nationalbibliothek)'))
    expect((await screen.findByRole('alert')).textContent).toContain('Could not save that change')
    await waitFor(async () => expect((await box('DNB (Deutsche Nationalbibliothek)')).checked).toBe(true))
    // The next save starts from the last saved list, not the failed one.
    await waitFor(async () => expect((await box('audnex')).disabled).toBe(false))
    fireEvent.click(await box('audnex'))
    await waitFor(() => expect(api.setSetting).toHaveBeenLastCalledWith(KEY, 'audnex'))
  })

  it('ignores a click while a save is in flight', async () => {
    let release!: () => void
    vi.mocked(api.setSetting).mockImplementationOnce(
      () => new Promise<void>(resolve => { release = () => resolve() }) as ReturnType<typeof api.setSetting>,
    )
    render(<MetadataTab />)
    fireEvent.click(await box('DNB (Deutsche Nationalbibliothek)'))
    await waitFor(() => expect(api.setSetting).toHaveBeenCalledTimes(1))
    fireEvent.click(await box('audnex'))
    expect(api.setSetting).toHaveBeenCalledTimes(1)
    release()
    await waitFor(async () => expect((await box('audnex')).disabled).toBe(false))
    expect((await box('audnex')).checked).toBe(true)
  })

  it('locks the current primary and frees the previous one', async () => {
    seedSettings({ 'metadata.primary_provider': 'dnb', [KEY]: 'dnb' })
    render(<MetadataTab />)
    const dnb = await box('DNB (Deutsche Nationalbibliothek)')
    expect(dnb.checked).toBe(true)
    expect(dnb.disabled).toBe(true)
    const ol = await box('OpenLibrary')
    expect(ol.disabled).toBe(false)
    expect(ol.checked).toBe(true)
  })
})
