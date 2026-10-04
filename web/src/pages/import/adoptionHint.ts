import type { TFunction } from 'i18next'
import type { AdoptionItem } from '../../api/client'
import { matchStrength, preselectable } from './adoptionMatch'

// The one sentence a row says about itself. It is written from facts the scan
// recorded (the parsed author, the closest title) and ends in the thing to do
// next. The scanner's reason code is only ever a tooltip: the codes are exact
// but read as blame, and "no title match" was what sent #2033's reporter off
// renaming files that were fine.

export interface AdoptionHintText {
  sentence: string
  tooltip: string
}

// A score is a 0 to 1 Jaro-Winkler similarity; people read a percentage.
export function scorePercent(score: number): number {
  return Math.round(Math.max(0, Math.min(1, score)) * 100)
}

export function adoptionHint(item: AdoptionItem, t: TFunction): AdoptionHintText {
  const tooltip = item.reason
    ? t('adoption.hint.reasonTooltip', { code: item.reason, defaultValue: 'Scan reason: {{code}}' })
    : ''
  const author = item.parsedAuthor
  // A look alike by the folder's author is not what the row is about when the
  // files name someone else (#2942); the hint speaks of the files' author.
  const top = preselectable(item)

  if (item.reason === 'too_small') {
    return {
      sentence: t('adoption.hint.tooSmall', 'This file is too small to be a book, so it cannot be adopted. Ignore it, or delete it from the folder.'),
      tooltip,
    }
  }

  if (top) {
    const strong = matchStrength(item, top) === 'strong'
    return {
      sentence: strong
        ? t('adoption.hint.strong', {
          title: top.book.title, author: top.book.authorName,
          defaultValue: 'Strong match: {{title}} by {{author}}. Confirm it or choose another book.',
        })
        : t('adoption.hint.possible', {
          title: top.book.title, author: top.book.authorName, percent: scorePercent(top.score),
          defaultValue: 'Possible match: {{title}} by {{author}}, {{percent}}% title similarity. Check it, then adopt it or choose another book.',
        }),
      tooltip,
    }
  }
  switch (item.reason) {
    case 'author_not_in_library':
      return {
        sentence: author
          ? t('adoption.hint.authorMissing', { author, defaultValue: '{{author}} is not in your library yet. Add the author, then scan again.' })
          : t('adoption.hint.authorUnreadable', 'No author could be read from these files. Choose the book they are.'),
        tooltip,
      }
    case 'no_candidate_books':
      return {
        sentence: t('adoption.hint.noCandidates', {
          author: author || item.authorFolder,
          defaultValue: '{{author}} has no book in your library waiting for these files. Choose the book or search metadata.',
        }),
        tooltip,
      }
    case 'no_title_parsed':
      return {
        sentence: t('adoption.hint.noTitle', 'No title could be read from the file names or tags. Choose the book they are.'),
        tooltip,
      }
    default:
      return {
        sentence: author
          ? t('adoption.hint.noTitleMatch', { author, defaultValue: 'No book by {{author}} has a title close to this one. Choose the book it is.' })
          : t('adoption.hint.noTitleMatchNoAuthor', 'No book in your library has a title close to this one. Choose the book it is.'),
        tooltip,
      }
  }
}

// The name a row goes by: the title the scan read, else the last part of its
// path.
export function unitDisplayName(item: AdoptionItem): string {
  if (item.parsedTitle) return item.parsedTitle
  const parts = item.relPath.split('/')
  return parts[parts.length - 1] || item.relPath
}

// "2 hours ago", in the reader's language.
export function relativeTime(iso: string | undefined, locale?: string): string {
  if (!iso) return ''
  const then = Date.parse(iso)
  if (Number.isNaN(then)) return ''
  const seconds = Math.round((then - Date.now()) / 1000)
  const rtf = new Intl.RelativeTimeFormat(locale, { numeric: 'auto' })
  const steps: [Intl.RelativeTimeFormatUnit, number][] = [['day', 86400], ['hour', 3600], ['minute', 60]]
  for (const [unit, size] of steps) {
    if (Math.abs(seconds) >= size) return rtf.format(Math.round(seconds / size), unit)
  }
  return rtf.format(0, 'minute')
}
