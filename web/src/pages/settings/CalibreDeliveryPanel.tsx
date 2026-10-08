import { useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'
import type { CalibreDeliveryList, CalibreDeliverySummary } from '../../api/client'
import { ModalPanel } from '../../components/useModal'

// How often the panel refreshes while books are waiting, so the counts move
// as the worker delivers them.
const POLL_MS = 15000

function errorText(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
}

function when(value?: string): string {
  return value ? new Date(value).toLocaleString() : ''
}

// CalibreDeliveryPanel is the Calibre delivery queue (#2832): what is
// waiting, what failed and why, with Retry failed, Clear waiting and Reset.
// Every endpoint behind it is admin only, like the rest of the tab.
// refreshKey reloads the panel when it changes, so a finished Push all shows
// up without a manual refresh.
export default function CalibreDeliveryPanel({ refreshKey }: { refreshKey?: unknown }) {
  const { t } = useTranslation()
  const [summary, setSummary] = useState<CalibreDeliverySummary | null>(null)
  const [failed, setFailed] = useState<CalibreDeliveryList | null>(null)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [busy, setBusy] = useState<'retry' | 'clear' | 'reset' | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [actionError, setActionError] = useState<string | null>(null)
  const [confirmReset, setConfirmReset] = useState(false)

  const refresh = useCallback(() => {
    return Promise.all([api.calibreDeliverySummary(), api.calibreDeliveries('failed', 50, 0)])
      .then(([s, f]) => {
        setSummary(s)
        setFailed(f)
        setLoadError(null)
      })
      .catch(err => setLoadError(errorText(err)))
  }, [])

  useEffect(() => {
    refresh()
  }, [refresh, refreshKey])

  const pending = summary?.pending ?? 0
  useEffect(() => {
    if (pending === 0) return
    const id = setInterval(refresh, POLL_MS)
    return () => clearInterval(id)
  }, [pending, refresh])

  const run = async (kind: 'retry' | 'clear' | 'reset', action: () => Promise<string>) => {
    setBusy(kind)
    setNotice(null)
    setActionError(null)
    try {
      setNotice(await action())
    } catch (err) {
      setActionError(t('settings.calibre.deliveries.actionFailed', { error: errorText(err) }))
    } finally {
      setBusy(null)
      await refresh()
    }
  }

  const retryFailed = () =>
    run('retry', async () => {
      const r = await api.calibreDeliveryRetry('failed')
      return t('settings.calibre.deliveries.retried', { count: r.requeued })
    })
  const clearWaiting = () =>
    run('clear', async () => {
      const r = await api.calibreDeliveryClearPending()
      return t('settings.calibre.deliveries.cleared', { count: r.cleared })
    })
  const reset = () =>
    run('reset', async () => {
      const r = await api.calibreDeliveryReset()
      setConfirmReset(false)
      return t('settings.calibre.deliveries.resetDone', { count: r.removed })
    })

  const target = summary?.target
  let reach: { text: string; tone: string }
  if (target?.reachable === true) {
    reach = { text: t('settings.calibre.deliveries.reachable'), tone: 'text-emerald-600 dark:text-emerald-400' }
  } else if (target?.reachable === false) {
    reach = {
      text: target.lastError
        ? t('settings.calibre.deliveries.unreachableWithError', { error: target.lastError })
        : t('settings.calibre.deliveries.unreachable'),
      tone: 'text-amber-600 dark:text-amber-400',
    }
  } else {
    reach = { text: t('settings.calibre.deliveries.reachableUnknown'), tone: 'text-slate-500 dark:text-zinc-500' }
  }
  // In pull (#2833) Bindery never probes Calibre; the plugin checks in.
  const pulling = summary?.mode === 'plugin' && summary?.transport === 'pull'
  if (pulling) {
    reach = summary?.pull?.lastSeen
      ? { text: t('settings.calibre.deliveries.lastCheckIn', { time: when(summary.pull.lastSeen) }), tone: 'text-emerald-600 dark:text-emerald-400' }
      : { text: t('settings.calibre.deliveries.neverCheckedIn'), tone: 'text-amber-600 dark:text-amber-400' }
  }

  const items = failed?.items ?? []
  const btn = 'px-3 py-1.5 rounded text-sm font-medium disabled:opacity-50 flex-shrink-0'

  return (
    <div className="pt-3 border-t border-slate-200 dark:border-zinc-800 space-y-3" data-testid="calibre-delivery-panel">
      <div>
        <label className="block text-sm font-medium text-slate-800 dark:text-zinc-200">
          {t('settings.calibre.deliveries.heading')}
        </label>
        <p className="text-xs text-slate-600 dark:text-zinc-500 mt-0.5">{t('settings.calibre.deliveries.description')}</p>
      </div>

      {loadError && (
        <p className="text-xs text-red-600 dark:text-red-400">
          {t('settings.calibre.deliveries.loadFailed', { error: loadError })}
        </p>
      )}
      {!summary && !loadError && (
        <p className="text-xs text-slate-500 dark:text-zinc-500">{t('settings.calibre.deliveries.loading')}</p>
      )}

      {summary && (
        <div data-testid="calibre-delivery-summary" className="text-xs text-slate-700 dark:text-zinc-300 flex flex-wrap gap-x-2 gap-y-1">
          <span>{t('settings.calibre.deliveries.waiting', { count: summary.pending })}</span>
          <span aria-hidden className="text-slate-400 dark:text-zinc-600">·</span>
          <span>{t('settings.calibre.deliveries.delivered', { count: summary.delivered })}</span>
          <span aria-hidden className="text-slate-400 dark:text-zinc-600">·</span>
          <span className={summary.failed > 0 ? 'text-red-600 dark:text-red-400' : ''}>
            {t('settings.calibre.deliveries.failed', { count: summary.failed })}
          </span>
          <span aria-hidden className="text-slate-400 dark:text-zinc-600">·</span>
          <span>
            {summary.lastDeliveredAt
              ? t('settings.calibre.deliveries.lastDelivered', { time: when(summary.lastDeliveredAt) })
              : t('settings.calibre.deliveries.neverDelivered')}
          </span>
          <span aria-hidden className="text-slate-400 dark:text-zinc-600">·</span>
          <span data-testid="calibre-delivery-reachability" className={`break-all ${reach.tone}`}>
            {reach.text}
            {!pulling && target?.checkedAt && (
              <span className="text-slate-500 dark:text-zinc-500">
                {' '}({t('settings.calibre.deliveries.checkedAt', { time: when(target.checkedAt) })})
              </span>
            )}
          </span>
        </div>
      )}

      {summary && (
        <div className="flex flex-wrap gap-2">
          <button
            onClick={retryFailed}
            disabled={busy !== null || summary.failed === 0}
            className={`${btn} bg-sky-600 hover:bg-sky-500 text-white`}
          >
            {busy === 'retry' ? t('settings.calibre.deliveries.retrying') : t('settings.calibre.deliveries.retryFailed')}
          </button>
          <button
            onClick={clearWaiting}
            disabled={busy !== null || summary.pending === 0}
            className={`${btn} bg-slate-600 hover:bg-slate-500 text-white`}
          >
            {busy === 'clear' ? t('settings.calibre.deliveries.clearing') : t('settings.calibre.deliveries.clearWaiting')}
          </button>
          <button
            onClick={() => setConfirmReset(true)}
            disabled={busy !== null}
            className={`${btn} bg-amber-600 hover:bg-amber-500 text-white`}
          >
            {t('settings.calibre.deliveries.reset')}
          </button>
        </div>
      )}

      {notice && <p className="text-xs text-emerald-600 dark:text-emerald-400">{notice}</p>}
      {actionError && <p className="text-xs text-red-600 dark:text-red-400">{actionError}</p>}

      {summary && (
        <div>
          <p className="text-xs font-semibold uppercase tracking-wide text-slate-700 dark:text-zinc-300 mb-1">
            {t('settings.calibre.deliveries.failedHeading')}
          </p>
          {items.length === 0 ? (
            <p className="text-xs text-slate-500 dark:text-zinc-500">{t('settings.calibre.deliveries.failedEmpty')}</p>
          ) : (
            <>
              <div
                data-testid="calibre-delivery-failed-table"
                className="max-h-72 overflow-auto rounded border border-slate-200 dark:border-zinc-800"
              >
                <table className="w-full text-xs">
                  <thead className="bg-slate-100 dark:bg-zinc-800 sticky top-0">
                    <tr>
                      {(['colTitle', 'colAuthor', 'colCode', 'colError', 'colAttempts', 'colLastTry'] as const).map(k => (
                        <th key={k} className="text-left px-2 py-1 text-slate-600 dark:text-zinc-400 font-medium">
                          {t(`settings.calibre.deliveries.${k}`)}
                        </th>
                      ))}
                    </tr>
                  </thead>
                  <tbody>
                    {items.map(d => (
                      <tr key={d.id} className="border-t border-slate-200 dark:border-zinc-800 align-top">
                        <td className="px-2 py-1 text-slate-800 dark:text-zinc-200">{d.bookTitle || `#${d.bookId}`}</td>
                        <td className="px-2 py-1 text-slate-600 dark:text-zinc-400">{d.authorName}</td>
                        <td className="px-2 py-1 text-slate-600 dark:text-zinc-400 font-mono">{d.lastErrorCode}</td>
                        <td className="px-2 py-1 text-red-600 dark:text-red-400 break-all">{d.lastError}</td>
                        <td className="px-2 py-1 text-slate-600 dark:text-zinc-400">{d.attempts}</td>
                        <td className="px-2 py-1 text-slate-600 dark:text-zinc-400 whitespace-nowrap">{when(d.updatedAt)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
              {failed && failed.total > items.length && (
                <p className="text-xs text-slate-500 dark:text-zinc-500 mt-1">
                  {t('settings.calibre.deliveries.failedMore', { shown: items.length, total: failed.total })}
                </p>
              )}
            </>
          )}
        </div>
      )}

      {confirmReset && (
        <div className="modal-overlay fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4">
          <ModalPanel
            onClose={() => setConfirmReset(false)}
            canClose={busy !== 'reset'}
            labelledBy="calibre-reset-title"
            className="w-full max-w-md rounded-lg bg-white dark:bg-zinc-900 border border-slate-200 dark:border-zinc-800 shadow-xl modal-max-h overflow-y-auto"
          >
            <div className="px-4 py-3 border-b border-slate-200 dark:border-zinc-800">
              <h3 id="calibre-reset-title" className="text-base font-semibold text-slate-800 dark:text-zinc-100">
                {t('settings.calibre.deliveries.resetTitle')}
              </h3>
            </div>
            <p className="p-4 text-sm text-slate-700 dark:text-zinc-300">{t('settings.calibre.deliveries.resetBody')}</p>
            <div className="px-4 py-3 border-t border-slate-200 dark:border-zinc-800 flex justify-end gap-2">
              <button
                onClick={() => setConfirmReset(false)}
                disabled={busy === 'reset'}
                className={`${btn} bg-slate-600 hover:bg-slate-500 text-white`}
              >
                {t('settings.calibre.deliveries.cancel')}
              </button>
              <button
                onClick={reset}
                disabled={busy === 'reset'}
                className={`${btn} bg-amber-600 hover:bg-amber-500 text-white`}
              >
                {busy === 'reset' ? t('settings.calibre.deliveries.resetting') : t('settings.calibre.deliveries.resetConfirm')}
              </button>
            </div>
          </ModalPanel>
        </div>
      )}
    </div>
  )
}
