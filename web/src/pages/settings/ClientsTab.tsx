import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, DownloadClient } from '../../api/client'
import { inputCls } from './formStyles'
import Toggle from './Toggle'
import PathRemapField from './PathRemapField'
import { downloadClientPathRemapHelp } from './helpers'

// clients is owned by SettingsPage so it can be fetched eagerly on page mount
// (matching the pre-refactor monolith), not on tab open.
interface Props {
  clients: DownloadClient[]
  setClients: React.Dispatch<React.SetStateAction<DownloadClient[]>>
}

export default function ClientsTab({ clients, setClients }: Props) {
  const { t } = useTranslation()
  const [showAddClient, setShowAddClient] = useState(false)
  const [editingClient, setEditingClient] = useState<number | null>(null)
  const [clientTestResult, setClientTestResult] = useState<Record<number, { ok: boolean; msg: string }>>({})
  const [confirmDeleteClient, setConfirmDeleteClient] = useState<number | null>(null)

  return (
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
                  <Toggle
                    checked={c.enabled}
                    onChange={async () => {
                      const updated = await api.updateDownloadClient(c.id, { ...c, enabled: !c.enabled })
                      setClients(clients.map(x => x.id === c.id ? updated : x))
                    }}
                    title={c.enabled ? t('common.disable') : t('common.enable')}
                  />
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
                        const result = await api.testDownloadClient(c.id)
                        if (result.health) {
                          setClients(prev => prev.map(x => x.id === c.id ? { ...x, health: result.health } : x))
                        }
                        setClientTestResult(prev => ({ ...prev, [c.id]: { ok: true, msg: t('common.connOk') } }))
                      } catch (err: unknown) {
                        setClientTestResult(prev => ({ ...prev, [c.id]: { ok: false, msg: t('common.connFail', { error: err instanceof Error ? err.message : 'Unknown error' }) } }))
                      }
                    }}
                    className="text-xs text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white"
                  >
                    {t('common.test')}
                  </button>
                  {confirmDeleteClient === c.id ? (
                    <span className="flex items-center gap-1.5">
                      <span className="text-xs text-slate-500 dark:text-zinc-500">{t('common.delete')}?</span>
                      <button
                        onClick={async () => {
                          await api.deleteDownloadClient(c.id)
                          setClients(clients.filter(x => x.id !== c.id))
                          setConfirmDeleteClient(null)
                        }}
                        className="text-xs text-red-500 font-medium hover:text-red-400"
                      >{t('common.yes')}</button>
                      <button onClick={() => setConfirmDeleteClient(null)} className="text-xs text-slate-500 dark:text-zinc-500 hover:text-slate-700 dark:hover:text-zinc-300">{t('common.no')}</button>
                    </span>
                  ) : (
                    <button onClick={() => setConfirmDeleteClient(c.id)} className="text-xs text-red-400 hover:text-red-300">
                      {t('common.delete')}
                    </button>
                  )}
                </div>
              </div>
              {c.type === 'qbittorrent' && c.health?.status === 'error' && (
                <div className="mt-1 px-3 py-2 bg-red-100 text-red-800 dark:bg-red-950/30 dark:text-red-300 border border-red-300 dark:border-red-900 rounded text-xs">
                  {c.health.message}
                </div>
              )}
              {editingClient === c.id && (
                <EditClientForm
                  client={c}
                  onClose={() => setEditingClient(null)}
                  onSaved={(updated) => { setClients(clients.map(x => x.id === updated.id ? updated : x)); setEditingClient(null) }}
                />
              )}
              {clientTestResult[c.id] && (
                <div role="status" className={`mt-1 px-3 py-1.5 rounded text-xs flex items-center gap-2 ${clientTestResult[c.id].ok ? 'bg-emerald-100 text-emerald-800 dark:bg-emerald-900/30 dark:text-emerald-300' : 'bg-red-100 text-red-800 dark:bg-red-900/30 dark:text-red-300'}`}>
                  <span className={`inline-block w-2 h-2 rounded-full flex-shrink-0 ${clientTestResult[c.id].ok ? 'bg-emerald-500' : 'bg-red-500'}`} />
                  {clientTestResult[c.id].msg}
                </div>
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
  )
}

