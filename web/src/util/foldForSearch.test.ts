import { describe, expect, it } from 'vitest'

import fixtures from '../../../internal/textutil/testdata/search_fixtures.json'

import { foldedIncludes, foldForSearch } from './foldForSearch'

interface Fixture {
  input: string
  want: string
  issue: string
  note?: string
}

const rows = fixtures as Fixture[]

// This file reads the corpus internal/textutil/foldsearch_test.go reads. The
// server folds the stored title and the browser folds what the user typed; if
// the two folds disagree the search silently returns nothing, so the only
// useful guard is one both suites share.
describe('foldForSearch matches the Go fold', () => {
  it('has fixtures to check', () => {
    expect(rows.length).toBeGreaterThan(20)
  })

  it.each(rows)('folds $input (#$issue)', ({ input, want }) => {
    expect(foldForSearch(input)).toBe(want)
  })

  it('is idempotent, so a folded key folds to itself', () => {
    for (const { want } of rows) expect(foldForSearch(want)).toBe(want)
  })

  it('agrees across Unicode normalization forms', () => {
    for (const { input, want } of rows) {
      expect(foldForSearch(input.normalize('NFC'))).toBe(want)
      expect(foldForSearch(input.normalize('NFD'))).toBe(want)
    }
  })

  it('handles null and undefined like the empty string', () => {
    expect(foldForSearch(null)).toBe('')
    expect(foldForSearch(undefined)).toBe('')
  })
})

// The counterweight: an over-eager fold corrupts data, where a missing one only
// hides it. #1645 collapsed every non-Latin series onto one key.
describe('foldForSearch keeps distinct things distinct', () => {
  it.each([
    ['ハード', 'ハート', 'kana dakuten is part of the letter'],
    ['Толстой', 'Толстои', 'Cyrillic й and и are separate letters'],
    ['कमला', 'कमल', 'Devanagari vowel signs are spacing marks'],
    ['Dune', 'Dunes', 'no stemming'],
  ])('%s is not %s (%s)', (a, b) => {
    expect(foldForSearch(a)).not.toBe(foldForSearch(b))
  })
})

describe('foldedIncludes', () => {
  it('matches accented text from an unaccented query', () => {
    expect(foldedIncludes('Café Society', 'cafe')).toBe(true)
    expect(foldedIncludes('Harry Potter und der Orden des Phönix', 'phonix')).toBe(true)
    expect(foldedIncludes('Jo Nesbø', 'nesbo')).toBe(true)
  })

  it('matches across apostrophe and ampersand spellings', () => {
    expect(foldedIncludes("Poseidon's Arrow", 'poseidons')).toBe(true)
    expect(foldedIncludes('Foundation & Empire', 'foundation and empire')).toBe(true)
  })

  it('still rejects what does not match', () => {
    expect(foldedIncludes('The Hobbit', 'dune')).toBe(false)
  })

  it('treats an empty query as matching everything', () => {
    expect(foldedIncludes('anything', '')).toBe(true)
  })
})
