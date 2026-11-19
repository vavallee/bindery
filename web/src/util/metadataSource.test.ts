import { describe, it, expect } from 'vitest'
import {
  hardcoverSeriesUrl,
  metadataRetrievedDate,
  metadataSourceLink,
  providerDisplayName,
  providerFromBookForeignId,
} from './metadataSource'

describe('metadataSourceLink', () => {
  it('links OpenLibrary authors', () => {
    expect(metadataSourceLink('OL23919A', 'author')).toEqual({
      url: 'https://openlibrary.org/authors/OL23919A',
      label: 'OpenLibrary',
    })
  })

  it('links OpenLibrary works and editions for books', () => {
    expect(metadataSourceLink('OL27448W', 'book')?.url).toBe('https://openlibrary.org/works/OL27448W')
    expect(metadataSourceLink('OL7353617M', 'book')?.url).toBe('https://openlibrary.org/books/OL7353617M')
  })

  it('links Google Books only for books', () => {
    expect(metadataSourceLink('gb:zyTCAlFPjgYC', 'book')).toEqual({
      url: 'https://books.google.com/books?id=zyTCAlFPjgYC',
      label: 'Google Books',
    })
    expect(metadataSourceLink('gb:zyTCAlFPjgYC', 'author')).toBeNull()
  })

  it('links unambiguous Hardcover book slugs', () => {
    expect(metadataSourceLink('hc:project-hail-mary', 'book')).toEqual({
      url: 'https://hardcover.app/books/project-hail-mary',
      label: 'Hardcover',
    })
    expect(metadataSourceLink('HC:project-hail-mary', 'book')).toEqual({
      url: 'https://hardcover.app/books/project-hail-mary',
      label: 'Hardcover',
    })
  })

  it('hides ambiguous numeric Hardcover identifiers', () => {
    expect(metadataSourceLink('hc:1984', 'book')).toBeNull()
    expect(metadataSourceLink('hc:12345', 'book')).toBeNull()
  })

  it('links DNB book control numbers', () => {
    expect(metadataSourceLink('dnb:123456789', 'book')).toEqual({
      url: 'https://d-nb.info/123456789',
      label: 'DNB',
    })
    expect(metadataSourceLink('DNB:123456789', 'book')).toEqual({
      url: 'https://d-nb.info/123456789',
      label: 'DNB',
    })
  })

  it('links NB book records but not authority-file authors', () => {
    expect(metadataSourceLink('nb:a0000000000000000000000000000001', 'book')).toEqual({
      url: 'https://www.nb.no/items/a0000000000000000000000000000001',
      label: 'Nasjonalbiblioteket',
    })
    expect(metadataSourceLink('nb:author:10000001', 'author')).toBeNull()
    expect(metadataSourceLink('nb:../x', 'book')).toBeNull()
  })

  it('links BnF book records to their catalogue ARK but not authority-file authors', () => {
    expect(metadataSourceLink('bnf:cb48780493c', 'book')).toEqual({
      url: 'https://catalogue.bnf.fr/ark:/12148/cb48780493c',
      label: 'BnF',
    })
    expect(metadataSourceLink('bnf:author:11885918', 'author')).toBeNull()
    expect(metadataSourceLink('bnf:author:11885918', 'book')).toBeNull()
    expect(metadataSourceLink('bnf:../x', 'book')).toBeNull()
    expect(metadataSourceLink('bnf:cb48780493c', 'author')).toBeNull()
  })

  it('returns null for local or malformed provider ids', () => {
    expect(metadataSourceLink('abs:abc', 'book')).toBeNull()
    expect(metadataSourceLink('calibre:7', 'book')).toBeNull()
    expect(metadataSourceLink('hc:bad/value', 'book')).toBeNull()
    expect(metadataSourceLink('dnb:gnd:118585665', 'book')).toBeNull()
    expect(metadataSourceLink('hc:project-hail-mary', 'author')).toBeNull()
  })

  it('returns null for empty / malformed ids', () => {
    expect(metadataSourceLink('', 'author')).toBeNull()
    expect(metadataSourceLink(undefined, 'book')).toBeNull()
    expect(metadataSourceLink('gb:', 'book')).toBeNull()
    expect(metadataSourceLink('OL123', 'author')).toBeNull() // no trailing A
  })
})

