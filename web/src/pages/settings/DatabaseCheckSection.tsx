import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, type DBIntegrityReport, type DBCleanupPreview, type DBCleanupResult, type DBCleanupAction } from '../../api/client'
import { slateFill } from '../../components/buttons'

const DOCS_URL = 'https://github.com/vavallee/bindery/blob/main/docs/DEPLOYMENT.md#database-foreign-key-integrity'

// DatabaseCheckSection is the foreign key drift check (#3356) and its cleanup
// (#3357). The scan is a full pass over the database, so it only ever runs from
// a button: not on mount, not on a timer. The cleanup is a three step flow:
// preview what would happen, confirm, see the result. A preview carries a token
// the confirmation must send, so what is confirmed is exactly what was shown.
// Rendered for admins only by its parent.
export default function DatabaseCheckSection() {
  const { t } = useTranslation()
  const [running, setRunning] = useState(false)
  const [report, setReport] = useState<DBIntegrityReport | null>(null)
  const [previewing, setPreviewing] = useState(false)
  const [preview, setPreview] = useState<DBCleanupPreview | null>(null)
  const [cleaning, setCleaning] = useState(false)
  const [result, setResult] = useState<DBCleanupResult | null>(null)
  const [error, setError] = useState('')
  const [errorKey, setErrorKey] = useState<'dbCheckFailed' | 'dbCleanupPreviewFailed' | 'dbCleanupFailed'>('dbCheckFailed')

  const busy = running || previewing || cleaning

  const messageOf = (e: unknown, fallback: string) =>
    e instanceof Error && e.message ? e.message : fallback

  const run = async () => {
    setRunning(true)
    setError('')
    setPreview(null)
    setResult(null)
    try {
      setReport(await api.checkDBIntegrity())
    } catch (e) {
      setReport(null)
      setErrorKey('dbCheckFailed')
      setError(messageOf(e, t('settings.general.dbCheckFailedGeneric')))
    } finally {
      setRunning(false)
    }
  }

  const runPreview = async () => {
    setPreviewing(true)
    setError('')
    setResult(null)
    try {
      setPreview(await api.previewDBCleanup())
    } catch (e) {
      setPreview(null)
      setErrorKey('dbCleanupPreviewFailed')
      setError(messageOf(e, t('settings.general.dbCleanupFailedGeneric')))
    } finally {
      setPreviewing(false)
    }
  }

  const confirmCleanup = async () => {
    if (!preview?.previewToken) return
    setCleaning(true)
    setError('')
    try {
      const res = await api.cleanupDB(preview.previewToken)
      setResult(res)
      // The preview is spent whatever happened next: a new cleanup needs a new one.
      setPreview(null)
      setReport(null)
    } catch (e) {
      // A refused confirmation (a stale or expired preview, another run in
      // progress) leaves the database as it was; the preview is no use any more.
      setPreview(null)
      setErrorKey('dbCleanupFailed')
      setError(messageOf(e, t('settings.general.dbCleanupFailedGeneric')))
    } finally {
      setCleaning(false)
    }
  }

  const onDeleteLabel = (action: string) => {
    switch (action) {
      case 'CASCADE': return t('settings.general.dbCheckOnDeleteCascade')
      case 'SET NULL': return t('settings.general.dbCheckOnDeleteSetNull')
      case '': return t('settings.general.dbCheckOnDeleteUnknown')
      default: return t('settings.general.dbCheckOnDeleteOther')
    }
  }

  const actionLabel = (action: DBCleanupAction) => {
    switch (action) {
      case 'delete': return t('settings.general.dbCleanupActionDelete')
      case 'clear': return t('settings.general.dbCleanupActionClear')
      case 'skipped': return t('settings.general.dbCleanupActionSkipped')
      default: return t('settings.general.dbCleanupActionKeep')
    }
  }

  return (
    <section>
      <h3 className="text-base font-semibold mb-3 text-slate-800 dark:text-zinc-200">{t('settings.general.dbCheck')}</h3>
      <div className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900 space-y-3">
        <div className="flex items-center justify-between gap-4">
          <p className="text-xs text-fg-muted">{t('settings.general.dbCheckHint')}</p>
          <button
            type="button"
            onClick={run}
            disabled={busy}
            className={`px-4 py-2 ${slateFill} rounded text-sm font-medium disabled:opacity-50 flex-shrink-0`}
          >
            {running ? t('settings.general.dbCheckRunning') : t('settings.general.dbCheckButton')}
          </button>
        </div>

        {error && (
          <p role="alert" className="text-xs text-red-700 dark:text-red-400">
            {t(`settings.general.${errorKey}`, { message: error })}
          </p>
        )}

        {report && report.total === 0 && (
          <p role="status" className="text-xs text-accent-text">{t('settings.general.dbCheckClean')}</p>
        )}

        {report && report.total > 0 && (
          <div className="space-y-2">
            <p role="status" className="text-xs text-amber-700 dark:text-amber-400">
              {t('settings.general.dbCheckFound', { count: report.total })}
            </p>
            <table className="w-full text-xs text-left">
              <thead className="text-fg-muted">
                <tr>
                  <th className="py-1 pr-3 font-medium">{t('settings.general.dbCheckColRelationship')}</th>
                  <th className="py-1 pr-3 font-medium">{t('settings.general.dbCheckColRows')}</th>
                  <th className="py-1 font-medium">{t('settings.general.dbCheckColOnDelete')}</th>
                </tr>
              </thead>
              <tbody className="text-slate-800 dark:text-zinc-200">
                {report.relationships.map((r, i) => (
                  // One child table can hold two foreign keys to the same parent
                  // (requests to users: owner CASCADE, decider SET NULL), so the
                  // on delete action is part of both the key and the label.
                  <tr key={`${r.childTable}->${r.parentTable}->${r.onDelete}#${i}`} className="border-t border-slate-200 dark:border-zinc-800">
                    <td className="py-1 pr-3 font-mono">
                      {r.childTable} {'->'} {r.parentTable}
                      {r.onDelete && <span className="text-fg-muted"> ({r.onDelete})</span>}
                    </td>
                    <td className="py-1 pr-3 font-mono">{r.violations}</td>
                    <td className="py-1">{onDeleteLabel(r.onDelete)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
            {!preview && (
              <button
                type="button"
                onClick={runPreview}
                disabled={busy}
                className={`px-3 py-1.5 ${slateFill} rounded text-xs font-medium disabled:opacity-50`}
              >
                {previewing ? t('settings.general.dbCleanupPreviewing') : t('settings.general.dbCleanupButton')}
              </button>
            )}
          </div>
        )}

        {report && (
          <p className="text-[11px] text-fg-muted">
            {t('settings.general.dbCheckCheckedAt', { time: new Date(report.checkedAt).toLocaleString() })}
          </p>
        )}

        {preview && (
          <div className="space-y-2 border-t border-slate-200 dark:border-zinc-800 pt-3" data-testid="db-cleanup-preview">
            <p className="text-sm font-semibold text-slate-800 dark:text-zinc-200">{t('settings.general.dbCleanupPreviewTitle')}</p>
            {preview.relationships.length > 0 && (
              <table className="w-full text-xs text-left">
                <thead className="text-fg-muted">
                  <tr>
                    <th className="py-1 pr-3 font-medium">{t('settings.general.dbCheckColRelationship')}</th>
                    <th className="py-1 pr-3 font-medium">{t('settings.general.dbCheckColRows')}</th>
                    <th className="py-1 font-medium">{t('settings.general.dbCleanupColAction')}</th>
                  </tr>
                </thead>
                <tbody className="text-slate-800 dark:text-zinc-200">
                  {preview.relationships.map((r, i) => (
                    <tr key={`${r.childTable}->${r.parentTable}->${r.onDelete}#${i}`} className="border-t border-slate-200 dark:border-zinc-800">
                      <td className="py-1 pr-3 font-mono">
                        {r.childTable} {'->'} {r.parentTable}
                        {r.onDelete && <span className="text-fg-muted"> ({r.onDelete})</span>}
                      </td>
                      <td className="py-1 pr-3 font-mono">{r.violations}</td>
                      <td className="py-1">
                        {actionLabel(r.action)}
                        {r.fixable > 0 && r.fixable < r.violations && (
                          <span className="block text-fg-muted">
                            {t('settings.general.dbCleanupPartial', { count: r.violations - r.fixable })}
                          </span>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
            <p className="text-xs text-slate-700 dark:text-zinc-300">
              {t('settings.general.dbCleanupSummary', { fixable: preview.fixable, unfixable: preview.unfixable })}
            </p>
            {preview.previewToken ? (
              <>
                <p className="text-xs text-fg-muted">{t('settings.general.dbCleanupCascadeNote')}</p>
                <p className="text-xs text-fg-muted">{t('settings.general.dbCleanupBackupNote')}</p>
                <p className="text-xs text-fg-muted">{t('settings.general.dbCleanupSlowNote')}</p>
                <div className="flex gap-2">
                  <button
                    type="button"
                    onClick={confirmCleanup}
                    disabled={busy}
                    className="px-3 py-1.5 rounded text-xs font-medium bg-red-700 hover:bg-red-600 text-white disabled:opacity-50"
                  >
                    {cleaning ? t('settings.general.dbCleanupConfirming') : t('settings.general.dbCleanupConfirm')}
                  </button>
                  <button
                    type="button"
                    onClick={() => setPreview(null)}
                    disabled={cleaning}
                    className={`px-3 py-1.5 ${slateFill} rounded text-xs font-medium disabled:opacity-50`}
                  >
                    {t('settings.general.dbCleanupCancel')}
                  </button>
                </div>
              </>
            ) : (
              <p role="status" className="text-xs text-fg-muted">{t('settings.general.dbCleanupNothing')}</p>
            )}
          </div>
        )}

        {result && (
          <div className="space-y-2 border-t border-slate-200 dark:border-zinc-800 pt-3" data-testid="db-cleanup-result">
            <p role="status" className="text-xs text-accent-text">
              {t('settings.general.dbCleanupDone', { deleted: result.deleted, cleared: result.cleared, skipped: result.skipped })}
            </p>
            <p className="text-xs text-slate-700 dark:text-zinc-300 break-all">
              {t('settings.general.dbCleanupBackupAt', { path: result.backupPath })}
            </p>
            {!result.verified && (
              <p role="alert" className="text-xs text-amber-700 dark:text-amber-400">{t('settings.general.dbCleanupNotVerified')}</p>
            )}
            {result.verified && result.remainingTotal === 0 && (
              <p className="text-xs text-accent-text">{t('settings.general.dbCleanupRemainingNone')}</p>
            )}
            {result.verified && result.remainingTotal > 0 && (
              <>
                <p role="status" className="text-xs text-amber-700 dark:text-amber-400">
                  {t('settings.general.dbCleanupRemaining', { count: result.remainingTotal })}
                </p>
                <table className="w-full text-xs text-left">
                  <tbody className="text-slate-800 dark:text-zinc-200">
                    {result.remaining.map((r, i) => (
                      <tr key={`${r.childTable}->${r.parentTable}->${r.onDelete}#${i}`} className="border-t border-slate-200 dark:border-zinc-800">
                        <td className="py-1 pr-3 font-mono">
                          {r.childTable} {'->'} {r.parentTable}
                          {r.onDelete && <span className="text-fg-muted"> ({r.onDelete})</span>}
                        </td>
                        <td className="py-1 pr-3 font-mono">{r.violations}</td>
                        <td className="py-1">{actionLabel(r.action)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </>
            )}
          </div>
        )}

        <p className="text-xs text-fg-muted border-t border-slate-200 dark:border-zinc-800 pt-3">
          {t('settings.general.dbCheckRepairNote')}{' '}
          <a href={DOCS_URL} target="_blank" rel="noopener noreferrer" className="text-accent-text underline">
            {t('settings.general.dbCheckDocsLink')}
          </a>
        </p>
      </div>
    </section>
  )
}
