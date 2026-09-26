// AdvancedTab edits the settings key/value store directly, driven entirely by
// GET /api/v1/settings/descriptors (#2311).
//
// The point of the tab is that it contains no per setting code. A descriptor's
// type and values choose the control, min and max constrain it, the server's
// description is the help text, restartRequired is a badge, state decides
// whether the key is editable at all, and secret/writable decide whether a
// value may be shown or sent. So the next niche knob needs a registry entry and
// nothing here: no label, no hint, no control, no locale entry.
//
// Two deliberate refusals, both of them the reason the tab is worth having:
//
//   - A secret's value never reaches the DOM. The server already omits secrets
//     from GET /setting and 404s the single key read, so there is normally
//     nothing to leak; this component also never reads a stored value for a
//     descriptor marked secret, so a future handler that regressed would still
//     not surface one here.
//   - restartRequired is a badge next to the control, not a sentence in the
//     description. A setting that silently does nothing until a restart is a
//     support thread, so the marker sits where the operator is typing.
//
// Nothing has moved off the curated tabs. This tab is additive: the settings
// most people touch stay where they are, and whether any of them should move is
// a separate change (#2311 says so itself).

import { ReactNode, useCallback, useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, SettingDescriptor } from '../../api/client'
import { useAuth } from '../../auth/AuthContext'
import { inputCls } from './formStyles'
import SaveButton from './SaveButton'
import Toggle from './Toggle'

// A row is either a descriptor (known key) or a stored key with no descriptor.
// The second case is kept rather than hidden: a row written by an older or
// newer build is still real, and the operator is the only one who can decide to
// remove it.
interface UnknownRow {
  key: string
  value: string
}

const badgeCls = 'text-[10px] px-1.5 py-0.5 rounded font-medium leading-none whitespace-nowrap'

function controlId(key: string): string {
  return `setting-${key}`
}

// Namespace is the key prefix before the first dot, used as a group heading so
// the grouping needs no hand maintained table. "telemetry.install_id" groups
// under "telemetry"; a key with no dot groups under itself.
function namespaceOf(key: string): string {
  const dot = key.indexOf('.')
  return dot === -1 ? key : key.slice(0, dot)
}

