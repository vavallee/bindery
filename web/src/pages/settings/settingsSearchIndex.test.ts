import { describe, it, expect } from 'vitest'
import en from '../../i18n/locales/en.json'
import { HEADINGS, MAX_RESULTS, searchSettings, SettingsTabId } from './settingsSearchIndex'
import { scanHeadings } from './scanHeadings'

const ALL: { id: SettingsTabId; label: string }[] = [
  { id: 'general', label: 'General' },
  { id: 'about', label: 'About' },
  { id: 'indexers', label: 'Indexers' },
  { id: 'logs', label: 'Logs' },
  { id: 'advanced', label: 'Advanced' },
  { id: 'metadata', label: 'Metadata' },
]

// A stand in for another locale: distinct names, one with diacritics.
const names: Record<string, string> = {
  'settings.general.appearance': 'Darstellung Bücher',
  'settings.general.fileNaming': 'Zeta Dateinamen',
  'settings.general.downloads': 'Alpha Dateinamen',
  'settings.general.storage': 'Speicher',
}
const t = (key: string, fallback?: string) => names[key] ?? fallback ?? key

const run = (query: string, extra: Partial<Parameters<typeof searchSettings>[0]> = {}) =>
  searchSettings({ query, tabs: ALL, t, isAdmin: true, ...extra })
const ids = (query: string, extra: Partial<Parameters<typeof searchSettings>[0]> = {}) =>
  run(query, extra).results.map(r => r.id)

describe('searchSettings ranking', () => {
  it('ranks a prefix match above a word prefix above a substring', () => {
    // Listed worst first, so only the rank can put them in order.
    const tabs = [
      { id: 'logs' as const, label: 'Bigzzq' }, // substring
      { id: 'indexers' as const, label: 'Zebra zzq' }, // word prefix
      { id: 'general' as const, label: 'Zzqbook' }, // prefix
    ]
    const r = searchSettings({ query: 'zzq', tabs, t, isAdmin: true }).results
    expect(r.map(x => x.id)).toEqual(['tab:general', 'tab:indexers', 'tab:logs'])
  })

  it('keeps index order within one rank', () => {
    // Both headings are word prefix matches; Zeta is listed first in HEADINGS.
    expect(ids('dateinamen')).toEqual(['h:general:settings.general.fileNaming', 'h:general:settings.general.downloads'])
  })

  it('lists tabs and headings before setting keys, even for a better textual key match', () => {
    const r = run('log', { settingKeys: ['catalog.size', 'log.level'] }).results
    expect(r[0]).toMatchObject({ kind: 'tab', tab: 'logs' })
    const firstKey = r.findIndex(x => x.kind === 'key')
    expect(firstKey).toBeGreaterThan(0)
    expect(r.slice(firstKey).every(x => x.kind === 'key')).toBe(true)
    // among keys, a prefix match still beats a substring match
    expect(r.filter(x => x.kind === 'key').map(x => x.id)).toEqual(['k:log.level', 'k:catalog.size'])
  })

  it('puts a substring heading match ahead of a prefix key match', () => {
    // The key offset must outweigh a heading's worst rank (substring, 2).
    const tabs = [{ id: 'advanced' as const, label: 'Advanced' }, { id: 'general' as const, label: 'General' }]
    const r = searchSettings({ query: 'stellung', tabs, t, isAdmin: true, settingKeys: ['stellung.x'] }).results
    expect(r.map(x => x.kind)).toEqual(['heading', 'key'])
  })

  it('offers keys only when Advanced is a tab the viewer has', () => {
    const noAdv = ALL.filter(x => x.id !== 'advanced')
    const out = searchSettings({ query: 'log', tabs: noAdv, t, isAdmin: false, settingKeys: ['log.level'] })
    expect(out.results.some(x => x.kind === 'key')).toBe(false)
  })

  it('folds diacritics in both the query and the heading', () => {
    expect(ids('bucher')).toEqual(['h:general:settings.general.appearance'])
    expect(ids('BÜCHER')).toEqual(['h:general:settings.general.appearance'])
  })

  it('trims and collapses whitespace in the query', () => {
    expect(ids('  zeta    dateinamen ')).toEqual(['h:general:settings.general.fileNaming'])
  })

  it('returns nothing for a blank query', () => {
    expect(run('   ')).toEqual({ results: [], total: 0 })
  })

  it('caps the list and reports the real total', () => {
    const settingKeys = Array.from({ length: 20 }, (_v, i) => `bulk.key${i}`)
    const out = run('bulk', { settingKeys })
    expect(out.results).toHaveLength(MAX_RESULTS)
    expect(out.total).toBe(20)
  })

  it('does not repeat a tab as its own heading', () => {
    // settings.advanced.title falls back to "Advanced", the tab's own name.
    expect(ids('advanced')).toEqual(['tab:advanced'])
  })

  it('hides adminOnly headings from a non admin even when the tab is open to them', () => {
    expect(ids('speicher', { isAdmin: false })).toEqual([])
    expect(ids('speicher', { isAdmin: true })).toEqual(['h:general:settings.general.storage'])
  })
})

