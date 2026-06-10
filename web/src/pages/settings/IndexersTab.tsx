import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, Indexer, IndexerTestResult, ProwlarrInstance } from '../../api/client'
import { inputCls } from './formStyles'
import { parseCats, parsePriority } from './helpers'
import Toggle from './Toggle'
import { dangerLink } from '../../components/buttons'

// IndexerTestResultBanner renders a probe result with the same ok/warn/fail
// semantics as the saved-row Test feedback (ok=true + 0 results → amber warn).
function IndexerTestResultBanner({ r }: { r: IndexerTestResult }) {
  const { t } = useTranslation()
  const warn = r.ok && r.searchResults === 0
  const colorCls = !r.ok
    ? 'bg-red-100 text-red-800 dark:bg-red-900/30 dark:text-red-300'
    : warn
      ? 'bg-amber-100 text-amber-800 dark:bg-amber-900/30 dark:text-amber-300'
      : 'bg-emerald-100 text-emerald-800 dark:bg-emerald-900/30 dark:text-emerald-300'
  const dotCls = !r.ok ? 'bg-red-500' : warn ? 'bg-amber-500' : 'bg-emerald-500'
  return (
    <div role="status" className={`px-3 py-2 rounded text-xs flex items-center gap-2 ${colorCls}`}>
      <span className={`inline-block w-2 h-2 rounded-full flex-shrink-0 ${dotCls}`} />
      {!r.ok ? (
        <span>{t('settings.indexers.testFail', { error: r.error ?? 'Unknown error' })}</span>
      ) : warn ? (
        <span>{t('settings.indexers.testWarn', { status: r.status, categories: r.categories, latency: r.latencyMs })}{r.searchError ? ` — ${r.searchError}` : ''}</span>
      ) : (
        <span>{t('settings.indexers.testOk', { status: r.status, categories: r.categories, latency: r.latencyMs, results: r.searchResults })}</span>
      )}
    </div>
  )
}

// SeedRatioField renders the per-indexer seed-ratio override (#883) as a
// numeric input plus an "unlimited" toggle, so users never have to learn the
// -1 sentinel. It round-trips three shapes:
//   - null/undefined → no override (input empty, toggle off)
//   - >= 0           → that ratio (input shows it, toggle off)
//   - -1             → unlimited (input disabled/blank, toggle on)
function SeedRatioField({ value, onChange, source }: { value: number | null | undefined; onChange: (v: number | null) => void; source?: string }) {
  const { t } = useTranslation()
  const unlimited = value === -1
  // Text mirror of the numeric input so a half-typed "1." doesn't get clobbered.
  const [text, setText] = useState(value != null && value >= 0 ? String(value) : '')

  const handleText = (raw: string) => {
    setText(raw)
    if (raw.trim() === '') {
      onChange(null)
      return
    }
    const n = Number(raw)
    if (!Number.isNaN(n) && n >= 0) onChange(n)
  }

  const toggleUnlimited = () => {
    if (unlimited) {
      onChange(null)
      setText('')
    } else {
      onChange(-1)
    }
  }

  return (
    <div>
      <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">{t('settings.indexers.form.seedRatio')}</label>
      <div className="flex items-center gap-3">
        <input
          type="number"
          min="0"
          step="0.1"
          value={unlimited ? '' : text}
          disabled={unlimited}
          onChange={e => handleText(e.target.value)}
          placeholder={t('settings.indexers.form.seedRatioPlaceholder')}
          className={`${inputCls} flex-1 disabled:opacity-50`}
        />
        <label className="flex items-center gap-1.5 text-xs text-slate-600 dark:text-zinc-400 whitespace-nowrap">
          <input type="checkbox" checked={unlimited} onChange={toggleUnlimited} className="accent-emerald-600 dark:accent-emerald-500" />
          {t('settings.indexers.form.seedRatioUnlimited')}
        </label>
      </div>
      {source === 'prowlarr' && (
        <p className="text-xs text-amber-600 dark:text-amber-500 mt-1">{t('settings.indexers.form.seedRatioFromProwlarr')}</p>
      )}
      <p className="text-xs text-slate-500 dark:text-zinc-500 mt-1">{t('settings.indexers.form.seedRatioHint')}</p>
    </div>
  )
}

