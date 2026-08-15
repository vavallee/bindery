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

function seedSettings(entries: Record<string, string>) {
  vi.mocked(api.listSettings).mockResolvedValue(
    Object.entries(entries).map(([key, value]) => ({ key, value })) as Awaited<
      ReturnType<typeof api.listSettings>
    >,
  )
}

function seedStatus(hardcoverTokenConfigured: boolean) {
  vi.mocked(api.status).mockResolvedValue({
    version: 'dev',
    commit: 'unknown',
    buildDate: '',
    enhancedHardcoverApi: false,
    hardcoverTokenConfigured,
  })
}

async function primaryProviderSelect() {
  const select = (await screen.findByText('Primary metadata provider'))
    .parentElement!.querySelector('select')
  if (!select) throw new Error('primary metadata provider select not found')
  return select
}

beforeEach(() => {
  seedSettings({})
  seedStatus(false)
  vi.mocked(api.listMetadataProfiles).mockResolvedValue([])
  vi.mocked(api.setSetting).mockReset()
  vi.mocked(api.setSetting).mockResolvedValue(undefined)
})

// #2040 — Hardcover may be promoted from enricher to primary, but only when an
// API token is stored: Hardcover authenticates every GraphQL query, so a
// tokenless primary would fail every author and book lookup.
describe('Primary metadata provider selector', () => {
  it('defaults to OpenLibrary when nothing is stored', async () => {
    render(<MetadataTab />)
    const select = await primaryProviderSelect()
    await waitFor(() => expect(select.value).toBe('openlibrary'))
  })

  it('disables the Hardcover option when no API token is configured', async () => {
    render(<MetadataTab />)
    await primaryProviderSelect()
    const option = await screen.findByRole('option', {
      name: 'Hardcover — requires an API token (Settings → API Keys)',
    })
    await waitFor(() => expect(option).toBeDisabled())
  })

  it('offers Hardcover once a token is configured and persists the choice', async () => {
    seedStatus(true)
    render(<MetadataTab />)
    const select = await primaryProviderSelect()
    const option = await screen.findByRole('option', { name: 'Hardcover (curated catalogue)' })
    await waitFor(() => expect(option).not.toBeDisabled())

    fireEvent.change(select, { target: { value: 'hardcover' } })
    await waitFor(() => {
      expect(api.setSetting).toHaveBeenCalledWith('metadata.primary_provider', 'hardcover')
    })
  })

  it('shows the stored provider on load', async () => {
    seedSettings({ 'metadata.primary_provider': 'dnb' })
    render(<MetadataTab />)
    const select = await primaryProviderSelect()
    await waitFor(() => expect(select.value).toBe('dnb'))
  })

  // The selector is wired at boot (cmd/bindery/main.go), and even after a
  // restart the catalogue provider is resolved per author from the ID they are
  // already linked to. Both facts have to be on screen: without the first a
  // user flips the setting and sees nothing happen, and without the second they
  // restart, still see their existing authors unchanged, and conclude it is
  // broken. #1771 tracks making the aggregator live-reconfigurable.
  it('explains that the change needs a restart and leaves existing authors alone', async () => {
    seedStatus(true)
    render(<MetadataTab />)
    const select = await primaryProviderSelect()

    const hint = select.parentElement!.querySelector('select ~ p')
    expect(hint).not.toBeNull()
    expect(hint!.textContent).toMatch(/restart/i)
    expect(hint!.textContent).toMatch(/already in your library/i)
  })
})