describe('HEADINGS', () => {
  const lookup = (key: string) => key.split('.').reduce<unknown>((o, k) => (o as Record<string, unknown> | undefined)?.[k], en)

  it('only names keys that exist in en.json or carry a fallback', () => {
    for (const h of HEADINGS) {
      if (h.key) expect(typeof lookup(h.key) === 'string' || !!h.fallback, h.key).toBe(true)
    }
  })

  // Drift guard: every heading a settings component renders must be in the
  // index (or be allowlisted with a reason), and every indexed heading must
  // still be rendered. It scans every non test .tsx in this directory, so a
  // panel or section split out of a tab is covered too. The render check in
  // settingsSearchIndex.render.test.tsx proves the indexed ones are on the page.
  const sources = import.meta.glob(['./*.tsx', '!./*.test.tsx'], { query: '?raw', import: 'default', eager: true }) as Record<string, string>

  // Headings that are deliberately not indexed, with the reason.
  const ALLOWED_KEYS: Record<string, string> = {
    'settings.calibre.runs.modalTitle': 'a modal title carrying a run id, only open on demand',
    'settings.calibre.pushAll.label': 'the title of the progress dialog shown during a push; on the page it is a label in plugin mode only',
    'settings.calibre.deliveries.resetTitle': 'the title of a confirmation dialog',
    'settings.clients.diagnose.heading': 'a panel that appears after Diagnose is pressed',
    'settings.clients.remapAssist.heading': 'a panel that appears on demand under a client',
  }
  const ALLOWED_LITERALS: Record<string, string> = {
    Calibre: 'the tab title, covered by the tab result',
    Audiobookshelf: 'the tab title, covered by the tab result',
    Grimmory: 'the tab title, covered by the tab result',
  }
  // Headings whose text is an expression, as "file|expression".
  const ALLOWED_DYNAMIC: Record<string, string> = {
    'AdvancedTab.tsx|{title}': 'a namespace name from the descriptors',
    'ClientDiagnosePanel.tsx|{mediaLabel(p.mediaType)}': 'inside the on demand diagnose panel',
  }

  const found = scanHeadings(sources)
  const sourceKeys = new Set(found.filter(f => f.kind === 'key').map(f => f.value))
  const sourceLiterals = new Set(found.filter(f => f.kind === 'literal').map(f => f.value))
  const dynamicIds = found.filter(f => f.kind === 'dynamic').map(f => `${f.file}|${f.value}`)

  it('finds the sources it is meant to scan', () => {
    expect(Object.keys(sources).length).toBeGreaterThan(20)
    expect(sourceKeys.size).toBeGreaterThan(20)
    // Translated headings are keys now; the product names (Audiobookshelf, Calibre, Grimmory, CWA) stay literal.
    expect(sourceLiterals.size).toBeGreaterThan(2)
  })

  it('indexes every heading the components render through t()', () => {
    const indexed = new Set(HEADINGS.map(h => h.key).filter(Boolean))
    expect([...sourceKeys].filter(k => !indexed.has(k) && !ALLOWED_KEYS[k])).toEqual([])
  })

  it('indexes every literal heading the components render', () => {
    const indexed = new Set(HEADINGS.map(h => h.literal).filter(Boolean))
    expect([...sourceLiterals].filter(l => !indexed.has(l) && !ALLOWED_LITERALS[l])).toEqual([])
  })

  it('accounts for every heading whose text is an expression or template', () => {
    // Per row names ({c.name}, {profile.name}) are data, not sections.
    const rowName = /\|\{[A-Za-z]+\.name\}$/
    expect(dynamicIds.filter(d => !ALLOWED_DYNAMIC[d] && !rowName.test(d))).toEqual([])
  })

  it('does not index a heading no component renders any more', () => {
    for (const h of HEADINGS) {
      if (h.key) expect(sourceKeys.has(h.key), h.key).toBe(true)
      if (h.literal) expect(sourceLiterals.has(h.literal), h.literal).toBe(true)
    }
  })

  it('does not allowlist a heading that no longer exists', () => {
    for (const k of Object.keys(ALLOWED_KEYS)) expect(sourceKeys.has(k), k).toBe(true)
    for (const l of Object.keys(ALLOWED_LITERALS)) expect(sourceLiterals.has(l), l).toBe(true)
    for (const d of Object.keys(ALLOWED_DYNAMIC)) expect(dynamicIds.includes(d), d).toBe(true)
  })
})

describe('scanHeadings', () => {
  const scan = (src: string) => scanHeadings({ './X.tsx': src }).map(f => `${f.kind}:${f.value}`)

  it('reads keys in either quote style, with a fallback or options', () => {
    expect(scan(`<h3>{t('a.b')}</h3><h2>{t("c.d", 'Fallback')}</h2><h4>{t('e.f', { n: 1 })}</h4>`)).toEqual(['key:a.b', 'key:c.d', 'key:e.f'])
  })

  it('reads literals of any shape, over several lines', () => {
    expect(scan(`<h3 className="x">\n  Step 2: set up (optional)\n</h3><h5>lower case</h5>`)).toEqual(['literal:Step 2: set up (optional)', 'literal:lower case'])
  })

  it('copes with => inside the opening tag', () => {
    expect(scan(`<h3 onClick={() => go()} className="x">{t('a.b')}</h3>`)).toEqual(['key:a.b'])
  })

  it('flags expressions and templates instead of skipping them', () => {
    expect(scan('<h3>{title}</h3><h4>{`${a} b`}</h4><h3>Name {x}</h3>')).toEqual(['dynamic:{title}', 'dynamic:{`${a} b`}', 'dynamic:Name {x}'])
  })
})
