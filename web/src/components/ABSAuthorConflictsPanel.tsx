import { useTranslation } from 'react-i18next'
import { ABSMetadataConflict } from '../api/client'
import { slateFill, primaryFill } from './buttons'

type Props = {
  title: string
  description: string
  entityType: ABSMetadataConflict['entityType']
  conflicts: ABSMetadataConflict[]
  show: boolean
  emptyMessage: string
  resolvedHeading: string
  conflictError?: string | null
  resolvingConflictId: number | null
  onToggle: () => void
  onRefresh: () => void
  onResolveConflict: (id: number, source: 'abs' | 'upstream') => void
  relinkAction?: {
    loadingId: number | null
    onRelink: (localId: number) => void
  }
}

function groupConflictsByEntity(conflicts: ABSMetadataConflict[]): ABSMetadataConflict[][] {
  const groups: Record<string, ABSMetadataConflict[]> = {}
  for (const conflict of conflicts) {
    const key = `${conflict.entityType}:${conflict.localId}`
    if (!groups[key]) {
      groups[key] = []
    }
    groups[key].push(conflict)
  }
  return Object.values(groups)
}

function RelinkButton({
  conflict,
  relinkAction,
}: {
  conflict: ABSMetadataConflict
  relinkAction?: Props['relinkAction']
}) {
  const { t } = useTranslation()
  if (!relinkAction || !conflict.authorRelinkEligible) {
    return null
  }

  const relinking = relinkAction.loadingId === conflict.localId
  return (
    <button
      onClick={() => relinkAction.onRelink(conflict.localId)}
      disabled={relinking}
      className={`px-3 py-1.5 ${primaryFill} rounded text-xs font-medium disabled:opacity-50`}
    >
      {relinking ? t('absConflicts.relinking', 'Relinking…') : t('absConflicts.relink', 'Attempt upstream relink')}
    </button>
  )
}

