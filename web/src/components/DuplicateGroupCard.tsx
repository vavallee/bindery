import { Link } from 'react-router'
import { useTranslation } from 'react-i18next'
import {
  DuplicateCandidateGroup,
  DuplicateCandidateMember,
  DuplicateRule,
  DuplicateSignal,
  DuplicateSignalKind,
} from '../api/client'
import { btn, btnSize } from './buttons'

const ruleDefaults: Record<DuplicateRule, string> = {
  'alnum-equal': 'Identical once punctuation, case, and diacritics are ignored',
  'article-strip': 'Same title with a leading article (The, A, An…) dropped',
  'edition-suffix': 'Same title with an edition marker (Unabridged, Audiobook…) dropped',
  'substring': 'One title is the main title or subtitle of the other',
}

// {{rows}} is the row numbers shown beside each title; {{values}} the facts.
const signalDefaults: Record<DuplicateSignalKind, string> = {
  'shared-isbn': 'Rows {{rows}} share ISBN {{values}}',
  'shared-asin': 'Rows {{rows}} share ASIN {{values}}',
  'same-series-position': 'Rows {{rows}} are both {{series}} #{{position}}',
  'series-position-conflict': 'Rows {{rows}} hold different positions in {{series}}: {{positions}}',
  'year-conflict': 'Rows {{rows}} were released years apart: {{values}}',
  'language-conflict': 'Rows {{rows}} are in different languages: {{values}}',
}

const statusDefaults: Record<string, [string, string]> = {
  wanted: ['books.statusWanted', 'Wanted'],
  imported: ['books.statusImported', 'In Library'],
  skipped: ['books.statusSkipped', 'Skipped'],
}

interface Props {
  group: DuplicateCandidateGroup
  busyBooks: Set<number>
  onToggle: (book: DuplicateCandidateMember) => void
  onExcludeEmpty: (group: DuplicateCandidateGroup) => void
  // Library-wide view: show the author and link titles to their book pages.
  showAuthor?: boolean
}

