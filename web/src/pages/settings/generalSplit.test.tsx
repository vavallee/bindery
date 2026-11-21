import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, cleanup, waitFor } from '@testing-library/react'
import { api } from '../../api/client'
import GeneralTab from './GeneralTab'
import NamingTab from './NamingTab'
import StorageTab from './StorageTab'
import SchedulesTab from './SchedulesTab'
import { FIELDS } from './settingsFieldIndex'
import { expectFieldValuesWired } from '../../test/fieldValue'

vi.mock('../../components/ThemeToggle', () => ({ default: () => <button type="button">Theme</button> }))
vi.mock('../../components/LanguageSwitcher', () => ({ default: () => <select aria-label="Language" /> }))
vi.mock('../../auth/AuthContext', () => ({
  useAuth: () => ({
    status: { authenticated: true, username: 'admin', role: 'admin', mode: 'enabled', setupRequired: false },
    loading: false,
    isAdmin: true,
    refresh: vi.fn(),
    logout: vi.fn(),
  }),
}))
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
      libraryScanStatus: vi.fn(),
      getStorage: vi.fn(),
      authConfig: vi.fn(),
      setSetting: vi.fn(),
    },
  }
})

// Every setting anchor the single General tab rendered before it was split
// (#3990). Taken from the id="..." and anchorId="..." attributes of
// GeneralTab.tsx at a5360bd6 (`git show a5360bd6:web/src/pages/settings/GeneralTab.tsx`),
// not from the new code, so a setting dropped by the split fails here.
const LEGACY_GENERAL_KEYS = [
  'import.mode', 'import.audiobook.mode', 'import.audiobook.flatten_multi_disc',
  'import.ebook.content_check', 'import.audiobook.content_check',
  'import.drop_folder', 'import.audiobook.drop_folder', 'import.drop_layout', 'import.drop_link_mode',
  'import.source_folders', 'import.write_opf_sidecar',
  'naming.bookTemplate', 'naming_template_audiobook', 'naming.audiobook_file_template',
  'search.preferredLanguage', 'search.rejectWords', 'search.interval',
  'hardcover.sync_interval', 'library.scan_interval', 'authors.discovery.interval', 'series.monitoring.interval',
]

// The legacy section headings, by the i18n key the old tab rendered them with.
const LEGACY_HEADINGS = [
  'settings.general.appearance', 'settings.general.security', 'settings.general.fileNaming',
  'settings.general.downloads', 'settings.general.storage', 'settings.general.dbCheck',
  'settings.general.library', 'settings.general.search', 'settings.general.hardcoverSync',
  'settings.general.libraryScan', 'settings.general.discovery', 'settings.general.seriesCheck',
  'settings.general.defaultLibraryLocation',
]

const TABS = { general: GeneralTab, naming: NamingTab, storage: StorageTab, schedules: SchedulesTab }

function seed(entries: Record<string, string>) {
  vi.mocked(api.listSettings).mockResolvedValue(
    Object.entries(entries).map(([key, value]) => ({ key, value })) as Awaited<ReturnType<typeof api.listSettings>>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  seed({})
  vi.mocked(api.libraryScanStatus).mockRejectedValue(new Error('no scan'))
  vi.mocked(api.getStorage).mockRejectedValue(new Error('no storage'))
  vi.mocked(api.authConfig).mockResolvedValue({ mode: 'enabled', username: 'admin' } as Awaited<ReturnType<typeof api.authConfig>>)
})

// Render one tab and wait for it to leave its loading state.
async function mount(name: keyof typeof TABS) {
  const Tab = TABS[name]
  const view = render(<Tab />)
  await waitFor(() => expect(screen.queryByText('common.loading')).toBeNull())
  await screen.findByTestId('settings-page-desc')
  return view
}

// Which tab each anchor id is rendered on, for one stored configuration.
async function placement(settings: Record<string, string>) {
  const found: Record<string, string[]> = {}
  for (const name of Object.keys(TABS) as (keyof typeof TABS)[]) {
    seed(settings)
    const { container } = await mount(name)
    for (const key of LEGACY_GENERAL_KEYS) {
      if (container.querySelector(`[id="${key}"]`)) (found[key] ??= []).push(name)
    }
    cleanup()
  }
  return found
}

describe('General settings split (#3990)', () => {
  it('renders every setting the old tab had exactly once across the four tabs', async () => {
    // Some controls only show for certain import modes, so look at two
    // configurations that together reveal them all.
    const ebookExternal = await placement({ 'import.mode': 'external', 'import.audiobook.mode': 'copy' })
    const audiobookExternal = await placement({ 'import.mode': 'copy', 'import.audiobook.mode': 'external' })
    for (const found of [ebookExternal, audiobookExternal]) {
      for (const [key, where] of Object.entries(found)) expect(where, `${key} rendered on ${where.join(', ')}`).toHaveLength(1)
    }
    const seen = new Set([...Object.keys(ebookExternal), ...Object.keys(audiobookExternal)])
    expect(LEGACY_GENERAL_KEYS.filter(k => !seen.has(k))).toEqual([])
  })

  it('points the field index at the tab that renders each setting', async () => {
    const hits: Record<string, string> = {}
    for (const settings of [{ 'import.mode': 'external', 'import.audiobook.mode': 'copy' }, { 'import.mode': 'copy', 'import.audiobook.mode': 'external' }]) {
      for (const [key, where] of Object.entries(await placement(settings))) hits[key] = where[0]
    }
    const wrong = LEGACY_GENERAL_KEYS.filter(k => FIELDS.find(f => f.key === k)?.tab !== hits[k])
    expect(wrong).toEqual([])
  })

  it('shows each legacy section heading on exactly one tab', async () => {
    const counts: Record<string, number> = {}
    for (const name of Object.keys(TABS) as (keyof typeof TABS)[]) {
      seed({})
      const { container } = await mount(name)
      for (const h of container.querySelectorAll('h3')) counts[h.textContent ?? ''] = (counts[h.textContent ?? ''] ?? 0) + 1
      cleanup()
    }
    for (const key of LEGACY_HEADINGS) {
      // The test t() returns the key, or the fallback the tab passes.
      const matches = Object.entries(counts).filter(([text]) => text === key || text === ({
        'settings.general.hardcoverSync': 'Hardcover list sync',
        'settings.general.libraryScan': 'Library scan',
        'settings.general.security': 'Security',
      } as Record<string, string>)[key])
      expect(matches.map(m => m[1]), key).toEqual([1])
    }
  })

  it.each(Object.keys(TABS) as (keyof typeof TABS)[])('%s tab: one line description, wired current values, unique ids', async name => {
    seed({ 'import.mode': 'external', 'import.audiobook.mode': 'copy' })
    const { container } = await mount(name)
    expect(screen.getByTestId('settings-page-desc').textContent).toMatch(/^settings\.general\.desc\./)
    if (name !== 'storage') expectFieldValuesWired(container)
    const ids = Array.from(container.querySelectorAll('[id]')).map(e => e.id)
    expect(ids.filter((id, i) => ids.indexOf(id) !== i)).toEqual([])
    expect(container.querySelectorAll('h3').length).toBeGreaterThan(0)
  })

  it('only fetches what its own sections need', async () => {
    await mount('schedules')
    expect(api.getStorage).not.toHaveBeenCalled()
    expect(api.libraryScanStatus).not.toHaveBeenCalled()
    expect(api.authConfig).not.toHaveBeenCalled()
    cleanup()
    await mount('naming')
    expect(api.getStorage).toHaveBeenCalled()
    expect(api.libraryScanStatus).not.toHaveBeenCalled()
  })
})
