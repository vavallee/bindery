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
| Release date | `YYYY-MM-DD` (the API also takes the RFC 3339 timestamp it returns), or clear it entirely |
| Series | The author's series, with a position; admin only. See [Fixing one book's series](Hardcover-Series-Wiki.md#fixing-one-books-series) |
| Narrator | API only; not in the edit dialog; at most 1000 bytes |
| Duration | API only (`durationSeconds`, whole seconds, at most 3600000 (1000 hours); `0` means not set and is ignored, so a duration cannot be cleared this way) |
| ASIN | The ASIN box on the book page, or the API; normalized to upper case, ten letters or digits |

Every field you change is **locked**: the scheduled metadata refresh, the
author-works sync, Audnex enrichment and Hardcover hydration keep your value
instead of overwriting it. For narrator, duration and ASIN, Audiobookshelf
re-imports and Hardcover list syncs keep it too. The Calibre import never
writes those three fields, so there is nothing for it to keep. A lock also
stops those paths *filling* the field when you cleared it on purpose, so
clearing an ASIN blocks ASIN fills until you unlock it.

Only a change locks. Sending a field with the value it already has (a
client that reads a book and writes it back, or `{"asin": ""}` on a book
with no ASIN) changes nothing and locks nothing. Locked fields show a 🔒 badge in the edit dialog for title, description,
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
are kept, and so is the edition you selected for the book, any edition with a
locked field, and any edition a download or a Calibre delivery recorded.
Re-binding to the record the book is already bound to changes no editions.

## Edition locks

An edition's publisher, publish date, ISBN-13, ISBN-10 and ASIN can be set
and locked through `PUT /api/v1/book/{id}/edition/{editionId}`. As on a book,
only a change locks: sending a value the edition already has, such as
`{"publisher": ""}` on an edition with no publisher, or writing back an
edition exactly as GET returned it, locks nothing. `publishDate` takes
`YYYY-MM-DD` or the RFC 3339 timestamp GET returns, and `publisher` is capped
at 500 bytes (a 400 above that). Both the import upsert (Audiobookshelf,
Calibre) and Hardcover hydration keep a locked column's value, whether they
would overwrite it or fill it. An edition's narrators and edition type are not
editable here, so they have no lock. There is no
edition editor in the UI; edition locks are API-only. `GET /api/v1/book/{id}`
returns each edition's `lockedFields` when the book has editions. The PUT
answers the stored edition, **404** when the edition is not this book's, and
**409** with `reason: "changed"` when the edition changed since the request
read it; read it again and resend.

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
`language`, `releaseDate`, `narrator`, `durationSeconds` and `asin`; a value
that differs from the stored one is applied and locks the field, and an
unchanged value is ignored, so the body of a GET can be sent straight back.
Send `lockedFields` (an array of those names) to replace the lock set
explicitly, which is also how to lock a field without changing it; an empty
array unlocks everything. A narrator over 1000 bytes or a `durationSeconds`
over 3600000 is a 400. `PUT /api/v1/book/{id}/edition/{editionId}` does the same for an
edition with `publisher`, `publishDate` (`YYYY-MM-DD`, RFC 3339, or empty to
clear), `isbn13`, `isbn10` and `asin`, and its own `lockedFields`. Bulk endpoints:
`PUT /api/v1/author/{id}/genres` and `PUT /api/v1/series/{id}/genres` with
`{"genres": ["Fantasy"]}`. `DELETE /api/v1/series/{id}/genres` removes a series
override.
