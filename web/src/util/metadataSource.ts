// Build a link to the upstream metadata source for an author or book, à la
// the *arr stacks' TMDB/IMDB links (#1296). The provider is implied by the
// foreign-ID prefix, matching the backend convention in
// internal/metadata/aggregator_providers.go and models.AuthorProviderFromForeignID.
//
// We only emit a link when the public URL can be constructed reliably from the
// stored ID. OpenLibrary (bare OL keys) and Google Books (gb:) qualify;
// Hardcover (hc:), DNB (dnb:), Calibre and Audiobookshelf do not — their stored
// IDs don't map to a stable public page — so those return null rather than risk
// a dead link.

export type MetadataSourceLink = { url: string; label: string }

export function metadataSourceLink(
  foreignId: string | undefined | null,
  kind: 'author' | 'book',
): MetadataSourceLink | null {
  const id = (foreignId ?? '').trim()
  if (!id) return null

  if (id.startsWith('gb:')) {
    const vol = id.slice(3).trim()
    // Google Books has no canonical author page; only books map cleanly.
    if (kind !== 'book' || !vol) return null
    return { url: `https://books.google.com/books?id=${encodeURIComponent(vol)}`, label: 'Google Books' }
  }

  // No reliable public URL for these providers.
  if (id.startsWith('hc:') || id.startsWith('dnb:') || id.startsWith('abs:') || id.startsWith('calibre:')) {
    return null
  }

  // Default: OpenLibrary, whose foreign IDs are bare OL keys. Author keys end
  // in A, work keys in W, edition keys in M.
  if (kind === 'author') {
    if (!/^OL\w+A$/i.test(id)) return null
    return { url: `https://openlibrary.org/authors/${id}`, label: 'OpenLibrary' }
  }
  if (/^OL\w+W$/i.test(id)) return { url: `https://openlibrary.org/works/${id}`, label: 'OpenLibrary' }
  if (/^OL\w+M$/i.test(id)) return { url: `https://openlibrary.org/books/${id}`, label: 'OpenLibrary' }
  return null
}
