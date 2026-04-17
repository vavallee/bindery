import { FormEvent, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, AuthConfig, AuthStatus, BlocklistEntry, Indexer, ProwlarrInstance, DownloadClient, NotificationConfig, QualityProfile, MetadataProfile, CalibreImportProgress, RootFolder, LogEntry, ImportList, HardcoverList } from '../api/client'
import Pagination from '../components/Pagination'
import { usePagination } from '../components/usePagination'
import ThemeToggle from '../components/ThemeToggle'
import LanguageSwitcher from '../components/LanguageSwitcher'
import { useAuth } from '../auth/AuthContext'

type Tab = 'indexers' | 'clients' | 'notifications' | 'quality' | 'metadata' | 'general' | 'import' | 'rootfolders' | 'logs' | 'blocklist' | 'calibre'

const inputCls = 'w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600'
const tabCls = (active: boolean) =>
  `px-4 py-2 rounded-md text-sm font-medium transition-colors ${active ? 'bg-slate-200 dark:bg-zinc-800 text-slate-900 dark:text-white' : 'text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white hover:bg-slate-200/50 dark:hover:bg-zinc-800/50'}`

export default function SettingsPage() {
  const { t, i18n } = useTranslation()
  const [tab, setTab] = useState<Tab>('general')
  const [indexers, setIndexers] = useState<Indexer[]>([])
  const [clients, setClients] = useState<DownloadClient[]>([])
  const [notifications, setNotifications] = useState<NotificationConfig[]>([])
  const [qualityProfiles, setQualityProfiles] = useState<QualityProfile[]>([])
  const [metadataProfiles, setMetadataProfiles] = useState<MetadataProfile[]>([])
  const [rootFolders, setRootFolders] = useState<RootFolder[]>([])
  const [newFolderPath, setNewFolderPath] = useState('')
  const [folderError, setFolderError] = useState('')
  const [prowlarrInstances, setProwlarrInstances] = useState<ProwlarrInstance[]>([])
  const [showAddProwlarr, setShowAddProwlarr] = useState(false)
  const [prowlarrSyncResult, setProwlarrSyncResult] = useState<Record<number, string>>({})
  const [showAddIndexer, setShowAddIndexer] = useState(false)
  const [showAddClient, setShowAddClient] = useState(false)
  const [showAddNotification, setShowAddNotification] = useState(false)
  const [editingIndexer, setEditingIndexer] = useState<number | null>(null)
  const [editingClient, setEditingClient] = useState<number | null>(null)
  const [editingNotification, setEditingNotification] = useState<number | null>(null)
  const [logEntries, setLogEntries] = useState<LogEntry[]>([])
  const [logLevel, setLogLevel] = useState<string>('info')
  const [logFilter, setLogFilter] = useState<string>('all')
  const [logAutoRefresh, setLogAutoRefresh] = useState(true)
  const logBottomRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    api.listIndexers().then(setIndexers).catch(console.error)
    api.listDownloadClients().then(setClients).catch(console.error)
    api.listProwlarr().then(setProwlarrInstances).catch(console.error)
  }, [])

  useEffect(() => {
    document.title = 'Settings · Bindery'
    return () => { document.title = 'Bindery' }
  }, [])

  useEffect(() => {
    if (tab === 'notifications') api.listNotifications().then(setNotifications).catch(console.error)
    if (tab === 'quality') api.listQualityProfiles().then(setQualityProfiles).catch(console.error)
    if (tab === 'metadata') api.listMetadataProfiles().then(setMetadataProfiles).catch(console.error)
    if (tab === 'rootfolders') api.listRootFolders().then(setRootFolders).catch(console.error)
    if (tab === 'logs') {
      api.getLogLevel().then(r => setLogLevel(r.level.toLowerCase())).catch(console.error)
      api.getLogs(undefined, 200).then(setLogEntries).catch(console.error)
    }
  }, [tab])

  // Auto-refresh logs every 5 s while the tab is active and toggle is on.
  useEffect(() => {
    if (tab !== 'logs' || !logAutoRefresh) return
    const id = setInterval(() => {
      api.getLogs(undefined, 200).then(setLogEntries).catch(console.error)
    }, 5000)
    return () => clearInterval(id)
  }, [tab, logAutoRefresh])

  function formatBytes(bytes: number): string {
    if (bytes === 0) return '0 B'
    const units = ['B', 'KB', 'MB', 'GB', 'TB']
    const i = Math.floor(Math.log(bytes) / Math.log(1024))
    return `${(bytes / Math.pow(1024, i)).toFixed(1)} ${units[i]}`
  }

  return (
    <div>
      <h2 className="text-2xl font-bold mb-6">{t('settings.title')}</h2>

      <div className="flex flex-wrap gap-2 mb-6">
        {(['general', 'indexers', 'clients', 'rootfolders', 'quality', 'metadata', 'notifications', 'calibre', 'import', 'blocklist', 'logs'] as Tab[]).map(tabKey => (
          <button key={tabKey} onClick={() => setTab(tabKey)} className={tabCls(tab === tabKey)}>
            {t(`settings.tabs.${tabKey}`)}
          </button>
        ))}
      </div>

      {/* Indexers */}
      {tab === 'indexers' && (
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
                      <button
                        onClick={async () => {
                          const updated = await api.updateIndexer(idx.id, { ...idx, enabled: !idx.enabled })
                          setIndexers(indexers.map(i => i.id === idx.id ? updated : i))
                        }}
                        className={`relative w-9 h-5 rounded-full transition-colors flex-shrink-0 ${idx.enabled ? 'bg-emerald-600' : 'bg-slate-300 dark:bg-zinc-700'}`}
                        title={idx.enabled ? t('common.disable') : t('common.enable')}
                      >
                        <span className={`absolute top-0.5 left-0.5 w-4 h-4 bg-white rounded-full transition-transform ${idx.enabled ? 'translate-x-4' : ''}`} />
                      </button>
                      <div className="min-w-0">
                        <h4 className={`font-medium text-sm ${!idx.enabled ? 'text-slate-600 dark:text-zinc-500' : ''}`}>{idx.name}</h4>
                        <p className="text-xs text-slate-600 dark:text-zinc-500 truncate">{idx.url}</p>
                      </div>
                    </div>
                    <div className="flex items-center gap-3 flex-shrink-0">
                      <button onClick={() => setEditingIndexer(editingIndexer === idx.id ? null : idx.id)} className="text-xs text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white">{t('common.edit')}</button>
                      <button
                        onClick={async () => {
                          try {
                            await api.testIndexer(idx.id)
                            alert(t('common.connOk'))
                          } catch (err: unknown) {
                            alert(t('common.connFail', { error: err instanceof Error ? err.message : 'Unknown error' }))
                          }
                        }}
                        className="text-xs text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white"
                      >
                        {t('common.test')}
                      </button>
                      <button
                        onClick={async () => {
                          await api.deleteIndexer(idx.id)
                          setIndexers(indexers.filter(i => i.id !== idx.id))
                        }}
                        className="text-xs text-red-400 hover:text-red-300"
                      >
                        {t('common.delete')}
                      </button>
                    </div>
                  </div>
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
                    </div>
                    <div className="flex items-center gap-3 flex-shrink-0">
                      <button
                        onClick={async () => {
                          try {
                            const r = await api.testProwlarr(p.id)
                            if (r.ok === 'true') alert(`Connected — Prowlarr ${r.version}`)
                            else alert(`Connection failed: ${r.error}`)
                          } catch (err: unknown) {
                            alert(err instanceof Error ? err.message : 'Connection failed')
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
                            api.listProwlarr().then(setProwlarrInstances).catch(console.error)
                          } catch (err: unknown) {
                            alert(err instanceof Error ? err.message : 'Sync failed')
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
      )}

      {/* Download Clients */}
      {tab === 'clients' && (
        <div>
          <div className="flex justify-between items-center mb-4">
            <h3 className="text-lg font-semibold">{t('settings.clients.heading')}</h3>
            <button onClick={() => setShowAddClient(true)} className="px-3 py-1.5 bg-emerald-600 hover:bg-emerald-500 rounded text-xs font-medium">
              {t('settings.clients.addButton')}
            </button>
          </div>
          {clients.length === 0 ? (
            <p className="text-slate-600 dark:text-zinc-500 text-sm">{t('settings.clients.empty')}</p>
          ) : (
            <div className="space-y-2">
              {clients.map(c => (
                <div key={c.id}>
                  <div className="flex items-center justify-between p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900">
                    <div className="flex items-center gap-3 min-w-0">
                      <button
                        onClick={async () => {
                          const updated = await api.updateDownloadClient(c.id, { ...c, enabled: !c.enabled })
                          setClients(clients.map(x => x.id === c.id ? updated : x))
                        }}
                        className={`relative w-9 h-5 rounded-full transition-colors flex-shrink-0 ${c.enabled ? 'bg-emerald-600' : 'bg-slate-300 dark:bg-zinc-700'}`}
                        title={c.enabled ? t('common.disable') : t('common.enable')}
                      >
                        <span className={`absolute top-0.5 left-0.5 w-4 h-4 bg-white rounded-full transition-transform ${c.enabled ? 'translate-x-4' : ''}`} />
                      </button>
                      <div className="min-w-0">
                        <h4 className={`font-medium text-sm ${!c.enabled ? 'text-slate-600 dark:text-zinc-500' : ''}`}>{c.name}</h4>
                        <p className="text-xs text-slate-600 dark:text-zinc-500">{c.host}:{c.port} ({c.category})</p>
                      </div>
                    </div>
                    <div className="flex items-center gap-3 flex-shrink-0">
                      <button onClick={() => setEditingClient(editingClient === c.id ? null : c.id)} className="text-xs text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white">{t('common.edit')}</button>
                      <button
                        onClick={async () => {
                          try {
                            await api.testDownloadClient(c.id)
                            alert(t('common.connOk'))
                          } catch (err: unknown) {
                            alert(t('common.connFail', { error: err instanceof Error ? err.message : 'Unknown error' }))
                          }
                        }}
                        className="text-xs text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white"
                      >
                        {t('common.test')}
                      </button>
                      <button
                        onClick={async () => {
                          await api.deleteDownloadClient(c.id)
                          setClients(clients.filter(x => x.id !== c.id))
                        }}
                        className="text-xs text-red-400 hover:text-red-300"
                      >
                        {t('common.delete')}
                      </button>
                    </div>
                  </div>
                  {editingClient === c.id && (
                    <EditClientForm
                      client={c}
                      onClose={() => setEditingClient(null)}
                      onSaved={(updated) => { setClients(clients.map(x => x.id === updated.id ? updated : x)); setEditingClient(null) }}
                    />
                  )}
                </div>
              ))}
            </div>
          )}
          {showAddClient && (
            <AddClientForm
              onClose={() => setShowAddClient(false)}
              onAdded={(c) => { setClients([...clients, c]); setShowAddClient(false) }}
            />
          )}
        </div>
      )}

      {/* Notifications */}
      {tab === 'notifications' && (
        <div>
          <div className="flex justify-between items-center mb-4">
            <h3 className="text-lg font-semibold">{t('settings.notifications.heading')}</h3>
            <button onClick={() => setShowAddNotification(true)} className="px-3 py-1.5 bg-emerald-600 hover:bg-emerald-500 rounded text-xs font-medium">
              {t('settings.notifications.addButton')}
            </button>
          </div>
          {notifications.length === 0 ? (
            <p className="text-slate-600 dark:text-zinc-500 text-sm">{t('settings.notifications.empty')}</p>
          ) : (
            <div className="space-y-2">
              {notifications.map(n => (
                <div key={n.id}>
                  <div className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900">
                    <div className="flex items-start justify-between gap-3">
                      <div className="flex items-start gap-3 min-w-0">
                        <button
                          onClick={async () => {
                            const updated = await api.updateNotification(n.id, { ...n, enabled: !n.enabled })
                            setNotifications(notifications.map(x => x.id === n.id ? updated : x))
                          }}
                          className={`relative w-9 h-5 rounded-full transition-colors flex-shrink-0 mt-0.5 ${n.enabled ? 'bg-emerald-600' : 'bg-slate-300 dark:bg-zinc-700'}`}
                          title={n.enabled ? t('common.disable') : t('common.enable')}
                        >
                          <span className={`absolute top-0.5 left-0.5 w-4 h-4 bg-white rounded-full transition-transform ${n.enabled ? 'translate-x-4' : ''}`} />
                        </button>
                        <div className="min-w-0">
                          <h4 className={`font-medium text-sm ${!n.enabled ? 'text-slate-600 dark:text-zinc-500' : ''}`}>{n.name}</h4>
                          <p className="text-xs text-slate-600 dark:text-zinc-500 truncate mt-0.5">{n.url}</p>
                          <div className="flex flex-wrap gap-1 mt-2">
                            {n.onGrab && <span className="text-[10px] px-1.5 py-0.5 bg-slate-200 dark:bg-zinc-800 text-slate-600 dark:text-zinc-400 rounded">{t('settings.notifications.onGrab')}</span>}
                            {n.onImport && <span className="text-[10px] px-1.5 py-0.5 bg-slate-200 dark:bg-zinc-800 text-slate-600 dark:text-zinc-400 rounded">{t('settings.notifications.onImport')}</span>}
                            {n.onUpgrade && <span className="text-[10px] px-1.5 py-0.5 bg-slate-200 dark:bg-zinc-800 text-slate-600 dark:text-zinc-400 rounded">{t('settings.notifications.onUpgrade')}</span>}
                            {n.onFailure && <span className="text-[10px] px-1.5 py-0.5 bg-slate-200 dark:bg-zinc-800 text-slate-600 dark:text-zinc-400 rounded">{t('settings.notifications.onFailure')}</span>}
                            {n.onHealth && <span className="text-[10px] px-1.5 py-0.5 bg-slate-200 dark:bg-zinc-800 text-slate-600 dark:text-zinc-400 rounded">{t('settings.notifications.onHealth')}</span>}
                          </div>
                        </div>
                      </div>
                      <div className="flex items-center gap-3 flex-shrink-0">
                        <button onClick={() => setEditingNotification(editingNotification === n.id ? null : n.id)} className="text-xs text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white">{t('common.edit')}</button>
                        <button
                          onClick={async () => {
                            try {
                              await api.testNotification(n.id)
                              alert(t('settings.notifications.testSent'))
                            } catch (err: unknown) {
                              alert(t('common.connFail', { error: err instanceof Error ? err.message : 'Unknown error' }))
                            }
                          }}
                          className="text-xs text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white"
                        >
                          {t('common.test')}
                        </button>
                        <button
                          onClick={async () => {
                            await api.deleteNotification(n.id)
                            setNotifications(notifications.filter(x => x.id !== n.id))
                          }}
                          className="text-xs text-red-400 hover:text-red-300"
                        >
                          {t('common.delete')}
                        </button>
                      </div>
                    </div>
                  </div>
                  {editingNotification === n.id && (
                    <EditNotificationForm
                      notification={n}
                      onClose={() => setEditingNotification(null)}
                      onSaved={(updated) => { setNotifications(notifications.map(x => x.id === updated.id ? updated : x)); setEditingNotification(null) }}
                    />
                  )}
                </div>
              ))}
            </div>
          )}
          {showAddNotification && (
            <AddNotificationForm
              onClose={() => setShowAddNotification(false)}
              onAdded={(n) => { setNotifications([...notifications, n]); setShowAddNotification(false) }}
            />
          )}
        </div>
      )}

      {/* Quality Profiles */}
      {tab === 'quality' && (
        <div>
          <h3 className="text-lg font-semibold mb-4">{t('settings.quality.heading')}</h3>
          {qualityProfiles.length === 0 ? (
            <p className="text-slate-600 dark:text-zinc-500 text-sm">{t('settings.quality.empty')}</p>
          ) : (
            <div className="space-y-3">
              {qualityProfiles.map(p => (
                <div key={p.id} className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900">
                  <div className="flex items-center justify-between mb-2">
                    <h4 className="font-medium text-sm">{p.name}</h4>
                    <div className="flex items-center gap-3 text-xs text-slate-600 dark:text-zinc-500">
                      <span>{t('settings.quality.cutoff')} <span className="text-slate-700 dark:text-zinc-300">{p.cutoff}</span></span>
                      {p.upgradeAllowed && <span className="text-emerald-400">{t('settings.quality.upgradesAllowed')}</span>}
                    </div>
                  </div>
                  {p.items && p.items.length > 0 && (
                    <div className="flex flex-wrap gap-1.5 mt-2">
                      {p.items.map((item, i) => (
                        <span key={i} className={`text-[10px] px-2 py-0.5 rounded ${item.allowed ? 'bg-emerald-500/20 text-emerald-400' : 'bg-slate-200 dark:bg-zinc-800 text-slate-500 dark:text-zinc-600'}`}>
                          {item.quality}
                        </span>
                      ))}
                    </div>
                  )}
                </div>
              ))}
            </div>
          )}
        </div>
      )}

      {/* Metadata Profiles */}
      {tab === 'metadata' && (
        <MetadataProfilesTab
          profiles={metadataProfiles}
          onReload={() => api.listMetadataProfiles().then(setMetadataProfiles).catch(console.error)}
        />
      )}

      {/* General */}
      {tab === 'import' && (
        <ImportTab />
      )}

      {tab === 'rootfolders' && (
        <div>
          <div className="flex justify-between items-center mb-4">
            <h3 className="text-lg font-semibold">{t('settings.rootfolders.heading')}</h3>
          </div>
          <p className="text-sm text-slate-600 dark:text-zinc-400 mb-4">
            {t('settings.rootfolders.description')} (<code className="font-mono bg-slate-200 dark:bg-zinc-800 px-1 rounded text-xs">BINDERY_LIBRARY_DIR</code>).
          </p>

          {rootFolders.length > 0 && (
            <div className="space-y-2 mb-6">
              {rootFolders.map(rf => (
                <div key={rf.id} className="flex items-center justify-between p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900">
                  <div className="min-w-0">
                    <p className="font-mono text-sm truncate">{rf.path}</p>
                    <p className="text-xs text-slate-500 dark:text-zinc-500 mt-0.5">{t('settings.rootfolders.free', { size: formatBytes(rf.freeSpace) })}</p>
                  </div>
                  <button
                    onClick={async () => {
                      await api.deleteRootFolder(rf.id)
                      setRootFolders(rootFolders.filter(f => f.id !== rf.id))
                    }}
                    className="ml-4 px-3 py-1 text-xs text-red-600 dark:text-red-400 hover:bg-red-50 dark:hover:bg-red-900/20 rounded border border-red-200 dark:border-red-800 flex-shrink-0"
                  >
                    {t('common.remove')}
                  </button>
                </div>
              ))}
            </div>
          )}

          <form
            onSubmit={async e => {
              e.preventDefault()
              setFolderError('')
              try {
                const created = await api.addRootFolder(newFolderPath.trim())
                setRootFolders([...rootFolders, created])
                setNewFolderPath('')
              } catch (err: unknown) {
                setFolderError(err instanceof Error ? err.message : 'Failed to add folder')
              }
            }}
            className="flex gap-2 items-start"
          >
            <div className="flex-1">
              <input
                value={newFolderPath}
                onChange={e => { setNewFolderPath(e.target.value); setFolderError('') }}
                placeholder={t('settings.rootfolders.addPlaceholder')}
                className={inputCls}
              />
              {folderError && <p className="text-xs text-red-500 mt-1">{folderError}</p>}
            </div>
            <button
              type="submit"
              disabled={!newFolderPath.trim()}
              className="px-3 py-2 bg-emerald-600 hover:bg-emerald-500 rounded text-sm font-medium disabled:opacity-50 flex-shrink-0"
            >
              {t('settings.rootfolders.addButton')}
            </button>
          </form>
        </div>
      )}

      {tab === 'logs' && (
        <div>
          {/* Toolbar */}
          <div className="flex flex-wrap items-center gap-3 mb-4">
            <h3 className="text-lg font-semibold mr-auto">{t('settings.logs.heading')}</h3>

            {/* Level filter (display) */}
            <div className="flex items-center gap-1.5 text-xs">
              {(['all', 'debug', 'info', 'warn', 'error'] as const).map(f => (
                <button
                  key={f}
                  onClick={() => setLogFilter(f)}
                  className={`px-2.5 py-1 rounded font-medium transition-colors ${logFilter === f
                    ? f === 'error' ? 'bg-red-600 text-white'
                      : f === 'warn' ? 'bg-amber-500 text-white'
                      : 'bg-slate-700 dark:bg-zinc-600 text-white'
                    : 'bg-slate-200 dark:bg-zinc-800 text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white'}`}
                >
                  {f.toUpperCase()}
                </button>
              ))}
            </div>

            {/* Runtime log level */}
            <div className="flex items-center gap-2 text-xs">
              <span className="text-slate-500 dark:text-zinc-500">{t('settings.logs.level')}</span>
              <select
                value={logLevel}
                onChange={async e => {
                  const l = e.target.value
                  await api.setLogLevel(l).catch(console.error)
                  setLogLevel(l)
                }}
                className="bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-2 py-1 text-xs"
              >
                {['debug', 'info', 'warn', 'error'].map(l => (
                  <option key={l} value={l}>{l.toUpperCase()}</option>
                ))}
              </select>
            </div>

            {/* Auto-refresh toggle */}
            <button
              onClick={() => setLogAutoRefresh(v => !v)}
              className={`text-xs px-2.5 py-1 rounded border transition-colors ${logAutoRefresh
                ? 'border-emerald-500 text-emerald-600 dark:text-emerald-400'
                : 'border-slate-300 dark:border-zinc-700 text-slate-500 dark:text-zinc-500'}`}
            >
              {logAutoRefresh ? `⏸ ${t('settings.logs.autoRefresh')}` : `▶ ${t('settings.logs.autoRefresh')}`}
            </button>

            <button
              onClick={() => api.getLogs(undefined, 200).then(setLogEntries).catch(console.error)}
              className="text-xs px-2.5 py-1 rounded border border-slate-300 dark:border-zinc-700 text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white"
            >
              {t('common.refresh')}
            </button>
          </div>

          {/* Log output */}
          <div className="font-mono text-xs bg-slate-50 dark:bg-black rounded-lg border border-slate-200 dark:border-zinc-900 overflow-auto max-h-[60vh]">
            {(() => {
              const matches = (level: string) => {
                if (logFilter === 'all' || logFilter === 'debug') return true
                if (logFilter === 'info') return level !== 'DEBUG'
                if (logFilter === 'warn') return level === 'WARN' || level === 'ERROR'
                if (logFilter === 'error') return level === 'ERROR'
                return true
              }
              const formatAttr = (k: string, v: unknown) => {
                const s = String(v)
                return /[\s=]/.test(s) ? `${k}="${s.replace(/"/g, '\\"')}"` : `${k}=${s}`
              }
              const filtered = logEntries.filter(e => matches(e.level))
              if (filtered.length === 0) {
                return <p className="text-slate-500 dark:text-zinc-600 p-4 text-center">{t('settings.logs.noEntries')}</p>
              }
              return (
                <table className="w-full border-collapse table-fixed">
                  <colgroup>
                    <col className="w-36" />
                    <col className="w-14" />
                    <col />
                    <col className="w-2/5" />
                  </colgroup>
                  <tbody>
                    {filtered.map((e, i) => {
                      const levelCls =
                        e.level === 'ERROR' ? 'text-red-500 dark:text-red-400' :
                        e.level === 'WARN'  ? 'text-amber-600 dark:text-amber-400' :
                        e.level === 'DEBUG' ? 'text-slate-400 dark:text-zinc-500' :
                        'text-emerald-600 dark:text-emerald-400'
                      const d = new Date(e.time)
                      const ts = d.toLocaleString(i18n.resolvedLanguage, {
                        day: '2-digit', month: '2-digit',
                        hour: '2-digit', minute: '2-digit', second: '2-digit',
                        hour12: false,
                      })
                      const attrStr = e.attrs
                        ? Object.entries(e.attrs).map(([k, v]) => formatAttr(k, v)).join(' ')
                        : ''
                      return (
                        <tr key={i} className="border-b border-slate-200 dark:border-zinc-900 hover:bg-slate-100 dark:hover:bg-zinc-900/50">
                          <td className="pl-3 pr-2 py-0.5 text-slate-500 dark:text-zinc-600 whitespace-nowrap align-top" title={d.toISOString()}>{ts}</td>
                          <td className={`pr-2 py-0.5 whitespace-nowrap font-semibold align-top ${levelCls}`}>{e.level}</td>
                          <td className="pr-2 py-0.5 text-slate-800 dark:text-zinc-200 break-words whitespace-pre-wrap align-top">{e.msg}</td>
                          <td className="pr-3 py-0.5 text-slate-500 dark:text-zinc-500 break-words whitespace-pre-wrap align-top">{attrStr}</td>
                        </tr>
                      )
                    })}
                  </tbody>
                </table>
              )
            })()}
            <div ref={logBottomRef} />
          </div>
          <p className="text-xs text-slate-500 dark:text-zinc-600 mt-2">
            {t('settings.logs.bufferNote')}
          </p>
        </div>
      )}

      {tab === 'general' && (
        <GeneralTab />
      )}

      {tab === 'calibre' && (
        <CalibreTab />
      )}

      {tab === 'blocklist' && (
        <BlocklistTab />
      )}
    </div>
  )
}

interface MigrateResult {
  requested?: number
  added?: number
  skipped?: number
  errors?: number
  addedNames?: string[]
  failures?: Record<string, string>
}

interface ReadarrResult {
  authors?: MigrateResult
  indexers?: MigrateResult
  downloadClients?: MigrateResult
  blocklist?: MigrateResult
}

// KNOWN_LANGUAGES are the ISO 639-2/B codes exposed in the profile editor.
// We keep the list short rather than dumping the full ISO catalogue because
// indexers and metadata providers only reliably tag a handful of majors, and
// a long list invites typos and half-implemented filters.
const KNOWN_LANGUAGES: Array<{ code: string; label: string }> = [
  { code: 'eng', label: 'English' },
  { code: 'fre', label: 'French' },
  { code: 'ger', label: 'German' },
  { code: 'dut', label: 'Dutch' },
  { code: 'spa', label: 'Spanish' },
  { code: 'ita', label: 'Italian' },
  { code: 'por', label: 'Portuguese' },
  { code: 'jpn', label: 'Japanese' },
  { code: 'chi', label: 'Chinese' },
  { code: 'rus', label: 'Russian' },
]

function MetadataProfilesTab({ profiles, onReload }: { profiles: MetadataProfile[]; onReload: () => void }) {
  const { t } = useTranslation()
  const [editing, setEditing] = useState<MetadataProfile | null>(null)
  const [creating, setCreating] = useState(false)

  return (
    <div>
      <div className="flex justify-between items-center mb-4">
        <h3 className="text-lg font-semibold">{t('settings.metadata.heading')}</h3>
        <button onClick={() => setCreating(true)} className="px-3 py-1.5 bg-emerald-600 hover:bg-emerald-500 rounded text-xs font-medium">
          {t('settings.metadata.newProfile')}
        </button>
      </div>
      <p className="text-xs text-slate-600 dark:text-zinc-500 mb-4">
        {t('settings.metadata.description')}
      </p>
      {creating && (
        <MetadataProfileForm
          onClose={() => setCreating(false)}
          onSaved={() => { setCreating(false); onReload() }}
        />
      )}
      {profiles.length === 0 && !creating ? (
        <p className="text-slate-600 dark:text-zinc-500 text-sm">{t('settings.metadata.empty')}</p>
      ) : (
        <div className="space-y-3">
          {profiles.map(p => (
            editing?.id === p.id ? (
              <MetadataProfileForm
                key={p.id}
                profile={p}
                onClose={() => setEditing(null)}
                onSaved={() => { setEditing(null); onReload() }}
              />
            ) : (
              <div key={p.id} className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900">
                <div className="flex items-start justify-between">
                  <div className="min-w-0">
                    <h4 className="font-medium text-sm">{p.name}</h4>
                    <div className="flex flex-wrap gap-3 mt-2 text-xs text-slate-600 dark:text-zinc-400">
                      <span>{t('settings.metadata.minPopularity')} <span className="text-slate-800 dark:text-zinc-200">{p.minPopularity}</span></span>
                      <span>{t('settings.metadata.minPages')} <span className="text-slate-800 dark:text-zinc-200">{p.minPages}</span></span>
                      <span>{t('settings.metadata.languages')} <span className="text-slate-800 dark:text-zinc-200">{formatLanguageList(p.allowedLanguages)}</span></span>
                    </div>
                    <div className="flex flex-wrap gap-1.5 mt-2">
                      {p.skipMissingDate && <span className="text-[10px] px-2 py-0.5 bg-slate-200 dark:bg-zinc-800 text-slate-600 dark:text-zinc-400 rounded">{t('settings.metadata.skipMissingDate')}</span>}
                      {p.skipMissingIsbn && <span className="text-[10px] px-2 py-0.5 bg-slate-200 dark:bg-zinc-800 text-slate-600 dark:text-zinc-400 rounded">{t('settings.metadata.skipMissingIsbn')}</span>}
                      {p.skipPartBooks && <span className="text-[10px] px-2 py-0.5 bg-slate-200 dark:bg-zinc-800 text-slate-600 dark:text-zinc-400 rounded">{t('settings.metadata.skipPartBooks')}</span>}
                    </div>
                  </div>
                  <div className="flex items-center gap-3 flex-shrink-0">
                    <button onClick={() => setEditing(p)} className="text-xs text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white">{t('common.edit')}</button>
                    <button
                      onClick={async () => {
                        if (!confirm(t('settings.metadata.deleteConfirm'))) return
                        await api.deleteMetadataProfile(p.id)
                        onReload()
                      }}
                      className="text-xs text-red-400 hover:text-red-300"
                    >
                      {t('common.delete')}
                    </button>
                  </div>
                </div>
              </div>
            )
          ))}
        </div>
      )}
    </div>
  )
}

function formatLanguageList(csv: string): string {
  if (!csv || csv.trim() === '' || csv.trim().toLowerCase() === 'any') return 'any'
  return csv.split(',').map(c => {
    const code = c.trim().toLowerCase()
    const known = KNOWN_LANGUAGES.find(k => k.code === code)
    return known ? known.label : code
  }).join(', ')
}

function MetadataProfileForm({ profile, onClose, onSaved }: { profile?: MetadataProfile; onClose: () => void; onSaved: () => void }) {
  const { t } = useTranslation()
  const [name, setName] = useState(profile?.name ?? '')
  const [minPopularity, setMinPopularity] = useState(profile?.minPopularity ?? 0)
  const [minPages, setMinPages] = useState(profile?.minPages ?? 0)
  const [skipMissingDate, setSkipMissingDate] = useState(profile?.skipMissingDate ?? false)
  const [skipMissingIsbn, setSkipMissingIsbn] = useState(profile?.skipMissingIsbn ?? false)
  const [skipPartBooks, setSkipPartBooks] = useState(profile?.skipPartBooks ?? false)
  const initialLangs = profile?.allowedLanguages
    ? profile.allowedLanguages.split(',').map(c => c.trim().toLowerCase()).filter(Boolean)
    : ['eng']
  const [languages, setLanguages] = useState<string[]>(initialLangs)
  const [saving, setSaving] = useState(false)
  const [err, setErr] = useState<string | null>(null)

  const toggleLang = (code: string) => {
    setLanguages(prev => prev.includes(code) ? prev.filter(c => c !== code) : [...prev, code])
  }

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setErr(null)
    setSaving(true)
    try {
      const payload: Partial<MetadataProfile> = {
        name: name.trim(),
        minPopularity,
        minPages,
        skipMissingDate,
        skipMissingIsbn,
        skipPartBooks,
        allowedLanguages: languages.join(','),
      }
      if (profile) {
        await api.updateMetadataProfile(profile.id, payload)
      } else {
        await api.addMetadataProfile(payload)
      }
      onSaved()
    } catch (e: unknown) {
      setErr(e instanceof Error ? e.message : t('settings.metadata.saveFail'))
    } finally {
      setSaving(false)
    }
  }

  return (
    <form onSubmit={submit} className="p-4 border border-slate-300 dark:border-zinc-700 rounded-lg bg-slate-50 dark:bg-zinc-900/50 space-y-4">
      <div>
        <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">{t('settings.metadata.formName')}</label>
        <input value={name} onChange={e => setName(e.target.value)} required className={inputCls} placeholder={t('settings.metadata.formNamePlaceholder')} />
      </div>
      <div>
        <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-2">{t('settings.metadata.formLanguages')}</label>
        <div className="flex flex-wrap gap-2">
          {KNOWN_LANGUAGES.map(l => {
            const on = languages.includes(l.code)
            return (
              <button
                type="button"
                key={l.code}
                onClick={() => toggleLang(l.code)}
                className={`text-xs px-2.5 py-1 rounded border transition-colors ${on
                  ? 'bg-emerald-500/20 border-emerald-500/40 text-emerald-300'
                  : 'bg-slate-200 dark:bg-zinc-800 border-slate-300 dark:border-zinc-700 text-slate-600 dark:text-zinc-400 hover:border-slate-400 dark:hover:border-zinc-600'}`}
              >
                {l.label}
              </button>
            )
          })}
        </div>
        <p className="text-[11px] text-slate-500 dark:text-zinc-500 mt-2">
          {t('settings.metadata.formLanguagesHint')}
        </p>
      </div>
      <div className="grid grid-cols-2 gap-3">
        <div>
          <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">{t('settings.metadata.formMinPopularity')}</label>
          <input type="number" min={0} value={minPopularity} onChange={e => setMinPopularity(Number(e.target.value))} className={inputCls} />
        </div>
        <div>
          <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">{t('settings.metadata.formMinPages')}</label>
          <input type="number" min={0} value={minPages} onChange={e => setMinPages(Number(e.target.value))} className={inputCls} />
        </div>
      </div>
      <div className="flex flex-wrap gap-4 text-xs">
        <label className="flex items-center gap-2 cursor-pointer">
          <input type="checkbox" checked={skipMissingDate} onChange={e => setSkipMissingDate(e.target.checked)} />
          {t('settings.metadata.formSkipMissingDate')}
        </label>
        <label className="flex items-center gap-2 cursor-pointer">
          <input type="checkbox" checked={skipMissingIsbn} onChange={e => setSkipMissingIsbn(e.target.checked)} />
          {t('settings.metadata.formSkipMissingIsbn')}
        </label>
        <label className="flex items-center gap-2 cursor-pointer">
          <input type="checkbox" checked={skipPartBooks} onChange={e => setSkipPartBooks(e.target.checked)} />
          {t('settings.metadata.formSkipPartBooks')}
        </label>
      </div>
      {err && <div className="text-xs text-red-400">{err}</div>}
      <div className="flex justify-end gap-2">
        <button type="button" onClick={onClose} className="px-3 py-1.5 text-xs text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white">
          {t('common.cancel')}
        </button>
        <button type="submit" disabled={saving} className="px-3 py-1.5 bg-emerald-600 hover:bg-emerald-500 disabled:opacity-50 rounded text-xs font-medium">
          {saving ? t('common.saving') : profile ? t('settings.metadata.saveChanges') : t('settings.metadata.createProfile')}
        </button>
      </div>
    </form>
  )
}

function ImportTab() {
  const { t } = useTranslation()
  const [csvResult, setCsvResult] = useState<MigrateResult | null>(null)
  const [readarrResult, setReadarrResult] = useState<ReadarrResult | null>(null)
  const [uploading, setUploading] = useState<'csv' | 'readarr' | null>(null)
  const [err, setErr] = useState<string | null>(null)

  const upload = async (endpoint: 'csv' | 'readarr', file: File) => {
    setUploading(endpoint)
    setErr(null)
    setCsvResult(null)
    setReadarrResult(null)
    try {
      const fd = new FormData()
      fd.append('file', file)
      const res = await fetch(`/api/v1/migrate/${endpoint}`, { method: 'POST', body: fd })
      if (!res.ok) {
        const body = await res.json().catch(() => ({ error: res.statusText }))
        throw new Error(body.error || `HTTP ${res.status}`)
      }
      const data = await res.json()
      if (endpoint === 'csv') setCsvResult(data)
      else setReadarrResult(data)
    } catch (e) {
      setErr(e instanceof Error ? e.message : 'Upload failed')
    } finally {
      setUploading(null)
    }
  }

  const renderResult = (r: MigrateResult | undefined, label: string) => {
    if (!r) return null
    return (
      <div className="p-3 border border-slate-200 dark:border-zinc-800 rounded bg-slate-100 dark:bg-zinc-900 space-y-1">
        <div className="text-sm font-medium">{label}</div>
        <div className="text-xs text-slate-600 dark:text-zinc-500">
          {r.requested ?? 0} requested · {r.added ?? 0} added · {r.skipped ?? 0} skipped (already exist) · {r.errors ?? 0} failed
        </div>
        {r.failures && Object.keys(r.failures).length > 0 && (
          <details className="text-xs">
            <summary className="cursor-pointer text-red-600 dark:text-red-400">Show {Object.keys(r.failures).length} failures</summary>
            <ul className="mt-2 space-y-0.5 font-mono">
              {Object.entries(r.failures).map(([name, reason]) => (
                <li key={name}><span className="text-slate-800 dark:text-zinc-200">{name}</span>: <span className="text-slate-500 dark:text-zinc-500">{reason}</span></li>
              ))}
            </ul>
          </details>
        )}
      </div>
    )
  }

  return (
    <div className="space-y-8 max-w-2xl">
      <section>
        <h3 className="text-base font-semibold mb-2 text-slate-800 dark:text-zinc-200">{t('settings.import.csvHeading')}</h3>
        <p className="text-xs text-slate-600 dark:text-zinc-500 mb-3">
          {t('settings.import.csvDescription')}
        </p>
        <label className="inline-flex items-center gap-2 px-3 py-2 bg-emerald-600 hover:bg-emerald-500 rounded text-sm font-medium cursor-pointer">
          {uploading === 'csv' ? t('settings.import.importingCsv') : t('settings.import.uploadCsv')}
          <input
            type="file"
            accept=".csv,.txt,text/csv,text/plain"
            className="hidden"
            disabled={uploading !== null}
            onChange={e => { const f = e.target.files?.[0]; if (f) upload('csv', f); e.currentTarget.value = '' }}
          />
        </label>
        {csvResult && <div className="mt-4">{renderResult(csvResult, 'Authors')}</div>}
      </section>

      <section>
        <h3 className="text-base font-semibold mb-2 text-slate-800 dark:text-zinc-200">{t('settings.import.readarrHeading')}</h3>
        <p className="text-xs text-slate-600 dark:text-zinc-500 mb-3">
          {t('settings.import.readarrDescription')}
        </p>
        <label className="inline-flex items-center gap-2 px-3 py-2 bg-emerald-600 hover:bg-emerald-500 rounded text-sm font-medium cursor-pointer">
          {uploading === 'readarr' ? t('settings.import.importingReadarr') : t('settings.import.uploadReadarr')}
          <input
            type="file"
            accept=".db,.sqlite,application/x-sqlite3,application/octet-stream"
            className="hidden"
            disabled={uploading !== null}
            onChange={e => { const f = e.target.files?.[0]; if (f) upload('readarr', f); e.currentTarget.value = '' }}
          />
        </label>
        {readarrResult && (
          <div className="mt-4 space-y-2">
            {renderResult(readarrResult.authors, 'Authors')}
            {renderResult(readarrResult.indexers, 'Indexers')}
            {renderResult(readarrResult.downloadClients, 'Download clients')}
            {renderResult(readarrResult.blocklist, 'Blocklist')}
          </div>
        )}
      </section>

      <HardcoverListsSection />

      {err && (
        <div className="px-3 py-2 bg-red-100 dark:bg-red-950/30 border border-red-300 dark:border-red-900 rounded text-sm text-red-800 dark:text-red-300">
          {err}
        </div>
      )}
    </div>
  )
}

function HardcoverListsSection() {
  const { t } = useTranslation()
  const [lists, setLists] = useState<ImportList[]>([])
  const [showAdd, setShowAdd] = useState(false)

  useEffect(() => {
    api.listImportLists().then(all => setLists(all.filter(l => l.type === 'hardcover'))).catch(console.error)
  }, [])

  const handleDelete = async (id: number) => {
    await api.deleteImportList(id)
    setLists(prev => prev.filter(l => l.id !== id))
  }

  const handleToggle = async (il: ImportList) => {
    const updated = await api.updateImportList(il.id, { ...il, enabled: !il.enabled })
    setLists(prev => prev.map(l => l.id === il.id ? updated : l))
  }

  return (
    <section>
      <div className="flex justify-between items-center mb-2">
        <h3 className="text-base font-semibold text-slate-800 dark:text-zinc-200">{t('settings.import.hardcoverHeading')}</h3>
        <button onClick={() => setShowAdd(true)} className="px-3 py-1.5 bg-emerald-600 hover:bg-emerald-500 rounded text-xs font-medium">
          {t('settings.import.hardcoverAddButton')}
        </button>
      </div>
      <p className="text-xs text-slate-600 dark:text-zinc-500 mb-3">
        {t('settings.import.hardcoverDescription')}
      </p>

      {lists.length === 0 && !showAdd && (
        <p className="text-sm text-slate-500 dark:text-zinc-600">{t('settings.import.hardcoverEmpty')}</p>
      )}

      {lists.map(il => (
        <div key={il.id} className="flex items-center justify-between p-3 mb-2 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900">
          <div className="min-w-0">
            <div className="flex items-center gap-2">
              <span className="text-sm font-medium">{il.name}</span>
              <span className="text-[10px] px-1.5 py-0.5 bg-slate-200 dark:bg-zinc-800 text-slate-600 dark:text-zinc-400 rounded">{il.url}</span>
            </div>
            <div className="text-xs text-slate-500 dark:text-zinc-600 mt-0.5">
              {il.lastSyncAt
                ? t('settings.import.hardcoverLastSync', { date: new Date(il.lastSyncAt).toLocaleString() })
                : t('settings.import.hardcoverNeverSynced')}
            </div>
          </div>
          <div className="flex items-center gap-2 ml-3">
            <button
              onClick={() => handleToggle(il)}
              className={`text-xs px-2 py-1 rounded ${il.enabled ? 'bg-emerald-100 dark:bg-emerald-950 text-emerald-700 dark:text-emerald-400' : 'bg-slate-200 dark:bg-zinc-800 text-slate-500 dark:text-zinc-500'}`}
            >
              {il.enabled ? t('common.enable') : t('common.disable')}
            </button>
            <button onClick={() => handleDelete(il.id)} className="text-xs text-red-600 dark:text-red-400 hover:underline">{t('common.delete')}</button>
          </div>
        </div>
      ))}

      {showAdd && (
        <AddHardcoverListForm
          onSaved={il => { setLists(prev => [...prev, il]); setShowAdd(false) }}
          onCancel={() => setShowAdd(false)}
        />
      )}
    </section>
  )
}

function AddHardcoverListForm({ onSaved, onCancel }: { onSaved: (il: ImportList) => void; onCancel: () => void }) {
  const { t } = useTranslation()
  const [name, setName] = useState('')
  const [token, setToken] = useState('')
  const [hcLists, setHcLists] = useState<HardcoverList[]>([])
  const [selectedSlug, setSelectedSlug] = useState('')
  const [loadingLists, setLoadingLists] = useState(false)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  const fetchLists = (tok: string) => {
    if (debounceRef.current) clearTimeout(debounceRef.current)
    if (!tok.trim()) { setHcLists([]); return }
    debounceRef.current = setTimeout(async () => {
      setLoadingLists(true)
      try {
        const lists = await api.hardcoverLists(tok)
        setHcLists(lists)
        if (lists.length > 0 && !selectedSlug) setSelectedSlug(lists[0].slug)
      } catch (e) {
        setHcLists([])
        setError(e instanceof Error ? e.message : 'Failed to load lists')
      } finally {
        setLoadingLists(false)
      }
    }, 500)
  }

  const handleTokenChange = (tok: string) => {
    setToken(tok)
    setError(null)
    fetchLists(tok)
  }

  const handleSave = async () => {
    if (!name.trim() || !token.trim() || !selectedSlug) return
    setSaving(true)
    setError(null)
    try {
      const il = await api.addImportList({
        name: name.trim(),
        type: 'hardcover',
        apiKey: token.trim(),
        url: selectedSlug,
        enabled: true,
        monitorNew: true,
        autoAdd: true,
      })
      onSaved(il)
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to save')
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900 space-y-3">
      <div>
        <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">{t('settings.import.hardcoverName')}</label>
        <input className={inputCls} placeholder={t('settings.import.hardcoverNamePlaceholder')} value={name} onChange={e => setName(e.target.value)} />
      </div>
      <div>
        <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">{t('settings.import.hardcoverToken')}</label>
        <input className={inputCls} type="password" placeholder={t('settings.import.hardcoverTokenPlaceholder')} value={token} onChange={e => handleTokenChange(e.target.value)} />
      </div>
      <div>
        <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">{t('settings.import.hardcoverList')}</label>
        {loadingLists ? (
          <p className="text-xs text-slate-500">{t('settings.import.hardcoverListLoading')}</p>
        ) : hcLists.length > 0 ? (
          <select className={inputCls} value={selectedSlug} onChange={e => setSelectedSlug(e.target.value)}>
            {hcLists.map(l => (
              <option key={l.slug} value={l.slug}>{l.name} ({l.booksCount} books)</option>
            ))}
          </select>
        ) : (
          <p className="text-xs text-slate-500">{token ? t('settings.import.hardcoverNoLists') : t('settings.import.hardcoverListPlaceholder')}</p>
        )}
      </div>
      {error && <p className="text-xs text-red-600 dark:text-red-400">{error}</p>}
      <div className="flex gap-2">
        <button
          onClick={handleSave}
          disabled={saving || !name.trim() || !token.trim() || !selectedSlug}
          className="px-3 py-1.5 bg-emerald-600 hover:bg-emerald-500 disabled:opacity-50 rounded text-xs font-medium"
        >
          {saving ? t('common.saving') : t('common.save')}
        </button>
        <button onClick={onCancel} className="px-3 py-1.5 bg-slate-300 dark:bg-zinc-700 hover:bg-slate-400 dark:hover:bg-zinc-600 rounded text-xs font-medium">
          {t('common.cancel')}
        </button>
      </div>
    </div>
  )
}

function GeneralTab() {
  const { t } = useTranslation()
  const [settings, setSettings] = useState<Record<string, string>>({})
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState<string | null>(null)
  const [backups, setBackups] = useState<string[]>([])
  const [creatingBackup, setCreatingBackup] = useState(false)
  const [scanningLibrary, setScanningLibrary] = useState(false)
  const [scanMessage, setScanMessage] = useState<string | null>(null)
  const [lastScan, setLastScan] = useState<{ ran_at: string; files_found: number; reconciled: number; unmatched: number } | null>(null)

  useEffect(() => {
    api.listSettings()
      .then(list => {
        const map: Record<string, string> = {}
        list.forEach(s => { map[s.key] = s.value })
        setSettings(map)
      })
      .catch(console.error)
      .finally(() => setLoading(false))
    api.listBackups().then(setBackups).catch(console.error)
    api.libraryScanStatus().then(setLastScan).catch(() => {/* no prior scan — ignore 404 */})
  }, [])

  const saveSetting = async (key: string) => {
    setSaving(key)
    try {
      await api.setSetting(key, settings[key] ?? '')
    } catch (err) {
      console.error(err)
    } finally {
      setSaving(null)
    }
  }

  const handleBackup = async () => {
    setCreatingBackup(true)
    try {
      const result = await api.createBackup()
      setBackups(prev => [result.filename, ...prev])
      alert(`Backup created: ${result.filename}`)
    } catch (err) {
      alert('Backup failed: ' + (err instanceof Error ? err.message : 'Unknown error'))
    } finally {
      setCreatingBackup(false)
    }
  }

  const handleScan = async () => {
    setScanningLibrary(true)
    setScanMessage('Scanning...')
    setLastScan(null)
    try {
      await api.triggerLibraryScan()
      // Poll for the result — the scan is async, so wait up to ~8s in 1s intervals.
      let attempts = 0
      const poll = async () => {
        attempts++
        try {
          const status = await api.libraryScanStatus()
          // Only accept a result that was produced after we triggered the scan.
          const ranAt = new Date(status.ran_at).getTime()
          const triggerTime = Date.now() - (attempts * 1000)
          if (ranAt >= triggerTime) {
            setLastScan(status)
            setScanMessage(null)
            setScanningLibrary(false)
            return
          }
        } catch {
          // result not ready yet
        }
        if (attempts < 8) {
          setTimeout(poll, 1000)
        } else {
          setScanMessage('Scan started — results will appear after it completes.')
          setScanningLibrary(false)
        }
      }
      setTimeout(poll, 1000)
    } catch (err) {
      setScanMessage('Scan failed: ' + (err instanceof Error ? err.message : 'Unknown error'))
      setScanningLibrary(false)
    }
  }

  if (loading) return <div className="text-slate-600 dark:text-zinc-500">{t('common.loading')}</div>

  return (
    <div className="space-y-8">
      {/* Appearance */}
      <section>
        <h3 className="text-base font-semibold mb-3 text-slate-800 dark:text-zinc-200">{t('settings.general.appearance')}</h3>
        <div className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900">
          <div className="flex items-center justify-between">
            <div>
              <label className="block text-sm font-medium text-slate-800 dark:text-zinc-200">{t('settings.general.theme')}</label>
              <p className="text-xs text-slate-600 dark:text-zinc-500 mt-1">{t('settings.general.themeHint')}</p>
            </div>
            <ThemeToggle />
          </div>
          <div className="flex items-center justify-between border-t border-slate-200 dark:border-zinc-800 pt-3 mt-3">
            <div>
              <label className="block text-sm font-medium text-slate-800 dark:text-zinc-200">{t('settings.general.language')}</label>
              <p className="text-xs text-slate-600 dark:text-zinc-500 mt-1">{t('settings.general.languageHint')}</p>
            </div>
            <LanguageSwitcher />
          </div>
        </div>
      </section>

      {/* Naming */}
      <section>
        <h3 className="text-base font-semibold mb-3 text-slate-800 dark:text-zinc-200">{t('settings.general.fileNaming')}</h3>
        <div className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900 space-y-3">
          <div>
            <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">Import Mode</label>
            <p className="text-xs text-slate-600 dark:text-zinc-500 mb-2">
              How Bindery places completed downloads into the library.
              Use <strong>Hardlink</strong> or <strong>Copy</strong> to keep the source file intact for torrent seeding.
              Hardlink requires the download folder and library to be on the same filesystem/volume.
            </p>
            <div className="flex gap-2">
              {(['move', 'copy', 'hardlink'] as const).map(m => (
                <button
                  key={m}
                  onClick={async () => {
                    setSettings(s => ({ ...s, 'import.mode': m }))
                    await api.setSetting('import.mode', m).catch(console.error)
                  }}
                  className={`px-3 py-1.5 rounded text-xs font-medium border transition-colors ${
                    (settings['import.mode'] ?? 'move') === m
                      ? 'bg-emerald-600 border-emerald-600 text-white'
                      : 'border-slate-300 dark:border-zinc-700 text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white'
                  }`}
                >
                  {m.charAt(0).toUpperCase() + m.slice(1)}
                </button>
              ))}
            </div>
          </div>
          <div>
            <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">{t('settings.general.bookTemplate')}</label>
            <div className="flex gap-2">
              <input
                value={settings['naming.bookTemplate'] ?? ''}
                onChange={e => setSettings(s => ({ ...s, 'naming.bookTemplate': e.target.value }))}
                placeholder="{Author}/{Title} ({Year})/{Title} - {Author}.{ext}"
                className="flex-1 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600"
              />
              <button
                onClick={() => saveSetting('naming.bookTemplate')}
                disabled={saving === 'naming.bookTemplate'}
                className="px-3 py-2 bg-emerald-600 hover:bg-emerald-500 rounded text-xs font-medium disabled:opacity-50"
              >
                {saving === 'naming.bookTemplate' ? t('common.saving') : t('common.save')}
              </button>
            </div>
          </div>
          <div>
            <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">{t('settings.general.audiobookTemplate')}</label>
            <p className="text-xs text-slate-600 dark:text-zinc-500 mb-2">{t('settings.general.audiobookTemplateHint')}</p>
            <div className="flex gap-2">
              <input
                value={settings['naming_template_audiobook'] ?? ''}
                onChange={e => setSettings(s => ({ ...s, 'naming_template_audiobook': e.target.value }))}
                placeholder="{Author}/{Title} ({Year})"
                className="flex-1 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600"
              />
              <button
                onClick={() => saveSetting('naming_template_audiobook')}
                disabled={saving === 'naming_template_audiobook'}
                className="px-3 py-2 bg-emerald-600 hover:bg-emerald-500 rounded text-xs font-medium disabled:opacity-50"
              >
                {saving === 'naming_template_audiobook' ? t('common.saving') : t('common.save')}
              </button>
            </div>
          </div>
        </div>
      </section>

      {/* Downloads */}
      <section>
        <h3 className="text-base font-semibold mb-3 text-slate-800 dark:text-zinc-200">{t('settings.general.downloads')}</h3>
        <div className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900 space-y-3">
          <div>
            <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">{t('settings.general.preferredLanguage')}</label>
            <p className="text-xs text-slate-600 dark:text-zinc-500 mb-2">{t('settings.general.preferredLanguageHint')}</p>
            <div className="flex gap-2">
              <select
                value={settings['search.preferredLanguage'] ?? 'en'}
                onChange={e => setSettings(s => ({ ...s, 'search.preferredLanguage': e.target.value }))}
                className="flex-1 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600"
              >
                <option value="any">{t('settings.general.preferredLanguageAny')}</option>
                <option value="en">{t('settings.general.preferredLanguageEn')}</option>
              </select>
              <button
                onClick={() => saveSetting('search.preferredLanguage')}
                disabled={saving === 'search.preferredLanguage'}
                className="px-3 py-2 bg-emerald-600 hover:bg-emerald-500 rounded text-xs font-medium disabled:opacity-50"
              >
                {saving === 'search.preferredLanguage' ? t('common.saving') : t('common.save')}
              </button>
            </div>
          </div>
        </div>
      </section>

      {/* Library */}
      <section>
        <h3 className="text-base font-semibold mb-3 text-slate-800 dark:text-zinc-200">{t('settings.general.library')}</h3>
        <div className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900">
          <div className="flex items-center justify-between">
            <div>
              <p className="text-sm text-slate-700 dark:text-zinc-300">{t('settings.general.scanLibrary')}</p>
              <p className="text-xs text-slate-600 dark:text-zinc-500 mt-0.5">{t('settings.general.scanLibraryHint')}</p>
              {scanMessage && (
                <p className="text-xs text-amber-600 dark:text-amber-400 mt-1">{scanMessage}</p>
              )}
            </div>
            <button
              onClick={handleScan}
              disabled={scanningLibrary}
              className="px-4 py-2 bg-slate-600 hover:bg-slate-500 rounded text-sm font-medium disabled:opacity-50 flex-shrink-0"
            >
              {scanningLibrary ? t('settings.general.scanning') : t('settings.general.scanLibraryButton')}
            </button>
          </div>
          {lastScan && (
            <div className="mt-3 border-t border-slate-200 dark:border-zinc-800 pt-3 text-xs text-slate-600 dark:text-zinc-400">
              <p className="font-medium text-slate-700 dark:text-zinc-300 mb-1">{t('settings.general.lastScan')}</p>
              <div className="flex gap-4">
                <span>{t('settings.general.filesFound')} <span className="font-mono text-slate-800 dark:text-zinc-200">{lastScan.files_found}</span></span>
                <span>{t('settings.general.reconciled')} <span className="font-mono text-emerald-700 dark:text-emerald-400">{lastScan.reconciled}</span></span>
                <span>{t('settings.general.unmatched')} <span className="font-mono text-slate-800 dark:text-zinc-200">{lastScan.unmatched}</span></span>
              </div>
              <p className="mt-1 text-slate-500 dark:text-zinc-500">
                {new Date(lastScan.ran_at).toLocaleString()}
              </p>
            </div>
          )}
        </div>
      </section>

      {/* Auto-grab */}
      <section>
        <h3 className="text-base font-semibold mb-3 text-slate-800 dark:text-zinc-200">{t('settings.general.autoGrab')}</h3>
        <div className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900">
          <div className="flex items-center justify-between">
            <div>
              <label className="block text-sm font-medium text-slate-800 dark:text-zinc-200">{t('settings.general.autoGrabLabel')}</label>
              <p className="text-xs text-slate-600 dark:text-zinc-500 mt-0.5">{t('settings.general.autoGrabHint')}</p>
            </div>
            <button
              onClick={async () => {
                const current = (settings['autoGrab.enabled'] ?? 'true').toLowerCase()
                const next = current === 'false' ? 'true' : 'false'
                setSettings(s => ({ ...s, 'autoGrab.enabled': next }))
                await api.setSetting('autoGrab.enabled', next).catch(console.error)
              }}
              className={`relative w-9 h-5 rounded-full transition-colors flex-shrink-0 ${(settings['autoGrab.enabled'] ?? 'true') !== 'false' ? 'bg-emerald-600' : 'bg-slate-300 dark:bg-zinc-700'}`}
              title={(settings['autoGrab.enabled'] ?? 'true') !== 'false' ? t('common.disable') : t('common.enable')}
            >
              <span className={`absolute top-0.5 left-0.5 w-4 h-4 bg-white rounded-full transition-transform ${(settings['autoGrab.enabled'] ?? 'true') !== 'false' ? 'translate-x-4' : ''}`} />
            </button>
          </div>
        </div>
      </section>

      {/* Recommendations */}
      <section>
        <h3 className="text-base font-semibold mb-3 text-slate-800 dark:text-zinc-200">{t('settings.general.recommendations')}</h3>
        <div className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900">
          <div className="flex items-center justify-between">
            <div>
              <label className="block text-sm font-medium text-slate-800 dark:text-zinc-200">{t('settings.general.recommendationsLabel')}</label>
              <p className="text-xs text-slate-600 dark:text-zinc-500 mt-0.5">{t('settings.general.recommendationsHint')}</p>
            </div>
            <button
              onClick={async () => {
                const current = (settings['recommendations.enabled'] ?? 'false').toLowerCase()
                const next = current === 'true' ? 'false' : 'true'
                setSettings(s => ({ ...s, 'recommendations.enabled': next }))
                await api.setSetting('recommendations.enabled', next).catch(console.error)
              }}
              className={`relative w-9 h-5 rounded-full transition-colors flex-shrink-0 ${(settings['recommendations.enabled'] ?? 'false') === 'true' ? 'bg-emerald-600' : 'bg-slate-300 dark:bg-zinc-700'}`}
              title={(settings['recommendations.enabled'] ?? 'false') === 'true' ? t('common.disable') : t('common.enable')}
            >
              <span className={`absolute top-0.5 left-0.5 w-4 h-4 bg-white rounded-full transition-transform ${(settings['recommendations.enabled'] ?? 'false') === 'true' ? 'translate-x-4' : ''}`} />
            </button>
          </div>
        </div>
      </section>

      {/* Security */}
      <SecuritySection />

      {/* API Keys */}
      <section>
        <h3 className="text-base font-semibold mb-3 text-slate-800 dark:text-zinc-200">{t('settings.general.apiKeys')}</h3>
        <div className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900 space-y-4">
          <div>
            <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">{t('settings.general.googleBooksKey')}</label>
            <div className="flex gap-2">
              <input
                value={settings['googlebooks.apiKey'] ?? ''}
                onChange={e => setSettings(s => ({ ...s, 'googlebooks.apiKey': e.target.value }))}
                placeholder="AIza..."
                type="password"
                className="flex-1 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600"
              />
              <button
                onClick={() => saveSetting('googlebooks.apiKey')}
                disabled={saving === 'googlebooks.apiKey'}
                className="px-3 py-2 bg-emerald-600 hover:bg-emerald-500 rounded text-xs font-medium disabled:opacity-50"
              >
                {saving === 'googlebooks.apiKey' ? t('common.saving') : t('common.save')}
              </button>
            </div>
          </div>
        </div>
      </section>

      {/* OPDS */}
      <section>
        <h3 className="text-base font-semibold mb-3 text-slate-800 dark:text-zinc-200">{t('settings.general.opds')}</h3>
        <div className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900">
          <div>
            <span className="block text-xs font-medium text-slate-600 dark:text-zinc-400 mb-1">{t('settings.general.opdsFeedUrl')}</span>
            <div className="flex items-center gap-2">
              <code className="flex-1 text-xs bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 px-2 py-1.5 rounded font-mono break-all">
                {window.location.origin}/opds
              </code>
              <button
                onClick={() => navigator.clipboard.writeText(window.location.origin + '/opds')}
                className="px-3 py-1.5 bg-slate-600 hover:bg-slate-500 rounded text-xs font-medium flex-shrink-0"
              >
                {t('settings.general.copy')}
              </button>
            </div>
            <p className="text-xs text-slate-500 dark:text-zinc-500 mt-1.5">{t('settings.general.opdsHint')}</p>
          </div>
        </div>
      </section>

      {/* Backup */}
      <section>
        <h3 className="text-base font-semibold mb-3 text-slate-800 dark:text-zinc-200">{t('settings.general.backup')}</h3>
        <div className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900 space-y-3">
          <div className="flex items-center justify-between">
            <div>
              <p className="text-sm text-slate-700 dark:text-zinc-300">{t('settings.general.backupCreate')}</p>
              <p className="text-xs text-slate-600 dark:text-zinc-500 mt-0.5">{t('settings.general.backupHint')}</p>
            </div>
            <button
              onClick={handleBackup}
              disabled={creatingBackup}
              className="px-4 py-2 bg-emerald-600 hover:bg-emerald-500 rounded text-sm font-medium disabled:opacity-50 flex-shrink-0"
            >
              {creatingBackup ? t('settings.general.backupCreating') : t('settings.general.backupButton')}
            </button>
          </div>
          {backups.length > 0 && (
            <div className="mt-3 border-t border-slate-200 dark:border-zinc-800 pt-3">
              <p className="text-xs text-slate-600 dark:text-zinc-500 mb-2">{t('settings.general.existingBackups')}</p>
              <ul className="space-y-1">
                {backups.map(b => (
                  <li key={b} className="text-xs text-slate-600 dark:text-zinc-400 font-mono">{b}</li>
                ))}
              </ul>
            </div>
          )}
        </div>
      </section>
    </div>
  )
}

function parseCats(s: string): number[] {
  return s.split(',').map(t => parseInt(t.trim(), 10)).filter(n => !isNaN(n))
}

function CalibreTab() {
  const [settings, setSettings] = useState<Record<string, string>>({})
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState<string | null>(null)

  useEffect(() => {
    api.listSettings()
      .then(list => {
        const map: Record<string, string> = {}
        list.forEach(s => { map[s.key] = s.value })
        setSettings(map)
      })
      .catch(console.error)
      .finally(() => setLoading(false))
  }, [])

  const saveSetting = async (key: string): Promise<string | null> => {
    setSaving(key)
    try {
      await api.setSetting(key, settings[key] ?? '')
      return null
    } catch (err) {
      return err instanceof Error ? err.message : 'Save failed'
    } finally {
      setSaving(null)
    }
  }

  if (loading) return <div className="text-slate-600 dark:text-zinc-500">Loading…</div>

  return (
    <div className="space-y-8">
      <CalibreSection settings={settings} setSettings={setSettings} saveSetting={saveSetting} saving={saving} />
    </div>
  )
}

// CalibreSection renders the calibre.* settings fields plus a Test button
// that hits /calibre/test.
function CalibreSection({
  settings,
  setSettings,
  saveSetting,
  saving,
}: {
  settings: Record<string, string>
  setSettings: (fn: (prev: Record<string, string>) => Record<string, string>) => void
  saveSetting: (key: string) => Promise<string | null>
  saving: string | null
}) {
  const [testing, setTesting] = useState(false)
  const [testResult, setTestResult] = useState<{ ok: boolean; msg: string } | null>(null)
  const [saveError, setSaveError] = useState<{ key: string; msg: string } | null>(null)
  const [importProgress, setImportProgress] = useState<CalibreImportProgress | null>(null)
  const [importError, setImportError] = useState<string | null>(null)

  const saveSettingWithError = async (key: string) => {
    setSaveError(null)
    const err = await saveSetting(key)
    if (err) setSaveError({ key, msg: err })
  }

  // Legacy fallback: a pre-migration DB with `calibre.enabled=true` but no
  // mode set should still render as 'calibredb' so the user's existing
  // setup is visible in the UI.
  const rawMode = settings['calibre.mode'] ?? ''
  const legacyEnabled = (settings['calibre.enabled'] ?? 'false').toLowerCase() === 'true'
  const mode: 'off' | 'calibredb' | 'plugin' =
    rawMode === 'calibredb' || rawMode === 'off' || rawMode === 'plugin'
      ? rawMode
      : legacyEnabled
      ? 'calibredb'
      : 'off'
  const libraryImportEnabled = (settings['calibre.library_import_enabled'] ?? 'false').toLowerCase() === 'true'
  const syncOnStartup = (settings['calibre.sync_on_startup'] ?? 'false').toLowerCase() === 'true'
  const lastImportAt = settings['calibre.last_import_at'] ?? ''

  // Hydrate progress on mount so navigating back mid-import still shows
  // the live bar instead of a dead "Import library" button.
  useEffect(() => {
    api.calibreImportStatus().then(setImportProgress).catch(() => {})
  }, [])

  // Poll while an import is running.
  useEffect(() => {
    if (!importProgress?.running) return
    const id = setInterval(() => {
      api.calibreImportStatus().then(setImportProgress).catch(() => {})
    }, 1000)
    return () => clearInterval(id)
  }, [importProgress?.running])

  const startImport = async () => {
    setImportError(null)
    try {
      const p = await api.calibreImportStart()
      setImportProgress(p)
    } catch (err) {
      setImportError(err instanceof Error ? err.message : 'Import failed to start')
    }
  }

  const runTest = async () => {
    setTesting(true)
    setTestResult(null)
    try {
      const r = await api.testCalibre()
      setTestResult({ ok: true, msg: `${r.message}${r.version ? ' — ' + r.version : ''}` })
    } catch (err) {
      setTestResult({ ok: false, msg: err instanceof Error ? err.message : 'Test failed' })
    } finally {
      setTesting(false)
    }
  }

  const setMode = async (next: 'off' | 'calibredb' | 'plugin') => {
    setSettings(s => ({ ...s, 'calibre.mode': next }))
    await api.setSetting('calibre.mode', next).catch(console.error)
  }

  return (
    <section>
      <h3 className="text-base font-semibold mb-3 text-slate-800 dark:text-zinc-200">Calibre</h3>
      <div className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900 space-y-4">

        {/* Shared library path — used by both write integration and library import */}
        <div>
          <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">Library path</label>
          <p className="text-xs text-slate-600 dark:text-zinc-500 mb-2">
            Directory containing <code className="text-[11px] bg-slate-200 dark:bg-zinc-800 px-1 rounded">metadata.db</code>.
            Used by both the write integration and library import.
          </p>
          <div className="flex gap-2">
            <input
              value={settings['calibre.library_path'] ?? ''}
              onChange={e => setSettings(s => ({ ...s, 'calibre.library_path': e.target.value }))}
              placeholder="/data/calibre-library"
              className="flex-1 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600"
            />
            <button
              onClick={() => saveSettingWithError('calibre.library_path')}
              disabled={saving === 'calibre.library_path'}
              className="px-3 py-2 bg-emerald-600 hover:bg-emerald-500 rounded text-xs font-medium disabled:opacity-50"
            >
              {saving === 'calibre.library_path' ? 'Saving...' : 'Save'}
            </button>
          </div>
          {saveError?.key === 'calibre.library_path' && (
            <p className="text-xs text-red-600 dark:text-red-400 mt-1">{saveError.msg}</p>
          )}
        </div>

        <div className="pt-1 border-t border-slate-200 dark:border-zinc-800">
          <label className="block text-sm font-medium text-slate-800 dark:text-zinc-200 mb-2 mt-3">Write integration</label>
          <div className="space-y-1.5">
            {([
              { v: 'off',       label: 'Off',           desc: 'No Calibre call on import.' },
              { v: 'calibredb', label: 'calibredb CLI', desc: 'Shell out to calibredb add --with-library. Requires calibredb reachable from the Bindery process.' },
              { v: 'plugin',    label: 'Calibre Bridge plugin', desc: 'POST imported files to the Bindery Bridge plugin running inside Calibre. Use when Calibre runs in a separate container/pod.' },
            ] as const).map(opt => (
              <label key={opt.v} className="flex items-start gap-2 cursor-pointer">
                <input
                  type="radio"
                  name="calibre-mode"
                  value={opt.v}
                  checked={mode === opt.v}
                  onChange={() => setMode(opt.v)}
                  className="mt-1"
                />
                <div>
                  <div className="text-sm text-slate-800 dark:text-zinc-200">{opt.label}</div>
                  <div className="text-xs text-slate-600 dark:text-zinc-500">{opt.desc}</div>
                </div>
              </label>
            ))}
          </div>
        </div>

        {mode === 'calibredb' && (
          <div>
            <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">Binary path (optional)</label>
            <p className="text-xs text-slate-600 dark:text-zinc-500 mb-2">Leave blank to resolve <code className="text-[11px] bg-slate-200 dark:bg-zinc-800 px-1 rounded">calibredb</code> on PATH. Set explicitly when running in a container that bundles Calibre at a pinned location.</p>
            <div className="flex gap-2">
              <input
                value={settings['calibre.binary_path'] ?? ''}
                onChange={e => setSettings(s => ({ ...s, 'calibre.binary_path': e.target.value }))}
                placeholder="/usr/bin/calibredb"
                className="flex-1 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600"
              />
              <button
                onClick={() => saveSettingWithError('calibre.binary_path')}
                disabled={saving === 'calibre.binary_path'}
                className="px-3 py-2 bg-emerald-600 hover:bg-emerald-500 rounded text-xs font-medium disabled:opacity-50"
              >
                {saving === 'calibre.binary_path' ? 'Saving...' : 'Save'}
              </button>
            </div>
            {saveError?.key === 'calibre.binary_path' && (
              <p className="text-xs text-red-600 dark:text-red-400 mt-1">{saveError.msg}</p>
            )}
          </div>
        )}

        {mode === 'plugin' && (
          <div>
            <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">Plugin URL</label>
            <p className="text-xs text-slate-600 dark:text-zinc-500 mb-2">
              Base URL of the Bindery Bridge plugin&rsquo;s HTTP server running inside Calibre.
            </p>
            <div className="flex gap-2">
              <input
                value={settings['calibre.plugin_url'] ?? ''}
                onChange={e => setSettings(s => ({ ...s, 'calibre.plugin_url': e.target.value }))}
                placeholder="http://calibre.default.svc:8099"
                className="flex-1 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600"
              />
              <button
                onClick={() => saveSettingWithError('calibre.plugin_url')}
                disabled={saving === 'calibre.plugin_url'}
                className="px-3 py-2 bg-emerald-600 hover:bg-emerald-500 rounded text-xs font-medium disabled:opacity-50"
              >
                {saving === 'calibre.plugin_url' ? 'Saving...' : 'Save'}
              </button>
            </div>
            {saveError?.key === 'calibre.plugin_url' && (
              <p className="text-xs text-red-600 dark:text-red-400 mt-1">{saveError.msg}</p>
            )}
          </div>
        )}

        {mode === 'plugin' && (
          <div>
            <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">API key</label>
            <p className="text-xs text-slate-600 dark:text-zinc-500 mb-2">
              Bearer token configured in the plugin&rsquo;s Calibre Preferences dialog.
            </p>
            <div className="flex gap-2">
              <input
                type="password"
                value={settings['calibre.plugin_api_key'] ?? ''}
                onChange={e => setSettings(s => ({ ...s, 'calibre.plugin_api_key': e.target.value }))}
                placeholder="plugin api key"
                className="flex-1 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600"
              />
              <button
                onClick={() => saveSettingWithError('calibre.plugin_api_key')}
                disabled={saving === 'calibre.plugin_api_key'}
                className="px-3 py-2 bg-emerald-600 hover:bg-emerald-500 rounded text-xs font-medium disabled:opacity-50"
              >
                {saving === 'calibre.plugin_api_key' ? 'Saving...' : 'Save'}
              </button>
            </div>
            {saveError?.key === 'calibre.plugin_api_key' && (
              <p className="text-xs text-red-600 dark:text-red-400 mt-1">{saveError.msg}</p>
            )}
          </div>
        )}

        {(mode === 'calibredb' || mode === 'plugin') && (
          <div className="flex items-center justify-between pt-1 border-t border-slate-200 dark:border-zinc-800">
            <div className="text-xs">
              {testResult && (
                <span className={testResult.ok ? 'text-emerald-600 dark:text-emerald-400' : 'text-red-600 dark:text-red-400'}>
                  {testResult.msg}
                </span>
              )}
            </div>
            <button
              onClick={runTest}
              disabled={testing}
              className="px-4 py-2 bg-slate-600 hover:bg-slate-500 rounded text-sm font-medium disabled:opacity-50 flex-shrink-0"
            >
              {testing ? 'Testing…' : 'Test connection'}
            </button>
          </div>
        )}

        {/* Library import (read side): Calibre → Bindery */}
        <div className="pt-3 border-t border-slate-200 dark:border-zinc-800 space-y-3">
          <div className="flex items-center justify-between">
            <div>
              <label className="block text-sm font-medium text-slate-800 dark:text-zinc-200">Library import</label>
              <p className="text-xs text-slate-600 dark:text-zinc-500 mt-0.5">
                Read your existing Calibre library and import books, authors, and editions into Bindery.
                Works independently of the write mode above.
              </p>
            </div>
            <button
              onClick={async () => {
                const next = libraryImportEnabled ? 'false' : 'true'
                setSettings(s => ({ ...s, 'calibre.library_import_enabled': next }))
                await api.setSetting('calibre.library_import_enabled', next).catch(console.error)
              }}
              className={`relative w-9 h-5 rounded-full transition-colors flex-shrink-0 ml-4 ${libraryImportEnabled ? 'bg-emerald-600' : 'bg-slate-300 dark:bg-zinc-700'}`}
              title={libraryImportEnabled ? 'Disable library import' : 'Enable library import'}
            >
              <span className={`absolute top-0.5 left-0.5 w-4 h-4 bg-white rounded-full transition-transform ${libraryImportEnabled ? 'translate-x-4' : ''}`} />
            </button>
          </div>

          {libraryImportEnabled && (
            <>
              <div className="flex items-center justify-between">
                <div>
                  <label className="block text-sm font-medium text-slate-800 dark:text-zinc-200">Sync on startup</label>
                  <p className="text-xs text-slate-600 dark:text-zinc-500 mt-0.5">Re-import each time Bindery starts. Safe to leave on — imports are incremental and idempotent.</p>
                </div>
                <button
                  onClick={async () => {
                    const next = syncOnStartup ? 'false' : 'true'
                    setSettings(s => ({ ...s, 'calibre.sync_on_startup': next }))
                    await api.setSetting('calibre.sync_on_startup', next).catch(console.error)
                  }}
                  className={`relative w-9 h-5 rounded-full transition-colors flex-shrink-0 ${syncOnStartup ? 'bg-emerald-600' : 'bg-slate-300 dark:bg-zinc-700'}`}
                  title={syncOnStartup ? 'Disable' : 'Enable'}
                >
                  <span className={`absolute top-0.5 left-0.5 w-4 h-4 bg-white rounded-full transition-transform ${syncOnStartup ? 'translate-x-4' : ''}`} />
                </button>
              </div>

              <div className="flex items-center justify-between">
                <div className="text-xs text-slate-600 dark:text-zinc-500">
                  {lastImportAt ? (
                    <>Last import: <span className="text-slate-800 dark:text-zinc-200">{new Date(lastImportAt).toLocaleString()}</span></>
                  ) : (
                    <>Never imported.</>
                  )}
                </div>
                <button
                  onClick={startImport}
                  disabled={importProgress?.running || !settings['calibre.library_path']}
                  className="px-4 py-2 bg-sky-600 hover:bg-sky-500 rounded text-sm font-medium disabled:opacity-50 flex-shrink-0"
                >
                  {importProgress?.running ? 'Importing…' : 'Import library'}
                </button>
              </div>

              {importError && (
                <p className="text-xs text-red-600 dark:text-red-400">{importError}</p>
              )}

              {importProgress && (importProgress.running || importProgress.stats || importProgress.error) && (
                <div className="rounded border border-slate-200 dark:border-zinc-800 bg-slate-50 dark:bg-zinc-950 px-3 py-2 space-y-2">
                  {importProgress.running && (
                    <>
                      <div className="flex justify-between text-xs text-slate-600 dark:text-zinc-400">
                        <span>{importProgress.message || 'Working…'}</span>
                        <span>{importProgress.processed} / {importProgress.total || '?'}</span>
                      </div>
                      <div className="h-1.5 bg-slate-200 dark:bg-zinc-800 rounded overflow-hidden">
                        <div
                          className="h-full bg-sky-600 transition-[width] duration-300"
                          style={{
                            width: importProgress.total > 0
                              ? `${Math.min(100, (importProgress.processed / importProgress.total) * 100)}%`
                              : '0%',
                          }}
                        />
                      </div>
                    </>
                  )}
                  {!importProgress.running && importProgress.error && (
                    <p className="text-xs text-red-600 dark:text-red-400">Import failed: {importProgress.error}</p>
                  )}
                  {!importProgress.running && importProgress.stats && (
                    <p className="text-xs text-slate-700 dark:text-zinc-300">
                      Import complete —{' '}
                      <span className="font-medium">{importProgress.stats.authorsAdded}</span> authors added,{' '}
                      <span className="font-medium">{importProgress.stats.booksAdded}</span> books added,{' '}
                      <span className="font-medium">{importProgress.stats.editionsAdded}</span> editions added,{' '}
                      <span className="font-medium">{importProgress.stats.duplicatesMerged}</span> merged,{' '}
                      <span className="font-medium">{importProgress.stats.skipped}</span> skipped.
                    </p>
                  )}
                </div>
              )}
            </>
          )}
        </div>
      </div>
    </section>
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

function EditClientForm({ client, onClose, onSaved }: { client: DownloadClient; onClose: () => void; onSaved: (c: DownloadClient) => void }) {
  const [name, setName] = useState(client.name)
  const [type, setType] = useState(client.type || 'sabnzbd')
  const [host, setHost] = useState(client.host)
  const [port, setPort] = useState(String(client.port))
  const [credential, setCredential] = useState(client.type === 'qbittorrent' || client.type === 'transmission' ? (client.password || '') : (client.apiKey || ''))
  const [username, setUsername] = useState(client.username || '')
  const [category, setCategory] = useState(client.category)
  const labelCls = 'block text-xs text-slate-600 dark:text-zinc-400 mb-1'

  const handleTypeChange = (newType: string) => {
    setType(newType)
    setCredential('')
    setUsername('')
  }

  const submit = async () => {
    const data = type === 'qbittorrent' || type === 'transmission'
      ? { ...client, name, type, host, port: parseInt(port), username, password: credential, apiKey: '', category }
      : { ...client, name, type, host, port: parseInt(port), apiKey: credential, username: '', password: '', category }
    const updated = await api.updateDownloadClient(client.id, data)
    onSaved(updated)
  }

  return (
    <div className="mt-1 p-4 border border-slate-300 dark:border-zinc-700 rounded-lg bg-slate-200/50 dark:bg-zinc-800/50 space-y-3">
      <div className="flex gap-2">
        <div className="flex-1">
          <label className={labelCls}>Name</label>
          <input value={name} onChange={e => setName(e.target.value)} placeholder="Name" className="w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600" />
        </div>
        <div className="w-40">
          <label className={labelCls}>Client Type</label>
          <select value={type} onChange={e => handleTypeChange(e.target.value)} className="w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600">
            <option value="sabnzbd">SABnzbd</option>
            <option value="qbittorrent">qBittorrent</option>
            <option value="transmission">Transmission</option>
          </select>
        </div>
      </div>
      <div>
        <label className={labelCls}>Connection</label>
        <div className="flex gap-2">
          <input value={host} onChange={e => setHost(e.target.value)} placeholder="Host" className="flex-1 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600" />
          <input value={port} onChange={e => setPort(e.target.value)} placeholder="Port" className="w-24 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600" />
        </div>
        <p className="text-xs text-slate-500 dark:text-zinc-500 mt-1">In Docker, use the service/container name (e.g. <code className="font-mono">sabnzbd</code>) — not <code className="font-mono">localhost</code>.</p>
      </div>
      {(type === 'qbittorrent' || type === 'transmission') && (
        <div>
          <label className={labelCls}>Username</label>
          <input value={username} onChange={e => setUsername(e.target.value)} placeholder="Username" className={inputCls} />
        </div>
      )}
      <div>
        <label className={labelCls}>{type === 'qbittorrent' || type === 'transmission' ? 'Password' : 'API Key'}</label>
        <input value={credential} onChange={e => setCredential(e.target.value)} placeholder={type === 'qbittorrent' || type === 'transmission' ? 'Password' : 'API Key'} type="password" className={inputCls} />
      </div>
      <div>
        <label className={labelCls}>{type === 'transmission' ? 'Download Directory' : 'Category'}</label>
        <input value={category} onChange={e => setCategory(e.target.value)} placeholder={type === 'transmission' ? '/downloads (leave blank for default)' : 'Category'} className={inputCls} />
        {type === 'transmission' && <p className="text-xs text-slate-500 dark:text-zinc-500 mt-1">Optional absolute path override. Leave blank to use Transmission's configured default download directory.</p>}
      </div>
      <div className="flex gap-2 justify-end">
        <button onClick={onClose} className="px-3 py-1.5 text-sm text-slate-600 dark:text-zinc-400">Cancel</button>
        <button onClick={submit} className="px-3 py-1.5 bg-emerald-600 hover:bg-emerald-500 rounded text-sm font-medium">Save</button>
      </div>
    </div>
  )
}

function EditNotificationForm({ notification, onClose, onSaved }: { notification: NotificationConfig; onClose: () => void; onSaved: (n: NotificationConfig) => void }) {
  const [name, setName] = useState(notification.name)
  const [url, setUrl] = useState(notification.url)
  const [method, setMethod] = useState(notification.method || 'POST')
  const [onGrab, setOnGrab] = useState(notification.onGrab)
  const [onImport, setOnImport] = useState(notification.onImport)
  const [onFailure, setOnFailure] = useState(notification.onFailure)
  const [onUpgrade, setOnUpgrade] = useState(notification.onUpgrade)
  const [onHealth, setOnHealth] = useState(notification.onHealth)

  const submit = async () => {
    const updated = await api.updateNotification(notification.id, { ...notification, name, url, method, onGrab, onImport, onFailure, onUpgrade, onHealth })
    onSaved(updated)
  }

  const toggleCls = (active: boolean) =>
    `px-3 py-1.5 rounded text-xs font-medium border transition-colors cursor-pointer select-none ${
      active
        ? 'bg-emerald-500/20 border-emerald-500/40 text-emerald-400'
        : 'bg-slate-200 dark:bg-zinc-800 border-slate-300 dark:border-zinc-700 text-slate-600 dark:text-zinc-400'
    }`

  return (
    <div className="mt-1 p-4 border border-slate-300 dark:border-zinc-700 rounded-lg bg-slate-200/50 dark:bg-zinc-800/50 space-y-4">
      <div className="grid grid-cols-2 gap-3">
        <input value={name} onChange={e => setName(e.target.value)} placeholder="Name" className={inputCls} />
        <select value={method} onChange={e => setMethod(e.target.value)} className={inputCls}>
          <option value="POST">POST</option>
          <option value="PUT">PUT</option>
          <option value="GET">GET</option>
        </select>
      </div>
      <input value={url} onChange={e => setUrl(e.target.value)} placeholder="Webhook URL" className={inputCls} />
      <div>
        <p className="text-xs text-slate-600 dark:text-zinc-400 mb-2">Trigger on:</p>
        <div className="flex flex-wrap gap-2">
          <button type="button" onClick={() => setOnGrab(!onGrab)} className={toggleCls(onGrab)}>Grab</button>
          <button type="button" onClick={() => setOnImport(!onImport)} className={toggleCls(onImport)}>Import</button>
          <button type="button" onClick={() => setOnFailure(!onFailure)} className={toggleCls(onFailure)}>Failure</button>
          <button type="button" onClick={() => setOnUpgrade(!onUpgrade)} className={toggleCls(onUpgrade)}>Upgrade</button>
          <button type="button" onClick={() => setOnHealth(!onHealth)} className={toggleCls(onHealth)}>Health</button>
        </div>
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

function AddClientForm({ onClose, onAdded }: { onClose: () => void; onAdded: (c: DownloadClient) => void }) {
  const [name, setName] = useState('SABnzbd')
  const [type, setType] = useState<'sabnzbd' | 'qbittorrent' | 'transmission'>('sabnzbd')
  const [host, setHost] = useState('')
  const [port, setPort] = useState('8080')
  const [credential, setCredential] = useState('')
  const [username, setUsername] = useState('')
  const [category, setCategory] = useState('books')
  const labelCls = 'block text-xs text-slate-600 dark:text-zinc-400 mb-1'

  const handleTypeChange = (newType: 'sabnzbd' | 'qbittorrent' | 'transmission') => {
    setType(newType)
    setCredential('')
    setUsername('')
    if (newType === 'qbittorrent') {
      setName('qBittorrent')
      setPort('8080')
      return
    }
    if (newType === 'transmission') {
      setName('Transmission')
      setPort('9091')
      return
    }
    setName('SABnzbd')
    setPort('8080')
  }

  const submit = async () => {
    const data = type === 'qbittorrent' || type === 'transmission'
      ? { name, host, port: parseInt(port), username, password: credential, apiKey: '', category, type, enabled: true }
      : { name, host, port: parseInt(port), apiKey: credential, username: '', password: '', category, type, enabled: true }
    const c = await api.addDownloadClient(data)
    onAdded(c)
  }

  return (
    <div className="mt-4 p-4 border border-slate-300 dark:border-zinc-700 rounded-lg bg-slate-200/50 dark:bg-zinc-800/50 space-y-3">
      <div className="flex gap-2">
        <div className="flex-1">
          <label className={labelCls}>Name</label>
          <input value={name} onChange={e => setName(e.target.value)} placeholder="Name" className="w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600" />
        </div>
        <div className="w-40">
          <label className={labelCls}>Client Type</label>
          <select value={type} onChange={e => handleTypeChange(e.target.value as 'sabnzbd' | 'qbittorrent' | 'transmission')} className="w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600">
            <option value="sabnzbd">SABnzbd</option>
            <option value="qbittorrent">qBittorrent</option>
            <option value="transmission">Transmission</option>
          </select>
        </div>
      </div>
      <div>
        <label className={labelCls}>Connection</label>
        <div className="flex gap-2">
          <input value={host} onChange={e => setHost(e.target.value)} placeholder="Host" className="flex-1 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600" />
          <input value={port} onChange={e => setPort(e.target.value)} placeholder="Port" className="w-24 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600" />
        </div>
        <p className="text-xs text-slate-500 dark:text-zinc-500 mt-1">In Docker, use the service/container name (e.g. <code className="font-mono">sabnzbd</code>) — not <code className="font-mono">localhost</code>.</p>
      </div>
      {(type === 'qbittorrent' || type === 'transmission') && (
        <div>
          <label className={labelCls}>Username</label>
          <input value={username} onChange={e => setUsername(e.target.value)} placeholder="Username" className={inputCls} />
        </div>
      )}
      <div>
        <label className={labelCls}>{type === 'qbittorrent' || type === 'transmission' ? 'Password' : 'API Key'}</label>
        <input value={credential} onChange={e => setCredential(e.target.value)} placeholder={type === 'qbittorrent' || type === 'transmission' ? 'Password' : 'API Key'} type="password" className={inputCls} />
      </div>
      <div>
        <label className={labelCls}>{type === 'transmission' ? 'Download Directory' : 'Category'}</label>
        <input value={category} onChange={e => setCategory(e.target.value)} placeholder={type === 'transmission' ? '/downloads (leave blank for default)' : 'Category'} className={inputCls} />
        {type === 'transmission' && <p className="text-xs text-slate-500 dark:text-zinc-500 mt-1">Optional absolute path override. Leave blank to use Transmission's configured default download directory.</p>}
      </div>
      <div className="flex gap-2 justify-end">
        <button onClick={onClose} className="px-3 py-1.5 text-sm text-slate-600 dark:text-zinc-400">Cancel</button>
        <button onClick={submit} className="px-3 py-1.5 bg-emerald-600 hover:bg-emerald-500 rounded text-sm font-medium">Save</button>
      </div>
    </div>
  )
}

function AddNotificationForm({ onClose, onAdded }: { onClose: () => void; onAdded: (n: NotificationConfig) => void }) {
  const [name, setName] = useState('')
  const [url, setUrl] = useState('')
  const [method, setMethod] = useState('POST')
  const [onGrab, setOnGrab] = useState(true)
  const [onImport, setOnImport] = useState(true)
  const [onFailure, setOnFailure] = useState(true)
  const [onUpgrade, setOnUpgrade] = useState(false)
  const [onHealth, setOnHealth] = useState(false)

  const submit = async () => {
    const n = await api.addNotification({
      name, url, method, type: 'webhook',
      headers: '{}',
      onGrab, onImport, onFailure, onUpgrade, onHealth,
      enabled: true,
    })
    onAdded(n)
  }

  const toggleCls = (active: boolean) =>
    `px-3 py-1.5 rounded text-xs font-medium border transition-colors cursor-pointer select-none ${
      active
        ? 'bg-emerald-500/20 border-emerald-500/40 text-emerald-400'
        : 'bg-slate-200 dark:bg-zinc-800 border-slate-300 dark:border-zinc-700 text-slate-600 dark:text-zinc-400'
    }`

  return (
    <div className="mt-4 p-4 border border-slate-300 dark:border-zinc-700 rounded-lg bg-slate-200/50 dark:bg-zinc-800/50 space-y-4">
      <div className="grid grid-cols-2 gap-3">
        <input value={name} onChange={e => setName(e.target.value)} placeholder="Name" className={inputCls} />
        <select value={method} onChange={e => setMethod(e.target.value)} className={inputCls}>
          <option value="POST">POST</option>
          <option value="PUT">PUT</option>
          <option value="GET">GET</option>
        </select>
      </div>
      <input value={url} onChange={e => setUrl(e.target.value)} placeholder="Webhook URL" className={inputCls} />
      <div>
        <p className="text-xs text-slate-600 dark:text-zinc-400 mb-2">Trigger on:</p>
        <div className="flex flex-wrap gap-2">
          <button type="button" onClick={() => setOnGrab(!onGrab)} className={toggleCls(onGrab)}>Grab</button>
          <button type="button" onClick={() => setOnImport(!onImport)} className={toggleCls(onImport)}>Import</button>
          <button type="button" onClick={() => setOnFailure(!onFailure)} className={toggleCls(onFailure)}>Failure</button>
          <button type="button" onClick={() => setOnUpgrade(!onUpgrade)} className={toggleCls(onUpgrade)}>Upgrade</button>
          <button type="button" onClick={() => setOnHealth(!onHealth)} className={toggleCls(onHealth)}>Health</button>
        </div>
      </div>
      <div className="flex gap-2 justify-end">
        <button onClick={onClose} className="px-3 py-1.5 text-sm text-slate-600 dark:text-zinc-400">Cancel</button>
        <button onClick={submit} className="px-3 py-1.5 bg-emerald-600 hover:bg-emerald-500 rounded text-sm font-medium">Save</button>
      </div>
    </div>
  )
}

function SecuritySection() {
  const { status, refresh } = useAuth()
  const [cfg, setCfg] = useState<AuthConfig | null>(null)
  const [showKey, setShowKey] = useState(false)
  const [regenerating, setRegenerating] = useState(false)
  const [savingMode, setSavingMode] = useState(false)
  const [copied, setCopied] = useState(false)

  useEffect(() => { loadCfg() }, [])

  const loadCfg = () => {
    api.authConfig().then(setCfg).catch(console.error)
  }

  const regenerate = async () => {
    if (!confirm('Regenerate the API key? Existing integrations using the old key will stop working.')) return
    setRegenerating(true)
    try {
      const r = await api.authRegenerateApiKey()
      setCfg(c => c ? { ...c, apiKey: r.apiKey } : c)
      setShowKey(true)
    } catch (e) {
      alert('Regenerate failed: ' + (e instanceof Error ? e.message : 'unknown'))
    } finally {
      setRegenerating(false)
    }
  }

  const setMode = async (mode: AuthStatus['mode']) => {
    setSavingMode(true)
    try {
      await api.authSetMode(mode)
      await refresh()
      loadCfg()
    } catch (e) {
      alert('Mode change failed: ' + (e instanceof Error ? e.message : 'unknown'))
    } finally {
      setSavingMode(false)
    }
  }

  const copyKey = async () => {
    if (!cfg?.apiKey) return
    try {
      await navigator.clipboard.writeText(cfg.apiKey)
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    } catch { /* clipboard blocked */ }
  }

  if (!cfg) return null

  return (
    <section>
      <h3 className="text-base font-semibold mb-3 text-slate-800 dark:text-zinc-200">Security</h3>
      <div className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900 space-y-5">
        <div>
          <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">Authentication Mode</label>
          <p className="text-xs text-slate-600 dark:text-zinc-500 mb-2">
            <strong>Enabled</strong>: always require login. <strong>Local only</strong>: skip login for requests from private IPs (home network). <strong>Disabled</strong>: no authentication — only safe behind a trusted reverse proxy.
          </p>
          <select
            value={cfg.mode}
            onChange={e => setMode(e.target.value as AuthStatus['mode'])}
            disabled={savingMode}
            className="w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600"
          >
            <option value="enabled">Enabled</option>
            <option value="local-only">Local only (bypass for private IPs)</option>
            <option value="disabled">Disabled (no auth)</option>
          </select>
        </div>

        <div className="border-t border-slate-200 dark:border-zinc-800 pt-4">
          <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">API Key</label>
          <p className="text-xs text-slate-600 dark:text-zinc-500 mb-2">
            Pass as <code className="font-mono">X-Api-Key</code> header or <code className="font-mono">?apikey=</code> query parameter. Used by external integrations (Tautulli, custom scripts, etc.).
          </p>
          <div className="flex gap-2">
            <code className="flex-1 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm font-mono text-slate-700 dark:text-zinc-300 truncate">
              {showKey ? cfg.apiKey : '••••••••••••••••••••••••••••••••'}
            </code>
            <button onClick={() => setShowKey(s => !s)} className="px-3 py-2 bg-slate-200 dark:bg-zinc-800 hover:bg-slate-300 dark:hover:bg-zinc-700 rounded text-xs font-medium">
              {showKey ? 'Hide' : 'Show'}
            </button>
            <button onClick={copyKey} className="px-3 py-2 bg-slate-200 dark:bg-zinc-800 hover:bg-slate-300 dark:hover:bg-zinc-700 rounded text-xs font-medium">
              {copied ? 'Copied' : 'Copy'}
            </button>
            <button onClick={regenerate} disabled={regenerating} className="px-3 py-2 bg-amber-600 hover:bg-amber-500 rounded text-xs font-medium disabled:opacity-50">
              {regenerating ? '...' : 'Regenerate'}
            </button>
          </div>
        </div>

        {status?.authenticated && (
          <div className="border-t border-slate-200 dark:border-zinc-800 pt-4">
            <ChangePasswordForm username={cfg.username} />
          </div>
        )}
      </div>
    </section>
  )
}

function formatBlocklistDate(s: string) {
  return new Date(s).toLocaleString(undefined, {
    year: 'numeric', month: 'short', day: 'numeric',
    hour: '2-digit', minute: '2-digit',
  })
}

function BlocklistTab() {
  const { t } = useTranslation()
  const [entries, setEntries] = useState<BlocklistEntry[]>([])
  const [loading, setLoading] = useState(true)
  const [selected, setSelected] = useState<Set<number>>(new Set())
  const [deleting, setDeleting] = useState(false)

  const load = () => {
    setLoading(true)
    api.listBlocklist().then(setEntries).catch(console.error).finally(() => setLoading(false))
  }

  useEffect(() => { load() }, [])

  const handleDelete = async (id: number) => {
    await api.deleteBlocklistEntry(id).catch(console.error)
    setEntries(prev => prev.filter(e => e.id !== id))
    setSelected(prev => { const s = new Set(prev); s.delete(id); return s })
  }

  const handleBulkDelete = async () => {
    if (selected.size === 0) return
    if (!confirm(t('blocklist.deleteConfirm', { count: selected.size }))) return
    setDeleting(true)
    try {
      await api.bulkDeleteBlocklist(Array.from(selected))
      setEntries(prev => prev.filter(e => !selected.has(e.id)))
      setSelected(new Set())
    } catch (err) {
      console.error(err)
    } finally {
      setDeleting(false)
    }
  }

  const toggleSelect = (id: number) => {
    setSelected(prev => {
      const s = new Set(prev)
      if (s.has(id)) s.delete(id)
      else s.add(id)
      return s
    })
  }

  const toggleAll = () => {
    if (selected.size === entries.length) {
      setSelected(new Set())
    } else {
      setSelected(new Set(entries.map(e => e.id)))
    }
  }

  const allSelected = entries.length > 0 && selected.size === entries.length

  const { pageItems, paginationProps } = usePagination(entries, 50, 'blocklist')

  return (
    <div>
      <div className="flex flex-wrap items-center justify-between gap-3 mb-6">
        <h3 className="text-lg font-semibold">{t('blocklist.title')}</h3>
        <div className="flex items-center gap-3">
          {selected.size > 0 && (
            <button
              onClick={handleBulkDelete}
              disabled={deleting}
              className="px-3 py-1.5 bg-red-600 hover:bg-red-500 rounded text-xs font-medium transition-colors disabled:opacity-50"
            >
              {deleting ? t('blocklist.deleting') : t('blocklist.deleteSelected', { count: selected.size })}
            </button>
          )}
          <span className="text-sm text-slate-600 dark:text-zinc-500">{t('blocklist.entries', { count: entries.length })}</span>
        </div>
      </div>

      {loading ? (
        <div className="text-slate-600 dark:text-zinc-500">{t('common.loading')}</div>
      ) : entries.length === 0 ? (
        <div className="text-center py-16 text-slate-600 dark:text-zinc-500">
          <p className="text-lg mb-2">{t('blocklist.empty')}</p>
          <p className="text-sm">{t('blocklist.emptyHint')}</p>
        </div>
      ) : (
        <>
          {/* Desktop table */}
          <div className="hidden sm:block border border-slate-200 dark:border-zinc-800 rounded-lg overflow-hidden">
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead>
                  <tr className="bg-slate-100 dark:bg-zinc-900 border-b border-slate-200 dark:border-zinc-800">
                    <th className="px-4 py-3 w-10">
                      <input
                        type="checkbox"
                        checked={allSelected}
                        onChange={toggleAll}
                        className="accent-emerald-500"
                      />
                    </th>
                    <th className="text-left px-4 py-3 text-xs font-medium text-slate-600 dark:text-zinc-400 uppercase tracking-wider">{t('blocklist.colTitle')}</th>
                    <th className="text-left px-4 py-3 text-xs font-medium text-slate-600 dark:text-zinc-400 uppercase tracking-wider">{t('blocklist.colReason')}</th>
                    <th className="text-left px-4 py-3 text-xs font-medium text-slate-600 dark:text-zinc-400 uppercase tracking-wider">{t('blocklist.colDate')}</th>
                    <th className="px-4 py-3" />
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-200 dark:divide-zinc-800">
                  {pageItems.map(entry => (
                    <tr key={entry.id} className={`transition-colors hover:bg-slate-200/50 dark:hover:bg-zinc-800/50 ${selected.has(entry.id) ? 'bg-slate-200/30 dark:bg-zinc-800/30' : 'bg-slate-100/50 dark:bg-zinc-900/50'}`}>
                      <td className="px-4 py-3">
                        <input
                          type="checkbox"
                          checked={selected.has(entry.id)}
                          onChange={() => toggleSelect(entry.id)}
                          className="accent-emerald-500"
                        />
                      </td>
                      <td className="px-4 py-3 max-w-xs">
                        <p className="text-slate-800 dark:text-zinc-200 truncate" title={entry.title}>{entry.title}</p>
                        {entry.guid && (
                          <p className="text-[10px] text-slate-500 dark:text-zinc-600 mt-0.5 font-mono truncate">{entry.guid}</p>
                        )}
                      </td>
                      <td className="px-4 py-3">
                        <span className="text-xs px-2 py-0.5 rounded bg-red-500/20 text-red-400">
                          {entry.reason || 'Unknown'}
                        </span>
                      </td>
                      <td className="px-4 py-3 text-slate-600 dark:text-zinc-400 whitespace-nowrap text-xs">
                        {formatBlocklistDate(entry.createdAt)}
                      </td>
                      <td className="px-4 py-3 text-right">
                        <button
                          onClick={() => handleDelete(entry.id)}
                          className="text-xs text-red-400 hover:text-red-300 transition-colors"
                        >
                          {t('common.delete')}
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </div>

          {/* Mobile card list */}
          <div className="sm:hidden space-y-2">
            <div className="flex items-center gap-2 mb-2">
              <input
                type="checkbox"
                checked={allSelected}
                onChange={toggleAll}
                className="accent-emerald-500"
              />
              <span className="text-xs text-slate-600 dark:text-zinc-500">{t('blocklist.selectAll')}</span>
            </div>
            {pageItems.map(entry => (
              <div
                key={entry.id}
                className={`border border-slate-200 dark:border-zinc-800 rounded-lg p-3 transition-colors ${selected.has(entry.id) ? 'bg-slate-200/30 dark:bg-zinc-800/30' : 'bg-slate-100/50 dark:bg-zinc-900/50'}`}
              >
                <div className="flex items-start gap-3">
                  <input
                    type="checkbox"
                    checked={selected.has(entry.id)}
                    onChange={() => toggleSelect(entry.id)}
                    className="accent-emerald-500 mt-0.5 flex-shrink-0"
                  />
                  <div className="min-w-0 flex-1">
                    <p className="text-sm text-slate-800 dark:text-zinc-200 break-words">{entry.title}</p>
                    {entry.guid && (
                      <p className="text-[10px] text-slate-500 dark:text-zinc-600 mt-0.5 font-mono truncate">{entry.guid}</p>
                    )}
                    <div className="flex flex-wrap items-center gap-2 mt-2">
                      <span className="text-xs px-2 py-0.5 rounded bg-red-500/20 text-red-400">
                        {entry.reason || 'Unknown'}
                      </span>
                      <span className="text-[10px] text-slate-600 dark:text-zinc-500">{formatBlocklistDate(entry.createdAt)}</span>
                    </div>
                  </div>
                  <button
                    onClick={() => handleDelete(entry.id)}
                    className="text-xs text-red-400 hover:text-red-300 transition-colors flex-shrink-0 py-1 px-2"
                  >
                    {t('common.delete')}
                  </button>
                </div>
              </div>
            ))}
          </div>
        </>
      )}
      <Pagination {...paginationProps} />
    </div>
  )
}

function ChangePasswordForm({ username }: { username: string }) {
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [confirmPw, setConfirmPw] = useState('')
  const [error, setError] = useState('')
  const [success, setSuccess] = useState(false)
  const [submitting, setSubmitting] = useState(false)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setError('')
    setSuccess(false)
    if (next !== confirmPw) { setError('New passwords do not match'); return }
    if (next.length < 8) { setError('Password must be at least 8 characters'); return }
    setSubmitting(true)
    try {
      await api.authChangePassword(current, next)
      setCurrent(''); setNext(''); setConfirmPw('')
      setSuccess(true)
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : 'Change failed')
    } finally {
      setSubmitting(false)
    }
  }

  const inputCls = 'w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600'

  return (
    <form onSubmit={submit} className="space-y-2">
      <label className="block text-xs text-slate-600 dark:text-zinc-400">Change password for <strong>{username}</strong></label>
      <input type="password" autoComplete="current-password" placeholder="Current password" value={current} onChange={e => setCurrent(e.target.value)} className={inputCls} />
      <input type="password" autoComplete="new-password" placeholder="New password" value={next} onChange={e => setNext(e.target.value)} className={inputCls} />
      <input type="password" autoComplete="new-password" placeholder="Confirm new password" value={confirmPw} onChange={e => setConfirmPw(e.target.value)} className={inputCls} />
      {error && <div className="text-xs text-red-600 dark:text-red-400">{error}</div>}
      {success && <div className="text-xs text-emerald-600 dark:text-emerald-400">Password updated</div>}
      <button type="submit" disabled={submitting || !current || !next} className="px-3 py-2 bg-emerald-600 hover:bg-emerald-500 rounded text-xs font-medium disabled:opacity-50">
        {submitting ? 'Updating…' : 'Change password'}
      </button>
    </form>
  )
}

function AddProwlarrForm({ onClose, onAdded }: { onClose: () => void; onAdded: (p: ProwlarrInstance) => void }) {
  const [name, setName] = useState('Prowlarr')
  const [url, setUrl] = useState('')
  const [apiKey, setApiKey] = useState('')
  const [syncOnStartup, setSyncOnStartup] = useState(true)
  const [syncing, setSyncing] = useState(false)
  const labelCls = 'block text-xs text-slate-600 dark:text-zinc-400 mb-1'

  const submit = async () => {
    setSyncing(true)
    try {
      const p = await api.addProwlarr({ name, url, apiKey, syncOnStartup, enabled: true })
      // Auto-sync immediately so the user sees indexers appear right away.
      try {
        await api.syncProwlarr(p.id)
        const updated = await api.listProwlarr()
        onAdded(updated.find(i => i.id === p.id) ?? p)
      } catch {
        onAdded(p)
      }
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
        <button
          type="button"
          onClick={() => setSyncOnStartup(!syncOnStartup)}
          className={`relative w-9 h-5 rounded-full transition-colors flex-shrink-0 ${syncOnStartup ? 'bg-emerald-600' : 'bg-slate-300 dark:bg-zinc-700'}`}
        >
          <span className={`absolute top-0.5 left-0.5 w-4 h-4 bg-white rounded-full transition-transform ${syncOnStartup ? 'translate-x-4' : ''}`} />
        </button>
        <span className="text-xs text-slate-600 dark:text-zinc-400">Sync on startup</span>
      </div>
      <div className="flex gap-2 justify-end">
        <button onClick={onClose} className="px-3 py-1.5 text-sm text-slate-600 dark:text-zinc-400">Cancel</button>
        <button onClick={submit} disabled={!url || !apiKey || syncing} className="px-3 py-1.5 bg-emerald-600 hover:bg-emerald-500 rounded text-sm font-medium disabled:opacity-50">
          {syncing ? 'Saving & syncing…' : 'Save & sync'}
        </button>
      </div>
    </div>
  )
}