// indexers/prowlarrInstances are owned by SettingsPage so they can be fetched
// eagerly on page mount (matching the pre-refactor monolith), not on tab open.
interface Props {
  indexers: Indexer[]
  setIndexers: React.Dispatch<React.SetStateAction<Indexer[]>>
  prowlarrInstances: ProwlarrInstance[]
  setProwlarrInstances: React.Dispatch<React.SetStateAction<ProwlarrInstance[]>>
}

export default function IndexersTab({ indexers, setIndexers, prowlarrInstances, setProwlarrInstances }: Props) {
  const { t } = useTranslation()
  const [showAddProwlarr, setShowAddProwlarr] = useState(false)
  const [editingProwlarr, setEditingProwlarr] = useState<number | null>(null)
  const [prowlarrSyncResult, setProwlarrSyncResult] = useState<Record<number, string>>({})
  const [showAddIndexer, setShowAddIndexer] = useState(false)
  const [editingIndexer, setEditingIndexer] = useState<number | null>(null)
  const [indexerTestResults, setIndexerTestResults] = useState<Record<number, IndexerTestResult & { testing?: boolean }>>({})
  const [confirmDeleteIndexer, setConfirmDeleteIndexer] = useState<number | null>(null)
  const [prowlarrTestResult, setProwlarrTestResult] = useState<Record<number, { ok: boolean; msg: string }>>({})

  return (
    <div>
      <div className="flex justify-between items-center mb-4">
        <h3 className="text-lg font-semibold">{t('settings.indexers.heading')}</h3>
        <button onClick={() => setShowAddIndexer(true)} className="px-3 py-1.5 bg-emerald-600 hover:bg-emerald-500 rounded text-xs font-medium">
          {t('settings.indexers.addButton')}
        </button>
      </div>
      {indexers.length === 0 ? (
        <p className="text-slate-600 dark:text-zinc-500 text-sm">{t('settings.indexers.empty')}</p>
      ) : (
        <div className="space-y-2">
          {indexers.map(idx => (
            <div key={idx.id}>
              <div className="flex items-center justify-between p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900">
                <div className="flex items-center gap-3 min-w-0">
                  <Toggle
                    checked={idx.enabled}
                    onChange={async () => {
                      const updated = await api.updateIndexer(idx.id, { ...idx, enabled: !idx.enabled })
                      setIndexers(indexers.map(i => i.id === idx.id ? updated : i))
                    }}
                    title={idx.enabled ? t('common.disable') : t('common.enable')}
                  />
                  <div className="min-w-0">
                    <h4 className={`font-medium text-sm ${!idx.enabled ? 'text-slate-600 dark:text-zinc-500' : ''}`}>{idx.name}</h4>
                    <p className="text-xs text-slate-600 dark:text-zinc-500 truncate">{idx.url}</p>
                  </div>
                </div>
                <div className="flex items-center gap-3 flex-shrink-0">
                  <button onClick={() => setEditingIndexer(editingIndexer === idx.id ? null : idx.id)} className="text-xs text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white">{t('common.edit')}</button>
                  <button
                    disabled={indexerTestResults[idx.id]?.testing}
                    onClick={async () => {
                      setIndexerTestResults(prev => ({ ...prev, [idx.id]: { ...(prev[idx.id] ?? { ok: false, status: 0, categories: 0, bookSearch: false, latencyMs: 0, searchResults: 0 }), testing: true } }))
                      try {
                        const r = await api.testIndexer(idx.id)
                        setIndexerTestResults(prev => ({ ...prev, [idx.id]: { ...r, testing: false } }))
                      } catch (err: unknown) {
                        setIndexerTestResults(prev => ({ ...prev, [idx.id]: { ok: false, status: 0, categories: 0, bookSearch: false, latencyMs: 0, searchResults: 0, error: err instanceof Error ? err.message : 'Request failed', testing: false } }))
                      }
                    }}
                    className="text-xs text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white disabled:opacity-50"
                  >
                    {indexerTestResults[idx.id]?.testing ? t('common.testing') : t('common.test')}
                  </button>
                  {confirmDeleteIndexer === idx.id ? (
                    <span className="flex items-center gap-1.5">
                      <span className="text-xs text-slate-500 dark:text-zinc-500">{t('common.delete')}?</span>
                      <button
                        onClick={async () => {
                          await api.deleteIndexer(idx.id)
                          setIndexers(indexers.filter(i => i.id !== idx.id))
                          setConfirmDeleteIndexer(null)
                        }}
                        className={`text-xs font-medium ${dangerLink}`}
                      >{t('common.yes')}</button>
                      <button onClick={() => setConfirmDeleteIndexer(null)} className="text-xs text-slate-500 dark:text-zinc-500 hover:text-slate-700 dark:hover:text-zinc-300">{t('common.no')}</button>
                    </span>
                  ) : (
                    <button onClick={() => setConfirmDeleteIndexer(idx.id)} className={`text-xs ${dangerLink}`}>
                      {t('common.delete')}
                    </button>
                  )}
                </div>
              </div>
              {indexerTestResults[idx.id] && !indexerTestResults[idx.id].testing && (() => {
                const r = indexerTestResults[idx.id]
                const warn = r.ok && r.searchResults === 0
                const colorCls = !r.ok
                  ? 'bg-red-100 text-red-800 dark:bg-red-900/30 dark:text-red-300'
                  : warn
                    ? 'bg-amber-100 text-amber-800 dark:bg-amber-900/30 dark:text-amber-300'
                    : 'bg-emerald-100 text-emerald-800 dark:bg-emerald-900/30 dark:text-emerald-300'
                const dotCls = !r.ok ? 'bg-red-500' : warn ? 'bg-amber-500' : 'bg-emerald-500'
                return (
                  <div role="status" className={`mt-2 px-3 py-2 rounded text-xs flex items-center gap-2 ${colorCls}`}>
                    <span className={`inline-block w-2 h-2 rounded-full flex-shrink-0 ${dotCls}`} />
                    {!r.ok ? (
                      <span>{t('settings.indexers.testFail', { error: r.error ?? 'Unknown error' })}</span>
                    ) : warn ? (
                      <span>{t('settings.indexers.testWarn', { status: r.status, categories: r.categories, latency: r.latencyMs })}{r.searchError ? ` — ${r.searchError}` : ''}</span>
                    ) : (
                      <span>{t('settings.indexers.testOk', { status: r.status, categories: r.categories, latency: r.latencyMs, results: r.searchResults })}</span>
                    )}
                  </div>
                )
              })()}
              {editingIndexer === idx.id && (
                <EditIndexerForm
                  indexer={idx}
                  onClose={() => setEditingIndexer(null)}
                  onSaved={(updated) => { setIndexers(indexers.map(i => i.id === updated.id ? updated : i)); setEditingIndexer(null) }}
                />
              )}
            </div>
          ))}
        </div>
      )}
      {showAddIndexer && (
        <AddIndexerForm
          onClose={() => setShowAddIndexer(false)}
          onAdded={(idx) => { setIndexers([...indexers, idx]); setShowAddIndexer(false) }}
        />
      )}

      {/* Prowlarr sync */}
      <div className="mt-8 border-t border-slate-200 dark:border-zinc-800 pt-6">
        <div className="flex justify-between items-center mb-4">
          <div>
            <h4 className="text-base font-semibold">{t('settings.prowlarr.heading')}</h4>
            <p className="text-xs text-slate-500 dark:text-zinc-500 mt-0.5">{t('settings.prowlarr.description')}</p>
          </div>
          {prowlarrInstances.length === 0 && (
            <button onClick={() => setShowAddProwlarr(true)} className="px-3 py-1.5 bg-emerald-600 hover:bg-emerald-500 rounded text-xs font-medium">
              {t('settings.prowlarr.addButton')}
            </button>
          )}
        </div>
        <div className="space-y-2">
          {prowlarrInstances.map(p => (
            <div key={p.id} className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900">
              <div className="flex items-center justify-between">
                <div>
                  <h5 className="font-medium text-sm">{p.name}</h5>
                  <p className="text-xs text-slate-500 dark:text-zinc-500">{p.url}</p>
                  {p.lastSyncAt && (
                    <p className="text-xs text-slate-400 dark:text-zinc-600 mt-0.5">
                      {t('settings.prowlarr.lastSynced', {
                        at: new Date(p.lastSyncAt).toLocaleString(),
                        count: indexers.filter(i => i.prowlarrInstanceId === p.id).length,
                      })}
                    </p>
                  )}
                  {prowlarrSyncResult[p.id] && (
                    <p className="text-xs text-emerald-600 dark:text-emerald-400 mt-0.5">{prowlarrSyncResult[p.id]}</p>
                  )}
                  {prowlarrTestResult[p.id] && (
                    <p className={`text-xs mt-0.5 ${prowlarrTestResult[p.id].ok ? 'text-emerald-600 dark:text-emerald-400' : 'text-red-600 dark:text-red-400'}`}>{prowlarrTestResult[p.id].msg}</p>
                  )}
                </div>
                <div className="flex items-center gap-3 flex-shrink-0">
                  <button onClick={() => setEditingProwlarr(editingProwlarr === p.id ? null : p.id)} className="text-xs text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white">
                    {t('settings.prowlarr.edit')}
                  </button>
                  <button
                    onClick={async () => {
                      try {
                        const r = await api.testProwlarr(p.id)
                        setProwlarrTestResult(prev => ({ ...prev, [p.id]: { ok: r.ok === 'true', msg: r.ok === 'true' ? t('settings.prowlarr.connectedVersion', { version: r.version }) : t('settings.prowlarr.connFailed', { error: r.error }) } }))
                      } catch (err: unknown) {
                        setProwlarrTestResult(prev => ({ ...prev, [p.id]: { ok: false, msg: err instanceof Error ? err.message : t('settings.prowlarr.connFailedGeneric') } }))
                      }
                    }}
                    className="text-xs text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white"
                  >
                    {t('settings.prowlarr.test')}
                  </button>
                  <button
                    onClick={async () => {
                      try {
                        const r = await api.syncProwlarr(p.id)
                        setProwlarrSyncResult(prev => ({ ...prev, [p.id]: t('settings.prowlarr.synced', { added: r.added, updated: r.updated, removed: r.removed }) }))
                        api.listIndexers().then(setIndexers).catch(console.error)
                        api.listProwlarr().then(r => setProwlarrInstances(r ?? [])).catch(console.error)
                      } catch (err: unknown) {
                        setProwlarrSyncResult(prev => ({ ...prev, [p.id]: t('settings.prowlarr.syncFailed', { error: err instanceof Error ? err.message : t('settings.prowlarr.unknownError') }) }))
                      }
                    }}
                    className="text-xs text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white"
                  >
                    {t('settings.prowlarr.syncNow')}
                  </button>
                  <button
                    onClick={async () => {
                      if (!confirm(t('settings.prowlarr.confirmDelete', { name: p.name }))) return
                      await api.deleteProwlarr(p.id)
                      setProwlarrInstances(prev => prev.filter(i => i.id !== p.id))
                      api.listIndexers().then(setIndexers).catch(console.error)
                    }}
                    className={`text-xs ${dangerLink}`}
                  >
                    {t('settings.prowlarr.delete')}
                  </button>
                </div>
              </div>
              {editingProwlarr === p.id && (
                <EditProwlarrForm
                  instance={p}
                  onClose={() => setEditingProwlarr(null)}
                  onSaved={(updated) => {
                    setProwlarrInstances(prev => prev.map(i => i.id === updated.id ? updated : i))
                    setEditingProwlarr(null)
                    api.listIndexers().then(setIndexers).catch(console.error)
                  }}
                />
              )}
            </div>
          ))}
        </div>
        {showAddProwlarr && (
          <AddProwlarrForm
            onClose={() => setShowAddProwlarr(false)}
            onAdded={(p) => {
              setProwlarrInstances(prev => [...prev, p])
              setShowAddProwlarr(false)
              api.listIndexers().then(setIndexers).catch(console.error)
            }}
          />
        )}
      </div>
    </div>
  )
}

