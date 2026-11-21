// Shared download-state table for the queue, in the same spirit as
// bookStatus.ts: one place that knows every state the backend can put a
// download in, so the label, the chip colour and the "which actions apply"
// predicates cannot drift apart the way four hand-maintained lists in
// QueuePage did (#2342).
//
// The keys MUST stay in sync with the DownloadState constants in
// internal/models/download_state.go. downloadStatus.test.ts asserts that, so a
// state added on the Go side fails CI here instead of shipping as a grey chip
// reading the raw enum (#2339).

import type { TFunction } from 'i18next'

export interface DownloadStatusEntry {
  /** i18n key for the chip label. */
  labelKey: string
  /** Tailwind classes for the chip. */
  chip: string
  /**
   * i18n key for a short explanation shown under the row and as the chip's
   * title attribute. Only set for states whose name does not explain itself.
   */
  descriptionKey?: string
  /** Counted by the "N failed" bar and cleared by "Clear all failed". */
  failed?: boolean
  /**
   * Accepted by POST /queue/{id}/retry-import. Mirrors the status filter in
   * DownloadRepo.ResetImportRetry (internal/db/downloads.go), which takes
   * StateImportFailed and StateImportBlocked. A plain `failed` grab never
   * produced a file, so there is nothing to re-import (#2336).
   */
  retryable?: boolean
  /**
   * The download's files are on disk waiting for a book, so the manual
   * match/retry controls apply.
   */
  matchable?: boolean
  /**
   * The download stage itself failed, so the row's own release can be sent to
   * the download client again (POST /queue/{id}/retry, #2295). Mirrors
   * api.regrabbable, minus importBlocked: a blocked import has its files on
   * disk already and Retry import is the action that suits it.
   */
  resendable?: boolean
  /**
   * Which Queue status chip counts this state, besides All, Active and Failed
   * (#3992). "Needs attention" is not listed here: it is derived from
   * `matchable`, the rows with files on disk that wait for a person to match
   * or retry them.
   */
  group?: 'downloading' | 'importing' | 'held' | 'external'
}

// Saturated red is reserved for these small chips. Error detail below a row is
// rendered muted, not as a full red row.
export const DOWNLOAD_STATUSES: Record<string, DownloadStatusEntry> = {
  grabbed: {
    labelKey: 'history.events.grabbed',
    group: 'downloading',
    chip: 'bg-slate-200 dark:bg-zinc-800 text-slate-700 dark:text-zinc-300',
  },
  downloading: {
    labelKey: 'queue.status.downloading',
    group: 'downloading',
    chip: 'bg-progress-subtle text-progress-strong',
  },
  completed: {
    labelKey: 'queue.status.completed',
    group: 'importing',
    chip: 'bg-info-subtle text-info-strong',
  },
  importPending: {
    labelKey: 'queue.status.importPending',
    group: 'importing',
    chip: 'bg-warning-subtle text-warning-strong',
  },
  importing: {
    labelKey: 'queue.status.importing',
    group: 'importing',
    chip: 'bg-progress-subtle text-progress-strong',
  },
  imported: {
    labelKey: 'queue.status.imported',
    chip: 'bg-success-subtle text-success-strong',
  },
  failed: {
    labelKey: 'queue.status.failed',
    chip: 'bg-danger-subtle text-danger-strong',
    failed: true,
    resendable: true,
  },
  importFailed: {
    labelKey: 'history.events.importFailed',
    chip: 'bg-orange-100 text-orange-900 dark:bg-orange-950 dark:text-orange-300',
    failed: true,
    retryable: true,
    matchable: true,
  },
  importBlocked: {
    labelKey: 'queue.status.importBlocked',
    chip: 'bg-danger-subtle text-danger-strong',
    failed: true,
    retryable: true,
    matchable: true,
  },
  // Non-terminal by design: the file was handed to an external import tool and
  // Bindery is waiting for it to be placed, so the row lives in the queue until
  // a manual retry or a library scan reconciles it. Without a label of its own
  // this rendered as the raw string "importExternal" (#2339).
  importExternal: {
    labelKey: 'queue.status.importExternal',
    group: 'external',
    chip: 'bg-indigo-100 text-indigo-800 dark:bg-indigo-950 dark:text-indigo-300',
    descriptionKey: 'queue.status.importExternalHint',
  },
  // Also non-terminal: pair gating (#942) parks the first-arriving format of a
  // media_type=both book until its sibling completes.
  importHeld: {
    labelKey: 'queue.status.importHeld',
    group: 'held',
    chip: 'bg-violet-100 text-violet-800 dark:bg-violet-950 dark:text-violet-300',
    descriptionKey: 'queue.status.importHeldHint',
  },
}

const UNKNOWN_CHIP = 'bg-slate-200 dark:bg-zinc-800 text-slate-700 dark:text-zinc-300'

/** States counted by the failed bar. Derived, never hand-listed. */
export const FAILED_STATUSES: ReadonlySet<string> = new Set(
  Object.entries(DOWNLOAD_STATUSES).filter(([, e]) => e.failed).map(([k]) => k),
)

/** States the retry-import endpoint accepts. */
export const RETRYABLE_STATUSES: ReadonlySet<string> = new Set(
  Object.entries(DOWNLOAD_STATUSES).filter(([, e]) => e.retryable).map(([k]) => k),
)

/**
 * States whose own release can be re-sent to the download client. Derived,
 * never hand-listed.
 */
export const RESENDABLE_STATUSES: ReadonlySet<string> = new Set(
  Object.entries(DOWNLOAD_STATUSES).filter(([, e]) => e.resendable).map(([k]) => k),
)

/** The Queue's status views. '' is All; 'active' and 'failed' predate the rest. */
export type QueueView = '' | 'active' | 'downloading' | 'importing' | 'held' | 'external' | 'attention' | 'failed'

export const QUEUE_VIEWS: readonly QueueView[] = ['', 'active', 'downloading', 'importing', 'held', 'external', 'attention', 'failed']

/** Parses a ?view= value. An unknown one reads as All. */
export const parseQueueView = (v: string | null): QueueView =>
  v !== null && v !== '' && (QUEUE_VIEWS as readonly string[]).includes(v) ? (v as QueueView) : ''

/** Whether a download in `status` belongs in the Queue's `view`. */
export function inQueueView(status: string, view: QueueView): boolean {
  switch (view) {
    case '': return true
    case 'failed': return isFailed(status)
    case 'active': return !isFailed(status)
    case 'attention': return isMatchable(status)
    default: return DOWNLOAD_STATUSES[status]?.group === view
  }
}

export const isFailed = (status: string): boolean => FAILED_STATUSES.has(status)
export const isRetryable = (status: string): boolean => RETRYABLE_STATUSES.has(status)
export const isResendable = (status: string): boolean => RESENDABLE_STATUSES.has(status)
export const isMatchable = (status: string): boolean => DOWNLOAD_STATUSES[status]?.matchable === true

/**
 * downloadStatusBadge returns the label, chip classes and optional explanation
 * for a download's status pill. Pass i18next's `t`. An unrecognised state falls
 * back to the raw string on a muted chip, same as before.
 */
export function downloadStatusBadge(
  status: string,
  t: TFunction,
): { label: string; chip: string; description?: string } {
  const entry = DOWNLOAD_STATUSES[status]
  if (!entry) return { label: status, chip: UNKNOWN_CHIP }
  return {
    label: t(entry.labelKey, { defaultValue: status }),
    chip: entry.chip,
    description: entry.descriptionKey ? t(entry.descriptionKey) : undefined,
  }
}
