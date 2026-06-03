// Shared status-badge logic for library books, used by the book detail page and
// the book/author list views so every surface agrees with the Wanted page.
//
// `status` and `monitored` are orthogonal: `status` is the acquisition
// lifecycle (every book starts `wanted`), while `monitored` is whether Bindery
// actively pursues it. The Wanted page lists `status=wanted AND monitored`, so a
// backlist book (`wanted` + unmonitored) must NOT read "Wanted" on its detail
// page or in lists — otherwise the pill and the Wanted page disagree (#977).

const statusColors: Record<string, string> = {
  wanted: 'bg-amber-500/20 text-amber-700 dark:text-amber-400',
  downloading: 'bg-blue-500/20 text-blue-700 dark:text-blue-400',
  downloaded: 'bg-purple-500/20 text-purple-700 dark:text-purple-400',
  imported: 'bg-emerald-500/20 text-emerald-700 dark:text-emerald-400',
  skipped: 'bg-slate-300 dark:bg-zinc-700 text-slate-600 dark:text-zinc-400',
}

// Muted slate, used for the not-monitored state and any unknown status.
const mutedColor = 'bg-slate-300 dark:bg-zinc-700 text-slate-600 dark:text-zinc-400'

import type { TFunction } from 'i18next'

// bookStatusBadge returns the label and colour classes for a book's status
// pill, made monitored-aware. Pass i18next's `t`. Callers supply their own
// sizing/layout classes and append `colorClass`.
export function bookStatusBadge(
  status: string,
  monitored: boolean,
  t: TFunction,
): { label: string; colorClass: string } {
  if (status === 'wanted' && !monitored) {
    return { label: t('bookStatus.notMonitored', { defaultValue: 'Not monitored' }), colorClass: mutedColor }
  }
  return { label: t(`bookStatus.${status}`, { defaultValue: status }), colorClass: statusColors[status] ?? mutedColor }
}