export default function ABSConflictPanel({
  title,
  description,
  entityType,
  conflicts,
  show,
  emptyMessage,
  resolvedHeading,
  conflictError,
  resolvingConflictId,
  onToggle,
  onRefresh,
  onResolveConflict,
  relinkAction,
}: Props) {
  const { t } = useTranslation()
  // The source is shown as stored, except that "upstream" is a word.
  const sourceLabel = (source: string) => source === 'upstream' ? t('absConflicts.upstreamSource', 'upstream') : source
  const entityConflicts = conflicts.filter(conflict => conflict.entityType === entityType)
  const unresolvedGroups = groupConflictsByEntity(entityConflicts.filter(conflict => conflict.resolutionStatus === 'unresolved'))
  const resolvedGroups = groupConflictsByEntity(entityConflicts.filter(conflict => conflict.resolutionStatus === 'resolved'))

  return (
    <div className="pt-3 border-t border-slate-200 dark:border-zinc-800 space-y-3">
      <div className="flex items-center justify-between gap-4">
        <button
          type="button"
          onClick={onToggle}
          aria-expanded={show}
          className="min-w-0 flex-1 text-left"
        >
          <div className="flex items-start gap-2">
            <span className="text-sm text-slate-500 dark:text-zinc-500 mt-0.5" aria-hidden="true">
              {show ? '▾' : '▸'}
            </span>
            <div>
              <label className="block text-sm font-medium text-slate-800 dark:text-zinc-200 cursor-pointer">{title}</label>
              <p className="text-xs text-fg-muted mt-0.5">{description}</p>
            </div>
          </div>
        </button>
        <button
          onClick={onRefresh}
          className={`px-3 py-2 ${slateFill} rounded text-sm font-medium flex-shrink-0`}
        >
          {t('common.refresh', 'Refresh')}
        </button>
      </div>

      {show && (
        <>
          {unresolvedGroups.length === 0 && resolvedGroups.length === 0 && (
            <p className="text-sm text-fg-muted">{emptyMessage}</p>
          )}

          {unresolvedGroups.map(group => (
            <div key={`${group[0].entityType}:${group[0].localId}`} className="rounded border border-amber-300 dark:border-amber-900 bg-amber-50 dark:bg-amber-950/20 px-3 py-3 space-y-2 overflow-hidden">
              <div className="flex items-center justify-between gap-3">
                <div className="min-w-0">
                  <p className="text-sm font-medium text-slate-800 dark:text-zinc-200">{group[0].entityName}</p>
                  <p className="text-[11px] text-fg-muted uppercase tracking-wide">{entityType}</p>
                </div>
                <div className="flex flex-wrap items-center justify-end gap-2">
                  <RelinkButton conflict={group[0]} relinkAction={relinkAction} />
                  <span className="text-[11px] px-2 py-1 rounded bg-amber-100 dark:bg-amber-950 text-amber-700 dark:text-amber-300">{t('absConflicts.needsReview', 'Needs review')}</span>
                </div>
              </div>
              {group.map(conflict => (
                <div key={conflict.id} className="rounded border border-amber-200 dark:border-amber-900/60 bg-white/70 dark:bg-zinc-950/30 px-3 py-2 space-y-2 overflow-hidden">
                  <div className="flex items-center justify-between gap-3">
                    <span className="text-xs font-medium text-slate-700 dark:text-zinc-300 min-w-0">{conflict.fieldLabel}</span>
                    <span className="text-[10px] uppercase tracking-wide text-fg-muted text-right">{t('absConflicts.using', { source: sourceLabel(conflict.appliedSource || 'upstream'), defaultValue: 'Using {{source}}' })}</span>
                  </div>
                  <div className="grid min-w-0 gap-2 md:grid-cols-2 text-xs">
                    <div className="min-w-0 rounded border border-slate-200 dark:border-zinc-800 px-2 py-2 bg-slate-50 dark:bg-zinc-950 overflow-hidden">
                      <div className="font-medium text-slate-700 dark:text-zinc-300 mb-1">ABS</div>
                      <div className="text-fg-muted whitespace-pre-wrap break-all min-w-0">{conflict.absValue || t('absConflicts.empty', 'Empty')}</div>
                    </div>
                    <div className="min-w-0 rounded border border-slate-200 dark:border-zinc-800 px-2 py-2 bg-slate-50 dark:bg-zinc-950 overflow-hidden">
                      <div className="font-medium text-slate-700 dark:text-zinc-300 mb-1">{t('absConflicts.upstream', 'Upstream')}</div>
                      <div className="text-fg-muted whitespace-pre-wrap break-all min-w-0">{conflict.upstreamValue || t('absConflicts.empty', 'Empty')}</div>
                    </div>
                  </div>
                  <div className="flex flex-wrap gap-2">
                    <button
                      onClick={() => onResolveConflict(conflict.id, 'abs')}
                      disabled={resolvingConflictId === conflict.id}
                      className={`px-3 py-1.5 ${slateFill} rounded text-xs font-medium disabled:opacity-50`}
                    >
                      {t('absConflicts.useAbs', 'Use ABS')}
                    </button>
                    <button
                      onClick={() => onResolveConflict(conflict.id, 'upstream')}
                      disabled={resolvingConflictId === conflict.id}
                      className="px-3 py-1.5 bg-sky-600 hover:bg-sky-500 rounded text-xs font-medium disabled:opacity-50"
                    >
                      {t('absConflicts.useUpstream', 'Use upstream')}
                    </button>
                  </div>
                </div>
              ))}
            </div>
          ))}

          {resolvedGroups.length > 0 && (
            <div className="space-y-2">
              <p className="text-xs font-medium uppercase tracking-wide text-fg-muted">{resolvedHeading}</p>
              {resolvedGroups.map(group => (
                <div key={`${group[0].entityType}:${group[0].localId}`} className="rounded border border-slate-200 dark:border-zinc-800 bg-slate-50 dark:bg-zinc-950 px-3 py-2 space-y-1">
                  <div className="flex items-center justify-between gap-3">
                    <p className="text-sm font-medium text-slate-800 dark:text-zinc-200">{group[0].entityName}</p>
                    <RelinkButton conflict={group[0]} relinkAction={relinkAction} />
                  </div>
                  {group.map(conflict => (
                    <div key={conflict.id} className="flex items-center justify-between gap-3 text-xs text-fg-muted">
                      <span>{conflict.fieldLabel}</span>
                      <span>{t('absConflicts.prefers', { source: sourceLabel(conflict.preferredSource || conflict.appliedSource), defaultValue: 'Prefers {{source}}' })}</span>
                    </div>
                  ))}
                </div>
              ))}
            </div>
          )}
        </>
      )}

      {conflictError && <div className="text-sm text-red-700 dark:text-red-400">{conflictError}</div>}
    </div>
  )
}