describe('metadataRetrievedDate', () => {
  it('prefers the last metadata fetch, then the date the row was added', () => {
    expect(metadataRetrievedDate({ lastMetadataRefreshAt: '2026-10-09T18:30:00Z', createdAt: '2026-10-02T09:00:00Z' })).toBe('2026-10-09')
    expect(metadataRetrievedDate({ lastMetadataRefreshAt: null, createdAt: '2026-10-02T09:00:00Z' })).toBe('2026-10-02')
  })

  it('returns null when there is no usable date', () => {
    expect(metadataRetrievedDate(undefined)).toBeNull()
    expect(metadataRetrievedDate({})).toBeNull()
    expect(metadataRetrievedDate({ createdAt: 'not a date' })).toBeNull()
    expect(metadataRetrievedDate({ createdAt: '0001-01-01T00:00:00Z' })).toBeNull()
  })
})

describe('providerDisplayName', () => {
  it('names the providers a book can come from', () => {
    expect(providerDisplayName('openlibrary')).toBe('OpenLibrary')
    expect(providerDisplayName('hardcover')).toBe('Hardcover')
    expect(providerDisplayName('googlebooks')).toBe('Google Books')
    expect(providerDisplayName('dnb')).toBe('DNB')
    expect(providerDisplayName('nb')).toBe('Nasjonalbiblioteket')
    expect(providerDisplayName('bnf')).toBe('BnF')
    expect(providerDisplayName('calibre')).toBe('Calibre')
    expect(providerDisplayName('audiobookshelf')).toBe('Audiobookshelf')
  })

  it('is case insensitive and trims', () => {
    expect(providerDisplayName('  HardCover ')).toBe('Hardcover')
  })

  it('passes an unknown provider through rather than hiding it', () => {
    expect(providerDisplayName('somethingnew')).toBe('somethingnew')
  })

  it('returns empty for no provider', () => {
    expect(providerDisplayName('')).toBe('')
    expect(providerDisplayName(undefined)).toBe('')
    expect(providerDisplayName(null)).toBe('')
  })
})

describe('providerFromBookForeignId', () => {
  it('mirrors models.BookProviderFromForeignID', () => {
    expect(providerFromBookForeignId('gb:xyz')).toBe('googlebooks')
    expect(providerFromBookForeignId('hc:123')).toBe('hardcover')
    expect(providerFromBookForeignId('HC:123')).toBe('hardcover')
    expect(providerFromBookForeignId('dnb:123')).toBe('dnb')
    expect(providerFromBookForeignId('nb:a0000000000000000000000000000001')).toBe('nb')
    expect(providerFromBookForeignId('bnf:cb48780493c')).toBe('bnf')
    expect(providerFromBookForeignId('calibre:7')).toBe('calibre')
    expect(providerFromBookForeignId('abs:lib:item')).toBe('audiobookshelf')
    expect(providerFromBookForeignId('manual:book:1700000000000000000')).toBe('manual')
  })

  it('treats an unprefixed id as OpenLibrary', () => {
    expect(providerFromBookForeignId('OL27448W')).toBe('openlibrary')
    expect(providerFromBookForeignId('something-random')).toBe('openlibrary')
    expect(providerFromBookForeignId('')).toBe('openlibrary')
  })
})

describe('hardcoverSeriesUrl', () => {
  it('builds the public series page from the slug', () => {
    expect(hardcoverSeriesUrl('the-stormlight-archive')).toBe(
      'https://hardcover.app/series/the-stormlight-archive',
    )
  })

  it('escapes anything that is not URL safe', () => {
    expect(hardcoverSeriesUrl('a b/c')).toBe('https://hardcover.app/series/a%20b%2Fc')
  })

  // hardcover.app does not route series on the numeric provider id, so a row
  // without a slug gets no link at all rather than one that 404s (#1708).
  it('returns null without a slug', () => {
    expect(hardcoverSeriesUrl('')).toBeNull()
    expect(hardcoverSeriesUrl('   ')).toBeNull()
    expect(hardcoverSeriesUrl(undefined)).toBeNull()
    expect(hardcoverSeriesUrl(null)).toBeNull()
  })
})
