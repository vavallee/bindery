import type { RejectImportResult } from '../api/client'

type Translate = (key: string, opts?: Record<string, unknown>) => string

// What a finished reject did, said as outcomes: a file left on disk, an import
// with no release to blocklist, and a file that was already gone each get their
// own sentence rather than a generic success.
export function rejectSummary(r: RejectImportResult, t: Translate): string {
  const parts: string[] = [t('rejectImport.doneWanted')]
  if (r.nothingTracked) parts.push(t('rejectImport.doneNothingTracked'))
  else if (r.deleted.length > 0) parts.push(t('rejectImport.doneDeleted', { count: r.deleted.length }))
  if (r.keptOnDisk.length > 0) parts.push(t('rejectImport.doneKept', { count: r.keptOnDisk.length }))
  if (r.alreadyGone.length > 0) parts.push(t('rejectImport.doneGone', { count: r.alreadyGone.length }))
  if (!r.releaseKnown) parts.push(t('rejectImport.doneNoRelease'))
  else if (r.alreadyBlocklisted) parts.push(t('rejectImport.doneAlreadyBlocklisted'))
  else if (r.blocklisted) parts.push(t('rejectImport.doneBlocklisted'))
  return parts.join(' ')
}