function EditIndexerForm({ indexer, onClose, onSaved }: { indexer: Indexer; onClose: () => void; onSaved: (idx: Indexer) => void }) {
  const { t } = useTranslation()
  const [name, setName] = useState(indexer.name)
  const [type, setType] = useState(indexer.type || 'newznab')
  const [url, setUrl] = useState(indexer.url)
  const [apiKey, setApiKey] = useState(indexer.apiKey)
  const [categories, setCategories] = useState((indexer.categories ?? [7020]).join(', '))
  const [priority, setPriority] = useState(String(indexer.priority ?? 0))
  const [seedRatio, setSeedRatio] = useState<number | null>(indexer.seedRatio ?? null)
  const [testing, setTesting] = useState(false)
  const [testResult, setTestResult] = useState<IndexerTestResult | null>(null)
  const labelCls = 'block text-xs text-slate-600 dark:text-zinc-400 mb-1'

  const submit = async () => {
    const updated = await api.updateIndexer(indexer.id, { ...indexer, name, type, url, apiKey, categories: parseCats(categories), priority: parsePriority(priority), seedRatio })
    onSaved(updated)
  }

  const handleTest = async () => {
    setTesting(true)
    setTestResult(null)
    try {
      const r = await api.testIndexerConfig({ name, type, url, apiKey, categories: parseCats(categories) })
      setTestResult(r)
    } catch (err: unknown) {
      setTestResult({ ok: false, status: 0, categories: 0, bookSearch: false, latencyMs: 0, searchResults: 0, error: err instanceof Error ? err.message : 'Request failed' })
    } finally {
      setTesting(false)
    }
  }

  return (
    <div className="mt-1 p-4 border border-slate-300 dark:border-zinc-700 rounded-lg bg-slate-200/50 dark:bg-zinc-800/50 space-y-3">
      <div className="flex gap-2">
        <div className="flex-1">
          <label className={labelCls}>{t('settings.indexers.form.name')}</label>
          <input value={name} onChange={e => setName(e.target.value)} placeholder={t('settings.indexers.form.namePlaceholder')} className="w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600" />
        </div>
        <div className="w-48">
          <label className={labelCls}>{t('settings.indexers.form.type')}</label>
          <select value={type} onChange={e => setType(e.target.value)} className="w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600">
            <option value="newznab">{t('settings.indexers.form.typeNewznabUsenet')}</option>
            <option value="torznab">{t('settings.indexers.form.typeTorznabTorrent')}</option>
          </select>
        </div>
      </div>
      <div>
        <label className={labelCls}>{t('settings.indexers.form.url')}</label>
        <input value={url} onChange={e => setUrl(e.target.value)} placeholder={t('settings.indexers.form.urlPlaceholder')} className={inputCls} />
        <p className="text-xs text-slate-500 dark:text-zinc-500 mt-1">{t('settings.indexers.form.urlHintEdit')}</p>
      </div>
      <div>
        <label className={labelCls}>{t('settings.indexers.form.apiKey')}</label>
        <input value={apiKey} onChange={e => setApiKey(e.target.value)} placeholder={t('settings.indexers.form.apiKey')} type="password" className={inputCls} />
      </div>
      <div>
        <label className={labelCls}>{t('settings.indexers.form.categories')}</label>
        <input value={categories} onChange={e => setCategories(e.target.value)} placeholder={t('settings.indexers.form.categoriesPlaceholder')} className={inputCls} />
        <p className="text-xs text-slate-500 dark:text-zinc-500 mt-1">{t('settings.indexers.form.categoriesHint')}</p>
      </div>
      <div>
        <label className={labelCls}>{t('settings.indexers.form.priority')}</label>
        <input type="number" value={priority} onChange={e => setPriority(e.target.value)} placeholder="0" className={inputCls} />
        <p className="text-xs text-slate-500 dark:text-zinc-500 mt-1">{t('settings.indexers.form.priorityHint')}</p>
      </div>
      <SeedRatioField value={seedRatio} onChange={setSeedRatio} source={indexer.seedRatioSource} />
      {testResult && <IndexerTestResultBanner r={testResult} />}
      <div className="flex gap-2 justify-end">
        <button onClick={onClose} className="px-3 py-1.5 text-sm text-slate-600 dark:text-zinc-400">{t('common.cancel')}</button>
        <button onClick={handleTest} disabled={testing || !url} className="px-3 py-1.5 text-sm text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white disabled:opacity-50">{testing ? t('common.testing') : t('common.test')}</button>
        <button onClick={submit} className="px-3 py-1.5 bg-emerald-600 hover:bg-emerald-500 rounded text-sm font-medium">{t('common.save')}</button>
      </div>
    </div>
  )
}

