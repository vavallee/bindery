# Manual metadata editing and field locks

Since v1.25.0 you can edit a book's metadata by hand and have your edits
survive every automatic refresh (#1237, #1446).

## Editing a book

Open a book's detail page and click **Edit**. You can change:

| Field | Notes |
|-------|-------|
| Title | Also used for sorting and file naming on the next rename |
| Description | |
| Genres | Comma-separated; the first genre drives the `{Genre}` naming token |
| Language | Language code, e.g. `en`, `it` — drives the `{Lang}` naming token |
| Release date | `YYYY-MM-DD`, or clear it entirely |

Every field you edit is **locked**: the nightly metadata refresh, the
author-works sync, and Audiobookshelf/Calibre re-imports keep your value
instead of overwriting it. Locked fields show a 🔒 badge in the edit dialog.

## Unlocking

**Unlock all fields** in the edit dialog hands the fields back to the
metadata pipeline. The current values stay until the next refresh replaces
them.

Two actions clear locks on their own, deliberately: **Re-bind** and metadata
**re-map**. Both mean "this is the wrong record, take the new one", so
Bindery applies the new provider record wholesale.

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
`language`, and `releaseDate` — setting one applies the value and locks the
field. Send `lockedFields` (an array of those names) to replace the lock set
explicitly; an empty array unlocks everything. Bulk endpoints:
`PUT /api/v1/author/{id}/genres` and `PUT /api/v1/series/{id}/genres` with
`{"genres": ["Fantasy"]}`.
