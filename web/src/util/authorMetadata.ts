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

// authorProviderKey names the catalogue provider an author record routes to,
// mirroring models.AuthorProviderFromForeignID on the backend: the foreign-ID
// prefix decides where every later catalogue fetch goes. Falls back to the
// stamped metadataProvider only when there is no foreign ID at all. Drives the
// add-flow provider notice (#2237).
export function authorProviderKey(author?: Pick<Author, 'foreignAuthorId' | 'metadataProvider'>): string {
  if (!author) return ''
  const id = (author.foreignAuthorId || '').trim().toLowerCase()
  if (id.startsWith('gb:')) return 'googlebooks'
  if (id.startsWith('hc:')) return 'hardcover'
  if (id.startsWith('dnb:')) return 'dnb'
  if (id.startsWith('calibre:')) return 'calibre'
  if (id.startsWith('abs:')) return 'audiobookshelf'
  if (id !== '') return 'openlibrary'
  return (author.metadataProvider || '').trim().toLowerCase()
}