function EditClientForm({ client, onClose, onSaved }: { client: DownloadClient; onClose: () => void; onSaved: (c: DownloadClient) => void }) {
  const { t } = useTranslation()
  const [name, setName] = useState(client.name)
  const [type, setType] = useState(client.type || 'sabnzbd')
  const [host, setHost] = useState(client.host)
  const [port, setPort] = useState(String(client.port))
  const usesPassword = client.type === 'qbittorrent' || client.type === 'transmission' || client.type === 'nzbget' || client.type === 'deluge'
  const [credential, setCredential] = useState(usesPassword ? (client.password || '') : (client.apiKey || ''))
  const [username, setUsername] = useState(client.username || '')
  const [useSSL, setUseSSL] = useState(client.useSsl || false)
  const [urlBase, setUrlBase] = useState(client.urlBase || '')
  const [category, setCategory] = useState(client.category)
  const [categoryAudiobook, setCategoryAudiobook] = useState(client.categoryAudiobook || '')
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [pathRemap, setPathRemap] = useState(client.pathRemap || '')
  const [testing, setTesting] = useState(false)
  const [testResult, setTestResult] = useState<{ ok: boolean; msg: string } | null>(null)
  const labelCls = 'block text-xs text-slate-600 dark:text-zinc-400 mb-1'

  const handleTypeChange = (newType: string) => {
    setType(newType)
    setCredential('')
    setUsername('')
  }

  const isPasswordClient = (t: string) => t === 'qbittorrent' || t === 'transmission' || t === 'nzbget' || t === 'deluge'
  const hasUsername = (t: string) => t === 'qbittorrent' || t === 'transmission' || t === 'nzbget'

  const buildData = () => isPasswordClient(type)
    ? { ...client, name, type, host, port: parseInt(port), username: hasUsername(type) ? username : '', password: credential, apiKey: '', category, categoryAudiobook: categoryAudiobook.trim(), pathRemap: pathRemap.trim(), useSsl: useSSL, urlBase: urlBase.trim() }
    : { ...client, name, type, host, port: parseInt(port), apiKey: credential, username: '', password: '', category, categoryAudiobook: categoryAudiobook.trim(), pathRemap: pathRemap.trim(), useSsl: useSSL, urlBase: urlBase.trim() }

  const submit = async () => {
    const data = buildData()
    setSaving(true)
    setSaveError(null)
    try {
      const updated = await api.updateDownloadClient(client.id, data)
      onSaved(updated)
    } catch (err) {
      setSaveError(err instanceof Error ? err.message : 'Failed to save')
    } finally {
      setSaving(false)
    }
  }

  const handleTest = async () => {
    setTesting(true)
    setTestResult(null)
    try {
      await api.testDownloadClientConfig(buildData())
      setTestResult({ ok: true, msg: t('common.connOk') })
    } catch (err: unknown) {
      setTestResult({ ok: false, msg: t('common.connFail', { error: err instanceof Error ? err.message : 'Unknown error' }) })
    } finally {
      setTesting(false)
    }
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
            <option value="nzbget">NZBGet</option>
            <option value="qbittorrent">qBittorrent</option>
            <option value="transmission">Transmission</option>
            <option value="deluge">Deluge</option>
          </select>
        </div>
      </div>
      <div>
        <label className={labelCls}>Connection</label>
        <div className="flex gap-2">
          <input value={host} onChange={e => setHost(e.target.value)} placeholder="Host" className="flex-1 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600" />
          <input value={port} onChange={e => setPort(e.target.value)} placeholder="Port" className="w-24 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600" />
        </div>
        <p className="text-xs text-slate-500 dark:text-zinc-500 mt-1">Hostname or IP only — no <code className="font-mono">http://</code> prefix. In Docker, use the service/container name (e.g. <code className="font-mono">nzbget</code>) — not <code className="font-mono">localhost</code>.</p>
      </div>
      <div className="flex items-center gap-2">
        <input
          type="checkbox"
          id={`edit-ssl-${client.id}`}
          checked={useSSL}
          onChange={e => setUseSSL(e.target.checked)}
          className="rounded border-slate-300 dark:border-zinc-700"
        />
        <label htmlFor={`edit-ssl-${client.id}`} className={labelCls}>Use SSL</label>
      </div>
      <div>
        <label className={labelCls}>URL Base</label>
        <input value={urlBase} onChange={e => setUrlBase(e.target.value)} placeholder="/sabnzbd" className={inputCls} />
        <p className="text-xs text-slate-500 dark:text-zinc-500 mt-1">Optional path prefix for reverse proxy deployments (e.g. <code className="font-mono">/sabnzbd</code>). Leave blank for direct connections.</p>
      </div>
      {hasUsername(type) && (
        <div>
          <label className={labelCls}>Username</label>
          <input value={username} onChange={e => setUsername(e.target.value)} placeholder="Username" className={inputCls} />
        </div>
      )}
      <div>
        <label className={labelCls}>{isPasswordClient(type) ? 'Password' : 'API Key'}</label>
        <input value={credential} onChange={e => setCredential(e.target.value)} placeholder={isPasswordClient(type) ? 'Password' : 'API Key'} type="password" className={inputCls} />
      </div>
      <div>
        <label className={labelCls}>{type === 'transmission' ? 'Download Directory' : 'Category / Label'}</label>
        <input value={category} onChange={e => setCategory(e.target.value)} placeholder={type === 'transmission' ? '/downloads (leave blank for default)' : 'books'} className={inputCls} />
        {type === 'transmission' && <p className="text-xs text-slate-500 dark:text-zinc-500 mt-1">Optional absolute path override. Leave blank to use Transmission's configured default download directory.</p>}
        {type === 'deluge' && <p className="text-xs text-slate-500 dark:text-zinc-500 mt-1">Applied via the Deluge label plugin. Leave blank if the plugin is not installed.</p>}
      </div>
      {type !== 'transmission' && (
        <div>
          <label className={labelCls}>{t('settings.clients.audiobookCategoryLabel')}</label>
          <input value={categoryAudiobook} onChange={e => setCategoryAudiobook(e.target.value)} placeholder={t('settings.clients.audiobookCategoryPlaceholder')} className={inputCls} />
          <p className="text-xs text-slate-500 dark:text-zinc-500 mt-1">{t('settings.clients.audiobookCategoryHelp')}</p>
        </div>
      )}
      <PathRemapField
        id={`edit-client-path-remap-${client.id}`}
        label="Download client path remap"
        value={pathRemap}
        onChange={setPathRemap}
        placeholder={type === 'qbittorrent' ? '/downloads:/media/books' : '/media:/books'}
        help={downloadClientPathRemapHelp(type)}
      />
      {saveError && <p className="text-sm text-red-500">{saveError}</p>}
      {testResult && (
        <div role="status" className={`px-3 py-1.5 rounded text-xs flex items-center gap-2 ${testResult.ok ? 'bg-emerald-100 text-emerald-800 dark:bg-emerald-900/30 dark:text-emerald-300' : 'bg-red-100 text-red-800 dark:bg-red-900/30 dark:text-red-300'}`}>
          <span className={`inline-block w-2 h-2 rounded-full flex-shrink-0 ${testResult.ok ? 'bg-emerald-500' : 'bg-red-500'}`} />
          {testResult.msg}
        </div>
      )}
      <div className="flex gap-2 justify-end">
        <button onClick={onClose} className="px-3 py-1.5 text-sm text-slate-600 dark:text-zinc-400">{t('common.cancel')}</button>
        <button onClick={handleTest} disabled={testing || !host} className="px-3 py-1.5 text-sm text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white disabled:opacity-50">{testing ? t('common.testing') : t('common.test')}</button>
        <button onClick={submit} disabled={saving} className="px-3 py-1.5 bg-emerald-600 hover:bg-emerald-500 rounded text-sm font-medium disabled:opacity-50">{t('common.save')}</button>
      </div>
    </div>
  )
}

function AddClientForm({ onClose, onAdded }: { onClose: () => void; onAdded: (c: DownloadClient) => void }) {
  const { t } = useTranslation()
  const [name, setName] = useState('SABnzbd')
  const [type, setType] = useState<'sabnzbd' | 'nzbget' | 'qbittorrent' | 'transmission' | 'deluge'>('sabnzbd')
  const [host, setHost] = useState('')
  const [port, setPort] = useState('8080')
  const [credential, setCredential] = useState('')
  const [username, setUsername] = useState('')
  const [useSSL, setUseSSL] = useState(false)
  const [urlBase, setUrlBase] = useState('')
  const [category, setCategory] = useState('books')
  const [categoryAudiobook, setCategoryAudiobook] = useState('')
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [pathRemap, setPathRemap] = useState('')
  const [testing, setTesting] = useState(false)
  const [testResult, setTestResult] = useState<{ ok: boolean; msg: string } | null>(null)
  const labelCls = 'block text-xs text-slate-600 dark:text-zinc-400 mb-1'

  const isPasswordClient = (t: string) => t === 'qbittorrent' || t === 'transmission' || t === 'nzbget' || t === 'deluge'
  const hasUsername = (t: string) => t === 'qbittorrent' || t === 'transmission' || t === 'nzbget'

  const handleTypeChange = (newType: 'sabnzbd' | 'nzbget' | 'qbittorrent' | 'transmission' | 'deluge') => {
    setType(newType)
    setCredential('')
    setUsername('')
    if (newType === 'nzbget') {
      setName('NZBGet')
      setPort('6789')
      return
    }
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
    if (newType === 'deluge') {
      setName('Deluge')
      setPort('8112')
      return
    }
    setName('SABnzbd')
    setPort('8080')
  }

  const buildData = () => isPasswordClient(type)
    ? { name, host, port: parseInt(port), username: hasUsername(type) ? username : '', password: credential, apiKey: '', category, categoryAudiobook: categoryAudiobook.trim(), pathRemap: pathRemap.trim(), type, enabled: true, useSsl: useSSL, urlBase: urlBase.trim() }
    : { name, host, port: parseInt(port), apiKey: credential, username: '', password: '', category, categoryAudiobook: categoryAudiobook.trim(), pathRemap: pathRemap.trim(), type, enabled: true, useSsl: useSSL, urlBase: urlBase.trim() }

  const submit = async () => {
    const data = buildData()
    setSaving(true)
    setSaveError(null)
    try {
      const c = await api.addDownloadClient(data)
      onAdded(c)
    } catch (err) {
      setSaveError(err instanceof Error ? err.message : 'Failed to save')
    } finally {
      setSaving(false)
    }
  }

  const handleTest = async () => {
    setTesting(true)
    setTestResult(null)
    try {
      await api.testDownloadClientConfig(buildData())
      setTestResult({ ok: true, msg: t('common.connOk') })
    } catch (err: unknown) {
      setTestResult({ ok: false, msg: t('common.connFail', { error: err instanceof Error ? err.message : 'Unknown error' }) })
    } finally {
      setTesting(false)
    }
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
          <select value={type} onChange={e => handleTypeChange(e.target.value as 'sabnzbd' | 'nzbget' | 'qbittorrent' | 'transmission' | 'deluge')} className="w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600">
            <option value="sabnzbd">SABnzbd</option>
            <option value="nzbget">NZBGet</option>
            <option value="qbittorrent">qBittorrent</option>
            <option value="transmission">Transmission</option>
            <option value="deluge">Deluge</option>
          </select>
        </div>
      </div>
      <div>
        <label className={labelCls}>Connection</label>
        <div className="flex gap-2">
          <input value={host} onChange={e => setHost(e.target.value)} placeholder="Host" className="flex-1 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600" />
          <input value={port} onChange={e => setPort(e.target.value)} placeholder="Port" className="w-24 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600" />
        </div>
        <p className="text-xs text-slate-500 dark:text-zinc-500 mt-1">Hostname or IP only — no <code className="font-mono">http://</code> prefix. In Docker, use the service/container name (e.g. <code className="font-mono">nzbget</code>) — not <code className="font-mono">localhost</code>.</p>
      </div>
      <div className="flex items-center gap-2">
        <input
          type="checkbox"
          id="add-ssl"
          checked={useSSL}
          onChange={e => setUseSSL(e.target.checked)}
          className="rounded border-slate-300 dark:border-zinc-700"
        />
        <label htmlFor="add-ssl" className={labelCls}>Use SSL</label>
      </div>
      <div>
        <label className={labelCls}>URL Base</label>
        <input value={urlBase} onChange={e => setUrlBase(e.target.value)} placeholder="/sabnzbd" className={inputCls} />
        <p className="text-xs text-slate-500 dark:text-zinc-500 mt-1">Optional path prefix for reverse proxy deployments (e.g. <code className="font-mono">/sabnzbd</code>). Leave blank for direct connections.</p>
      </div>
      {hasUsername(type) && (
        <div>
          <label className={labelCls}>Username</label>
          <input value={username} onChange={e => setUsername(e.target.value)} placeholder="Username" className={inputCls} />
        </div>
      )}
      <div>
        <label className={labelCls}>{isPasswordClient(type) ? 'Password' : 'API Key'}</label>
        <input value={credential} onChange={e => setCredential(e.target.value)} placeholder={isPasswordClient(type) ? 'Password' : 'API Key'} type="password" className={inputCls} />
      </div>
      <div>
        <label className={labelCls}>{type === 'transmission' ? 'Download Directory' : 'Category / Label'}</label>
        <input value={category} onChange={e => setCategory(e.target.value)} placeholder={type === 'transmission' ? '/downloads (leave blank for default)' : 'books'} className={inputCls} />
        {type === 'transmission' && <p className="text-xs text-slate-500 dark:text-zinc-500 mt-1">Optional absolute path override. Leave blank to use Transmission's configured default download directory.</p>}
        {type === 'deluge' && <p className="text-xs text-slate-500 dark:text-zinc-500 mt-1">Applied via the Deluge label plugin. Leave blank if the plugin is not installed.</p>}
      </div>
      {type !== 'transmission' && (
        <div>
          <label className={labelCls}>{t('settings.clients.audiobookCategoryLabel')}</label>
          <input value={categoryAudiobook} onChange={e => setCategoryAudiobook(e.target.value)} placeholder={t('settings.clients.audiobookCategoryPlaceholder')} className={inputCls} />
          <p className="text-xs text-slate-500 dark:text-zinc-500 mt-1">{t('settings.clients.audiobookCategoryHelp')}</p>
        </div>
      )}
      <PathRemapField
        id="add-client-path-remap"
        label="Download client path remap"
        value={pathRemap}
        onChange={setPathRemap}
        placeholder={type === 'qbittorrent' ? '/downloads:/media/books' : '/media:/books'}
        help={downloadClientPathRemapHelp(type)}
      />
      {saveError && <p className="text-sm text-red-500">{saveError}</p>}
      {testResult && (
        <div role="status" className={`px-3 py-1.5 rounded text-xs flex items-center gap-2 ${testResult.ok ? 'bg-emerald-100 text-emerald-800 dark:bg-emerald-900/30 dark:text-emerald-300' : 'bg-red-100 text-red-800 dark:bg-red-900/30 dark:text-red-300'}`}>
          <span className={`inline-block w-2 h-2 rounded-full flex-shrink-0 ${testResult.ok ? 'bg-emerald-500' : 'bg-red-500'}`} />
          {testResult.msg}
        </div>
      )}
      <div className="flex gap-2 justify-end">
        <button onClick={onClose} className="px-3 py-1.5 text-sm text-slate-600 dark:text-zinc-400">{t('common.cancel')}</button>
        <button onClick={handleTest} disabled={testing || !host} className="px-3 py-1.5 text-sm text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white disabled:opacity-50">{testing ? t('common.testing') : t('common.test')}</button>
        <button onClick={submit} disabled={saving} className="px-3 py-1.5 bg-emerald-600 hover:bg-emerald-500 rounded text-sm font-medium disabled:opacity-50">{t('common.save')}</button>
      </div>
    </div>
  )
}
