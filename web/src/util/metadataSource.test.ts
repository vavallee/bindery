import { describe, it, expect } from 'vitest'
import { metadataSourceLink } from './metadataSource'

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

  it('returns null for providers without a reliable public URL', () => {
    expect(metadataSourceLink('hc:12345', 'book')).toBeNull()
    expect(metadataSourceLink('dnb:123456789', 'book')).toBeNull()
    expect(metadataSourceLink('abs:abc', 'book')).toBeNull()
    expect(metadataSourceLink('calibre:7', 'book')).toBeNull()
  })

  it('returns null for empty / malformed ids', () => {
    expect(metadataSourceLink('', 'author')).toBeNull()
    expect(metadataSourceLink(undefined, 'book')).toBeNull()
    expect(metadataSourceLink('gb:', 'book')).toBeNull()
    expect(metadataSourceLink('OL123', 'author')).toBeNull() // no trailing A
  })
})
