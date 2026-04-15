import { FormEvent, useEffect, useState } from 'react'
import { api, AuthConfig, AuthStatus, Indexer, DownloadClient, NotificationConfig, QualityProfile, MetadataProfile, CalibreImportProgress, RootFolder } from '../api/client'
import ThemeToggle from '../components/ThemeToggle'
import { useAuth } from '../auth/AuthContext'

type Tab = 'indexers' | 'clients' | 'notifications' | 'quality' | 'metadata' | 'general' | 'import' | 'rootfolders'

const inputCls = 'w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600'
const tabCls = (active: boolean) =>
  `px-4 py-2 rounded-md text-sm font-medium transition-colors ${active ? 'bg-slate-200 dark:bg-zinc-800 text-slate-900 dark:text-white' : 'text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white hover:bg-slate-200/50 dark:hover:bg-zinc-800/50'}`

export default function SettingsPage() {
  const [tab, setTab] = useState<Tab>('indexers')
  const [indexers, setIndexers] = useState<Indexer[]>([])
  const [clients, setClients] = useState<DownloadClient[]>([])
  const [notifications, setNotifications] = useState<NotificationConfig[]>([])
  const [qualityProfiles, setQualityProfiles] = useState<QualityProfile[]>([])
  const [metadataProfiles, setMetadataProfiles] = useState<MetadataProfile[]>([])
  const [rootFolders, setRootFolders] = useState<RootFolder[]>([])
  const [newFolderPath, setNewFolderPath] = useState('')
  const [folderError, setFolderError] = useState('')
  const [showAddIndexer, setShowAddIndexer] = useState(false)
  const [showAddClient, setShowAddClient] = useState(false)
  const [showAddNotification, setShowAddNotification] = useState(false)
  const [editingIndexer, setEditingIndexer] = useState<number | null>(null)
  const [editingClient, setEditingClient] = useState<number | null>(null)
  const [editingNotification, setEditingNotification] = useState<number | null>(null)

  useEffect(() => {
    api.listIndexers().then(setIndexers).catch(console.error)
    api.listDownloadClients().then(setClients).catch(console.error)
  }, [])

  useEffect(() => {
    if (tab === 'notifications') api.listNotifications().then(setNotifications).catch(console.error)
    if (tab === 'quality') api.listQualityProfiles().then(setQualityProfiles).catch(console.error)
    if (tab === 'metadata') api.listMetadataProfiles().then(setMetadataProfiles).catch(console.error)
    if (tab === 'rootfolders') api.listRootFolders().then(setRootFolders).catch(console.error)
  }, [tab])

  function formatBytes(bytes: number): string {
    if (bytes === 0) return '0 B'
    const units = ['B', 'KB', 'MB', 'GB', 'TB']
    const i = Math.floor(Math.log(bytes) / Math.log(1024))
    return `${(bytes / Math.pow(1024, i)).toFixed(1)} ${units[i]}`
  }

  return (
    <div>
      <h2 className="text-2xl font-bold mb-6">Settings</h2>

      <div className="flex flex-wrap gap-2 mb-6">
        {(['indexers', 'clients', 'notifications', 'quality', 'metadata', 'import', 'rootfolders', 'general'] as Tab[]).map(t => (
          <button key={t} onClick={() => setTab(t)} className={tabCls(tab === t)}>
            {t === 'indexers' ? 'Indexers'
              : t === 'clients' ? 'Download Clients'
              : t === 'notifications' ? 'Notifications'
              : t === 'quality' ? 'Quality Profiles'
              : t === 'metadata' ? 'Metadata Profiles'
              : t === 'import' ? 'Import'
              : t === 'rootfolders' ? 'Root Folders'
              : 'General'}
          </button>
        ))}
      </div>

      {/* Indexers */}
      {tab === 'indexers' && (
        <div>
          <div className="flex justify-between items-center mb-4">
            <h3 className="text-lg font-semibold">Indexers</h3>
            <button onClick={() => setShowAddIndexer(true)} className="px-3 py-1.5 bg-emerald-600 hover:bg-emerald-500 rounded text-xs font-medium">
              + Add Indexer
            </button>
          </div>
          {indexers.length === 0 ? (
            <p className="text-slate-600 dark:text-zinc-500 text-sm">No indexers configured. Add a Newznab indexer to search for books.</p>
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
                        title={idx.enabled ? 'Disable' : 'Enable'}
                      >
                        <span className={`absolute top-0.5 left-0.5 w-4 h-4 bg-white rounded-full transition-transform ${idx.enabled ? 'translate-x-4' : ''}`} />
                      </button>
                      <div className="min-w-0">
                        <h4 className={`font-medium text-sm ${!idx.enabled ? 'text-slate-600 dark:text-zinc-500' : ''}`}>{idx.name}</h4>
                        <p className="text-xs text-slate-600 dark:text-zinc-500 truncate">{idx.url}</p>
                      </div>
                    </div>
                    <div className="flex items-center gap-3 flex-shrink-0">
                      <button onClick={() => setEditingIndexer(editingIndexer === idx.id ? null : idx.id)} className="text-xs text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white">Edit</button>
                      <button
                        onClick={async () => {
                          try {
                            await api.testIndexer(idx.id)
                            alert('Connection successful!')
                          } catch (err: unknown) {
                            alert('Test failed: ' + (err instanceof Error ? err.message : 'Unknown error'))
                          }
                        }}
                        className="text-xs text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white"
                      >
                        Test
                      </button>
                      <button
                        onClick={async () => {
                          await api.deleteIndexer(idx.id)
                          setIndexers(indexers.filter(i => i.id !== idx.id))
                        }}
                        className="text-xs text-red-400 hover:text-red-300"
                      >
                        Delete
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
        </div>
      )}

      {/* Download Clients */}
      {tab === 'clients' && (
        <div>
          <div className="flex justify-between items-center mb-4">
            <h3 className="text-lg font-semibold">Download Clients</h3>
            <button onClick={() => setShowAddClient(true)} className="px-3 py-1.5 bg-emerald-600 hover:bg-emerald-500 rounded text-xs font-medium">
              + Add Client
            </button>
          </div>
          {clients.length === 0 ? (
            <p className="text-slate-600 dark:text-zinc-500 text-sm">No download clients configured. Add SABnzbd to enable downloads.</p>
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
                        title={c.enabled ? 'Disable' : 'Enable'}
                      >
                        <span className={`absolute top-0.5 left-0.5 w-4 h-4 bg-white rounded-full transition-transform ${c.enabled ? 'translate-x-4' : ''}`} />
                      </button>
                      <div className="min-w-0">
                        <h4 className={`font-medium text-sm ${!c.enabled ? 'text-slate-600 dark:text-zinc-500' : ''}`}>{c.name}</h4>
                        <p className="text-xs text-slate-600 dark:text-zinc-500">{c.host}:{c.port} ({c.category})</p>
                      </div>
                    </div>
                    <div className="flex items-center gap-3 flex-shrink-0">
                      <button onClick={() => setEditingClient(editingClient === c.id ? null : c.id)} className="text-xs text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white">Edit</button>
                      <button
                        onClick={async () => {
                          try {
                            await api.testDownloadClient(c.id)
                            alert('Connection successful!')
                          } catch (err: unknown) {
                            alert('Test failed: ' + (err instanceof Error ? err.message : 'Unknown error'))
                          }
                        }}
                        className="text-xs text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white"
                      >
                        Test
                      </button>
                      <button
                        onClick={async () => {
                          await api.deleteDownloadClient(c.id)
                          setClients(clients.filter(x => x.id !== c.id))
                        }}
                        className="text-xs text-red-400 hover:text-red-300"
                      >
                        Delete
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
            <h3 className="text-lg font-semibold">Webhook Notifications</h3>
            <button onClick={() => setShowAddNotification(true)} className="px-3 py-1.5 bg-emerald-600 hover:bg-emerald-500 rounded text-xs font-medium">
              + Add Notification
            </button>
          </div>
          {notifications.length === 0 ? (
            <p className="text-slate-600 dark:text-zinc-500 text-sm">No notifications configured. Add a webhook to receive event alerts.</p>
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
                          title={n.enabled ? 'Disable' : 'Enable'}
                        >
                          <span className={`absolute top-0.5 left-0.5 w-4 h-4 bg-white rounded-full transition-transform ${n.enabled ? 'translate-x-4' : ''}`} />
                        </button>
                        <div className="min-w-0">
                          <h4 className={`font-medium text-sm ${!n.enabled ? 'text-slate-600 dark:text-zinc-500' : ''}`}>{n.name}</h4>
                          <p className="text-xs text-slate-600 dark:text-zinc-500 truncate mt-0.5">{n.url}</p>
                          <div className="flex flex-wrap gap-1 mt-2">
                            {n.onGrab && <span className="text-[10px] px-1.5 py-0.5 bg-slate-200 dark:bg-zinc-800 text-slate-600 dark:text-zinc-400 rounded">On Grab</span>}
                            {n.onImport && <span className="text-[10px] px-1.5 py-0.5 bg-slate-200 dark:bg-zinc-800 text-slate-600 dark:text-zinc-400 rounded">On Import</span>}
                            {n.onUpgrade && <span className="text-[10px] px-1.5 py-0.5 bg-slate-200 dark:bg-zinc-800 text-slate-600 dark:text-zinc-400 rounded">On Upgrade</span>}
                            {n.onFailure && <span className="text-[10px] px-1.5 py-0.5 bg-slate-200 dark:bg-zinc-800 text-slate-600 dark:text-zinc-400 rounded">On Failure</span>}
                            {n.onHealth && <span className="text-[10px] px-1.5 py-0.5 bg-slate-200 dark:bg-zinc-800 text-slate-600 dark:text-zinc-400 rounded">On Health</span>}
                          </div>
                        </div>
                      </div>
                      <div className="flex items-center gap-3 flex-shrink-0">
                        <button onClick={() => setEditingNotification(editingNotification === n.id ? null : n.id)} className="text-xs text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white">Edit</button>
                        <button
                          onClick={async () => {
                            try {
                              await api.testNotification(n.id)
                              alert('Test notification sent!')
                            } catch (err: unknown) {
                              alert('Test failed: ' + (err instanceof Error ? err.message : 'Unknown error'))
                            }
                          }}
                          className="text-xs text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white"
                        >
                          Test
                        </button>
                        <button
                          onClick={async () => {
                            await api.deleteNotification(n.id)
                            setNotifications(notifications.filter(x => x.id !== n.id))
                          }}
                          className="text-xs text-red-400 hover:text-red-300"
                        >
                          Delete
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
          <h3 className="text-lg font-semibold mb-4">Quality Profiles</h3>
          {qualityProfiles.length === 0 ? (
            <p className="text-slate-600 dark:text-zinc-500 text-sm">No quality profiles configured.</p>
          ) : (
            <div className="space-y-3">
              {qualityProfiles.map(p => (
                <div key={p.id} className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900">
                  <div className="flex items-center justify-between mb-2">
                    <h4 className="font-medium text-sm">{p.name}</h4>
                    <div className="flex items-center gap-3 text-xs text-slate-600 dark:text-zinc-500">
                      <span>Cutoff: <span className="text-slate-700 dark:text-zinc-300">{p.cutoff}</span></span>
                      {p.upgradeAllowed && <span className="text-emerald-400">Upgrades allowed</span>}
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
            <h3 className="text-lg font-semibold">Root Folders</h3>
          </div>
          <p className="text-sm text-slate-600 dark:text-zinc-400 mb-4">
            Root folders are the top-level library directories where Bindery moves finished downloads.
            Each author can be assigned to a specific root folder; authors without one use the default
            path configured at startup (<code className="font-mono bg-slate-200 dark:bg-zinc-800 px-1 rounded text-xs">BINDERY_LIBRARY_DIR</code>).
          </p>

          {rootFolders.length > 0 && (
            <div className="space-y-2 mb-6">
              {rootFolders.map(rf => (
                <div key={rf.id} className="flex items-center justify-between p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900">
                  <div className="min-w-0">
                    <p className="font-mono text-sm truncate">{rf.path}</p>
                    <p className="text-xs text-slate-500 dark:text-zinc-500 mt-0.5">{formatBytes(rf.freeSpace)} free</p>
                  </div>
                  <button
                    onClick={async () => {
                      await api.deleteRootFolder(rf.id)
                      setRootFolders(rootFolders.filter(f => f.id !== rf.id))
                    }}
                    className="ml-4 px-3 py-1 text-xs text-red-600 dark:text-red-400 hover:bg-red-50 dark:hover:bg-red-900/20 rounded border border-red-200 dark:border-red-800 flex-shrink-0"
                  >
                    Remove
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
                placeholder="/data/books"
                className={inputCls}
              />
              {folderError && <p className="text-xs text-red-500 mt-1">{folderError}</p>}
            </div>
            <button
              type="submit"
              disabled={!newFolderPath.trim()}
              className="px-3 py-2 bg-emerald-600 hover:bg-emerald-500 rounded text-sm font-medium disabled:opacity-50 flex-shrink-0"
            >
              Add Folder
            </button>
          </form>
        </div>
      )}

      {tab === 'general' && (
        <GeneralTab />
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
  const [editing, setEditing] = useState<MetadataProfile | null>(null)
  const [creating, setCreating] = useState(false)

  return (
    <div>
      <div className="flex justify-between items-center mb-4">
        <h3 className="text-lg font-semibold">Metadata Profiles</h3>
        <button onClick={() => setCreating(true)} className="px-3 py-1.5 bg-emerald-600 hover:bg-emerald-500 rounded text-xs font-medium">
          + New Profile
        </button>
      </div>
      <p className="text-xs text-slate-600 dark:text-zinc-500 mb-4">
        Books in languages outside the profile's allowed list are filtered out when an author is added or refreshed.
        Leave the list empty to accept any language.
      </p>
      {creating && (
        <MetadataProfileForm
          onClose={() => setCreating(false)}
          onSaved={() => { setCreating(false); onReload() }}
        />
      )}
      {profiles.length === 0 && !creating ? (
        <p className="text-slate-600 dark:text-zinc-500 text-sm">No metadata profiles configured.</p>
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
                      <span>Min popularity: <span className="text-slate-800 dark:text-zinc-200">{p.minPopularity}</span></span>
                      <span>Min pages: <span className="text-slate-800 dark:text-zinc-200">{p.minPages}</span></span>
                      <span>Languages: <span className="text-slate-800 dark:text-zinc-200">{formatLanguageList(p.allowedLanguages)}</span></span>
                    </div>
                    <div className="flex flex-wrap gap-1.5 mt-2">
                      {p.skipMissingDate && <span className="text-[10px] px-2 py-0.5 bg-slate-200 dark:bg-zinc-800 text-slate-600 dark:text-zinc-400 rounded">Skip missing date</span>}
                      {p.skipMissingIsbn && <span className="text-[10px] px-2 py-0.5 bg-slate-200 dark:bg-zinc-800 text-slate-600 dark:text-zinc-400 rounded">Skip missing ISBN</span>}
                      {p.skipPartBooks && <span className="text-[10px] px-2 py-0.5 bg-slate-200 dark:bg-zinc-800 text-slate-600 dark:text-zinc-400 rounded">Skip part books</span>}
                    </div>
                  </div>
                  <div className="flex items-center gap-3 flex-shrink-0">
                    <button onClick={() => setEditing(p)} className="text-xs text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white">Edit</button>
                    <button
                      onClick={async () => {
                        if (!confirm('Delete this metadata profile?')) return
                        await api.deleteMetadataProfile(p.id)
                        onReload()
                      }}
                      className="text-xs text-red-400 hover:text-red-300"
                    >
                      Delete
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
      setErr(e instanceof Error ? e.message : 'Failed to save profile')
    } finally {
      setSaving(false)
    }
  }

  return (
    <form onSubmit={submit} className="p-4 border border-slate-300 dark:border-zinc-700 rounded-lg bg-slate-50 dark:bg-zinc-900/50 space-y-4">
      <div>
        <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">Name</label>
        <input value={name} onChange={e => setName(e.target.value)} required className={inputCls} placeholder="e.g. English Only" />
      </div>
      <div>
        <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-2">Allowed languages</label>
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
          Select none to accept any language. Books whose language is unknown are always kept.
        </p>
      </div>
      <div className="grid grid-cols-2 gap-3">
        <div>
          <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">Min popularity</label>
          <input type="number" min={0} value={minPopularity} onChange={e => setMinPopularity(Number(e.target.value))} className={inputCls} />
        </div>
        <div>
          <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">Min pages</label>
          <input type="number" min={0} value={minPages} onChange={e => setMinPages(Number(e.target.value))} className={inputCls} />
        </div>
      </div>
      <div className="flex flex-wrap gap-4 text-xs">
        <label className="flex items-center gap-2 cursor-pointer">
          <input type="checkbox" checked={skipMissingDate} onChange={e => setSkipMissingDate(e.target.checked)} />
          Skip missing release date
        </label>
        <label className="flex items-center gap-2 cursor-pointer">
          <input type="checkbox" checked={skipMissingIsbn} onChange={e => setSkipMissingIsbn(e.target.checked)} />
          Skip missing ISBN
        </label>
        <label className="flex items-center gap-2 cursor-pointer">
          <input type="checkbox" checked={skipPartBooks} onChange={e => setSkipPartBooks(e.target.checked)} />
          Skip part books
        </label>
      </div>
      {err && <div className="text-xs text-red-400">{err}</div>}
      <div className="flex justify-end gap-2">
        <button type="button" onClick={onClose} className="px-3 py-1.5 text-xs text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white">
          Cancel
        </button>
        <button type="submit" disabled={saving} className="px-3 py-1.5 bg-emerald-600 hover:bg-emerald-500 disabled:opacity-50 rounded text-xs font-medium">
          {saving ? 'Saving...' : profile ? 'Save changes' : 'Create profile'}
        </button>
      </div>
    </form>
  )
}

function ImportTab() {
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
        <h3 className="text-base font-semibold mb-2 text-slate-800 dark:text-zinc-200">CSV of author names</h3>
        <p className="text-xs text-slate-600 dark:text-zinc-500 mb-3">
          One name per line, or CSV columns <code className="text-[11px] bg-slate-200 dark:bg-zinc-800 px-1 rounded">name,monitored,searchOnAdd</code>.
          Each name is resolved against OpenLibrary — the top match is added.
        </p>
        <label className="inline-flex items-center gap-2 px-3 py-2 bg-emerald-600 hover:bg-emerald-500 rounded text-sm font-medium cursor-pointer">
          {uploading === 'csv' ? 'Importing…' : 'Upload CSV'}
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
        <h3 className="text-base font-semibold mb-2 text-slate-800 dark:text-zinc-200">Readarr database</h3>
        <p className="text-xs text-slate-600 dark:text-zinc-500 mb-3">
          Upload <code className="text-[11px] bg-slate-200 dark:bg-zinc-800 px-1 rounded">readarr.db</code> (typically under
          <code className="text-[11px] bg-slate-200 dark:bg-zinc-800 px-1 rounded mx-1">/config/readarr.db</code>).
          Authors are re-resolved via OpenLibrary. Indexers, download clients, and blocklist entries port directly.
          Run a library scan afterward to match existing book files.
        </p>
        <label className="inline-flex items-center gap-2 px-3 py-2 bg-emerald-600 hover:bg-emerald-500 rounded text-sm font-medium cursor-pointer">
          {uploading === 'readarr' ? 'Importing (may take minutes)…' : 'Upload readarr.db'}
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

      {err && (
        <div className="px-3 py-2 bg-red-100 dark:bg-red-950/30 border border-red-300 dark:border-red-900 rounded text-sm text-red-800 dark:text-red-300">
          {err}
        </div>
      )}
    </div>
  )
}

function GeneralTab() {
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

  if (loading) return <div className="text-slate-600 dark:text-zinc-500">Loading...</div>

  return (
    <div className="space-y-8">
      {/* Appearance */}
      <section>
        <h3 className="text-base font-semibold mb-3 text-slate-800 dark:text-zinc-200">Appearance</h3>
        <div className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900">
          <div className="flex items-center justify-between">
            <div>
              <label className="block text-sm font-medium text-slate-800 dark:text-zinc-200">Theme</label>
              <p className="text-xs text-slate-600 dark:text-zinc-500 mt-1">Light or dark interface. Defaults to your OS preference on first visit.</p>
            </div>
            <ThemeToggle />
          </div>
        </div>
      </section>

      {/* Downloads */}
      <section>
        <h3 className="text-base font-semibold mb-3 text-slate-800 dark:text-zinc-200">Downloads</h3>
        <div className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900 space-y-3">
          <div>
            <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">Preferred Language</label>
            <p className="text-xs text-slate-600 dark:text-zinc-500 mb-2">Filter search results to the selected language. Releases with detected foreign-language tags in the title will be excluded.</p>
            <div className="flex gap-2">
              <select
                value={settings['search.preferredLanguage'] ?? 'en'}
                onChange={e => setSettings(s => ({ ...s, 'search.preferredLanguage': e.target.value }))}
                className="flex-1 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600"
              >
                <option value="any">Any (no filter)</option>
                <option value="en">English</option>
              </select>
              <button
                onClick={() => saveSetting('search.preferredLanguage')}
                disabled={saving === 'search.preferredLanguage'}
                className="px-3 py-2 bg-emerald-600 hover:bg-emerald-500 rounded text-xs font-medium disabled:opacity-50"
              >
                {saving === 'search.preferredLanguage' ? 'Saving...' : 'Save'}
              </button>
            </div>
          </div>
        </div>
      </section>

      {/* Auto-grab */}
      <section>
        <h3 className="text-base font-semibold mb-3 text-slate-800 dark:text-zinc-200">Auto-grab</h3>
        <div className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900">
          <div className="flex items-center justify-between">
            <div>
              <label className="block text-sm font-medium text-slate-800 dark:text-zinc-200">Enable automatic grabbing</label>
              <p className="text-xs text-slate-600 dark:text-zinc-500 mt-0.5">When enabled, Bindery automatically sends found books to the download client. When disabled, all searches run but you manually initiate grabs from the Wanted page.</p>
            </div>
            <button
              onClick={async () => {
                const current = (settings['autoGrab.enabled'] ?? 'true').toLowerCase()
                const next = current === 'false' ? 'true' : 'false'
                setSettings(s => ({ ...s, 'autoGrab.enabled': next }))
                await api.setSetting('autoGrab.enabled', next).catch(console.error)
              }}
              className={`relative w-9 h-5 rounded-full transition-colors flex-shrink-0 ${(settings['autoGrab.enabled'] ?? 'true') !== 'false' ? 'bg-emerald-600' : 'bg-slate-300 dark:bg-zinc-700'}`}
              title={(settings['autoGrab.enabled'] ?? 'true') !== 'false' ? 'Disable' : 'Enable'}
            >
              <span className={`absolute top-0.5 left-0.5 w-4 h-4 bg-white rounded-full transition-transform ${(settings['autoGrab.enabled'] ?? 'true') !== 'false' ? 'translate-x-4' : ''}`} />
            </button>
          </div>
        </div>
      </section>

      {/* Naming */}
      <section>
        <h3 className="text-base font-semibold mb-3 text-slate-800 dark:text-zinc-200">File Naming</h3>
        <div className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900 space-y-3">
          <div>
            <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">Book Naming Template</label>
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
                {saving === 'naming.bookTemplate' ? 'Saving...' : 'Save'}
              </button>
            </div>
          </div>
          <div>
            <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">Audiobook Folder Template</label>
            <p className="text-xs text-slate-600 dark:text-zinc-500 mb-2">Audiobooks are imported as whole directories (multi-part m4b/mp3 + cover stay together). Template produces the destination folder; original filenames inside are preserved.</p>
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
                {saving === 'naming_template_audiobook' ? 'Saving...' : 'Save'}
              </button>
            </div>
          </div>
        </div>
      </section>

      {/* Security */}
      <SecuritySection />

      {/* API Keys */}
      <section>
        <h3 className="text-base font-semibold mb-3 text-slate-800 dark:text-zinc-200">External API Keys</h3>
        <div className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900 space-y-4">
          <div>
            <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">Google Books API Key</label>
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
                {saving === 'googlebooks.apiKey' ? 'Saving...' : 'Save'}
              </button>
            </div>
          </div>
        </div>
      </section>

      {/* Library */}
      <section>
        <h3 className="text-base font-semibold mb-3 text-slate-800 dark:text-zinc-200">Library</h3>
        <div className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900">
          <div className="flex items-center justify-between">
            <div>
              <p className="text-sm text-slate-700 dark:text-zinc-300">Scan library</p>
              <p className="text-xs text-slate-600 dark:text-zinc-500 mt-0.5">Walk the books directory and reconcile files with the database</p>
              {scanMessage && (
                <p className="text-xs text-amber-600 dark:text-amber-400 mt-1">{scanMessage}</p>
              )}
            </div>
            <button
              onClick={handleScan}
              disabled={scanningLibrary}
              className="px-4 py-2 bg-slate-600 hover:bg-slate-500 rounded text-sm font-medium disabled:opacity-50 flex-shrink-0"
            >
              {scanningLibrary ? 'Scanning…' : 'Scan Library'}
            </button>
          </div>
          {lastScan && (
            <div className="mt-3 border-t border-slate-200 dark:border-zinc-800 pt-3 text-xs text-slate-600 dark:text-zinc-400">
              <p className="font-medium text-slate-700 dark:text-zinc-300 mb-1">Last scan result</p>
              <div className="flex gap-4">
                <span>Files found: <span className="font-mono text-slate-800 dark:text-zinc-200">{lastScan.files_found}</span></span>
                <span>Reconciled: <span className="font-mono text-emerald-700 dark:text-emerald-400">{lastScan.reconciled}</span></span>
                <span>Unmatched: <span className="font-mono text-slate-800 dark:text-zinc-200">{lastScan.unmatched}</span></span>
              </div>
              <p className="mt-1 text-slate-500 dark:text-zinc-500">
                {new Date(lastScan.ran_at).toLocaleString()}
              </p>
            </div>
          )}
        </div>
      </section>

      {/* OPDS */}
      <section>
        <h3 className="text-base font-semibold mb-3 text-slate-800 dark:text-zinc-200">OPDS Feed</h3>
        <div className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900">
          <div>
            <span className="block text-xs font-medium text-slate-600 dark:text-zinc-400 mb-1">OPDS Feed URL</span>
            <div className="flex items-center gap-2">
              <code className="flex-1 text-xs bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 px-2 py-1.5 rounded font-mono break-all">
                {window.location.origin}/opds
              </code>
              <button
                onClick={() => navigator.clipboard.writeText(window.location.origin + '/opds')}
                className="px-3 py-1.5 bg-slate-600 hover:bg-slate-500 rounded text-xs font-medium flex-shrink-0"
              >
                Copy
              </button>
            </div>
            <p className="text-xs text-slate-500 dark:text-zinc-500 mt-1.5">Use this URL in KOReader, Moon+ Reader, or any OPDS client. Authenticate with your API key as the password (leave the username blank or use any value).</p>
          </div>
        </div>
      </section>

      {/* Calibre */}
      <CalibreSection
        settings={settings}
        setSettings={setSettings}
        saveSetting={saveSetting}
        saving={saving}
      />

      {/* Backup */}
      <section>
        <h3 className="text-base font-semibold mb-3 text-slate-800 dark:text-zinc-200">Backup & Restore</h3>
        <div className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900 space-y-3">
          <div className="flex items-center justify-between">
            <div>
              <p className="text-sm text-slate-700 dark:text-zinc-300">Create a backup of all Bindery configuration</p>
              <p className="text-xs text-slate-600 dark:text-zinc-500 mt-0.5">Includes authors, books, indexers, and settings</p>
            </div>
            <button
              onClick={handleBackup}
              disabled={creatingBackup}
              className="px-4 py-2 bg-emerald-600 hover:bg-emerald-500 rounded text-sm font-medium disabled:opacity-50 flex-shrink-0"
            >
              {creatingBackup ? 'Creating...' : 'Create Backup'}
            </button>
          </div>
          {backups.length > 0 && (
            <div className="mt-3 border-t border-slate-200 dark:border-zinc-800 pt-3">
              <p className="text-xs text-slate-600 dark:text-zinc-500 mb-2">Existing backups:</p>
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

// CalibreSection renders the calibre.* settings fields plus a Test button
// that hits /calibre/test. The Mode radio picks between two integration
// flows: `calibredb` shells out to the CLI (requires Calibre on the host),
// `drop_folder` drops files into a Calibre-watched directory (decouples
// Bindery from where Calibre runs). The fields for each path are only
// shown when their mode is selected so the form stays unambiguous.
function CalibreSection({
  settings,
  setSettings,
  saveSetting,
  saving,
}: {
  settings: Record<string, string>
  setSettings: (fn: (prev: Record<string, string>) => Record<string, string>) => void
  saveSetting: (key: string) => Promise<void>
  saving: string | null
}) {
  const [testing, setTesting] = useState(false)
  const [testResult, setTestResult] = useState<{ ok: boolean; msg: string } | null>(null)
  const [importProgress, setImportProgress] = useState<CalibreImportProgress | null>(null)
  const [importError, setImportError] = useState<string | null>(null)

  // Legacy fallback: a pre-migration DB with `calibre.enabled=true` but no
  // mode set should still render as 'calibredb' so the user's existing
  // setup is visible in the UI.
  const rawMode = settings['calibre.mode'] ?? ''
  const legacyEnabled = (settings['calibre.enabled'] ?? 'false').toLowerCase() === 'true'
  const mode: 'off' | 'calibredb' | 'drop_folder' =
    rawMode === 'calibredb' || rawMode === 'drop_folder' || rawMode === 'off'
      ? rawMode
      : legacyEnabled
      ? 'calibredb'
      : 'off'
  const enabled = mode !== 'off'
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

  const setMode = async (next: 'off' | 'calibredb' | 'drop_folder') => {
    setSettings(s => ({ ...s, 'calibre.mode': next }))
    // Persist immediately on change — matches the indexer/client toggles
    // that don't require a separate Save click.
    await api.setSetting('calibre.mode', next).catch(console.error)
  }

  return (
    <section>
      <h3 className="text-base font-semibold mb-3 text-slate-800 dark:text-zinc-200">Calibre</h3>
      <div className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900 space-y-4">
        <p className="text-xs text-slate-600 dark:text-zinc-500 -mt-1">
          Mirror imported books into a Calibre library. Pick the mode that fits your deployment — {' '}
          <code className="text-[11px] bg-slate-200 dark:bg-zinc-800 px-1 rounded">calibredb</code>{' '}
          when Calibre runs alongside Bindery, or drop-folder when Calibre runs elsewhere and watches a directory.
        </p>

        <div>
          <label className="block text-sm font-medium text-slate-800 dark:text-zinc-200 mb-2">Mode</label>
          <div className="space-y-1.5">
            {([
              { v: 'off',         label: 'Off',            desc: 'No Calibre call on import.' },
              { v: 'calibredb',   label: 'calibredb CLI',  desc: 'Shell out to calibredb add --with-library. Requires calibredb reachable from the Bindery process.' },
              { v: 'drop_folder', label: 'Drop folder',    desc: 'Write the file into Calibre\u2019s watched folder, then poll metadata.db for the assigned book id.' },
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

        {mode !== 'off' && (
          <div>
            <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">Library path</label>
            <p className="text-xs text-slate-600 dark:text-zinc-500 mb-2">
              Directory containing <code className="text-[11px] bg-slate-200 dark:bg-zinc-800 px-1 rounded">metadata.db</code>.
              {mode === 'calibredb'
                ? ' Passed to calibredb as --with-library.'
                : ' Read directly to resolve the assigned Calibre book id after drop-folder ingest.'}
            </p>
            <div className="flex gap-2">
              <input
                value={settings['calibre.library_path'] ?? ''}
                onChange={e => setSettings(s => ({ ...s, 'calibre.library_path': e.target.value }))}
                placeholder="/data/calibre-library"
                className="flex-1 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600"
              />
              <button
                onClick={() => saveSetting('calibre.library_path')}
                disabled={saving === 'calibre.library_path'}
                className="px-3 py-2 bg-emerald-600 hover:bg-emerald-500 rounded text-xs font-medium disabled:opacity-50"
              >
                {saving === 'calibre.library_path' ? 'Saving...' : 'Save'}
              </button>
            </div>
          </div>
        )}

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
                onClick={() => saveSetting('calibre.binary_path')}
                disabled={saving === 'calibre.binary_path'}
                className="px-3 py-2 bg-emerald-600 hover:bg-emerald-500 rounded text-xs font-medium disabled:opacity-50"
              >
                {saving === 'calibre.binary_path' ? 'Saving...' : 'Save'}
              </button>
            </div>
          </div>
        )}

        {mode === 'drop_folder' && (
          <div>
            <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">Drop folder path</label>
            <p className="text-xs text-slate-600 dark:text-zinc-500 mb-2">
              {'Directory Calibre\u2019s '}<em>Add books to library from folders</em>{' feature watches. Bindery will copy imported files into '}<code className="text-[11px] bg-slate-200 dark:bg-zinc-800 px-1 rounded">&lt;folder&gt;/&lt;Author&gt;/&lt;Title&gt;.ext</code>{' and wait for Calibre to ingest them.'}
            </p>
            <div className="flex gap-2">
              <input
                value={settings['calibre.drop_folder_path'] ?? ''}
                onChange={e => setSettings(s => ({ ...s, 'calibre.drop_folder_path': e.target.value }))}
                placeholder="/data/calibre-watch"
                className="flex-1 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600"
              />
              <button
                onClick={() => saveSetting('calibre.drop_folder_path')}
                disabled={saving === 'calibre.drop_folder_path'}
                className="px-3 py-2 bg-emerald-600 hover:bg-emerald-500 rounded text-xs font-medium disabled:opacity-50"
              >
                {saving === 'calibre.drop_folder_path' ? 'Saving...' : 'Save'}
              </button>
            </div>
          </div>
        )}

        {mode === 'calibredb' && (
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
              <label className="block text-sm font-medium text-slate-800 dark:text-zinc-200">Sync on startup</label>
              <p className="text-xs text-slate-600 dark:text-zinc-500 mt-0.5">Run a library import automatically each time Bindery boots. Safe to leave on — re-imports are incremental.</p>
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
              disabled={!enabled || importProgress?.running}
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

  const submit = async () => {
    const updated = await api.updateIndexer(indexer.id, { ...indexer, name, type, url, apiKey, categories: parseCats(categories) })
    onSaved(updated)
  }

  return (
    <div className="mt-1 p-4 border border-slate-300 dark:border-zinc-700 rounded-lg bg-slate-200/50 dark:bg-zinc-800/50 space-y-3">
      <div className="flex gap-2">
        <input value={name} onChange={e => setName(e.target.value)} placeholder="Name" className="flex-1 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600" />
        <select value={type} onChange={e => setType(e.target.value)} className="bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600">
          <option value="newznab">Newznab (Usenet)</option>
          <option value="torznab">Torznab (Torrent)</option>
        </select>
      </div>
      <input value={url} onChange={e => setUrl(e.target.value)} placeholder="URL" className={inputCls} />
      <input value={apiKey} onChange={e => setApiKey(e.target.value)} placeholder="API Key" type="password" className={inputCls} />
      <div>
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
  const [credential, setCredential] = useState(client.type === 'qbittorrent' ? (client.password || '') : (client.apiKey || ''))
  const [username, setUsername] = useState(client.username || '')
  const [category, setCategory] = useState(client.category)

  const handleTypeChange = (newType: string) => {
    setType(newType)
    setCredential('')
    setUsername('')
  }

  const submit = async () => {
    const data = type === 'qbittorrent'
      ? { ...client, name, type, host, port: parseInt(port), username, password: credential, apiKey: '', category }
      : { ...client, name, type, host, port: parseInt(port), apiKey: credential, username: '', password: '', category }
    const updated = await api.updateDownloadClient(client.id, data)
    onSaved(updated)
  }

  return (
    <div className="mt-1 p-4 border border-slate-300 dark:border-zinc-700 rounded-lg bg-slate-200/50 dark:bg-zinc-800/50 space-y-3">
      <div className="flex gap-2">
        <input value={name} onChange={e => setName(e.target.value)} placeholder="Name" className="flex-1 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600" />
        <select value={type} onChange={e => handleTypeChange(e.target.value)} className="bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600">
          <option value="sabnzbd">SABnzbd</option>
          <option value="qbittorrent">qBittorrent</option>
        </select>
      </div>
      <div>
        <div className="flex gap-2">
          <input value={host} onChange={e => setHost(e.target.value)} placeholder="Host" className="flex-1 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600" />
          <input value={port} onChange={e => setPort(e.target.value)} placeholder="Port" className="w-24 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600" />
        </div>
        <p className="text-xs text-slate-500 dark:text-zinc-500 mt-1">In Docker, use the service/container name (e.g. <code className="font-mono">sabnzbd</code>) — not <code className="font-mono">localhost</code>.</p>
      </div>
      {type === 'qbittorrent' && (
        <input value={username} onChange={e => setUsername(e.target.value)} placeholder="Username" className={inputCls} />
      )}
      <input value={credential} onChange={e => setCredential(e.target.value)} placeholder={type === 'qbittorrent' ? 'Password' : 'API Key'} type="password" className={inputCls} />
      <input value={category} onChange={e => setCategory(e.target.value)} placeholder="Category" className={inputCls} />
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

  const submit = async () => {
    const idx = await api.addIndexer({ name, url, apiKey, type, categories: parseCats(categories), enabled: true })
    onAdded(idx)
  }

  return (
    <div className="mt-4 p-4 border border-slate-300 dark:border-zinc-700 rounded-lg bg-slate-200/50 dark:bg-zinc-800/50 space-y-3">
      <div className="flex gap-2">
        <input value={name} onChange={e => setName(e.target.value)} placeholder="Name (e.g. NZBGeek)" className="flex-1 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600" />
        <select value={type} onChange={e => setType(e.target.value as 'newznab' | 'torznab')} className="bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600">
          <option value="newznab">Newznab</option>
          <option value="torznab">Torznab</option>
        </select>
      </div>
      <input value={url} onChange={e => setUrl(e.target.value)} placeholder="URL (e.g. https://api.nzbgeek.info)" className={inputCls} />
      <input value={apiKey} onChange={e => setApiKey(e.target.value)} placeholder="API Key" type="password" className={inputCls} />
      <div>
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
  const [type, setType] = useState<'sabnzbd' | 'qbittorrent'>('sabnzbd')
  const [host, setHost] = useState('')
  const [port, setPort] = useState('8080')
  const [credential, setCredential] = useState('')
  const [username, setUsername] = useState('')
  const [category, setCategory] = useState('books')

  const handleTypeChange = (newType: 'sabnzbd' | 'qbittorrent') => {
    setType(newType)
    setCredential('')
    setUsername('')
    setName(newType === 'qbittorrent' ? 'qBittorrent' : 'SABnzbd')
  }

  const submit = async () => {
    const data = type === 'qbittorrent'
      ? { name, host, port: parseInt(port), username, password: credential, apiKey: '', category, type, enabled: true }
      : { name, host, port: parseInt(port), apiKey: credential, username: '', password: '', category, type, enabled: true }
    const c = await api.addDownloadClient(data)
    onAdded(c)
  }

  return (
    <div className="mt-4 p-4 border border-slate-300 dark:border-zinc-700 rounded-lg bg-slate-200/50 dark:bg-zinc-800/50 space-y-3">
      <div className="flex gap-2">
        <input value={name} onChange={e => setName(e.target.value)} placeholder="Name" className="flex-1 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600" />
        <select value={type} onChange={e => handleTypeChange(e.target.value as 'sabnzbd' | 'qbittorrent')} className="bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600">
          <option value="sabnzbd">SABnzbd</option>
          <option value="qbittorrent">qBittorrent</option>
        </select>
      </div>
      <div>
        <div className="flex gap-2">
          <input value={host} onChange={e => setHost(e.target.value)} placeholder="Host" className="flex-1 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600" />
          <input value={port} onChange={e => setPort(e.target.value)} placeholder="Port" className="w-24 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600" />
        </div>
        <p className="text-xs text-slate-500 dark:text-zinc-500 mt-1">In Docker, use the service/container name (e.g. <code className="font-mono">sabnzbd</code>) — not <code className="font-mono">localhost</code>.</p>
      </div>
      {type === 'qbittorrent' && (
        <input value={username} onChange={e => setUsername(e.target.value)} placeholder="Username" className={inputCls} />
      )}
      <input value={credential} onChange={e => setCredential(e.target.value)} placeholder={type === 'qbittorrent' ? 'Password' : 'API Key'} type="password" className={inputCls} />
      <input value={category} onChange={e => setCategory(e.target.value)} placeholder="Category" className={inputCls} />
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
