import { describe, it, expect } from 'vitest'
import type { Author } from '../api/client'
import { canLinkAuthorMetadata, hasSparseMetadata } from './authorMetadata'

const author = (over: Partial<Author>): Author => ({ id: 1, name: 'X', ...over }) as Author

describe('canLinkAuthorMetadata', () => {
  it('is true when unlinked or import-derived', () => {
    expect(canLinkAuthorMetadata(author({ foreignAuthorId: '' }))).toBe(true)
    expect(canLinkAuthorMetadata(author({ foreignAuthorId: 'abs:123' }))).toBe(true)
    expect(canLinkAuthorMetadata(author({ foreignAuthorId: 'calibre:7' }))).toBe(true)
    expect(canLinkAuthorMetadata(author({ foreignAuthorId: 'OL1A', metadataProvider: 'audiobookshelf' }))).toBe(true)
    expect(canLinkAuthorMetadata(author({ foreignAuthorId: 'OL1A', metadataProvider: 'calibre' }))).toBe(true)
  })
  it('is false for a real provider link', () => {
    expect(canLinkAuthorMetadata(author({ foreignAuthorId: 'OL1A', metadataProvider: 'openlibrary' }))).toBe(false)
  })
  it('defaults to true for a nil author', () => {
    expect(canLinkAuthorMetadata(undefined)).toBe(true)
  })
})

describe('hasSparseMetadata', () => {
  it('is true only when description, image, disambiguation, and ratings are all empty', () => {
    expect(hasSparseMetadata(author({}))).toBe(true)
    expect(hasSparseMetadata(author({ description: 'A bio' }))).toBe(false)
    expect(hasSparseMetadata(author({ imageUrl: 'http://x/y.jpg' }))).toBe(false)
    expect(hasSparseMetadata(author({ disambiguation: 'the elder' }))).toBe(false)
    expect(hasSparseMetadata(author({ ratingsCount: 5 }))).toBe(false)
    expect(hasSparseMetadata(author({ averageRating: 4.2 }))).toBe(false)
  })
  it('defaults to true for a nil author', () => {
    expect(hasSparseMetadata(undefined)).toBe(true)
  })
})
