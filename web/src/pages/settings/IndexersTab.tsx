import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, Indexer, IndexerTestResult, ProwlarrInstance } from '../../api/client'
import { inputCls } from './formStyles'
import { parseCats } from './helpers'
import Toggle from './Toggle'

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
                        className="text-xs text-red-500 font-medium hover:text-red-400"
                      >{t('common.yes')}</button>
                      <button onClick={() => setConfirmDeleteIndexer(null)} className="text-xs text-slate-500 dark:text-zinc-500 hover:text-slate-700 dark:hover:text-zinc-300">{t('common.no')}</button>
                    </span>
                  ) : (
                    <button onClick={() => setConfirmDeleteIndexer(idx.id)} className="text-xs text-red-400 hover:text-red-300">
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
            <h4 className="text-base font-semibold">Prowlarr</h4>
            <p className="text-xs text-slate-500 dark:text-zinc-500 mt-0.5">Add Prowlarr once — all configured indexers sync automatically.</p>
          </div>
          {prowlarrInstances.length === 0 && (
            <button onClick={() => setShowAddProwlarr(true)} className="px-3 py-1.5 bg-emerald-600 hover:bg-emerald-500 rounded text-xs font-medium">
              Add Prowlarr
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
                      Last synced: {new Date(p.lastSyncAt).toLocaleString()}
                      {' · '}{indexers.filter(i => i.prowlarrInstanceId === p.id).length} indexers
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
                    {t('common.edit')}
                  </button>
                  <button
                    onClick={async () => {
                      try {
                        const r = await api.testProwlarr(p.id)
                        setProwlarrTestResult(prev => ({ ...prev, [p.id]: { ok: r.ok === 'true', msg: r.ok === 'true' ? `Connected — Prowlarr ${r.version}` : `Connection failed: ${r.error}` } }))
                      } catch (err: unknown) {
                        setProwlarrTestResult(prev => ({ ...prev, [p.id]: { ok: false, msg: err instanceof Error ? err.message : 'Connection failed' } }))
                      }
                    }}
                    className="text-xs text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white"
                  >
                    Test
                  </button>
                  <button
                    onClick={async () => {
                      try {
                        const r = await api.syncProwlarr(p.id)
                        setProwlarrSyncResult(prev => ({ ...prev, [p.id]: `Synced — added ${r.added}, updated ${r.updated}, removed ${r.removed}` }))
                        api.listIndexers().then(setIndexers).catch(console.error)
                        api.listProwlarr().then(r => setProwlarrInstances(r ?? [])).catch(console.error)
                      } catch (err: unknown) {
                        setProwlarrSyncResult(prev => ({ ...prev, [p.id]: `Sync failed: ${err instanceof Error ? err.message : 'Unknown error'}` }))
                      }
                    }}
                    className="text-xs text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white"
                  >
                    Sync now
                  </button>
                  <button
                    onClick={async () => {
                      if (!confirm(`Delete Prowlarr instance "${p.name}" and all its synced indexers?`)) return
                      await api.deleteProwlarr(p.id)
                      setProwlarrInstances(prev => prev.filter(i => i.id !== p.id))
                      api.listIndexers().then(setIndexers).catch(console.error)
                    }}
                    className="text-xs text-red-400 hover:text-red-300"
                  >
                    Delete
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
  const [name, setName] = useState(indexer.name)
  const [type, setType] = useState(indexer.type || 'newznab')
  const [url, setUrl] = useState(indexer.url)
  const [apiKey, setApiKey] = useState(indexer.apiKey)
  const [categories, setCategories] = useState((indexer.categories ?? [7020]).join(', '))
  const labelCls = 'block text-xs text-slate-600 dark:text-zinc-400 mb-1'

  const submit = async () => {
    const updated = await api.updateIndexer(indexer.id, { ...indexer, name, type, url, apiKey, categories: parseCats(categories) })
    onSaved(updated)
  }

  return (
    <div className="mt-1 p-4 border border-slate-300 dark:border-zinc-700 rounded-lg bg-slate-200/50 dark:bg-zinc-800/50 space-y-3">
      <div className="flex gap-2">
        <div className="flex-1">
          <label className={labelCls}>Name</label>
          <input value={name} onChange={e => setName(e.target.value)} placeholder="Name" className="w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600" />
        </div>
        <div className="w-48">
          <label className={labelCls}>Indexer Type</label>
          <select value={type} onChange={e => setType(e.target.value)} className="w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600">
            <option value="newznab">Newznab (Usenet)</option>
            <option value="torznab">Torznab (Torrent)</option>
          </select>
        </div>
      </div>
      <div>
        <label className={labelCls}>URL</label>
        <input value={url} onChange={e => setUrl(e.target.value)} placeholder="URL" className={inputCls} />
        <p className="text-xs text-slate-500 dark:text-zinc-500 mt-1">Use a base Newznab URL (for example: https://api.nzbgeek.info) or a full Torznab endpoint (for example: http://prowlarr:9696/1/api).</p>
      </div>
      <div>
        <label className={labelCls}>API Key</label>
        <input value={apiKey} onChange={e => setApiKey(e.target.value)} placeholder="API Key" type="password" className={inputCls} />
      </div>
      <div>
        <label className={labelCls}>Categories</label>
        <input value={categories} onChange={e => setCategories(e.target.value)} placeholder="Categories (e.g. 7020, 7120, 3030)" className={inputCls} />
        <p className="text-xs text-slate-500 dark:text-zinc-500 mt-1">Comma-separated Newznab category IDs. 7020 = eBooks, 3030 = Audiobooks. Add custom IDs for indexers with non-standard categories (e.g. 7120 for German books).</p>
      </div>
      <div className="flex gap-2 justify-end">
        <button onClick={onClose} className="px-3 py-1.5 text-sm text-slate-600 dark:text-zinc-400">Cancel</button>
        <button onClick={submit} className="px-3 py-1.5 bg-emerald-600 hover:bg-emerald-500 rounded text-sm font-medium">Save</button>
      </div>
    </div>
  )
}

function AddIndexerForm({ onClose, onAdded }: { onClose: () => void; onAdded: (idx: Indexer) => void }) {
  const [name, setName] = useState('')
  const [type, setType] = useState<'newznab' | 'torznab'>('newznab')
  const [url, setUrl] = useState('')
  const [apiKey, setApiKey] = useState('')
  const [categories, setCategories] = useState('7020')
  const labelCls = 'block text-xs text-slate-600 dark:text-zinc-400 mb-1'

  const submit = async () => {
    const idx = await api.addIndexer({ name, url, apiKey, type, categories: parseCats(categories), enabled: true })
    onAdded(idx)
  }

  return (
    <div className="mt-4 p-4 border border-slate-300 dark:border-zinc-700 rounded-lg bg-slate-200/50 dark:bg-zinc-800/50 space-y-3">
      <div className="flex gap-2">
        <div className="flex-1">
          <label className={labelCls}>Name</label>
          <input value={name} onChange={e => setName(e.target.value)} placeholder="Name (e.g. NZBGeek)" className="w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600" />
        </div>
        <div className="w-40">
          <label className={labelCls}>Indexer Type</label>
          <select value={type} onChange={e => setType(e.target.value as 'newznab' | 'torznab')} className="w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600">
            <option value="newznab">Newznab</option>
            <option value="torznab">Torznab</option>
          </select>
        </div>
      </div>
      <div>
        <label className={labelCls}>URL</label>
        <input value={url} onChange={e => setUrl(e.target.value)} placeholder="URL (e.g. https://api.nzbgeek.info or http://prowlarr:9696/1/api)" className={inputCls} />
        <p className="text-xs text-slate-500 dark:text-zinc-500 mt-1">For Prowlarr, paste the Torznab endpoint URL (usually ending in /api) and API key.</p>
      </div>
      <div>
        <label className={labelCls}>API Key</label>
        <input value={apiKey} onChange={e => setApiKey(e.target.value)} placeholder="API Key" type="password" className={inputCls} />
      </div>
      <div>
        <label className={labelCls}>Categories</label>
        <input value={categories} onChange={e => setCategories(e.target.value)} placeholder="Categories (e.g. 7020, 7120, 3030)" className={inputCls} />
        <p className="text-xs text-slate-500 dark:text-zinc-500 mt-1">Comma-separated Newznab category IDs. 7020 = eBooks, 3030 = Audiobooks. Add custom IDs for indexers with non-standard categories (e.g. 7120 for German books).</p>
      </div>
      <div className="flex gap-2 justify-end">
        <button onClick={onClose} className="px-3 py-1.5 text-sm text-slate-600 dark:text-zinc-400">Cancel</button>
        <button onClick={submit} className="px-3 py-1.5 bg-emerald-600 hover:bg-emerald-500 rounded text-sm font-medium">Save</button>
      </div>
    </div>
  )
}

function EditProwlarrForm({ instance, onClose, onSaved }: { instance: ProwlarrInstance; onClose: () => void; onSaved: (p: ProwlarrInstance) => void }) {
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
      setError(e instanceof Error ? e.message : 'Save failed')
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="mt-3 p-4 border border-slate-300 dark:border-zinc-700 rounded-lg bg-slate-200/50 dark:bg-zinc-800/50 space-y-3">
      <div>
        <label className={labelCls}>Name</label>
        <input value={name} onChange={e => setName(e.target.value)} placeholder="Prowlarr" className={inputCls} />
      </div>
      <div>
        <label className={labelCls}>URL</label>
        <input value={url} onChange={e => setUrl(e.target.value)} placeholder="http://prowlarr:9696" className={inputCls} />
        {urlChanged && (
          <p className="text-xs text-amber-600 dark:text-amber-400 mt-1">
            Click <strong>Sync now</strong> after saving to rebuild the per-indexer URLs against the new base.
          </p>
        )}
      </div>
      <div>
        <label className={labelCls}>API Key (leave blank to keep current)</label>
        <input value={apiKey} onChange={e => setApiKey(e.target.value)} placeholder="••••••••" type="password" className={inputCls} />
        <p className="text-xs text-slate-500 dark:text-zinc-500 mt-1">A new key is propagated to every indexer synced from this Prowlarr instance immediately.</p>
      </div>
      <div className="flex items-center gap-2">
        <Toggle checked={syncOnStartup} onChange={() => setSyncOnStartup(!syncOnStartup)} />
        <span className="text-xs text-slate-600 dark:text-zinc-400">Sync on startup</span>
      </div>
      {error && (
        <div className="text-xs text-red-600 dark:text-red-400 break-words">{error}</div>
      )}
      <div className="flex gap-2 justify-end">
        <button onClick={onClose} className="px-3 py-1.5 text-sm text-slate-600 dark:text-zinc-400">Cancel</button>
        <button onClick={submit} disabled={!url || saving} className="px-3 py-1.5 bg-emerald-600 hover:bg-emerald-500 rounded text-sm font-medium disabled:opacity-50">
          {saving ? 'Saving…' : 'Save'}
        </button>
      </div>
    </div>
  )
}

function AddProwlarrForm({ onClose, onAdded }: { onClose: () => void; onAdded: (p: ProwlarrInstance) => void }) {
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
      setError(e instanceof Error ? e.message : 'Save failed')
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
          <label className={labelCls}>Name</label>
          <input value={name} onChange={e => setName(e.target.value)} placeholder="Prowlarr" className={inputCls} />
        </div>
      </div>
      <div>
        <label className={labelCls}>URL</label>
        <input value={url} onChange={e => setUrl(e.target.value)} placeholder="http://prowlarr:9696" className={inputCls} />
      </div>
      <div>
        <label className={labelCls}>API Key</label>
        <input value={apiKey} onChange={e => setApiKey(e.target.value)} placeholder="API Key" type="password" className={inputCls} />
      </div>
      <div className="flex items-center gap-2">
        <Toggle checked={syncOnStartup} onChange={() => setSyncOnStartup(!syncOnStartup)} />
        <span className="text-xs text-slate-600 dark:text-zinc-400">Sync on startup</span>
      </div>
      {error && (
        <div className="text-xs text-red-600 dark:text-red-400 break-words">{error}</div>
      )}
      <div className="flex gap-2 justify-end">
        <button onClick={onClose} className="px-3 py-1.5 text-sm text-slate-600 dark:text-zinc-400">Cancel</button>
        <button onClick={submit} disabled={!url || !apiKey || syncing} className="px-3 py-1.5 bg-emerald-600 hover:bg-emerald-500 rounded text-sm font-medium disabled:opacity-50">
          {syncing ? 'Saving & syncing…' : 'Save & sync'}
        </button>
      </div>
    </div>
  )
}
