// A JavaScript port of Go's textutil.FoldForSearch (alphabet 6). See
// docs/search-design.md for the reasoning and the primary sources.
//
// Two kinds of caller need it:
//
//   - The list filters that run in the browser (Wanted, the series-book picker)
//     compare with toLowerCase().includes(), which folds case but not accents,
//     so "cafe" never matched "Café" on the client even where the same query
//     worked against the server.
//   - Anything comparing a metadata result against what the user typed, where
//     the two sides come from different providers and different keyboards.
//
// Parity with the Go fold is asserted by foldForSearch.test.ts, which reads the
// same fixture file the Go test reads. That shared corpus is the point: the
// server folds the stored title and the browser folds the query, so a
// disagreement between them is the two-alphabet bug this work exists to remove,
// and it would surface as "search finds nothing" rather than as a failing test.
//
// JavaScript has no full Unicode case folding — toLowerCase is a case *mapping*,
// so it leaves ß alone — so the letters folding would have handled are in the
// explicit table below.

// Latin letters with no canonical decomposition. NFD does nothing for these, so
// stripping combining marks alone leaves "Nesbø" unreachable by "nesbo": the
// withdrawn UTR #30 distinction between accent removal and diacritic removal.
const NON_DECOMPOSABLE: Record<string, string> = {
  ø: 'o', ł: 'l', æ: 'ae', œ: 'oe', ß: 'ss', þ: 'th',
  ð: 'd', đ: 'd', ħ: 'h', ı: 'i', ŀ: 'l',
  // Greek final sigma, which Go gets for free from case folding.
  ς: 'σ',
  // The one deliberate Cyrillic fold: Russian is routinely typed with е for ё.
  ё: 'е',
}
const NON_DECOMPOSABLE_RE = /[øłæœßþðđħıŀςё]/gu

const APOSTROPHES = /['’‘`ʼ]/gu
// Katakana middle dot: catalogues disagree about writing it, so deleting rather
// than separating makes ハリー・ポッター and ハリーポッター converge (#1645).
const MIDDLE_DOT = /・/gu
// Marks are stripped only where they are diacritics. A kana dakuten changes the
// letter and a Devanagari vowel sign is a spacing mark that is part of the word,
// so a blanket \p{M} strip would merge unrelated titles. Mn only, matching the
// Go stripLatinGreekMarks: \p{M} also covers Mc and Me, which that function
// keeps, so the wider class made the two sides disagree on a spacing mark
// sitting after a Latin letter.
const LATIN_GREEK_MARKS = /(?<=[\p{Script=Latin}\p{Script=Greek}])\p{Mn}+/gu
// Everything that is not a letter, a number or a mark is a separator. Marks are
// word characters here for the reason directly above.
const SEPARATORS = /[^\p{L}\p{N}\p{M}]+/gu

export function foldForSearch(value: string | undefined | null): string {
  const input = (value ?? '').trim()
  if (input === '') return ''
  let s = input.normalize('NFKC').toLowerCase()
  // Marks first, table second, matching the Go order. The table keys on the
  // bare letters, and a precomposed ǣ or ǿ only becomes one of them once NFD
  // has dropped its macron or acute. The other way round, "Ǣlfric" folded to
  // "ælfric" while "Ælfric" folded to "aelfric".
  s = s.normalize('NFD').replace(LATIN_GREEK_MARKS, '').normalize('NFC')
  s = s.replace(NON_DECOMPOSABLE_RE, ch => NON_DECOMPOSABLE[ch] ?? ch)
  s = s.replace(APOSTROPHES, '').replace(MIDDLE_DOT, '')
  s = s.replace(/&/gu, ' and ')
  return s.replace(SEPARATORS, ' ').trim()
}

// foldedIncludes reports whether haystack contains needle once both are folded.
// It is the client-side equivalent of the server's LIKE '%q%' over search_key.
export function foldedIncludes(haystack: string | undefined | null, needle: string): boolean {
  const q = foldForSearch(needle)
  if (q === '') return true
  return foldForSearch(haystack).includes(q)
}
