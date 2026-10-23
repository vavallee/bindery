import { describe, it, expect, vi } from 'vitest'
import { render, screen, cleanup } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import type { ComponentType } from 'react'
import en from '../../i18n/locales/en.json'
import { HEADINGS, SettingsTabId } from './settingsSearchIndex'
import { mockMatchMedia } from '../../test-utils'

// Render check for the Settings search index (#3059): a heading in the index is
// only useful if the exact text is on the page when its tab opens. A heading
// that sits in a modal, behind a mode switch or behind a loading state would
// send the person to the tab and then do nothing. Each tab is mounted with the
// bare minimum (every API call answers with an empty list or object) and every
// indexed heading must be in the DOM.

vi.mock('../../auth/AuthContext', () => ({
  useAuth: () => ({ isAdmin: true, status: { authenticated: true, role: 'admin' }, refresh: vi.fn() }),
}))
vi.mock('react-i18next', async () => {
  const lookup = (key: string): string | undefined => {
    const v = key.split('.').reduce<unknown>((o, k) => (o as Record<string, unknown> | undefined)?.[k], en)
    return typeof v === 'string' ? v : undefined
  }
  const t = (key: string, opts?: unknown) => {
    const fallback = typeof opts === 'string' ? opts : undefined
    const vars = opts && typeof opts === 'object' ? (opts as Record<string, unknown>) : {}
    return (lookup(key) ?? fallback ?? key).replace(/\{\{(\w+)\}\}/g, (_m, k) => String(vars[k] ?? ''))
  }
  const value = { t, i18n: { changeLanguage: vi.fn(), language: 'en' } }
  return { useTranslation: () => value, Trans: ({ children }: { children?: unknown }) => children }
})
vi.mock('../../api/client', async importOriginal => {
  const actual = await importOriginal<typeof import('../../api/client')>()
  // Any method the tab calls resolves: list* with [], the rest with {}.
  const shaped: Record<string, unknown> = {
    getLogs: [],
    getStorage: { dirs: [] },
    hardcoverLists: { account: '', lists: [] },
    oidcProviders: [],
    absLibraries: [],
    absConfig: { featureEnabled: true, baseUrl: '', label: '', enabled: false, libraryId: '', pathRemap: '', apiKeyConfigured: false },
    absConflicts: { items: [], total: 0 },
    absImportRuns: [],
    absReviewItems: { items: [], total: 0 },
  }
  const api = new Proxy({}, {
    get: (_t, name: string) => vi.fn(() => Promise.resolve(name in shaped ? shaped[name] : /^list/.test(name) ? [] : {})),
  })
  return { ...actual, api }
})

const noop = () => {}
const TABS: Record<SettingsTabId, () => Promise<{ default: ComponentType<Record<string, unknown>> }>> = {
  general: () => import('./GeneralTab'),
  indexers: () => import('./IndexersTab'),
  clients: () => import('./ClientsTab'),
  notifications: () => import('./NotificationsTab'),
  quality: () => import('./QualityTab'),
  metadata: () => import('./MetadataTab'),
  rootfolders: () => import('./RootFoldersTab'),
  calibre: () => import('./CalibreTab'),
  abs: () => import('./ABSTab'),
  grimmory: () => import('./GrimmoryTab'),
  'api-keys': () => import('./ApiKeysTab'),
  import: () => import('./ImportTab'),
  blocklist: () => import('./BlocklistTab'),
  logs: () => import('./LogsTab'),
  advanced: () => import('./AdvancedTab'),
  about: () => import('./AboutTab'),
} as never

const PROPS: Partial<Record<SettingsTabId, Record<string, unknown>>> = {
  indexers: { indexers: [], setIndexers: noop, prowlarrInstances: [], setProwlarrInstances: noop },
  clients: { clients: [], setClients: noop },
}

// Headings that are deliberately indexed but not on the page by default.
const NOT_ON_PAGE: Record<string, string> = {}

const text = (h: (typeof HEADINGS)[number]) => {
  if (h.literal) return h.literal
  const v = (h.key as string).split('.').reduce<unknown>((o, k) => (o as Record<string, unknown> | undefined)?.[k], en)
  return typeof v === 'string' ? v : (h.fallback as string)
}

describe('every indexed heading is on the page when its tab opens', () => {
  const byTab = new Map<SettingsTabId, typeof HEADINGS>()
  for (const h of HEADINGS) byTab.set(h.tab, [...(byTab.get(h.tab) ?? []), h])

  it.each([...byTab.entries()])('%s', async (tab, headings) => {
    const restore = mockMatchMedia(false)
    const Tab = (await TABS[tab]()).default
    render(<MemoryRouter><Tab {...(PROPS[tab] ?? {})} /></MemoryRouter>)
    for (const h of headings) {
      if (NOT_ON_PAGE[h.key ?? h.literal ?? '']) continue
      const name = text(h)
      const found = await screen.findAllByText(name, { selector: 'h1,h2,h3,h4,h5,h6' }, { timeout: 3000 }).catch(() => [])
      expect(found.length, `${tab}: "${name}" is not rendered as a heading`).toBeGreaterThan(0)
    }
    cleanup()
    restore()
  })
})
