import { describe, it, expect } from 'vitest'
import type { Author } from '../api/client'
import { authorProviderKey, canLinkAuthorMetadata } from './authorMetadata'

const author = (over: Partial<Author>): Author => ({ id: 1, name: 'X', ...over }) as Author

describe('authorProviderKey', () => {
  it('routes national library ids by their prefix (#3144)', () => {
    expect(authorProviderKey(author({ foreignAuthorId: 'bnf:author:11885918' }))).toBe('bnf')
    expect(authorProviderKey(author({ foreignAuthorId: 'nb:author:90053882' }))).toBe('nb')
    expect(authorProviderKey(author({ foreignAuthorId: 'dnb:118540238' }))).toBe('dnb')
  })
})

describe('canLinkAuthorMetadata', () => {
  it('is true when unlinked, import-derived or made by hand', () => {
    expect(canLinkAuthorMetadata(author({ foreignAuthorId: '' }))).toBe(true)
    expect(canLinkAuthorMetadata(author({ foreignAuthorId: 'abs:123' }))).toBe(true)
    expect(canLinkAuthorMetadata(author({ foreignAuthorId: 'calibre:7' }))).toBe(true)
    expect(canLinkAuthorMetadata(author({ foreignAuthorId: 'OL1A', metadataProvider: 'audiobookshelf' }))).toBe(true)
    expect(canLinkAuthorMetadata(author({ foreignAuthorId: 'OL1A', metadataProvider: 'calibre' }))).toBe(true)
    expect(canLinkAuthorMetadata(author({ foreignAuthorId: 'manual:author:dan-abnett' }))).toBe(true)
    expect(canLinkAuthorMetadata(author({ foreignAuthorId: 'OL1A', metadataProvider: 'manual' }))).toBe(true)
  })
  it('is false for a real provider link', () => {
    expect(canLinkAuthorMetadata(author({ foreignAuthorId: 'OL1A', metadataProvider: 'openlibrary' }))).toBe(false)
  })
  it('defaults to true for a nil author', () => {
    expect(canLinkAuthorMetadata(undefined)).toBe(true)
  })
})
