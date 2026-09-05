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
}

// Saturated red is reserved for these small chips. Error detail below a row is
// rendered muted, not as a full red row.
export const DOWNLOAD_STATUSES: Record<string, DownloadStatusEntry> = {
  grabbed: {
    labelKey: 'history.events.grabbed',
    chip: 'bg-slate-200 dark:bg-zinc-800 text-slate-700 dark:text-zinc-300',
  },
  downloading: {
    labelKey: 'queue.status.downloading',
    chip: 'bg-blue-100 text-blue-800 dark:bg-blue-950 dark:text-blue-300',
  },
  completed: {
    labelKey: 'queue.status.completed',
    chip: 'bg-sky-100 text-sky-800 dark:bg-sky-950 dark:text-sky-300',
  },
  importPending: {
    labelKey: 'queue.status.importPending',
    chip: 'bg-amber-100 text-amber-900 dark:bg-amber-950 dark:text-amber-300',
  },
  importing: {
    labelKey: 'queue.status.importing',
    chip: 'bg-blue-100 text-blue-800 dark:bg-blue-950 dark:text-blue-300',
  },
  imported: {
    labelKey: 'queue.status.imported',
    chip: 'bg-emerald-100 text-emerald-800 dark:bg-emerald-950 dark:text-emerald-300',
  },
  failed: {
    labelKey: 'queue.status.failed',
    chip: 'bg-red-100 text-red-800 dark:bg-red-950 dark:text-red-300',
    failed: true,
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
    chip: 'bg-red-100 text-red-800 dark:bg-red-950 dark:text-red-300',
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
    chip: 'bg-indigo-100 text-indigo-800 dark:bg-indigo-950 dark:text-indigo-300',
    descriptionKey: 'queue.status.importExternalHint',
  },
  // Also non-terminal: pair gating (#942) parks the first-arriving format of a
  // media_type=both book until its sibling completes.
  importHeld: {
    labelKey: 'queue.status.importHeld',
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

export const isFailed = (status: string): boolean => FAILED_STATUSES.has(status)
export const isRetryable = (status: string): boolean => RETRYABLE_STATUSES.has(status)
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