export default function AdvancedTab() {
  const { t } = useTranslation()
  const { isAdmin } = useAuth()

  const [descriptors, setDescriptors] = useState<SettingDescriptor[]>([])
  const [stored, setStored] = useState<Record<string, string>>({})
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [filter, setFilter] = useState('')

  // Per row transient state, keyed by setting key.
  const [edits, setEdits] = useState<Record<string, string>>({})
  const [saving, setSaving] = useState<string | null>(null)
  const [results, setResults] = useState<Record<string, 'idle' | 'saved' | 'error'>>({})
  const [errors, setErrors] = useState<Record<string, string>>({})

  const load = useCallback(async () => {
    setLoading(true)
    setLoadError(null)
    try {
      const [descs, settings] = await Promise.all([api.listSettingDescriptors(), api.listSettings()])
      setDescriptors(descs ?? [])
      const map: Record<string, string> = {}
      for (const s of settings ?? []) map[s.key] = s.value
      setStored(map)
    } catch (e) {
      setLoadError(e instanceof Error ? e.message : String(e))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    // The registry endpoint is admin only, so a non admin must not even ask:
    // the request would 403 and the only thing to show is the notice below.
    if (!isAdmin) {
      setLoading(false)
      return
    }
    void load()
  }, [isAdmin, load])

  // A secret's stored value is never read, whatever the API returned.
  const storedValue = useCallback(
    (d: SettingDescriptor): string => (d.secret ? '' : (stored[d.key] ?? '')),
    [stored],
  )

  const currentValue = useCallback(
    (d: SettingDescriptor): string => edits[d.key] ?? storedValue(d),
    [edits, storedValue],
  )

  const save = useCallback(async (key: string, value: string) => {
    setSaving(key)
    setErrors(e => ({ ...e, [key]: '' }))
    try {
      await api.setSetting(key, value)
      setStored(s => ({ ...s, [key]: value }))
      setEdits(e => {
        const next = { ...e }
        delete next[key]
        return next
      })
      setResults(r => ({ ...r, [key]: 'saved' }))
      setTimeout(() => setResults(r => ({ ...r, [key]: 'idle' })), 2000)
    } catch (e) {
      setResults(r => ({ ...r, [key]: 'error' }))
      setErrors(err => ({ ...err, [key]: e instanceof Error ? e.message : String(e) }))
    } finally {
      setSaving(null)
    }
  }, [])

  const reset = useCallback(async (key: string) => {
    setSaving(key)
    setErrors(e => ({ ...e, [key]: '' }))
    try {
      await api.deleteSetting(key)
      setStored(s => {
        const next = { ...s }
        delete next[key]
        return next
      })
      setEdits(e => {
        const next = { ...e }
        delete next[key]
        return next
      })
    } catch (e) {
      setErrors(err => ({ ...err, [key]: e instanceof Error ? e.message : String(e) }))
    } finally {
      setSaving(null)
    }
  }, [])

  const needle = filter.trim().toLowerCase()
  const matches = useCallback(
    (key: string, description: string) =>
      needle === '' || key.toLowerCase().includes(needle) || description.toLowerCase().includes(needle),
    [needle],
  )

  const visible = useMemo(
    () => descriptors.filter(d => matches(d.key, d.description)),
    [descriptors, matches],
  )

  const active = visible.filter(d => d.state === 'active')
  const internal = visible.filter(d => d.state === 'internal')
  const inert = visible.filter(d => d.state === 'inert')

  const unknown = useMemo<UnknownRow[]>(() => {
    const known = new Set(descriptors.map(d => d.key))
    return Object.entries(stored)
      .filter(([key]) => !known.has(key))
      .filter(([key, value]) => matches(key, value))
      .map(([key, value]) => ({ key, value }))
      .sort((a, b) => a.key.localeCompare(b.key))
  }, [descriptors, stored, matches])

  const namespaces = useMemo(() => {
    const groups = new Map<string, SettingDescriptor[]>()
    for (const d of active) {
      const ns = namespaceOf(d.key)
      const list = groups.get(ns)
      if (list) list.push(d)
      else groups.set(ns, [d])
    }
    return [...groups.entries()].sort((a, b) => a[0].localeCompare(b[0]))
  }, [active])

  if (!isAdmin) {
    return (
      <div className="rounded-lg border border-amber-300 dark:border-amber-900 bg-amber-50 dark:bg-amber-950/40 p-4 text-sm text-amber-800 dark:text-amber-300">
        {t('settings.advanced.adminOnly', 'Advanced settings are available to administrators only.')}
      </div>
    )
  }

  function renderBadges(d: SettingDescriptor) {
    return (
      <>
        {d.restartRequired && (
          <span className={`${badgeCls} bg-amber-100 text-amber-800 dark:bg-amber-950 dark:text-amber-400`}>
            {t('settings.advanced.restartRequired', 'Restart required')}
          </span>
        )}
        {d.secret && (
          <span className={`${badgeCls} bg-slate-200 text-slate-700 dark:bg-zinc-800 dark:text-zinc-300`}>
            {t('settings.advanced.secret', 'Value hidden')}
          </span>
        )}
        {d.state === 'internal' && (
          <span className={`${badgeCls} bg-sky-100 text-sky-800 dark:bg-sky-950 dark:text-sky-300`}>
            {t('settings.advanced.internal', 'Bindery writes this')}
          </span>
        )}
        {d.state === 'inert' && (
          <span className={`${badgeCls} bg-slate-200 text-slate-600 dark:bg-zinc-800 dark:text-zinc-400`}>
            {t('settings.advanced.inert', 'Nothing reads this')}
          </span>
        )}
      </>
    )
  }

  // Editable is the whole gate, in one place: only an active key, only one the
  // generic PUT accepts. An internal key is Bindery's own bookkeeping and hand
  // editing it corrupts whatever wrote it; an inert key changes nothing; a non
  // writable secret is refused by the server with a 403.
  function isEditable(d: SettingDescriptor): boolean {
    return d.state === 'active' && d.writable
  }

  function renderControl(d: SettingDescriptor) {
    const id = controlId(d.key)
    const value = currentValue(d)
    const busy = saving === d.key

    if (d.secret) {
      // Write only: blank on every render, and a save replaces the stored value.
      return (
        <input
          id={id}
          type="password"
          autoComplete="new-password"
          className={inputCls}
          value={edits[d.key] ?? ''}
          placeholder={t('settings.advanced.secretPlaceholder', 'Hidden. Type a new value to replace it')}
          onChange={e => setEdits(prev => ({ ...prev, [d.key]: e.target.value }))}
          disabled={busy}
        />
      )
    }

    switch (d.type) {
      case 'bool': {
        const checked = (value || d.default).toLowerCase() === 'true'
        return (
          <Toggle
            checked={checked}
            title={d.key}
            disabled={busy}
            onChange={() => void save(d.key, checked ? 'false' : 'true')}
          />
        )
      }
      case 'enum':
        return (
          <select
            id={id}
            className={inputCls}
            value={value}
            onChange={e => setEdits(prev => ({ ...prev, [d.key]: e.target.value }))}
            disabled={busy}
          >
            <option value="">
              {d.default
                ? t('settings.advanced.defaultOption', 'Default ({{value}})', { value: d.default })
                : t('settings.advanced.notSet', 'Not set')}
            </option>
            {(d.values ?? []).map(v => (
              <option key={v} value={v}>{v}</option>
            ))}
          </select>
        )
      case 'int':
        return (
          <input
            id={id}
            type="number"
            className={inputCls}
            min={d.min || undefined}
            max={d.max || undefined}
            value={value}
            placeholder={d.default}
            onChange={e => setEdits(prev => ({ ...prev, [d.key]: e.target.value }))}
            disabled={busy}
          />
        )
      default:
        // string and duration. Duration is text rather than a stepper because
        // the stored form is a Go duration, and a bounded key can still accept
        // a sentinel outside the bounds (author.discovery_interval defaults to
        // "off" with a 24h minimum), so the range is stated, not enforced.
        return (
          <input
            id={id}
            type="text"
            className={inputCls}
            value={value}
            placeholder={d.default}
            onChange={e => setEdits(prev => ({ ...prev, [d.key]: e.target.value }))}
            disabled={busy}
          />
        )
    }
  }

  function renderRow(d: SettingDescriptor) {
    const editable = isEditable(d)
    const dirty = edits[d.key] !== undefined && edits[d.key] !== storedValue(d)
    // A secret cannot be deleted through the generic endpoint (403), and a key
    // with no stored row has nothing to reset.
    const resettable = !d.secret && stored[d.key] !== undefined
    const bounds = [d.min, d.max].filter(Boolean)

    return (
      <div
        key={d.key}
        data-testid={`setting-row-${d.key}`}
        className="py-3 border-t border-slate-200 dark:border-zinc-800 first:border-t-0"
      >
        <div className="flex flex-wrap items-center gap-2 mb-1">
          {editable ? (
            <label htmlFor={controlId(d.key)} className="font-mono text-xs text-slate-900 dark:text-zinc-100">
              {d.key}
            </label>
          ) : (
            <span className="font-mono text-xs text-slate-900 dark:text-zinc-100">{d.key}</span>
          )}
          <span className="text-[10px] uppercase tracking-wide text-slate-400 dark:text-zinc-600">{d.type}</span>
          {renderBadges(d)}
        </div>

        <p className="text-xs text-slate-600 dark:text-zinc-400 mb-2">{d.description}</p>

        {editable ? (
          <div className="flex flex-wrap items-center gap-2">
            <div className={d.type === 'bool' ? '' : 'w-full sm:w-80'}>{renderControl(d)}</div>
            {d.type !== 'bool' && (
              <SaveButton
                result={results[d.key] ?? 'idle'}
                saving={saving === d.key}
                disabled={!dirty}
                onClick={() => void save(d.key, edits[d.key] ?? '')}
                ariaLabel={t('settings.advanced.saveKey', 'Save {{key}}', { key: d.key })}
              />
            )}
            {resettable && (
              <button
                type="button"
                onClick={() => void reset(d.key)}
                disabled={saving === d.key}
                className="px-3 py-2 text-xs rounded font-medium border border-slate-300 dark:border-zinc-700 text-slate-700 dark:text-zinc-300 hover:bg-slate-100 dark:hover:bg-zinc-800 disabled:opacity-50"
              >
                {t('settings.advanced.reset', 'Reset to default')}
              </button>
            )}
          </div>
        ) : (
          <div className="flex flex-wrap items-center gap-2">
            <p className="text-xs text-slate-500 dark:text-zinc-500">
              {d.secret
                ? t('settings.advanced.secretElsewhere', 'Set on its own settings screen, never through this tab.')
                : d.state === 'internal'
                  ? t('settings.advanced.internalNote', 'Bindery writes this value itself. Editing it by hand corrupts whatever wrote it.')
                  : t('settings.advanced.inertNote', 'Nothing reads this key any more. The row is kept so an upgrade does not turn it into an unrecognised key.')}
            </p>
            {resettable && (
              <button
                type="button"
                onClick={() => void reset(d.key)}
                disabled={saving === d.key}
                className="px-3 py-2 text-xs rounded font-medium border border-slate-300 dark:border-zinc-700 text-slate-700 dark:text-zinc-300 hover:bg-slate-100 dark:hover:bg-zinc-800 disabled:opacity-50"
              >
                {t('settings.advanced.reset', 'Reset to default')}
              </button>
            )}
          </div>
        )}

        {!d.secret && !editable && stored[d.key] !== undefined && (
          <pre className="mt-2 max-h-24 overflow-auto text-[11px] font-mono text-slate-600 dark:text-zinc-400 bg-slate-100 dark:bg-zinc-900 rounded p-2 whitespace-pre-wrap break-all">
            {stored[d.key]}
          </pre>
        )}

        {bounds.length > 0 && editable && (
          <p className="mt-1 text-[11px] text-slate-500 dark:text-zinc-500">
            {d.min && (
              <span>
                {t('settings.advanced.min', 'Minimum')} <span className="font-mono">{d.min}</span>
              </span>
            )}
            {d.min && d.max && <span aria-hidden="true"> · </span>}
            {d.max && (
              <span>
                {t('settings.advanced.max', 'Maximum')} <span className="font-mono">{d.max}</span>
              </span>
            )}
          </p>
        )}

        {errors[d.key] && (
          <p className="mt-1 text-xs text-red-600 dark:text-red-400">{errors[d.key]}</p>
        )}
      </div>
    )
  }

  function renderUnknownRow(r: UnknownRow) {
    return (
      <div
        key={r.key}
        data-testid={`setting-row-${r.key}`}
        className="py-3 border-t border-slate-200 dark:border-zinc-800 first:border-t-0"
      >
        <div className="flex flex-wrap items-center gap-2 mb-1">
          <span className="font-mono text-xs text-slate-900 dark:text-zinc-100">{r.key}</span>
          <span className={`${badgeCls} bg-red-100 text-red-700 dark:bg-red-950 dark:text-red-400`}>
            {t('settings.advanced.unknown', 'Unrecognised')}
          </span>
        </div>
        <p className="text-xs text-slate-600 dark:text-zinc-400 mb-2">
          {t(
            'settings.advanced.unknownNote',
            'Bindery does not recognise this key, so nothing reads it. It was probably written by another version. Saving it is refused; removing it is safe.',
          )}
        </p>
        <pre className="text-[11px] font-mono text-slate-600 dark:text-zinc-400 bg-slate-100 dark:bg-zinc-900 rounded p-2 max-h-24 overflow-auto whitespace-pre-wrap break-all">
          {r.value}
        </pre>
        <button
          type="button"
          onClick={() => void reset(r.key)}
          disabled={saving === r.key}
          className="mt-2 px-3 py-2 text-xs rounded font-medium border border-slate-300 dark:border-zinc-700 text-slate-700 dark:text-zinc-300 hover:bg-slate-100 dark:hover:bg-zinc-800 disabled:opacity-50"
        >
          {t('settings.advanced.remove', 'Remove row')}
        </button>
        {errors[r.key] && <p className="mt-1 text-xs text-red-600 dark:text-red-400">{errors[r.key]}</p>}
      </div>
    )
  }

  function section(title: string, body: ReactNode) {
    return (
      <div className="bg-slate-100 dark:bg-zinc-900 rounded-lg p-4">
        <h4 className="text-sm font-semibold mb-2">{title}</h4>
        {body}
      </div>
    )
  }

  const nothingVisible =
    !loading && !loadError && active.length === 0 && internal.length === 0 && inert.length === 0 && unknown.length === 0

  return (
    <div className="space-y-4">
      <div className="bg-slate-100 dark:bg-zinc-900 rounded-lg p-4">
        <h3 className="font-semibold mb-1">{t('settings.advanced.title', 'Advanced')}</h3>
        <p className="text-xs text-slate-600 dark:text-zinc-400">
          {t(
            'settings.advanced.intro',
            'Every setting Bindery stores, straight from its key/value store. The settings most people need have their own tab; this screen is for the rare ones, and for seeing what a key holds. Descriptions come from the server and are not translated.',
          )}
        </p>
        <div className="mt-3">
          <input
            type="text"
            aria-label={t('settings.advanced.filterLabel', 'Filter settings')}
            placeholder={t('settings.advanced.filterPlaceholder', 'Filter by key or description')}
            className={`${inputCls} sm:w-80`}
            value={filter}
            onChange={e => setFilter(e.target.value)}
          />
        </div>
      </div>

      {loading && <p className="text-sm text-slate-600 dark:text-zinc-400">{t('common.loading')}</p>}
      {loadError && (
        <p className="text-sm text-red-600 dark:text-red-400">
          {t('settings.advanced.loadError', 'Could not load the settings registry: {{error}}', { error: loadError })}
        </p>
      )}
      {nothingVisible && (
        <p className="text-sm text-slate-600 dark:text-zinc-400">{t('common.noResults')}</p>
      )}

      {namespaces.map(([ns, rows]) => section(ns, <div>{rows.map(renderRow)}</div>))}

      {internal.length > 0 &&
        section(
          t('settings.advanced.sectionInternal', "Bindery's own bookkeeping"),
          <div>{internal.map(renderRow)}</div>,
        )}

      {inert.length > 0 &&
        section(t('settings.advanced.sectionInert', 'No longer used'), <div>{inert.map(renderRow)}</div>)}

      {unknown.length > 0 &&
        section(
          t('settings.advanced.sectionUnknown', 'Unrecognised keys'),
          <div>{unknown.map(renderUnknownRow)}</div>,
        )}
    </div>
  )
}
