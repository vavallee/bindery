import { describe, expect, it } from 'vitest'
import type { TFunction } from 'i18next'
import en from '../../i18n/locales/en.json'
import type { AdoptionItem } from '../../api/client'
import { adoptionHint, scorePercent } from './adoptionHint'
import { STRONG_MATCH_SCORE, authorsMatch, inOtherFormatRoot, matchStrength, shortHint } from './adoptionMatch'

const lookup = (key: string): unknown =>
  key.split('.').reduce<unknown>((n, p) => (n && typeof n === 'object' ? (n as Record<string, unknown>)[p] : undefined), en)

const t = ((key: string, options?: string | Record<string, unknown>) => {
  const opts = typeof options === 'object' ? options : {}
  const value = lookup(key)
  return (typeof value === 'string' ? value : key).replace(/\{\{(\w+)\}\}/g, (_, k) => String(opts[k] ?? ''))
}) as unknown as TFunction

function item(overrides: Partial<AdoptionItem>): AdoptionItem {
  return {
    id: 1, kind: 'file', format: 'ebook', fileCount: 1, sizeBytes: 1, relPath: 'A/B.epub', rootPath: '/books',
    authorFolder: 'A', parsedTitle: 'B', parsedAuthor: 'Becky Chambers', reason: 'no_title_match', candidates: [],
    topScore: 0, state: 'pending', bookCreated: false, authorCreated: false, members: [], firstSeenAt: '',
    ...overrides,
  }
}

// The row's sentence is written from facts and ends in an action; the raw
// reason code only ever appears in the tooltip (#2033).
describe('adoptionHint', () => {
  const codes = ['author_not_in_library', 'no_candidate_books', 'no_title_match', 'no_title_parsed'] as const

  it.each(codes)('never puts the %s code in the sentence, and ends in an action', code => {
    const hint = adoptionHint(item({ reason: code }), t)
    expect(hint.sentence).not.toContain(code)
    expect(hint.sentence).toMatch(/(Add the author, then scan again|Choose the book[^.]*|search metadata)\.$/)
    expect(hint.tooltip).toContain(code)
  })

  it('names the author the scan read when the author is missing', () => {
    expect(adoptionHint(item({ reason: 'author_not_in_library' }), t).sentence)
      .toBe('Becky Chambers is not in your library yet. Add the author, then scan again.')
  })

  const martian = { id: 9, title: 'The Martian', authorId: 1, authorName: 'Andy Weir', status: 'wanted', mediaType: 'ebook', monitored: true }

  it('calls a close title by the same author a strong match', () => {
    const strong = item({ parsedAuthor: 'Andy Weir', candidates: [{ book: martian, score: 0.95 }] })
    expect(matchStrength(strong)).toBe('strong')
    expect(adoptionHint(strong, t).sentence).toBe('Strong match: The Martian by Andy Weir. Confirm it or choose another book.')
  })

  it('calls anything weaker a possible match, never a one click confirm', () => {
    // 0.82 is the "A Martyrs Tale" against "The Martian" case.
    const weak = item({ parsedAuthor: 'Andy Weir', candidates: [{ book: martian, score: 0.82 }] })
    expect(matchStrength(weak)).toBe('possible')
    expect(adoptionHint(weak, t).sentence).not.toContain('Confirm')
    // A near identical title by another author is still only possible.
    expect(matchStrength(item({ parsedAuthor: 'Ann Leckie', candidates: [{ book: martian, score: 0.99 }] }))).toBe('possible')
    expect(STRONG_MATCH_SCORE).toBe(0.92)
  })

  it('never offers a one click confirm into a book that already has its files or was skipped (#2879)', () => {
    for (const status of ['imported', 'skipped']) {
      const settled = item({ parsedAuthor: 'Andy Weir', candidates: [{ book: { ...martian, status }, score: 1 }] })
      expect(matchStrength(settled)).toBe('possible')
      expect(adoptionHint(settled, t).sentence).not.toContain('Confirm')
    }
  })

  // #2944: a 1 KB .txt under the audiobooks root was offered as a strong
  // match and adopted in one click as the book's ebook.
  it('never offers a one click confirm for a row in the other format\'s folder', () => {
    const misplaced = item({ format: 'ebook', rootFormat: 'audiobook', parsedAuthor: 'Andy Weir', candidates: [{ book: martian, score: 1 }] })
    expect(matchStrength(misplaced)).toBe('possible')
    expect(adoptionHint(misplaced, t).sentence).not.toContain('Confirm')
    expect(inOtherFormatRoot(misplaced)).toBe(true)
    expect(inOtherFormatRoot(item({ format: 'audiobook', rootFormat: 'ebook' }))).toBe(true)
    // Same folder as its format, or one combined root: nothing to say.
    expect(inOtherFormatRoot(item({ format: 'ebook', rootFormat: 'ebook' }))).toBe(false)
    expect(inOtherFormatRoot(item({ format: 'ebook' }))).toBe(false)
    expect(matchStrength(item({ format: 'ebook', parsedAuthor: 'Andy Weir', candidates: [{ book: martian, score: 1 }] }))).toBe('strong')
  })

  it('says a too small file is not a book, and offers no adoption', () => {
    const tiny = item({ reason: 'too_small', sizeBytes: 1008, relPath: 'James Patterson/Die 6. Geisel ()/Die 6. Geisel.txt' })
    const hint = adoptionHint(tiny, t)
    expect(hint.sentence).toMatch(/too small to be a book/)
    expect(hint.sentence).not.toMatch(/Choose the book|Confirm/)
    expect(hint.tooltip).toContain('too_small')
    expect(shortHint(tiny, t)).toBe('Too small to be a book')
  })

  it('matches authors by their words', () => {
    expect(authorsMatch('Weir', 'Andy Weir')).toBe(true)
    expect(authorsMatch('Álvaro Enrigue', 'Alvaro Enrigue')).toBe(true)
    expect(authorsMatch('Andy Weir', 'Ann Leckie')).toBe(false)
    expect(authorsMatch('', 'Andy Weir')).toBe(false)
  })

  it('clamps scores to a percentage', () => {
    expect([scorePercent(-1), scorePercent(0.6), scorePercent(1.4)]).toEqual([0, 60, 100])
  })
})
