# Manual metadata editing and field locks

Since v1.25.0 you can edit a book's metadata by hand and have your edits
survive every automatic refresh (#1237, #1446).

## Editing a book

Open a book's detail page and click **Edit**, or send the same fields
through the API, to change:

| Field | Notes |
|-------|-------|
| Title | Also used for sorting and file naming on the next rename |
| Description | |
| Genres | Comma-separated; the first genre drives the `{Genre}` naming token |
| Language | Language code, e.g. `en`, `it` — drives the `{Lang}` naming token |
| Release date | `YYYY-MM-DD`, or clear it entirely |
| Series | The author's series, with a position; admin only. See [Fixing one book's series](Hardcover-Series-Wiki.md#fixing-one-books-series) |
| Narrator | API only; not in the edit dialog |
| Duration | API only (`durationSeconds`, whole seconds; `0` means not set and is ignored) |
| ASIN | The ASIN box on the book page, or the API; normalized to upper case, ten letters or digits |

Every field you edit is **locked**: the scheduled metadata refresh, the
author-works sync, Audnex enrichment, Hardcover hydration and
Audiobookshelf/Calibre re-imports keep your value instead of overwriting
it. A lock also stops those paths *filling* the field when you cleared it
on purpose. Locked fields show a 🔒 badge in the edit dialog for title, description,
genres, language and release date; narrator, duration and ASIN are locked
through the API and the ASIN box and are not shown there.

A field you have not edited is not protected: refreshing an author updates
the release date of its existing books when the provider's record differs, and
fills a description the book lacks. Language is not updated. See
[Refresh metadata](User-Guide-Wiki.md).

## Unlocking

**Unlock all fields** in the edit dialog hands every field back to the
metadata pipeline, including narrator, duration and ASIN. The current values
stay until the next refresh replaces them. To unlock one field, send
`lockedFields` without it through the API.

One action clears locks on its own, deliberately: **Re-bind**, which means
"this is the wrong record, take the new one". It clears the locks on the
work fields it replaces (title, description, genres, language, release date)
and keeps the narrator, duration and ASIN locks, because it keeps those
values. The ASIN re-map that runs inside **Enrich** used to clear every lock
too and no longer does; it is automatic, not a choice of record.

## Known gaps

A few writers predate the locks and do not check them yet: the upstream
metadata merge inside the Audiobookshelf import, which can write over a locked
description, release date or language; a value applied from the
Audiobookshelf conflicts page; the language a library scan detects from a
file's tags; the language an author sync fills into an empty field; and the
Calibre import rollback. Rolling back an Audiobookshelf import undoes what its
merge wrote, locked or not, as long as the field still holds the value the run
wrote.

A re-bind also empties the fields the new record does not have, so a book
moved to a record without a release date does not keep the old one. That covers
the release date, language, description and genres. Bindery tracks the values
you edited only through locks, so a value it cannot tell you typed (one an
Audiobookshelf or Calibre import wrote, or one you edited without locking it)
is emptied the same way. A field you locked is not emptied, although the
re-bind still drops the lock itself, as it always did. When the provider could
not answer for a field (for example OpenLibrary's search lookup failed, or
Hardcover's single book fetch, which does not carry genres or language), the
field is left alone. The rating, original title, cover, narrator, duration,
ASIN, format and files are never emptied. When the new record does have a
value, it replaces yours as before. The re-bind result lists what was emptied
in `cleared`, and the book's history entry records the same list.

A re-bind to a different record also drops the editions the metadata provider
listed for the old record, so their ISBNs no longer steer searches for the
book. Editions created by the Calibre or Audiobookshelf import for your files
are kept, and so is the edition you selected for the book and any edition a
download or a Calibre delivery recorded. Re-binding to the record the book is
already bound to changes no editions.

## What you cannot edit: an author's sort name

The value that decides which letter an author files under, in the Authors list
and in the `{SortAuthor}` naming token, is the author's sort name, and there is
no field for it. Bindery derives it from the display name as "Last, First", and
a metadata refresh replaces it with the provider's own sort name when the
provider has one. Nothing in the UI or the API accepts a hand written sort
name, so unlike a book's title this cannot be locked to your own value.

[How author names are filed](User-Guide-Wiki.md#how-author-names-are-filed)
sets out the rules, including the case rule that files "Vincent van Gogh" under
G and "Thomas De Quincey" under D, and the limits of the heuristic.

## Genre overrides in bulk (#1446)

If you keep an opinionated genre taxonomy (say, `{Genre:Unsorted}/…` folder
paths), per-book editing does not scale. Two bulk actions set the same genre
list on many books at once and lock it on each:

- **Author level** — Edit Author → *Genre override*: applies to every book
  by that author.
- **Series level** — Series page → *Set genre* on an expanded series:
  applies to every book in the series.

Both are idempotent; re-applying with a different list simply replaces it.

## API

`PUT /api/v1/book/{id}` accepts `title`, `description`, `genres`,
`language`, `releaseDate`, `narrator`, `durationSeconds` and `asin`; setting
one applies the value and locks the field. Send `lockedFields` (an array of
those names) to replace the lock set explicitly; an empty array unlocks
everything. Bulk endpoints:
`PUT /api/v1/author/{id}/genres` and `PUT /api/v1/series/{id}/genres` with
`{"genres": ["Fantasy"]}`. `DELETE /api/v1/series/{id}/genres` removes a series
override.
