import { describe, it, expect } from 'vitest'
import en from '../../i18n/locales/en.json'
import { FIELDS, fieldForAnchor } from './settingsFieldIndex'
import type { SettingsTabId } from './settingsSearchIndex'

// The guard behind the Settings search (#3059): a setting a tab saves must have
// a row in the field index, and a row must have its anchor in the tab it names.
// Both directions fail the build, so a new setting cannot be forgotten and a
// moved or renamed one cannot leave a dead deep link.
const sources = import.meta.glob('./*.tsx', { query: '?raw', import: 'default', eager: true }) as Record<string, string>

const TAB_FILE: Record<SettingsTabId, string> = {
  general: 'GeneralTab', indexers: 'IndexersTab', clients: 'ClientsTab', notifications: 'NotificationsTab',
  quality: 'QualityTab', metadata: 'MetadataTab', import: 'ImportTab', rootfolders: 'RootFoldersTab',
  logs: 'LogsTab', blocklist: 'BlocklistTab', calibre: 'CalibreTab', abs: 'ABSTab', grimmory: 'GrimmoryTab',
  'api-keys': 'ApiKeysTab', advanced: 'AdvancedTab', about: 'AboutTab',
}
const source = (file: string) => {
  const s = sources[`./${file}.tsx`]
  if (s === undefined) throw new Error(`no source for ${file}`)
  return s
}
const lookup = (key: string) => key.split('.').reduce<unknown>((o, k) => (o as Record<string, unknown> | undefined)?.[k], en)

// Setting keys a tab saves that are deliberately not searchable. Empty today:
// add a key here, with the reason, rather than skipping the check.
const NOT_SEARCHABLE: Record<string, string> = {}

describe('settings field index', () => {
  it('has one row per setting key', () => {
    const keys = FIELDS.map(f => f.key)
    expect(keys.filter((k, i) => keys.indexOf(k) !== i)).toEqual([])
  })

  it('has every row anchored exactly once in the tab it names', () => {
    const problems: string[] = []
    for (const f of FIELDS) {
      const src = source(TAB_FILE[f.tab])
      const anchored = src.split(`id="${f.key}"`).length - 1 + src.split(`anchorId="${f.key}"`).length - 1
      if (anchored !== 1) problems.push(`${f.key} is anchored ${anchored} times in ${TAB_FILE[f.tab]}`)
    }
    expect(problems).toEqual([])
  })

  it('has a row for every setting a tab saves', () => {
    const indexed = new Set(FIELDS.map(f => f.key))
    const saved: { key: string; file: string }[] = []
    for (const [path, src] of Object.entries(sources)) {
      const file = path.replace(/^\.\//, '').replace(/\.tsx$/, '')
      if (!/Tab$/.test(file) || file === 'AdvancedTab') continue // Advanced renders every key from the descriptors
      for (const m of src.matchAll(/(?:setSetting|saveSetting\w*)\(\s*'([^']+)'/g)) saved.push({ key: m[1], file })
      for (const m of src.matchAll(/_KEY = '([^']+)'/g)) saved.push({ key: m[1], file })
    }
    expect(saved.length).toBeGreaterThan(30) // the scan itself still finds the saves
    const missing = saved.filter(s => !indexed.has(s.key) && !(s.key in NOT_SEARCHABLE)).map(s => `${s.key} (${s.file})`)
    expect([...new Set(missing)]).toEqual([])
  })

  it('reads every label and help text through an en.json key', () => {
    const missing = FIELDS.flatMap(f => [f.label, f.help]).filter((k): k is string => !!k && typeof lookup(k) !== 'string')
    expect(missing).toEqual([])
  })

  it('only marks admin tabs and admin sections as admin only, so non admins keep their fields', () => {
    // General is the one tab a non admin opens that has fields, and its fields
    // all sit inside the admin block today.
    const nonAdminTabs = new Set<SettingsTabId>(['general', 'about'])
    const leaks = FIELDS.filter(f => nonAdminTabs.has(f.tab) && !f.adminOnly).map(f => f.key)
    expect(leaks).toEqual([])
  })

  it('resolves an anchor back to its field', () => {
    expect(fieldForAnchor('metadata.primary_provider')?.tab).toBe('metadata')
    expect(fieldForAnchor('not.a.setting')).toBeUndefined()
  })
})
