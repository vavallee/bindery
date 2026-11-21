import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import MetadataTab from './MetadataTab'
import { api } from '../../api/client'
import { expectFieldValuesWired } from '../../test/fieldValue'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, fallback?: unknown, opts?: Record<string, string>) => {
      if (typeof fallback !== 'string') return key
      return fallback.replace(/\{\{(\w+)\}\}/g, (_, k) => opts?.[k] ?? '')
    },
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

function seed(entries: Record<string, string>) {
  vi.mocked(api.listSettings).mockResolvedValue(
    Object.entries(entries).map(([key, value]) => ({ key, value })) as Awaited<ReturnType<typeof api.listSettings>>,
  )
}

async function providerSelect() {
  return (await screen.findByText('Primary metadata provider')).parentElement!.querySelector('select')!
}
const subOf = (el: HTMLElement) => document.getElementById(el.getAttribute('aria-describedby')!)!

beforeEach(() => {
  seed({})
  vi.mocked(api.status).mockResolvedValue({ version: 'dev', commit: 'x', buildDate: '', enhancedHardcoverApi: false, hardcoverTokenConfigured: true })
  vi.mocked(api.listMetadataProfiles).mockResolvedValue([])
  vi.mocked(api.setSetting).mockReset().mockResolvedValue(undefined)
})

describe('MetadataTab current value subtitles (#3989)', () => {
  it('shows the default provider, then the chosen provider by name after a save', async () => {
    render(<MetadataTab />)
    const select = await providerSelect()
    expect(subOf(select)).toHaveTextContent('Current: Default (OpenLibrary)')
    fireEvent.change(select, { target: { value: 'bnf' } })
    await waitFor(() => expect(subOf(select)).toHaveTextContent('Current: BnF (Bibliothèque nationale de France)'))
    expect(subOf(select).textContent).not.toContain('bnf')
  })

  it('goes back to the saved provider when the save fails', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    seed({ 'metadata.primary_provider': 'dnb' })
    vi.mocked(api.setSetting).mockRejectedValue(new Error('boom'))
    render(<MetadataTab />)
    const select = await providerSelect()
    await waitFor(() => expect(subOf(select)).toHaveTextContent('Current: DNB (Deutsche Nationalbibliothek)'))
    fireEvent.change(select, { target: { value: 'nb' } })
    await waitFor(() => expect(api.setSetting).toHaveBeenCalled())
    await waitFor(() => expect(subOf(select)).toHaveTextContent('Current: DNB (Deutsche Nationalbibliothek)'))
  })

  it('shows translated media type, monitor mode and new item choices', async () => {
    seed({ 'default.media_type': 'audiobook', 'author.default_monitor_mode': 'future', 'author.default_monitor_new_items': 'none' })
    render(<MetadataTab />)
    await providerSelect()
    const mediaType = screen.getByText('Default media type').parentElement!.querySelector('select')!
    await waitFor(() => expect(subOf(mediaType)).toHaveTextContent('Current: Audiobook'))
    const mode = screen.getByText('Default monitor mode').parentElement!.querySelector('select')!
    expect(subOf(mode)).toHaveTextContent('Current: Future books only (grows on refresh)')
    const newItems = screen.getByText('Default for monitoring newly discovered books').parentElement!.querySelector('select')!
    expect(subOf(newItems)).toHaveTextContent('Current: Don’t add them')
  })

  it('shows On or Off for a toggle and follows it after a save', async () => {
    render(<MetadataTab />)
    await providerSelect()
    const toggle = document.querySelector('[aria-describedby="recommendations-enabled-value"]') as HTMLElement
    expect(document.getElementById('recommendations-enabled-value')).toHaveTextContent('Current: Off')
    fireEvent.click(toggle)
    await waitFor(() => expect(document.getElementById('recommendations-enabled-value')).toHaveTextContent('Current: On'))
  })

  it('wires every subtitle on the tab to a control', async () => {
    render(<MetadataTab />)
    await providerSelect()
    expectFieldValuesWired(document.body)
  })
})
