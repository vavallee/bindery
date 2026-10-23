import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import type { RemapCandidate } from '../../api/client'
import { inputCls, labelCls } from './formStyles'
import { supportsRemapAssistant } from './helpers'
import { primaryFill } from '../../components/buttons'

interface Props {
  clientId: number
  clientType: string
  remotePath: string
  candidates: RemapCandidate[]
  // Why there is nothing to pick, when the server says so.
  note?: 'samePath' | 'unsupportedChars'
  // Saves the new remap through the normal update route and re-runs Test. The
  // assistant never applies anything from the diagnostic response itself.
  onApply: (local: string) => Promise<void>
}

export default function RemapAssistant({ clientId, clientType, remotePath, candidates, note, onApply }: Props) {
  const { t } = useTranslation()
  const [selected, setSelected] = useState(candidates[0]?.path ?? '')
  const [applying, setApplying] = useState(false)
  const [error, setError] = useState<string | null>(null)

  if (!supportsRemapAssistant(clientType) || !remotePath) return null
  if (candidates.length === 0 && !note) return null

  const current = candidates.find(c => c.path === selected) ?? candidates[0]
  const selectId = `remap-assist-${clientId}`

  const apply = async () => {
    setApplying(true)
    setError(null)
    try {
      await onApply(current.path)
    } catch (err) {
      setError(err instanceof Error ? err.message : t('settings.clients.unknownError'))
    } finally {
      setApplying(false)
    }
  }

  return (
    <section
      aria-label={t('settings.clients.remapAssist.heading')}
      className="mt-1 p-3 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-50 dark:bg-zinc-900/60 text-xs space-y-2"
    >
      <h5 className="font-semibold text-sm">{t('settings.clients.remapAssist.heading')}</h5>
      {current && <p className="text-fg-muted">{t('settings.clients.remapAssist.intro')}</p>}
      <p>
        <span className="text-fg-muted">{t('settings.clients.remapAssist.clientPath')}: </span>
        <code className="font-mono break-all">{remotePath}</code>
      </p>
      {!current && note && <p role="note">{t(`settings.clients.remapAssist.note.${note}`)}</p>}
      {current && (<>
      <div>
        <label htmlFor={selectId} className={labelCls}>{t('settings.clients.remapAssist.chooseLabel')}</label>
        <select id={selectId} value={current.path} onChange={e => setSelected(e.target.value)} className={inputCls}>
          {candidates.map(c => (
            <option key={c.path} value={c.path}>{`${c.path} (${t(`settings.clients.remapAssist.source.${c.source}`)})`}</option>
          ))}
        </select>
      </div>
      <div aria-live="polite">
        <span className="text-fg-muted">{t('settings.clients.remapAssist.previewLabel')}: </span>
        <code className="font-mono break-all">{current.before}</code>
        <span aria-hidden="true">{' -> '}</span>
        <code className="font-mono break-all">{current.after}</code>
      </div>
      {error && (
        <p role="alert" className="text-red-700 dark:text-red-400">{t('settings.clients.remapAssist.failed', { error })}</p>
      )}
      <button
        type="button"
        onClick={() => { void apply() }}
        disabled={applying}
        className={`px-3 py-1.5 ${primaryFill} rounded text-xs font-medium disabled:opacity-50`}
      >
        {applying ? t('settings.clients.remapAssist.applying') : t('settings.clients.remapAssist.apply')}
      </button>
      </>)}
    </section>
  )
}
