import type { Author } from '../api/client'

// canLinkAuthorMetadata reports whether an author has no real upstream metadata
// link yet — unlinked, or created from an Audiobookshelf / Calibre import (those
// use `abs:` / `calibre:` foreign IDs). The UI offers a "Link metadata" action
// in that case so a provider record can be attached. A nil author defaults to
// true (the add-author conflict path may not have a canonical author yet).
export function canLinkAuthorMetadata(author?: Author): boolean {
  if (!author) return true
  const foreignId = (author.foreignAuthorId || '').trim()
  const provider = (author.metadataProvider || '').trim().toLowerCase()
  return foreignId === '' || foreignId.startsWith('abs:') || foreignId.startsWith('calibre:') || provider === 'audiobookshelf' || provider === 'calibre'
}

// hasSparseMetadata reports whether a linked author's record is thin enough to be
// worth relinking to a richer source: no description, image, disambiguation, or
// ratings. Drives the "Find better metadata" action. A nil author defaults to
// true for the same reason as canLinkAuthorMetadata.
export function hasSparseMetadata(author?: Author): boolean {
  if (!author) return true
  return !author.description && !author.imageUrl && !author.disambiguation && (author.ratingsCount ?? 0) === 0 && (author.averageRating ?? 0) === 0
}
