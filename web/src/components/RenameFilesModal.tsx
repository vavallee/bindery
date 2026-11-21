import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import {
  reorganizeApi,
  ReorganizeMove,
  ReorganizeResponse,
  ReorganizeScope,
} from '../api/reorganize'
import { useModal } from './useModal'
import RichText from './RichText'
import { errorText } from '../util/errorText'
import { ApiError } from '../api/core'
import type { TFunction } from 'i18next'

interface Props {
  scope: ReorganizeScope
  id?: number
  // A human label for what is being reorganized ("Dune", "Frank Herbert",
  // "your library") shown in the modal header.
  label: string
  onClose: () => void
  // Called after an apply that moved at least one file, so the caller can
  // reload the affected book/author view.
  onApplied?: () => void
}

const statusStyles: Record<string, string> = {
  move: 'text-info',
  moved: 'text-accent-text',
  noop: 'text-fg-muted',
  collision: 'text-warning',
  missing: 'text-warning',
  error: 'text-danger',
  failed: 'text-danger',
}

// knownFormats keeps the media formats the dialog can name, in the order the
// API sent them, without duplicates.
function knownFormats(raw: unknown): string[] {
  if (!Array.isArray(raw)) return []
  const out: string[] = []
  for (const f of raw) {
    if ((f === 'ebook' || f === 'audiobook') && !out.includes(f)) out.push(f)
  }
  return out
}

// sidecarSummary appends the metadata.opf counts of an apply, empty when the
// setting is off and nothing was reported.
function sidecarSummary(s: ReorganizeResponse['summary'], t: TFunction): string {
  const written = s.sidecarWritten ?? 0
  const skipped = (s.sidecarUnchanged ?? 0) + (s.sidecarForeign ?? 0) + (s.sidecarShared ?? 0)
  const failed = s.sidecarFailed ?? 0
  if (written + skipped + failed === 0) return ''
  return ', ' + t('renameFiles.sidecarSummary', {
    written, skipped, failed,
    defaultValue: '{{written}} metadata.opf written, {{skipped}} skipped, {{failed}} failed',
  })
}

const statusLabel: Record<string, string> = {
  move: 'Will move',
  moved: 'Moved',
  noop: 'Already correct',
  collision: 'Destination exists',
  missing: 'Not on disk',
  error: 'Error',
  failed: 'Failed',
}

