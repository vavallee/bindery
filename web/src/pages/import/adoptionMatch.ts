import type { TFunction } from 'i18next'
import type { AdoptionCandidate, AdoptionItem } from '../../api/client'

// How much a suggestion deserves. Only a strong one gets a one click Confirm:
// a close title alone is not enough ("A Martyrs Tale" scores 0.83 against "The
// Martian"), so the author the scan read must also be the book's author.
// Everything below is a possible match that a person looks at first.

// STRONG_MATCH_SCORE is the title similarity (0 to 1) a suggestion needs, with
// a matching author, to be offered as a one click Confirm.
export const STRONG_MATCH_SCORE = 0.92

export type MatchStrength = 'strong' | 'possible'

function nameTokens(name: string): string[] {
  return name
    .normalize('NFKD')
    .replace(/\p{M}/gu, '')
    .toLowerCase()
    .split(/[^\p{L}\p{N}]+/u)
    .filter(w => w.length > 1)
}

// authorsMatch reports whether every significant word of the author the scan
// read appears in the book's author, so "Weir" and "Andy Weir" match and
// "Andy Weir" and "Ann Leckie" do not. An unreadable author never matches.
export function authorsMatch(parsedAuthor: string, bookAuthor: string): boolean {
  const parsed = nameTokens(parsedAuthor)
  if (parsed.length === 0) return false
  const book = new Set(nameTokens(bookAuthor))
  return parsed.every(w => book.has(w))
}

// alreadySettled reports whether a suggested book is one the scan would not
// have claimed by itself: it already has its files, or it was skipped.
// Suggestions include such books (#2879), so an untracked copy of a book
// already imported can be pointed at it, but adopting there adds a second copy
// or gives files to a book the user skipped. Both deserve a look in the
// editor, where the book's status is shown, so neither is a one click Confirm.
export function alreadySettled(status: string): boolean {
  return status === 'imported' || status === 'skipped'
}

// inOtherFormatRoot reports whether a row sits in the other format's library
// folder, such as an ebook under a separate audiobooks root (#2944). With one
// combined root the server sends no rootFormat and nothing is out of place.
export function inOtherFormatRoot(item: AdoptionItem): boolean {
  return item.rootFormat !== undefined && item.rootFormat !== item.format
}

// preselectable is the suggestion the editor may choose for the reader: the
// first one, unless the files name another author than their folder, when it
// is the first by the files' author. A look alike from the folder's author is
// never picked for them (#2942).
export function preselectable(item: AdoptionItem): AdoptionCandidate | undefined {
  return item.candidates.find(c => !c.folderAuthorOnly)
}

export function matchStrength(item: AdoptionItem, candidate: AdoptionCandidate | undefined = item.candidates[0]): MatchStrength | null {
  if (!candidate) return null
  const author = item.parsedAuthor || item.authorFolder
  // A file in the other format's folder is a look first decision, never a
  // one click Confirm: the scan itself would not have claimed it there.
  return !candidate.folderAuthorOnly && candidate.score >= STRONG_MATCH_SCORE &&
    authorsMatch(author, candidate.book.authorName) && !alreadySettled(candidate.book.status) &&
    !inOtherFormatRoot(item)
    ? 'strong'
    : 'possible'
}

// conflictLine names the two authors when a row's files and folder disagree,
// and is empty when they do not.
export function conflictLine(item: AdoptionItem, t: TFunction): string {
  const c = item.authorConflict
  if (!c) return ''
  return t('adoption.short.authorConflict', { files: c.files, folder: c.folder, defaultValue: 'Files say {{files}}, folder says {{folder}}' })
}

// shortHint is the row's one line: the fact, without the advice, which lives
// in the tooltip and the editor.
export function shortHint(item: AdoptionItem, t: TFunction): string {
  // Too small to be a book says the one thing that matters about the row,
  // whatever its files and folder say about the author (#2944).
  if (item.reason === 'too_small') return t('adoption.short.tooSmall', 'Too small to be a book')
  if (item.authorConflict) return conflictLine(item, t)
  const author = item.parsedAuthor || item.authorFolder
  switch (item.reason) {
    case 'author_not_in_library':
      return author
        ? t('adoption.short.authorMissing', { author, defaultValue: '{{author}} is not in your library' })
        : t('adoption.short.authorUnreadable', 'No author could be read')
    case 'no_candidate_books':
      return t('adoption.short.noCandidates', { author, defaultValue: 'No book by {{author}} is waiting for a file' })
    case 'no_title_parsed':
      return t('adoption.short.noTitle', 'No title could be read')
    default:
      return author
        ? t('adoption.short.noTitleMatch', { author, defaultValue: 'No close title by {{author}}' })
        : t('adoption.short.noTitleMatchNoAuthor', 'No close title in your library')
  }
}
