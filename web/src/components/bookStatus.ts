// Shared status-badge logic for library books, used by the book detail page and
// the book/author list views so every surface agrees with the Wanted page.
//
// `status` and `monitored` are orthogonal: `status` is the acquisition
// lifecycle (every book starts `wanted`), while `monitored` is whether Bindery
// actively pursues it. The Wanted page lists `status=wanted AND monitored`, so a
// backlist book (`wanted` + unmonitored) must NOT read "Wanted" on its detail
// page or in lists — otherwise the pill and the Wanted page disagree (#977).

// Text shades are the AA-safe pair for each tinted background: amber-700 and
// emerald-700 on their `/20` tints fall just under 4.5:1 on a light card
// (3.99 and 4.20), so light uses the -800 step; zinc-400 on zinc-700 is 4.07,
// so dark muted uses zinc-300. See bookStatus.test.ts.
const statusColors: Record<string, string> = {
  wanted: 'bg-amber-500/20 text-amber-800 dark:text-amber-400',
  imported: 'bg-emerald-500/20 text-emerald-800 dark:text-emerald-400',
  skipped: 'bg-slate-300 dark:bg-zinc-700 text-slate-600 dark:text-zinc-300',
}

// Muted slate, used for the not-monitored state and any unknown status.
const mutedColor = 'bg-slate-300 dark:bg-zinc-700 text-slate-600 dark:text-zinc-300'

import type { TFunction } from 'i18next'

// One sentence per pill saying what the state means for the user, in the
// terms they ask about: is a file missing, and will Bindery go and get it.
// "Wanted" versus "monitored" is the single most asked question in support,
// and the legend used to show the four swatches with no explanation at all.
const statusDescriptions: Record<string, [string, string]> = {
  wanted: ['bookStatus.wantedDescription', 'A format is missing and Bindery will search for it on the next sweep.'],
  imported: ['bookStatus.importedDescription', 'Every format this book wants is on disk. A book with only some of its formats reads Wanted, and the In library filter still lists it.'],
  skipped: ['bookStatus.skippedDescription', 'Blocklisted or excluded. Bindery will not search for it and it stays off the Wanted page.'],
}

// bookStatusBadge returns the label, colour classes and a one-line
// description for a book's status pill, made monitored-aware. Pass i18next's
// `t`. Callers supply their own sizing/layout classes and append `colorClass`;
// `description` goes on the pill's title and in the legend.
export function bookStatusBadge(
  status: string,
  monitored: boolean,
  t: TFunction,
): { label: string; colorClass: string; description: string } {
  if (status === 'wanted' && !monitored) {
    return {
      label: t('bookStatus.notMonitored', { defaultValue: 'Not monitored' }),
      colorClass: mutedColor,
      description: t('bookStatus.notMonitoredDescription', { defaultValue: 'A format is missing, but Bindery will not search for it. Turn monitoring on to fetch it.' }),
    }
  }
  const [key, fallback] = statusDescriptions[status] ?? ['', '']
  return {
    label: t(`bookStatus.${status}`, { defaultValue: status }),
    colorClass: statusColors[status] ?? mutedColor,
    description: key ? t(key, { defaultValue: fallback }) : '',
  }
}
