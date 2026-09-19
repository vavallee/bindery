import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, NotificationConfig } from '../../api/client'
import { inputCls } from './formStyles'
import Toggle from './Toggle'
import { dangerLink } from '../../components/buttons'

// The seven trigger chips, in the order both forms render them, paired with the
// hint that says what actually sends the event. Wording checked against the
// senders: Grab is internal/api/queue.go and the scheduler's auto grab, Import
// and Failure are internal/importer/scanner.go, Health is the indexer and
// download client health checks, New book is internal/api/author_discovery.go,
// and Request is internal/api/requests.go. Upgrade has no sender at all today,
// and its hint says so rather than describing behaviour Bindery does not have.
const TRIGGER_HINTS: Array<{ key: string; label: string; labelKey?: string }> = [
  { key: 'grab', label: 'Grab' },
  { key: 'import', label: 'Import' },
  { key: 'failure', label: 'Failure' },
  { key: 'upgrade', label: 'Upgrade' },
  { key: 'health', label: 'Health' },
  { key: 'bookAnnounced', label: 'New book', labelKey: 'settings.notifications.onBookAnnouncedToggle' },
  { key: 'requestCreated', label: 'Request', labelKey: 'settings.notifications.requestToggle' },
]

// Plain descriptions under the chip row. The chips are jargon on their own, and
// a title attribute alone never reaches a touch user, so the same text is
// rendered as a list and reused as each chip's tooltip.
function TriggerHints() {
  const { t } = useTranslation()
  return (
    <ul className="mt-2 space-y-0.5 text-xs text-slate-500 dark:text-zinc-500" aria-label={t('settings.notifications.triggerHintsLabel')}>
      {TRIGGER_HINTS.map(h => (
        <li key={h.key}>
          <span className="font-medium text-slate-600 dark:text-zinc-400">{h.labelKey ? t(h.labelKey) : h.label}</span>
          {': '}
          {t(`settings.notifications.eventHints.${h.key}`)}
        </li>
      ))}
    </ul>
  )
}

// triggerTitle is the tooltip for a chip, keyed the same way as the list above.
function triggerTitle(t: (k: string) => string, key: string): string {
  return t(`settings.notifications.eventHints.${key}`)
}