export default function RenameFilesModal({ scope, id, label, onClose, onApplied }: Props) {
  const { t } = useTranslation()
  const [preview, setPreview] = useState<ReorganizeResponse | null>(null)
  const [loading, setLoading] = useState(true)
  const [applying, setApplying] = useState(false)
  const [applied, setApplied] = useState<ReorganizeResponse | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [confirmExternal, setConfirmExternal] = useState(false)
  // Set when apply answers 409 reorganize_external_mode: the mode flipped after
  // the preview loaded, so the confirmation is needed now.
  const [externalRefused, setExternalRefused] = useState(false)
  // Formats the 409 named, for the case where the preview did not know.
  const [refusedFormats, setRefusedFormats] = useState<string[]>([])

  useEffect(() => {
    let cancelled = false
    setLoading(true)
    reorganizeApi
      .preview(scope, id)
      .then(res => {
        if (!cancelled) setPreview(res)
      })
      .catch(err => {
        if (!cancelled) setError(errorText(err, t('renameFiles.previewFailed', 'Preview failed')))
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [scope, id]) // eslint-disable-line react-hooks/exhaustive-deps

  const movable = (preview?.moves ?? []).filter(m => m.status === 'move')
  // Files already in place whose metadata.opf would be written (#4008). They
  // go to apply too, but only the moves count as renames.
  const sidecarOnly = (preview?.moves ?? []).filter(m => m.status === 'noop' && m.sidecar === 'write')
  const actionable = movable.length + sidecarOnly.length
  const previewFormats = knownFormats(preview?.externalFormats)
  // The warning shows only when the API reports External mode for at least one
  // format in the plan; a 409 on apply turns it on when the mode flipped late.
  const externalMode = (preview?.externalMode === true && previewFormats.length > 0) || externalRefused
  // A 409 reflects the mode as it is now, so its formats win over the preview's.
  const externalFormats = externalRefused ? refusedFormats : previewFormats
  const formatWords = externalFormats.map(f =>
    f === 'ebook' ? t('renameFiles.formatEbook', 'ebook files') : t('renameFiles.formatAudiobook', 'audiobook files'),
  )
  const externalWarning = formatWords.length === 0
    ? t('renameFiles.externalWarning', 'Import mode is External, so another tool owns this layout. Renaming here can fight that tool.')
    : t('renameFiles.externalWarningFor', {
        formats: formatWords.length === 1
          ? formatWords[0]
          : t('renameFiles.formatsJoin', { first: formatWords[0], second: formatWords[1], defaultValue: '{{first}} and {{second}}' }),
        defaultValue: 'Import mode is External for {{formats}}, so another tool owns this layout. Renaming here can fight that tool.',
      })

  const apply = async () => {
    if (actionable === 0) return
    setApplying(true)
    setError(null)
    try {
      const ids = [...movable, ...sidecarOnly].map(m => m.fileId)
      const res = externalMode && confirmExternal
        ? await reorganizeApi.apply(ids, true)
        : await reorganizeApi.apply(ids)
      setApplied(res)
      if (res.summary.moved > 0 || (res.summary.sidecarWritten ?? 0) > 0) onApplied?.()
    } catch (err) {
      if (err instanceof ApiError && err.status === 409 && err.body?.code === 'reorganize_external_mode') {
        setExternalRefused(true)
        setRefusedFormats(knownFormats(err.body?.formats))
        setConfirmExternal(false)
      } else {
        setError(errorText(err, t('renameFiles.renameFailed', 'Rename failed')))
      }
    } finally {
      setApplying(false)
    }
  }

  const shown = applied ?? preview
  const moves = shown?.moves ?? []

  const { titleId, panelProps } = useModal({ onClose, canClose: !applying })

  return (
    <div className="modal-overlay fixed inset-0 z-50 flex items-center justify-center bg-black/50" onClick={onClose}>
      <div
        {...panelProps}
        className="bg-white dark:bg-zinc-900 border border-slate-200 dark:border-zinc-700 rounded-lg shadow-xl p-6 w-full max-w-2xl mx-4 modal-max-h flex flex-col"
        onClick={e => e.stopPropagation()}
      >
        <h2 id={titleId} className="text-base font-semibold mb-1 text-slate-900 dark:text-white">{t('renameFiles.title', 'Rename files')}</h2>
        <p className="text-xs text-fg-muted mb-4">
          <RichText
            text={t('renameFiles.intro', { label, defaultValue: 'Move the files for <label>{{label}}</label> to match the current naming template. Files already in the right place are left alone.' })}
            components={{ label: <span className="font-medium text-slate-700 dark:text-zinc-200" /> }}
          />
        </p>

        {externalMode && !applied && (
          <div role="alert" className="mb-3 p-3 rounded border border-warning-marker bg-warning-subtle text-xs text-warning-strong">
            <p>{externalWarning}</p>
            <label className="mt-2 flex items-start gap-2">
              <input
                type="checkbox"
                checked={confirmExternal}
                onChange={e => setConfirmExternal(e.target.checked)}
                className="mt-0.5"
              />
              <span>{t('renameFiles.externalConfirm', 'I understand, rename these files anyway')}</span>
            </label>
          </div>
        )}

        {loading ? (
          <p className="text-sm text-fg-muted">{t('renameFiles.computing', 'Computing changes…')}</p>
        ) : error && !moves.length ? (
          <p className="text-sm text-danger">{error}</p>
        ) : moves.length === 0 ? (
          <p className="text-sm text-fg-muted">{t('renameFiles.none', 'No tracked files to rename.')}</p>
        ) : (
          <div className="overflow-y-auto flex-1 -mx-2 px-2">
            <ul className="space-y-3">
              {moves.map(m => (
                <MoveRow key={m.fileId} move={m} />
              ))}
            </ul>
          </div>
        )}

        {error && moves.length > 0 && (
          <p className="mt-3 text-sm text-danger">{error}</p>
        )}

        <div className="mt-4 pt-4 border-t border-slate-200 dark:border-zinc-800 flex items-center gap-2">
          <span className="text-xs text-fg-muted">
            {applied
              ? t('renameFiles.appliedSummary', { moved: applied.summary.moved, failed: applied.summary.failed, defaultValue: '{{moved}} moved, {{failed}} failed' }) + sidecarSummary(applied.summary, t)
              : t('renameFiles.previewSummary', { movable: movable.length, total: moves.length, defaultValue: '{{movable}} of {{total}} will move' }) +
                (sidecarOnly.length > 0 ? ', ' + t('renameFiles.sidecarPreview', { count: preview?.summary.sidecarToWrite ?? sidecarOnly.length, defaultValue: '{{count}} metadata.opf to write' }) : '')}
          </span>
          <div className="ml-auto flex gap-2">
            <button
              type="button"
              onClick={onClose}
              className="px-4 py-1.5 text-sm rounded bg-slate-200 dark:bg-zinc-800 hover:bg-slate-300 dark:hover:bg-zinc-700"
            >
              {applied ? t('common.close', 'Close') : t('common.cancel', 'Cancel')}
            </button>
            {!applied && (
              <button
                type="button"
                onClick={apply}
                disabled={applying || actionable === 0 || (externalMode && !confirmExternal)}
                className="px-4 py-1.5 text-sm rounded bg-info-fill hover:bg-info-fill-hover disabled:opacity-40 font-medium text-on-fill"
              >
                {applying
                  ? t('renameFiles.renaming', 'Renaming…')
                  : movable.length === 0 && sidecarOnly.length > 0
                    ? t('renameFiles.applySidecars', { count: sidecarOnly.length, defaultValue: 'Write {{count}} metadata.opf' })
                    : t('renameFiles.apply', { count: movable.length })}
              </button>
            )}
          </div>
        </div>
      </div>
    </div>
  )
}

function MoveRow({ move }: { move: ReorganizeMove }) {
  const { t } = useTranslation()
  return (
    <li className="text-xs">
      <div className="flex items-center gap-2 mb-0.5">
        <span className={`font-medium ${statusStyles[move.status] ?? ''}`}>
          {t(`renameFiles.status.${move.status}`, statusLabel[move.status] ?? move.status)}
        </span>
        <span className="text-fg-muted">·</span>
        <span className="text-slate-600 dark:text-zinc-300 truncate">{move.bookTitle}</span>
        <span className="uppercase text-[10px] tracking-wide text-fg-muted">
          {move.format}
        </span>
      </div>
      <div className="font-mono text-fg-muted break-all">
        <div>
          <span className="text-fg-muted">{t('renameFiles.from', 'from')} </span>
          {move.current}
        </div>
        {move.status !== 'noop' && (
          <div>
            <span className="text-fg-muted">{t('renameFiles.to', 'to')} </span>
            {move.proposed}
          </div>
        )}
        {move.sidecar && move.sidecar !== 'write' && (
          <div className="text-fg-muted not-italic">
            {t(`renameFiles.sidecar.${move.sidecar}`, `metadata.opf ${move.sidecar}`)}
            {move.sidecarMessage ? `: ${move.sidecarMessage}` : ''}
          </div>
        )}
        {move.message && (
          <div className="text-warning not-italic">{move.message}</div>
        )}
      </div>
    </li>
  )
}
