import { describe, expect, it, vi } from 'vitest'

import { hardcoverSlugFromQuery, normalizeISBNInput, resolveBookQuery } from './booklookup'
import { api } from './client'

const ISBN_13 = '9780441172719'
const ISBN_10 = '0441172717'

// The separators a real ISBN arrives with. A publisher's page, a PDF, a
// spreadsheet and a word processor each substitute a different one for the
// plain hyphen, and only the ASCII hyphen used to be stripped — so pasting the
// ISBN of a book Bindery could have found fell through to the title search and
// returned nothing, with nothing on screen to explain why.
const separated: Array<[string, string]> = [
  ['ascii hyphen', '978-0-441-17271-9'],
  ['hyphen U+2010', '978‐0‐441‐17271‐9'],
  ['non-breaking hyphen U+2011', '978‑0‑441‑17271‑9'],
  ['figure dash U+2012', '978‒0‒441‒17271‒9'],
  ['en dash U+2013', '978–0–441–17271–9'],
  ['em dash U+2014', '978—0—441—17271—9'],
  ['horizontal bar U+2015', '978―0―441―17271―9'],
  ['minus sign U+2212', '978−0−441−17271−9'],
  ['soft hyphen U+00AD', '978­0­441­17271­9'],
  ['spaces', '978 0 441 17271 9'],
  ['non-breaking spaces', '978 0 441 17271 9'],
  ['mixed', '978–0 441‑17271­9'],
]

describe('normalizeISBNInput', () => {
  it.each(separated)('strips a %s', (_name, input) => {
    expect(normalizeISBNInput(input)).toBe(ISBN_13)
  })

  it('strips the dash class out of an ISBN-10 too', () => {
    expect(normalizeISBNInput('0–441–17271–7')).toBe(ISBN_10)
  })

  it('leaves the digits and an X check digit alone', () => {
    expect(normalizeISBNInput('080442957X')).toBe('080442957X')
  })

  it('does not touch a title', () => {
    expect(normalizeISBNInput('Artemis')).toBe('Artemis')
  })
})

describe('resolveBookQuery routes a separated ISBN to the lookup endpoint', () => {
  it.each(separated)('recognises an ISBN written with a %s', async (_name, input) => {
    const lookupISBN = vi.spyOn(api, 'lookupISBN').mockResolvedValue({ id: 1 } as never)
    const searchBooks = vi.spyOn(api, 'searchBooks').mockResolvedValue([] as never)
    try {
      await resolveBookQuery(input)
      expect(lookupISBN).toHaveBeenCalledWith(ISBN_13)
      expect(searchBooks).not.toHaveBeenCalled()
    } finally {
      lookupISBN.mockRestore()
      searchBooks.mockRestore()
    }
  })

  it('still sends a title to the search endpoint', async () => {
    const lookupISBN = vi.spyOn(api, 'lookupISBN').mockResolvedValue({ id: 1 } as never)
    const searchBooks = vi.spyOn(api, 'searchBooks').mockResolvedValue([] as never)
    try {
      await resolveBookQuery('Project Hail Mary')
      expect(searchBooks).toHaveBeenCalledWith('Project Hail Mary')
      expect(lookupISBN).not.toHaveBeenCalled()
    } finally {
      lookupISBN.mockRestore()
      searchBooks.mockRestore()
    }
  })
})

// A Hardcover book reference names one book, so it goes to the lookup rather
// than a title search that may not surface the book at all (#3839).
describe('hardcoverSlugFromQuery', () => {
  it.each([
    ['the hc: id', 'hc:howl-of-the-hearthworld'],
    ['an upper case prefix', 'HC:howl-of-the-hearthworld'],
    ['the book page', 'https://hardcover.app/books/howl-of-the-hearthworld'],
    ['a page with a trailing slash', 'https://hardcover.app/books/howl-of-the-hearthworld/'],
    ['a tab of the page', 'https://hardcover.app/books/howl-of-the-hearthworld/editions'],
    ['a query string', 'https://hardcover.app/books/howl-of-the-hearthworld?ref=search'],
    ['www and http', 'http://www.hardcover.app/books/howl-of-the-hearthworld'],
    ['no scheme', 'hardcover.app/books/howl-of-the-hearthworld'],
    ['surrounding spaces', '  https://hardcover.app/books/howl-of-the-hearthworld  '],
  ])('reads the slug from %s', (_name, input) => {
    expect(hardcoverSlugFromQuery(input)).toBe('howl-of-the-hearthworld')
  })

  it.each([
    ['a title', 'Howl of the Hearthworld'],
    ['an empty hc: id', 'hc:'],
    ['an author page', 'https://hardcover.app/authors/some-author'],
    ['another site', 'https://example.com/books/howl-of-the-hearthworld'],
    ['a look-alike host', 'https://hardcover.app.example.com/books/howl'],
    ['the books index', 'https://hardcover.app/books/'],
  ])('returns null for %s', (_name, input) => {
    expect(hardcoverSlugFromQuery(input)).toBeNull()
  })
})

describe('resolveBookQuery routes a Hardcover reference to the lookup endpoint', () => {
  it.each([
    ['hc:howl-of-the-hearthworld'],
    ['https://hardcover.app/books/howl-of-the-hearthworld'],
  ])('looks up %s by its slug', async input => {
    const lookupHardcover = vi.spyOn(api, 'lookupHardcover').mockResolvedValue({ id: 1 } as never)
    const searchBooks = vi.spyOn(api, 'searchBooks').mockResolvedValue([] as never)
    try {
      await resolveBookQuery(input)
      expect(lookupHardcover).toHaveBeenCalledWith('howl-of-the-hearthworld')
      expect(searchBooks).not.toHaveBeenCalled()
    } finally {
      lookupHardcover.mockRestore()
      searchBooks.mockRestore()
    }
  })
})