function AddIndexerForm({ onClose, onAdded }: { onClose: () => void; onAdded: (idx: Indexer) => void }) {
  const { t } = useTranslation()
  const [name, setName] = useState('')
  const [type, setType] = useState<'newznab' | 'torznab'>('newznab')
  const [url, setUrl] = useState('')
  const [apiKey, setApiKey] = useState('')
  const [categories, setCategories] = useState('7020')
  const [priority, setPriority] = useState('0')
  const [seedRatio, setSeedRatio] = useState<number | null>(null)
  const [testing, setTesting] = useState(false)
  const [testResult, setTestResult] = useState<IndexerTestResult | null>(null)
  const labelCls = 'block text-xs text-slate-600 dark:text-zinc-400 mb-1'

  const submit = async () => {
    const idx = await api.addIndexer({ name, url, apiKey, type, categories: parseCats(categories), priority: parsePriority(priority), enabled: true, seedRatio })
    onAdded(idx)
  }

  const handleTest = async () => {
    setTesting(true)
    setTestResult(null)
    try {
      const r = await api.testIndexerConfig({ name, type, url, apiKey, categories: parseCats(categories) })
      setTestResult(r)
    } catch (err: unknown) {
      setTestResult({ ok: false, status: 0, categories: 0, bookSearch: false, latencyMs: 0, searchResults: 0, error: err instanceof Error ? err.message : 'Request failed' })
    } finally {
      setTesting(false)
    }
  }

  return (
    <div className="mt-4 p-4 border border-slate-300 dark:border-zinc-700 rounded-lg bg-slate-200/50 dark:bg-zinc-800/50 space-y-3">
      <div className="flex gap-2">
        <div className="flex-1">
          <label className={labelCls}>{t('settings.indexers.form.name')}</label>
          <input value={name} onChange={e => setName(e.target.value)} placeholder={t('settings.indexers.form.namePlaceholderExample')} className="w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600" />
        </div>
        <div className="w-40">
          <label className={labelCls}>{t('settings.indexers.form.type')}</label>
          <select value={type} onChange={e => setType(e.target.value as 'newznab' | 'torznab')} className="w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600">
            <option value="newznab">{t('settings.indexers.form.typeNewznab')}</option>
            <option value="torznab">{t('settings.indexers.form.typeTorznab')}</option>
          </select>
        </div>
      </div>
      <div>
        <label className={labelCls}>{t('settings.indexers.form.url')}</label>
        <input value={url} onChange={e => setUrl(e.target.value)} placeholder={t('settings.indexers.form.urlPlaceholderExample')} className={inputCls} />
        <p className="text-xs text-slate-500 dark:text-zinc-500 mt-1">{t('settings.indexers.form.urlHintAdd')}</p>
      </div>
      <div>
        <label className={labelCls}>{t('settings.indexers.form.apiKey')}</label>
        <input value={apiKey} onChange={e => setApiKey(e.target.value)} placeholder={t('settings.indexers.form.apiKey')} type="password" className={inputCls} />
      </div>
      <div>
        <label className={labelCls}>{t('settings.indexers.form.categories')}</label>
        <input value={categories} onChange={e => setCategories(e.target.value)} placeholder={t('settings.indexers.form.categoriesPlaceholder')} className={inputCls} />
        <p className="text-xs text-slate-500 dark:text-zinc-500 mt-1">{t('settings.indexers.form.categoriesHint')}</p>
      </div>
      <div>
        <label className={labelCls}>{t('settings.indexers.form.priority')}</label>
        <input type="number" value={priority} onChange={e => setPriority(e.target.value)} placeholder="0" className={inputCls} />
        <p className="text-xs text-slate-500 dark:text-zinc-500 mt-1">{t('settings.indexers.form.priorityHint')}</p>
      </div>
      <SeedRatioField value={seedRatio} onChange={setSeedRatio} />
      {testResult && <IndexerTestResultBanner r={testResult} />}
      <div className="flex gap-2 justify-end">
        <button onClick={onClose} className="px-3 py-1.5 text-sm text-slate-600 dark:text-zinc-400">{t('common.cancel')}</button>
        <button onClick={handleTest} disabled={testing || !url} className="px-3 py-1.5 text-sm text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white disabled:opacity-50">{testing ? t('common.testing') : t('common.test')}</button>
        <button onClick={submit} className="px-3 py-1.5 bg-emerald-600 hover:bg-emerald-500 rounded text-sm font-medium">{t('common.save')}</button>
      </div>
    </div>
  )
}

