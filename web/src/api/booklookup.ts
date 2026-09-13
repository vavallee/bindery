import { api } from './client'
import type { Book } from './client'

// An ISBN-13 (978/979 + 10 digits) or ISBN-10 (9 digits + check digit/X),
// after hyphens and spaces are stripped.
const ISBN_RE = /^97[89]\d{10}$|^\d{9}[\dX]$/
// An Audible/Amazon ASIN: 10 characters starting with B. Checked after ISBN;
// the two patterns do not overlap.
const ASIN_RE = /^B[0-9A-Z]{9}$/i

// Everything that can stand in for the hyphen in a written ISBN, plus
// whitespace. An ISBN copied out of a PDF, a publisher's page or a word
// processor carries an en dash, a non-breaking hyphen or a soft hyphen far more
// often than a plain "-", and stripping only the ASCII one left the query
// failing ISBN_RE and going to the title search, which finds nothing.
//
// U+2010–U+2015 are hyphen, non-breaking hyphen, figure dash, en dash, em dash
// and horizontal bar; U+2212 is the minus sign; U+00AD is the soft hyphen, which
// is invisible and so the hardest one to diagnose from a bug report. The same
// list is isbnutil.isSeparator on the server and importer's dashNormalizer for
// filenames — all three have to agree or an ISBN normalizes differently
// depending on where it entered.
const ISBN_SEPARATORS = /[-\u00ad\u2010-\u2015\u2212\s]/g

// normalizeISBNInput strips the separators out of a pasted ISBN so it can be
// shape-checked and sent to the lookup endpoint. Exported for the unit test.
export function normalizeISBNInput(raw: string): string {
  return raw.replace(ISBN_SEPARATORS, '')
}

export function isbnFromQuery(query: string): string | null {
  const compact = normalizeISBNInput(query.trim())
  return ISBN_RE.test(compact) ? compact : null
}

// resolveBookQuery turns one free-text query into provider metadata results,
// choosing the endpoint by the shape of the query: an ISBN or ASIN goes to
// /book/lookup (exact, one result), anything else to /search/book.
//
// Shared by the Add Book modal and Manual Import's metadata search so the two
// accept the same inputs. A user who can paste an ISBN into one should not find
// it treated as a title by the other.
export async function resolveBookQuery(query: string): Promise<Book[]> {
  const q = query.trim()
  if (!q) return []
  const isbn = isbnFromQuery(q)
  if (isbn) return [await api.lookupISBN(isbn)]
  if (ASIN_RE.test(q)) return [await api.lookupASIN(q.toUpperCase())]
  // A backend that encodes an empty search as `null` rather than `[]` must not
  // reach the callers' `.map()`.
  return (await api.searchBooks(q)) ?? []
}