export default function NotificationsTab() {
  const { t } = useTranslation()
  const [notifications, setNotifications] = useState<NotificationConfig[]>([])
  const [showAddNotification, setShowAddNotification] = useState(false)
  const [editingNotification, setEditingNotification] = useState<number | null>(null)
  const [notificationTestResult, setNotificationTestResult] = useState<Record<number, { ok: boolean; msg: string }>>({})
  const [confirmDeleteNotification, setConfirmDeleteNotification] = useState<number | null>(null)

  useEffect(() => {
    api.listNotifications().then(setNotifications).catch(console.error)
  }, [])

  return (
    <div>
      <div className="flex flex-wrap gap-2 justify-between items-center mb-4">
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
                    <Toggle
                      checked={n.enabled}
                      onChange={async () => {
                        const updated = await api.updateNotification(n.id, { ...n, enabled: !n.enabled })
                        setNotifications(notifications.map(x => x.id === n.id ? updated : x))
                      }}
                      title={n.enabled ? t('common.disable') : t('common.enable')}
                    />
                    <div className="min-w-0">
                      <h4 className={`font-medium text-sm ${!n.enabled ? 'text-slate-600 dark:text-zinc-500' : ''}`}>{n.name}</h4>
                      <p className="text-xs text-slate-600 dark:text-zinc-500 truncate mt-0.5">{n.url}</p>
                      <div className="flex flex-wrap gap-1 mt-2">
                        {n.onGrab && <span className="text-[10px] px-1.5 py-0.5 bg-slate-200 dark:bg-zinc-800 text-slate-600 dark:text-zinc-400 rounded">{t('settings.notifications.onGrab')}</span>}
                        {n.onImport && <span className="text-[10px] px-1.5 py-0.5 bg-slate-200 dark:bg-zinc-800 text-slate-600 dark:text-zinc-400 rounded">{t('settings.notifications.onImport')}</span>}
                        {n.onUpgrade && <span className="text-[10px] px-1.5 py-0.5 bg-slate-200 dark:bg-zinc-800 text-slate-600 dark:text-zinc-400 rounded">{t('settings.notifications.onUpgrade')}</span>}
                        {n.onFailure && <span className="text-[10px] px-1.5 py-0.5 bg-slate-200 dark:bg-zinc-800 text-slate-600 dark:text-zinc-400 rounded">{t('settings.notifications.onFailure')}</span>}
                        {n.onHealth && <span className="text-[10px] px-1.5 py-0.5 bg-slate-200 dark:bg-zinc-800 text-slate-600 dark:text-zinc-400 rounded">{t('settings.notifications.onHealth')}</span>}
                        {n.onBookAnnounced && <span className="text-[10px] px-1.5 py-0.5 bg-slate-200 dark:bg-zinc-800 text-slate-600 dark:text-zinc-400 rounded">{t('settings.notifications.onBookAnnounced')}</span>}
                        {n.onRequestCreated && <span className="text-[10px] px-1.5 py-0.5 bg-slate-200 dark:bg-zinc-800 text-slate-600 dark:text-zinc-400 rounded">{t('settings.notifications.onRequestCreated')}</span>}
                      </div>
                    </div>
                  </div>
                  <div className="flex items-center gap-3 flex-shrink-0">
                    <button onClick={() => setEditingNotification(editingNotification === n.id ? null : n.id)} className="text-xs text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white">{t('common.edit')}</button>
                    <button
                      onClick={async () => {
                        try {
                          await api.testNotification(n.id)
                          setNotificationTestResult(prev => ({ ...prev, [n.id]: { ok: true, msg: t('settings.notifications.testSent') } }))
                        } catch (err: unknown) {
                          setNotificationTestResult(prev => ({ ...prev, [n.id]: { ok: false, msg: t('common.connFail', { error: err instanceof Error ? err.message : 'Unknown error' }) } }))
                        }
                      }}
                      className="text-xs text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white"
                    >
                      {t('common.test')}
                    </button>
                    {confirmDeleteNotification === n.id ? (
                      <span className="flex items-center gap-1.5">
                        <span className="text-xs text-slate-500 dark:text-zinc-500">{t('common.delete')}?</span>
                        <button
                          onClick={async () => {
                            await api.deleteNotification(n.id)
                            setNotifications(notifications.filter(x => x.id !== n.id))
                            setConfirmDeleteNotification(null)
                          }}
                          className={`text-xs font-medium ${dangerLink}`}
                        >{t('common.yes')}</button>
                        <button onClick={() => setConfirmDeleteNotification(null)} className="text-xs text-slate-500 dark:text-zinc-500 hover:text-slate-700 dark:hover:text-zinc-300">{t('common.no')}</button>
                      </span>
                    ) : (
                      <button onClick={() => setConfirmDeleteNotification(n.id)} className={`text-xs ${dangerLink}`}>
                        {t('common.delete')}
                      </button>
                    )}
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
              {notificationTestResult[n.id] && (
                <div role="status" className={`mt-1 px-3 py-1.5 rounded text-xs flex items-center gap-2 ${notificationTestResult[n.id].ok ? 'bg-emerald-100 text-emerald-800 dark:bg-emerald-900/30 dark:text-emerald-300' : 'bg-red-100 text-red-800 dark:bg-red-900/30 dark:text-red-300'}`}>
                  <span className={`inline-block w-2 h-2 rounded-full flex-shrink-0 ${notificationTestResult[n.id].ok ? 'bg-emerald-500' : 'bg-red-500'}`} />
                  {notificationTestResult[n.id].msg}
                </div>
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
  )
}

function EditNotificationForm({ notification, onClose, onSaved }: { notification: NotificationConfig; onClose: () => void; onSaved: (n: NotificationConfig) => void }) {
  const { t } = useTranslation()
  const [name, setName] = useState(notification.name)
  const [url, setUrl] = useState(notification.url)
  const [topic, setTopic] = useState(notification.topic || '')
  const [method, setMethod] = useState(notification.method || 'POST')
  // Prefill the textarea pretty-printed when the stored value is non-empty;
  // empty string when the row is "{}" so the placeholder still shows.
  const [headers, setHeaders] = useState(() => {
    const raw = (notification.headers || '').trim()
    if (raw === '' || raw === '{}') return ''
    try { return JSON.stringify(JSON.parse(raw), null, 2) } catch { return raw }
  })
  const [onGrab, setOnGrab] = useState(notification.onGrab)
  const [onImport, setOnImport] = useState(notification.onImport)
  const [onFailure, setOnFailure] = useState(notification.onFailure)
  const [onUpgrade, setOnUpgrade] = useState(notification.onUpgrade)
  const [onHealth, setOnHealth] = useState(notification.onHealth)
  const [onBookAnnounced, setOnBookAnnounced] = useState(notification.onBookAnnounced ?? false)
  const [onRequestCreated, setOnRequestCreated] = useState(notification.onRequestCreated ?? false)
  const [saveError, setSaveError] = useState('')
  const [saving, setSaving] = useState(false)

  const submit = async () => {
    setSaveError('')
    const headersJSON = normalizeHeadersJSON(headers)
    if (headersJSON === null) {
      setSaveError('Headers must be valid JSON object with string values, or empty.')
      return
    }
    setSaving(true)
    try {
      const updated = await api.updateNotification(notification.id, { ...notification, name, url, topic, method, headers: headersJSON, onGrab, onImport, onFailure, onUpgrade, onHealth, onBookAnnounced, onRequestCreated })
      onSaved(updated)
    } catch (err: unknown) {
      setSaveError(t('settings.notifications.saveFailed', { error: err instanceof Error ? err.message : 'Unknown error' }))
    } finally {
      setSaving(false)
    }
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
        <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">ntfy topic (optional)</label>
        <input value={topic} onChange={e => setTopic(e.target.value)} placeholder="my-topic" className={inputCls} />
        <p className="text-xs text-slate-500 dark:text-zinc-500 mt-1">For ntfy: set the topic here and point the URL at the ntfy server root (e.g. https://ntfy.sh). Bindery then posts a JSON body ntfy renders natively, instead of printing raw JSON.</p>
      </div>
      <div>
        <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">Custom headers (JSON, optional)</label>
        <textarea
          value={headers}
          onChange={e => setHeaders(e.target.value)}
          placeholder={'{"Authorization": "Bearer YOUR_NTFY_TOKEN"}'}
          rows={3}
          className={`${inputCls} font-mono text-xs`}
        />
        <p className="text-xs text-slate-500 dark:text-zinc-500 mt-1">For ntfy auth, Discord routing, or any custom header. Leave blank to send none.</p>
      </div>
      <div>
        <p className="text-xs text-slate-600 dark:text-zinc-400 mb-2">Trigger on:</p>
        <div className="flex flex-wrap gap-2">
          <button type="button" onClick={() => setOnGrab(!onGrab)} title={triggerTitle(t, 'grab')} className={toggleCls(onGrab)}>Grab</button>
          <button type="button" onClick={() => setOnImport(!onImport)} title={triggerTitle(t, 'import')} className={toggleCls(onImport)}>Import</button>
          <button type="button" onClick={() => setOnFailure(!onFailure)} title={triggerTitle(t, 'failure')} className={toggleCls(onFailure)}>Failure</button>
          <button type="button" onClick={() => setOnUpgrade(!onUpgrade)} title={triggerTitle(t, 'upgrade')} className={toggleCls(onUpgrade)}>Upgrade</button>
          <button type="button" onClick={() => setOnHealth(!onHealth)} title={triggerTitle(t, 'health')} className={toggleCls(onHealth)}>Health</button>
          <button type="button" aria-pressed={onBookAnnounced} onClick={() => setOnBookAnnounced(!onBookAnnounced)} title={triggerTitle(t, 'bookAnnounced')} className={toggleCls(onBookAnnounced)}>{t('settings.notifications.onBookAnnouncedToggle')}</button>
          <button type="button" aria-pressed={onRequestCreated} onClick={() => setOnRequestCreated(!onRequestCreated)} title={triggerTitle(t, 'requestCreated')} className={toggleCls(onRequestCreated)}>{t('settings.notifications.requestToggle')}</button>
        </div>
        <TriggerHints />
      </div>
      {saveError && (
        <div role="alert" className="px-3 py-1.5 rounded text-xs bg-red-100 text-red-800 dark:bg-red-900/30 dark:text-red-300">
          {saveError}
        </div>
      )}
      <div className="flex gap-2 justify-end">
        <button onClick={onClose} className="px-3 py-1.5 text-sm text-slate-600 dark:text-zinc-400">Cancel</button>
        <button onClick={submit} disabled={saving} className="px-3 py-1.5 bg-emerald-600 hover:bg-emerald-500 disabled:opacity-60 disabled:cursor-not-allowed rounded text-sm font-medium">Save</button>
      </div>
    </div>
  )
}

function AddNotificationForm({ onClose, onAdded }: { onClose: () => void; onAdded: (n: NotificationConfig) => void }) {
  const { t } = useTranslation()
  const [name, setName] = useState('')
  const [url, setUrl] = useState('')
  const [topic, setTopic] = useState('')
  const [method, setMethod] = useState('POST')
  const [headers, setHeaders] = useState('')
  const [onGrab, setOnGrab] = useState(true)
  const [onImport, setOnImport] = useState(true)
  const [onFailure, setOnFailure] = useState(true)
  const [onUpgrade, setOnUpgrade] = useState(false)
  const [onHealth, setOnHealth] = useState(false)
  const [onBookAnnounced, setOnBookAnnounced] = useState(false)
  const [onRequestCreated, setOnRequestCreated] = useState(false)
  const [saveError, setSaveError] = useState('')
  const [saving, setSaving] = useState(false)

  const submit = async () => {
    setSaveError('')
    const headersJSON = normalizeHeadersJSON(headers)
    if (headersJSON === null) {
      setSaveError('Headers must be valid JSON or empty.')
      return
    }
    setSaving(true)
    try {
      const n = await api.addNotification({
        name, url, topic, method, type: 'webhook',
        headers: headersJSON,
        onGrab, onImport, onFailure, onUpgrade, onHealth, onBookAnnounced, onRequestCreated,
        enabled: true,
      })
      onAdded(n)
    } catch (err: unknown) {
      setSaveError(t('settings.notifications.saveFailed', { error: err instanceof Error ? err.message : 'Unknown error' }))
    } finally {
      setSaving(false)
    }
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
        <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">ntfy topic (optional)</label>
        <input value={topic} onChange={e => setTopic(e.target.value)} placeholder="my-topic" className={inputCls} />
        <p className="text-xs text-slate-500 dark:text-zinc-500 mt-1">For ntfy: set the topic here and point the URL at the ntfy server root (e.g. https://ntfy.sh). Bindery then posts a JSON body ntfy renders natively, instead of printing raw JSON.</p>
      </div>
      <div>
        <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">Custom headers (JSON, optional)</label>
        <textarea
          value={headers}
          onChange={e => setHeaders(e.target.value)}
          placeholder={'{"Authorization": "Bearer YOUR_NTFY_TOKEN"}'}
          rows={3}
          className={`${inputCls} font-mono text-xs`}
        />
        <p className="text-xs text-slate-500 dark:text-zinc-500 mt-1">For ntfy auth, Discord routing, or any custom header. Leave blank to send none.</p>
      </div>
      <div>
        <p className="text-xs text-slate-600 dark:text-zinc-400 mb-2">Trigger on:</p>
        <div className="flex flex-wrap gap-2">
          <button type="button" onClick={() => setOnGrab(!onGrab)} title={triggerTitle(t, 'grab')} className={toggleCls(onGrab)}>Grab</button>
          <button type="button" onClick={() => setOnImport(!onImport)} title={triggerTitle(t, 'import')} className={toggleCls(onImport)}>Import</button>
          <button type="button" onClick={() => setOnFailure(!onFailure)} title={triggerTitle(t, 'failure')} className={toggleCls(onFailure)}>Failure</button>
          <button type="button" onClick={() => setOnUpgrade(!onUpgrade)} title={triggerTitle(t, 'upgrade')} className={toggleCls(onUpgrade)}>Upgrade</button>
          <button type="button" onClick={() => setOnHealth(!onHealth)} title={triggerTitle(t, 'health')} className={toggleCls(onHealth)}>Health</button>
          <button type="button" aria-pressed={onBookAnnounced} onClick={() => setOnBookAnnounced(!onBookAnnounced)} title={triggerTitle(t, 'bookAnnounced')} className={toggleCls(onBookAnnounced)}>{t('settings.notifications.onBookAnnouncedToggle')}</button>
          <button type="button" aria-pressed={onRequestCreated} onClick={() => setOnRequestCreated(!onRequestCreated)} title={triggerTitle(t, 'requestCreated')} className={toggleCls(onRequestCreated)}>{t('settings.notifications.requestToggle')}</button>
        </div>
        <TriggerHints />
      </div>
      {saveError && (
        <div role="alert" className="px-3 py-1.5 rounded text-xs bg-red-100 text-red-800 dark:bg-red-900/30 dark:text-red-300">
          {saveError}
        </div>
      )}
      <div className="flex gap-2 justify-end">
        <button onClick={onClose} className="px-3 py-1.5 text-sm text-slate-600 dark:text-zinc-400">Cancel</button>
        <button onClick={submit} disabled={saving} className="px-3 py-1.5 bg-emerald-600 hover:bg-emerald-500 disabled:opacity-60 disabled:cursor-not-allowed rounded text-sm font-medium">Save</button>
      </div>
    </div>
  )
}

// normalizeHeadersJSON coerces the headers textarea value into the JSON
// string the backend expects. Returns null on parse error so the caller
// can refuse to submit. Empty input maps to "{}" — semantically equal to
// "send no extra headers".
function normalizeHeadersJSON(raw: string): string | null {
  const trimmed = raw.trim()
  if (trimmed === '') return '{}'
  try {
    const parsed = JSON.parse(trimmed)
    if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) return null
    for (const v of Object.values(parsed)) {
      if (typeof v !== 'string') return null
    }
    return JSON.stringify(parsed)
  } catch {
    return null
  }
}