function EditProwlarrForm({ instance, onClose, onSaved }: { instance: ProwlarrInstance; onClose: () => void; onSaved: (p: ProwlarrInstance) => void }) {
  const { t } = useTranslation()
  const [name, setName] = useState(instance.name)
  const [url, setUrl] = useState(instance.url)
  // Empty means "keep existing key" — payload omits apiKey when blank so the
  // backend's struct-decode leaves the column alone (#820).
  const [apiKey, setApiKey] = useState('')
  const [syncOnStartup, setSyncOnStartup] = useState(instance.syncOnStartup)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const urlChanged = url.trim() !== instance.url.trim()
  const labelCls = 'block text-xs text-slate-600 dark:text-zinc-400 mb-1'

  const submit = async () => {
    setSaving(true)
    setError(null)
    try {
      const payload: Partial<ProwlarrInstance> = { name, url, syncOnStartup, enabled: instance.enabled }
      if (apiKey) payload.apiKey = apiKey
      const updated = await api.updateProwlarr(instance.id, payload)
      onSaved(updated)
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : t('settings.prowlarr.saveFailed'))
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="mt-3 p-4 border border-slate-300 dark:border-zinc-700 rounded-lg bg-slate-200/50 dark:bg-zinc-800/50 space-y-3">
      <div>
        <label className={labelCls}>{t('settings.indexers.form.name')}</label>
        <input value={name} onChange={e => setName(e.target.value)} placeholder={t('settings.prowlarr.namePlaceholder')} className={inputCls} />
      </div>
      <div>
        <label className={labelCls}>{t('settings.indexers.form.url')}</label>
        <input value={url} onChange={e => setUrl(e.target.value)} placeholder={t('settings.prowlarr.urlPlaceholder')} className={inputCls} />
        {urlChanged && (
          <p className="text-xs text-amber-600 dark:text-amber-400 mt-1">{t('settings.prowlarr.urlChangeWarning')}</p>
        )}
      </div>
      <div>
        <label className={labelCls}>{t('settings.prowlarr.apiKeyEditLabel')}</label>
        <input value={apiKey} onChange={e => setApiKey(e.target.value)} placeholder="••••••••" type="password" className={inputCls} />
        <p className="text-xs text-slate-500 dark:text-zinc-500 mt-1">{t('settings.prowlarr.apiKeyEditHint')}</p>
      </div>
      <div className="flex items-center gap-2">
        <Toggle checked={syncOnStartup} onChange={() => setSyncOnStartup(!syncOnStartup)} />
        <span className="text-xs text-slate-600 dark:text-zinc-400">{t('settings.prowlarr.syncOnStartup')}</span>
      </div>
      {error && (
        <div className="text-xs text-red-600 dark:text-red-400 break-words">{error}</div>
      )}
      <div className="flex gap-2 justify-end">
        <button onClick={onClose} className="px-3 py-1.5 text-sm text-slate-600 dark:text-zinc-400">{t('common.cancel')}</button>
        <button onClick={submit} disabled={!url || saving} className="px-3 py-1.5 bg-emerald-600 hover:bg-emerald-500 rounded text-sm font-medium disabled:opacity-50">
          {saving ? t('common.saving') : t('common.save')}
        </button>
      </div>
    </div>
  )
}

