import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, LogEntry } from '../../api/client'
import Toggle from './Toggle'

function formatBackupSize(bytes: number): string {
  if (!bytes || bytes <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.min(units.length - 1, Math.floor(Math.log(bytes) / Math.log(1024)))
  return `${(bytes / Math.pow(1024, i)).toFixed(1)} ${units[i]}`
}

function formatRelativeTime(iso: string): string {
  const t = Date.parse(iso)
  if (isNaN(t)) return ''
  const diffSec = Math.round((Date.now() - t) / 1000)
  const abs = Math.abs(diffSec)
  if (abs < 60) return diffSec >= 0 ? 'just now' : 'in a moment'
  const mins = Math.round(diffSec / 60)
  if (Math.abs(mins) < 60) return mins >= 0 ? `${mins}m ago` : `in ${-mins}m`
  const hrs = Math.round(diffSec / 3600)
  if (Math.abs(hrs) < 24) return hrs >= 0 ? `${hrs}h ago` : `in ${-hrs}h`
  const days = Math.round(diffSec / 86400)
  return days >= 0 ? `${days}d ago` : `in ${-days}d`
}

export default function LogsTab() {
  const { t, i18n } = useTranslation()
  const [logEntries, setLogEntries] = useState<LogEntry[]>([])
  const [logLevel, setLogLevel] = useState<string>('info')
  const [logFilter, setLogFilter] = useState<string>('all')
  const [logAutoRefresh, setLogAutoRefresh] = useState(true)
  const [logComponent, setLogComponent] = useState<string>('')
  const [logSearch, setLogSearch] = useState<string>('')
  const [logFrom, setLogFrom] = useState<string>('')
  const [logTo, setLogTo] = useState<string>('')
  const [logPage, setLogPage] = useState(0)
  const logPageSize = 200
  const logBottomRef = useRef<HTMLDivElement>(null)
  const [settings, setSettings] = useState<Record<string, string>>({})
  const [saving, setSaving] = useState<string | null>(null)
  const [backups, setBackups] = useState<Array<{ name: string; size: number; modTime: string }>>([])
  const [creatingBackup, setCreatingBackup] = useState(false)
  const [deletingBackup, setDeletingBackup] = useState<string | null>(null)

  const fetchLogs = (page = 0) => {
    api.getLogs({
      level: logFilter !== 'all' ? logFilter : undefined,
      component: logComponent || undefined,
      from: logFrom || undefined,
      to: logTo || undefined,
      q: logSearch || undefined,
      limit: logPageSize,
      offset: page * logPageSize,
    }).then(entries => {
      setLogEntries(entries ?? [])
      setLogPage(page)
    }).catch(console.error)
  }

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
      setBackups(prev => [result, ...prev])
      alert(`Backup created: ${result.name}`)
    } catch (err) {
      alert('Backup failed: ' + (err instanceof Error ? err.message : 'Unknown error'))
    } finally {
      setCreatingBackup(false)
    }
  }

  const handleDeleteBackup = async (filename: string) => {
    if (!confirm(`Delete backup ${filename}?`)) return
    setDeletingBackup(filename)
    try {
      await api.deleteBackup(filename)
      setBackups(prev => prev.filter(b => b.name !== filename))
    } catch (err) {
      alert('Delete failed: ' + (err instanceof Error ? err.message : 'Unknown error'))
    } finally {
      setDeletingBackup(null)
    }
  }

  useEffect(() => {
    api.getLogLevel().then(r => setLogLevel(r.level.toLowerCase())).catch(console.error)
    fetchLogs()
    api.listSettings().then(list => {
      const map: Record<string, string> = {}
      list.forEach(s => { map[s.key] = s.value })
      setSettings(map)
    }).catch(console.error)
    api.listBackups().then(setBackups).catch(console.error)
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  // Auto-refresh logs every 5 s while the toggle is on.
  useEffect(() => {
    if (!logAutoRefresh) return
    const id = setInterval(() => { fetchLogs(logPage) }, 5000)
    return () => clearInterval(id)
  }, [logAutoRefresh, logPage, logFilter, logComponent, logFrom, logTo, logSearch]) // eslint-disable-line react-hooks/exhaustive-deps

  return (
    <div>
      {/* Toolbar row 1: heading + level pills + runtime level + auto-refresh */}
      <div className="flex flex-wrap items-center gap-3 mb-3">
        <h3 className="text-lg font-semibold mr-auto">{t('settings.logs.heading')}</h3>

        {/* Level filter pills */}
        <div className="flex items-center gap-1.5 text-xs">
          <span className="text-[10px] font-medium uppercase text-slate-400 dark:text-zinc-600 mr-1">View</span>
          {(['all', 'debug', 'info', 'warn', 'error'] as const).map(f => (
            <button
              key={f}
              onClick={() => { setLogFilter(f); fetchLogs(0) }}
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
          <span className="text-slate-500 dark:text-zinc-500 font-medium">Runtime level</span>
          <select
            value={logLevel}
            onChange={async e => {
              const l = e.target.value
              await api.setLogLevel(l).catch(console.error)
              setLogLevel(l)
            }}
            title="Controls which log levels are written to the database"
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
          onClick={() => fetchLogs(0)}
          className="text-xs px-2.5 py-1 rounded border border-slate-300 dark:border-zinc-700 text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white"
        >
          {t('common.refresh')}
        </button>
      </div>

      {/* Toolbar row 2: date range + component + search */}
      <div className="flex flex-wrap items-center gap-2 mb-3 text-xs">
        <div className="flex items-center gap-1">
          <span className="text-slate-500 dark:text-zinc-500">{t('settings.logs.from')}</span>
          <input
            type="datetime-local"
            value={logFrom}
            onChange={e => setLogFrom(e.target.value)}
            className="bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-2 py-1 text-xs"
          />
        </div>
        <div className="flex items-center gap-1">
          <span className="text-slate-500 dark:text-zinc-500">{t('settings.logs.to')}</span>
          <input
            type="datetime-local"
            value={logTo}
            onChange={e => setLogTo(e.target.value)}
            className="bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-2 py-1 text-xs"
          />
        </div>
        <input
          type="text"
          placeholder={t('settings.logs.componentPlaceholder')}
          value={logComponent}
          onChange={e => setLogComponent(e.target.value)}
          className="bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-2 py-1 text-xs w-32"
        />
        <input
          type="text"
          placeholder={t('settings.logs.searchPlaceholder')}
          value={logSearch}
          onChange={e => setLogSearch(e.target.value)}
          className="bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-2 py-1 text-xs w-48"
        />
        <button
          onClick={() => fetchLogs(0)}
          className="px-3 py-1 rounded bg-slate-700 dark:bg-zinc-600 text-white hover:bg-slate-600 dark:hover:bg-zinc-500 text-xs"
        >
          {t('common.search')}
        </button>
        <button
          onClick={() => {
            setLogFrom(''); setLogTo(''); setLogComponent(''); setLogSearch(''); setLogFilter('all')
            setTimeout(() => fetchLogs(0), 0)
          }}
          className="px-2 py-1 rounded border border-slate-300 dark:border-zinc-700 text-slate-500 dark:text-zinc-500 text-xs"
        >
          {t('settings.logs.clearFilters')}
        </button>
      </div>

      {/* Log output */}
      <div className="font-mono text-xs bg-slate-50 dark:bg-black rounded-lg border border-slate-200 dark:border-zinc-900 overflow-auto max-h-[60vh]">
        {(() => {
          const formatAttr = (k: string, v: unknown) => {
            const s = String(v)
            return /[\s=]/.test(s) ? `${k}="${s.replace(/"/g, '\\"')}"` : `${k}=${s}`
          }
          if (logEntries.length === 0) {
            return <p className="text-slate-500 dark:text-zinc-600 p-4 text-center">{t('settings.logs.noEntries')}</p>
          }
          return (
            <table className="w-full border-collapse table-fixed">
              <colgroup>
                <col className="w-36" />
                <col className="w-14" />
                <col className="w-24" />
                <col />
                <col className="w-2/5" />
              </colgroup>
              <tbody>
                {logEntries.map((e, i) => {
                  const levelCls =
                    e.level === 'ERROR' ? 'text-red-500 dark:text-red-400' :
                    e.level === 'WARN'  ? 'text-amber-600 dark:text-amber-400' :
                    e.level === 'DEBUG' ? 'text-slate-400 dark:text-zinc-500' :
                    'text-emerald-600 dark:text-emerald-400'
                  // Support both ring buffer (time/msg/attrs) and DB (ts/message/fields) shapes.
                  const rawTs = e.ts ?? e.time ?? ''
                  const d = new Date(rawTs)
                  const ts = rawTs ? d.toLocaleString(i18n.resolvedLanguage, {
                    day: '2-digit', month: '2-digit',
                    hour: '2-digit', minute: '2-digit', second: '2-digit',
                    hour12: false,
                  }) : ''
                  const msgText = e.message ?? e.msg ?? ''
                  const attrsObj = e.fields ?? e.attrs ?? {}
                  const attrStr = Object.entries(attrsObj).map(([k, v]) => formatAttr(k, v)).join(' ')
                  return (
                    <tr key={e.id ?? i} className="border-b border-slate-200 dark:border-zinc-900 hover:bg-slate-100 dark:hover:bg-zinc-900/50">
                      <td className="pl-3 pr-2 py-0.5 text-slate-500 dark:text-zinc-600 whitespace-nowrap align-top" title={d.toISOString()}>{ts}</td>
                      <td className={`pr-2 py-0.5 whitespace-nowrap font-semibold align-top ${levelCls}`}>{e.level}</td>
                      <td className="pr-2 py-0.5 text-slate-500 dark:text-zinc-500 whitespace-nowrap align-top">{e.component ?? ''}</td>
                      <td className="pr-2 py-0.5 text-slate-800 dark:text-zinc-200 break-words whitespace-pre-wrap align-top">{msgText}</td>
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

      {/* Pagination */}
      <div className="flex items-center gap-3 mt-2 text-xs text-slate-600 dark:text-zinc-400">
        <button
          disabled={logPage === 0}
          onClick={() => fetchLogs(logPage - 1)}
          className="px-2 py-1 rounded border border-slate-300 dark:border-zinc-700 disabled:opacity-40"
        >
          ← {t('common.prev')}
        </button>
        <span>{t('settings.logs.page', { page: logPage + 1 })}</span>
        <button
          disabled={logEntries.length < logPageSize}
          onClick={() => fetchLogs(logPage + 1)}
          className="px-2 py-1 rounded border border-slate-300 dark:border-zinc-700 disabled:opacity-40"
        >
          {t('common.next')} →
        </button>
      </div>

      <p className="text-xs text-slate-500 dark:text-zinc-600 mt-2">
        {t('settings.logs.persistNote')}
      </p>

      {/* Log retention */}
      <section className="mt-8">
        <h3 className="text-base font-semibold mb-3 text-slate-800 dark:text-zinc-200">{t('settings.general.logRetention')}</h3>
        <div className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900">
          <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-3">
            <div className="min-w-0">
              <label className="block text-sm font-medium text-slate-800 dark:text-zinc-200">{t('settings.general.logRetentionLabel')}</label>
              <p className="text-xs text-slate-600 dark:text-zinc-500 mt-0.5">{t('settings.general.logRetentionHint')}</p>
            </div>
            <div className="flex items-center gap-2 flex-shrink-0">
              <input
                type="number"
                min={1}
                max={365}
                value={settings['log.retention_days'] ?? '14'}
                onChange={e => setSettings(s => ({ ...s, 'log.retention_days': e.target.value }))}
                className="w-20 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-2 py-1 text-sm text-right"
              />
              <span className="text-sm text-slate-600 dark:text-zinc-400">{t('settings.general.days')}</span>
              <button
                onClick={() => saveSetting('log.retention_days')}
                disabled={saving === 'log.retention_days'}
                className="px-3 py-1.5 bg-emerald-600 hover:bg-emerald-500 rounded text-sm font-medium disabled:opacity-50"
              >
                {saving === 'log.retention_days' ? t('common.saving') : t('common.save')}
              </button>
            </div>
          </div>
        </div>
      </section>

      {/* Backup */}
      <section className="mt-8">
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
                  <li key={b.name} className="flex items-center justify-between text-xs text-slate-600 dark:text-zinc-400">
                    <span>
                      <span className="font-mono">{b.name}</span>
                      <span className="ml-2 text-slate-500 dark:text-zinc-500">{formatBackupSize(b.size)} · {formatRelativeTime(b.modTime)}</span>
                    </span>
                    <button
                      onClick={() => handleDeleteBackup(b.name)}
                      disabled={deletingBackup === b.name}
                      className="ml-4 text-red-600 dark:text-red-400 hover:underline disabled:opacity-50"
                    >
                      {deletingBackup === b.name ? '…' : t('common.delete')}
                    </button>
                  </li>
                ))}
              </ul>
            </div>
          )}
        </div>
      </section>

      {/* Telemetry */}
      <section className="mt-8">
        <h3 className="text-base font-semibold mb-3 text-slate-800 dark:text-zinc-200">{t('settings.general.telemetry')}</h3>
        <div className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900 space-y-3">
          <div className="flex items-start justify-between gap-4">
            <div>
              <label className="block text-sm font-medium text-slate-800 dark:text-zinc-200">{t('settings.general.telemetryLabel')}</label>
              <p className="text-xs text-slate-600 dark:text-zinc-500 mt-0.5">{t('settings.general.telemetryHint')}</p>
            </div>
            <Toggle
              checked={(settings['telemetry.enabled'] ?? 'true') !== 'false'}
              onChange={async () => {
                const current = (settings['telemetry.enabled'] ?? 'true').toLowerCase()
                const next = current !== 'false' ? 'false' : 'true'
                setSettings(s => ({ ...s, 'telemetry.enabled': next }))
                await api.setSetting('telemetry.enabled', next).catch(console.error)
              }}
              title={(settings['telemetry.enabled'] ?? 'true') !== 'false' ? t('common.disable') : t('common.enable')}
            />
          </div>
          <p className="text-xs text-slate-500 dark:text-zinc-600">
            {t('settings.general.telemetryDetail')}
          </p>
        </div>
      </section>
    </div>
  )
}
