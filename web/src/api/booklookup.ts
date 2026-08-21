import { api } from './client'
import type { Book } from './client'

// An ISBN-13 (978/979 + 10 digits) or ISBN-10 (9 digits + check digit/X),
// after hyphens and spaces are stripped.
const ISBN_RE = /^97[89]\d{10}$|^\d{9}[\dX]$/
// An Audible/Amazon ASIN: 10 characters starting with B. Checked after ISBN;
// the two patterns do not overlap.
const ASIN_RE = /^B[0-9A-Z]{9}$/i

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
  const compact = q.replace(/[-\s]/g, '')
  if (ISBN_RE.test(compact)) return [await api.lookupISBN(compact)]
  if (ASIN_RE.test(q)) return [await api.lookupASIN(q.toUpperCase())]
  // A backend that encodes an empty search as `null` rather than `[]` must not
  // reach the callers' `.map()`.
  return (await api.searchBooks(q)) ?? []
}