function AddProwlarrForm({ onClose, onAdded }: { onClose: () => void; onAdded: (p: ProwlarrInstance) => void }) {
  const { t } = useTranslation()
  const [name, setName] = useState('Prowlarr')
  const [url, setUrl] = useState('')
  const [apiKey, setApiKey] = useState('')
  const [syncOnStartup, setSyncOnStartup] = useState(true)
  const [syncing, setSyncing] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const labelCls = 'block text-xs text-slate-600 dark:text-zinc-400 mb-1'

  const submit = async () => {
    setSyncing(true)
    setError(null)
    let p: ProwlarrInstance
    try {
      p = await api.addProwlarr({ name, url, apiKey, syncOnStartup, enabled: true })
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : t('settings.prowlarr.saveFailed'))
      setSyncing(false)
      return
    }
    // Save succeeded — auto-sync so indexers appear right away. Sync failures
    // are non-fatal: the instance is already persisted, the user can retry sync
    // from the row's button.
    try {
      await api.syncProwlarr(p.id)
      const updated = await api.listProwlarr()
      onAdded(updated.find(i => i.id === p.id) ?? p)
    } catch {
      onAdded(p)
    } finally {
      setSyncing(false)
    }
  }

  return (
    <div className="mt-4 p-4 border border-slate-300 dark:border-zinc-700 rounded-lg bg-slate-200/50 dark:bg-zinc-800/50 space-y-3">
      <div className="flex gap-2">
        <div className="flex-1">
          <label className={labelCls}>{t('settings.indexers.form.name')}</label>
          <input value={name} onChange={e => setName(e.target.value)} placeholder={t('settings.prowlarr.namePlaceholder')} className={inputCls} />
        </div>
      </div>
      <div>
        <label className={labelCls}>{t('settings.indexers.form.url')}</label>
        <input value={url} onChange={e => setUrl(e.target.value)} placeholder={t('settings.prowlarr.urlPlaceholder')} className={inputCls} />
      </div>
      <div>
        <label className={labelCls}>{t('settings.indexers.form.apiKey')}</label>
        <input value={apiKey} onChange={e => setApiKey(e.target.value)} placeholder={t('settings.prowlarr.apiKeyPlaceholder')} type="password" className={inputCls} />
      </div>
      <div className="flex items-center gap-2">
        <Toggle checked={syncOnStartup} onChange={() => setSyncOnStartup(!syncOnStartup)} />
        <span className="text-xs text-slate-600 dark:text-zinc-400">{t('settings.prowlarr.syncOnStartup')}</span>
      </div>
      {error && (
        <div className="text-xs text-red-600 dark:text-red-400 break-words">{error}</div>
      )}
      <div className="flex gap-2 justify-end">
        <button onClick={onClose} className="px-3 py-1.5 text-sm text-slate-600 dark:text-zinc-400">{t('common.cancel')}</button>
        <button onClick={submit} disabled={!url || !apiKey || syncing} className="px-3 py-1.5 bg-emerald-600 hover:bg-emerald-500 rounded text-sm font-medium disabled:opacity-50">
          {syncing ? t('settings.prowlarr.savingAndSyncing') : t('settings.prowlarr.saveAndSync')}
        </button>
      </div>
    </div>
  )
}