// One duplicate group with the evidence a person needs to pick the row to
// keep (#2999). It never preselects anything for exclusion: the row with
// files is marked as the one to keep, and the only group action is the
// server's suggestion to exclude the empty rows, offered behind a confirm.
export default function DuplicateGroupCard({ group, busyBooks, onToggle, onExcludeEmpty, showAuthor }: Props) {
  const { t } = useTranslation()
  const rowNumber = new Map(group.books.map((b, i) => [b.id, i + 1]))
  const signals = group.signals ?? []
  const suggested = (group.suggestedExcludeIds ?? []).filter(id => {
    const b = group.books.find(m => m.id === id)
    return b && !b.hasFiles && !b.excluded
  })
  const activeWithFiles = group.books.filter(b => !b.excluded && b.hasFiles).length
  // The server says why it withheld a suggestion; an older server does not,
  // so fall back to what the rows themselves show.
  const withheld = group.suggestionWithheld
    ?? (group.conflict && group.keeperId ? 'conflict' : activeWithFiles > 1 ? 'several-with-files' : undefined)

  const chip = (text: string, tone: 'neutral' | 'good' | 'warn' | 'keep' = 'neutral', key?: string) => {
    const tones = {
      neutral: 'bg-slate-200 text-slate-600 dark:bg-zinc-800 dark:text-zinc-400',
      good: 'bg-emerald-100 text-emerald-800 dark:bg-emerald-900/40 dark:text-emerald-300',
      warn: 'bg-amber-100 text-amber-800 dark:bg-amber-900/40 dark:text-amber-300',
      keep: 'bg-emerald-600 text-white',
    }
    return <span key={key ?? text} className={`rounded px-1.5 py-0.5 text-[11px] ${tones[tone]}`}>{text}</span>
  }

  const renderRules = (rules: DuplicateRule[]) => (
    <span className="flex flex-wrap gap-1">
      {rules.map(rule => chip(t(`duplicateCandidates.rules.${rule}`, ruleDefaults[rule]), 'neutral', rule))}
    </span>
  )

  const signalText = (s: DuplicateSignal) => {
    const rows = s.bookIds.map(id => rowNumber.get(id)).filter(Boolean).join(', ')
    return t(`duplicateReview.signals.${s.kind}`, {
      rows,
      values: s.values.join(', '),
      series: s.values[0] ?? '',
      position: s.values[1] ?? '',
      positions: s.values.slice(1).map(p => `#${p}`).join(', '),
      defaultValue: signalDefaults[s.kind] ?? s.kind,
    })
  }

  const fileText = (book: DuplicateCandidateMember) => {
    const files = book.evidence?.files ?? []
    if (files.length === 0) return t('duplicateReview.noFiles', 'No files')
    return files.map(f => {
      const kind = f.kind === 'audiobook' ? t('common.audiobook', 'Audiobook') : t('common.ebook', 'Ebook')
      return f.format ? `${kind} (${f.format})` : kind
    }).join(', ')
  }

  const evidenceItems = (book: DuplicateCandidateMember): string[] => {
    const ev = book.evidence
    const items: string[] = []
    const status = statusDefaults[book.status]
    items.push(status ? t(status[0], status[1]) : book.status)
    if (ev?.year) items.push(String(ev.year))
    if (book.language) items.push(book.language)
    if (ev && ev.isbns.length > 0) {
      const more = ev.isbnCount - 1
      items.push(more > 0
        ? t('duplicateReview.isbnMore', { isbn: ev.isbns[0], count: more, defaultValue: 'ISBN {{isbn}} (+{{count}})' })
        : t('duplicateReview.isbn', { isbn: ev.isbns[0], defaultValue: 'ISBN {{isbn}}' }))
    }
    if (ev && ev.asins.length > 0) items.push(t('duplicateReview.asin', { asin: ev.asins.join(', '), defaultValue: 'ASIN {{asin}}' }))
    for (const s of ev?.series ?? []) {
      items.push(s.position ? `${s.title} #${s.position}` : s.title)
    }
    return items
  }

  return (
    <section
      className="border-b border-slate-200 px-3 py-3 last:border-b-0 dark:border-zinc-800"
      aria-label={t('duplicateReview.groupLabel', { title: group.books[0]?.title ?? group.key, defaultValue: 'Duplicate group: {{title}}' })}
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="text-xs font-semibold text-slate-800 dark:text-zinc-200">
          {showAuthor && group.authorName && group.authorId ? (
            <>
              <Link to={`/author/${group.authorId}`} className="hover:underline">{group.authorName}</Link>
              <span className="mx-1 text-slate-400 dark:text-zinc-500">·</span>
            </>
          ) : null}
          {t('duplicateCandidates.group', { count: group.books.length, defaultValue: '{{count}} row(s)' })}
        </span>
        {renderRules(group.rules)}
      </div>

      {signals.length > 0 && (
        <ul className="mt-2 flex flex-col gap-1" aria-label={t('duplicateReview.signalsLabel', 'Evidence across rows')}>
          {signals.map(s => (
            <li
              key={`${s.kind}:${s.bookIds.join(',')}:${s.values.join(',')}`}
              data-conflict={s.conflict ? 'true' : 'false'}
              className={`rounded px-2 py-1 text-[11px] ${s.conflict
                ? 'bg-amber-100 text-amber-900 dark:bg-amber-900/40 dark:text-amber-200'
                : 'bg-emerald-50 text-emerald-900 dark:bg-emerald-900/30 dark:text-emerald-200'}`}
            >
              <span className="font-semibold">
                {s.conflict
                  ? t('duplicateReview.conflictLabel', 'May be different books:')
                  : t('duplicateReview.agreementLabel', 'Likely the same book:')}
              </span>{' '}
              {signalText(s)}
            </li>
          ))}
        </ul>
      )}

      <ul className="mt-2 divide-y divide-slate-100 dark:divide-zinc-800">
        {group.books.map(book => {
          const isKeeper = group.keeperId === book.id
          return (
            <li
              key={book.id}
              data-testid={`duplicate-row-${book.id}`}
              className={`flex items-start justify-between gap-3 py-2 text-xs ${isKeeper ? 'rounded bg-emerald-50/70 px-1 dark:bg-emerald-900/20' : ''}`}
            >
              <span className="min-w-0">
                <span className="flex items-baseline gap-1.5">
                  <span className="shrink-0 text-[11px] text-slate-400 dark:text-zinc-500">{rowNumber.get(book.id)}.</span>
                  {showAuthor ? (
                    <Link
                      to={`/book/${book.id}`}
                      className={`block truncate font-medium hover:underline ${book.excluded
                        ? 'text-slate-400 line-through dark:text-zinc-500'
                        : 'text-slate-800 dark:text-zinc-200'}`}
                    >
                      {book.title}
                    </Link>
                  ) : (
                    <span
                      className={`block truncate font-medium ${book.excluded
                        ? 'text-slate-400 line-through dark:text-zinc-500'
                        : 'text-slate-800 dark:text-zinc-200'}`}
                    >
                      {book.title}
                    </span>
                  )}
                </span>
                <span className="mt-0.5 flex flex-wrap items-center gap-1">
                  {isKeeper && chip(t('duplicateReview.keep', 'Keep: has files'), 'keep')}
                  {!isKeeper && book.hasFiles && chip(t('duplicateReview.hasFiles', 'Has files'), 'good')}
                  {book.excluded && chip(t('duplicateCandidates.excluded', 'Excluded'), 'warn')}
                  {renderRules(book.rules)}
                </span>
                <span className="mt-1 block text-[11px] text-slate-600 dark:text-zinc-400" data-testid={`duplicate-evidence-${book.id}`}>
                  <span className={book.hasFiles ? 'font-medium text-emerald-700 dark:text-emerald-400' : ''}>{fileText(book)}</span>
                  {evidenceItems(book).map((item, i) => (
                    <span key={i}>
                      <span className="mx-1 text-slate-400 dark:text-zinc-600">·</span>
                      {item}
                    </span>
                  ))}
                </span>
              </span>
              <button
                type="button"
                onClick={() => onToggle(book)}
                disabled={busyBooks.has(book.id)}
                className={`${btn.secondary} ${btnSize.sm} shrink-0`}
                aria-label={book.excluded
                  ? t('duplicateCandidates.include', 'Include')
                  : t('duplicateCandidates.exclude', 'Exclude')}
              >
                {book.excluded
                  ? t('duplicateCandidates.include', 'Include')
                  : t('duplicateCandidates.exclude', 'Exclude')}
              </button>
            </li>
          )
        })}
      </ul>

      {suggested.length > 0 ? (
        <div className="mt-2 flex justify-end">
          <button
            type="button"
            onClick={() => onExcludeEmpty(group)}
            disabled={suggested.some(id => busyBooks.has(id))}
            className={`${btn.secondary} ${btnSize.sm}`}
          >
            {t('duplicateReview.excludeEmpty', { count: suggested.length, defaultValue: 'Exclude the {{count}} empty row(s) in this group' })}
          </button>
        </div>
      ) : withheld === 'conflict' ? (
        <p className="mt-2 text-[11px] text-slate-500 dark:text-zinc-400">
          {t('duplicateReview.noSuggestionConflict', 'The rows disagree, so nothing is suggested. Check the evidence before excluding a row.')}
        </p>
      ) : withheld === 'several-with-files' ? (
        <p className="mt-2 text-[11px] text-slate-500 dark:text-zinc-400">
          {t('duplicateReview.noSuggestionFiles', 'More than one row has files, so nothing is suggested.')}
        </p>
      ) : withheld === 'no-evidence' ? (
        <p className="mt-2 text-[11px] text-slate-500 dark:text-zinc-400">
          {t('duplicateReview.noSuggestionEvidence', 'Nothing ties every empty row to the row with files (no shared ISBN, ASIN, series position or matching title), so nothing is suggested.')}
        </p>
      ) : null}
    </section>
  )
}
