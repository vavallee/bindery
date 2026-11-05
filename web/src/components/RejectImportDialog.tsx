import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, HistoryEvent, RejectImportResult } from '../api/client'
import ConfirmDialog from './ConfirmDialog'
import { rejectSummary } from '../util/rejectSummary'

interface Props {
  event: HistoryEvent
  onClose: () => void
  /** Called once the reject has committed, with a sentence saying what happened. */
  onRejected: (summary: string, result: RejectImportResult) => void
}

function parse(data: string): { guid?: unknown; paths?: unknown; path?: unknown } {
  if (!data) return {}
  try { return JSON.parse(data) } catch { return {} }
}

// Confirmation for Reject import (#3250). The body says exactly what will
// happen, including the two cases where it does less: an import that recorded
// no release (nothing to blocklist) and a file outside the library folders
// (detached, never deleted).
export default function RejectImportDialog({ event, onClose, onRejected }: Props) {
  const { t } = useTranslation()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const data = parse(event.data)
  const hasRelease = typeof data.guid === 'string' && data.guid !== ''
  const shown = Array.isArray(data.paths)
    ? (data.paths as unknown[]).filter((p): p is string => typeof p === 'string')
    : typeof data.path === 'string' && data.path ? [data.path] : []

  const confirm = async () => {
    setBusy(true)
    setError(null)
    try {
      const result = await api.rejectImport(event.id)
      onRejected(rejectSummary(result, t), result)
    } catch (e) {
      setError(e instanceof Error && e.message ? e.message : t('rejectImport.failed'))
      setBusy(false)
    }
  }

  return (
    <ConfirmDialog
      title={t('rejectImport.title')}
      body={
        <div className="space-y-2">
          <p>{t('rejectImport.intro')}</p>
          <ul className="list-disc pl-5 space-y-1">
            <li>{t('rejectImport.stepDetach')}</li>
            <li>{t('rejectImport.stepDelete')}</li>
            <li>{hasRelease ? t('rejectImport.stepBlocklist') : t('rejectImport.stepNoRelease')}</li>
            <li>{t('rejectImport.stepWanted')}</li>
          </ul>
          {shown.length > 0 && (
            <p className="font-mono text-xs break-all text-slate-700 dark:text-zinc-300">{shown.join(' ')}</p>
          )}
          {error && <p role="alert" className="text-red-700 dark:text-red-400">{error}</p>}
        </div>
      }
      acknowledgeLabel={t('rejectImport.acknowledge')}
      confirmLabel={t('rejectImport.confirm')}
      confirmingLabel={t('rejectImport.confirming')}
      confirming={busy}
      onConfirm={confirm}
      onClose={onClose}
    />
  )
}
