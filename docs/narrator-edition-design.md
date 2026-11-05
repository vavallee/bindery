# Narrator and edition identity design

Status: phase 1 is proposed together with this document. Phases 2 and 3 are
direction, not commitments.

Tracked in [#1358](https://github.com/vavallee/bindery/issues/1358) and, for the
audiobook scoring side, [#2740](https://github.com/vavallee/bindery/issues/2740).
The gap review that motivated this is `docs/audits/2026-11-feature-gap-review.md`.

## Problem

An audiobook is a performance of a book, and the same book often exists as
several recordings: unabridged and abridged, a single narrator and a full cast,
a dramatised adaptation. Readers care which one they get. Bindery cannot tell
them apart today.

What exists, verified against `main`:

- `Book.Narrator` is one free text string (`internal/models/book.go`). ABS import,
  Audnex, Audible and Hardcover write it by joining names with a comma
  (`joinNarrators` in `internal/abs/import_utils.go`, `NarratorList` in
  `internal/metadata/audnex/client.go`). Nothing splits it back.
- `models.Edition` has no narrator and no edition type. Its `Language`, `Format`,
  `ASIN` and `EditionInfo` fields exist and are filled by providers.
- Abridged and unabridged exist only as two booleans on `ParsedRelease`
  (`internal/indexer/release.go`). They come from a substring test over the whole
  release title, so a book named "The Abridged History of Rome" parses as abridged.
  Ranking gives unabridged +30 and abridged -50 (`scoreResult`), for every install,
  with no way to change it.
- A quality profile ranks formats only. It has no opinion on narrator or edition.

## Decision: bounded phase 1, no Work/Edition rewrite first

The full model is a Work with many Editions, each carrying contributors, and a
book in the library being a chosen edition of a work (#1358 asks for this, and for
grouping translations). That touches matching, the wanted sweep, folder naming and
search expansion. It is a large, risky change and nothing in narrator selection
needs it.

So phase 1 hangs the new data off the existing `editions` row, which already
exists, already belongs to one book, and is already written by ABS import and the
metadata providers. A book keeps being the unit that is wanted, searched and
imported. Nothing in phase 1 merges, splits or re-parents books.

## Data model

### Edition row (phase 1)

Two new columns on `editions`:

| Column | Type | Meaning |
|---|---|---|
| `narrators` | TEXT, JSON array of strings, default `[]` | Ordered narrator names for this recording. A full cast keeps its named narrators here; it is not a single "Full Cast" entry. |
| `edition_type` | TEXT, default `''` | One of `unabridged`, `abridged`, `dramatized`, `full_cast`, or empty for unknown. |

Why on the edition and not the book: the narrator and the abridgement belong to a
recording, and one book can have several. `Book.Narrator` stays, because it is the
single value the UI, the renamer (`{Narrator}`), the OPF sidecar and ABS rollback
already use. It remains the display and compatibility value.

Why a JSON array column and not a `contributors` table: phase 1 only ever reads
the whole list to compare names. A table is justified when contributors become
entities with their own identity and roles (phase 3). A JSON text column is the
same choice `quality_profiles.items` already made.

`edition_type` is a closed set, validated in Go, stored as text. Unknown is the
empty string, never a guess. `full_cast` and `dramatized` are separate because
providers and release names use them differently: a full cast reads the book
unabridged with several voices, a dramatisation adapts it.

### Backward compatibility

- The single string `Book.Narrator` is untouched and keeps working everywhere.
- The effective narrator list of an edition is its stored `narrators` when
  non empty, otherwise `Book.Narrator` split on `,`, `;`, ` and ` and `&`
  (`models.SplitNarrators`). Rows created before the migration therefore have a
  list without any data rewrite, and an install that never touches the feature
  sees no change.
- Writers fill the list when they know it (ABS import has the names as an array
  already). A narrator the user locked on the book (#3216) is not overwritten.
- Splitting is only used as a fallback for display and matching. It is never
  written back over a stored list.

### Where quality preferences live

Two new columns on `quality_profiles`, so the preference is per profile, the same
scope as the format order and the same tenancy rules (`owner_user_id`):

| Column | Meaning |
|---|---|
| `preferred_narrators` | JSON array of names, default `[]`, ordered best first |
| `edition_preference` | `''` (off, the default), `prefer_unabridged`, `require_unabridged` |

Off means the current ranking exactly. With nothing set, `scoreResult` takes the
same path it takes today, including its existing abridged and unabridged weights.

## Parsing

Parsing is conservative. A release title is free text from an indexer, so the
rule is: recognise a small number of unambiguous shapes, and return unknown for
everything else.

Edition type from a release title, only when the marker is delimited, meaning
inside brackets or parentheses, or a whole word after the book title and author
have been removed from the string. The wanted book's own title is stripped first,
so "The Abridged History of Rome" is not abridged. A title carrying both
"unabridged" and "abridged" markers is unknown. "Full cast" and "dramatized" are
recognised the same way.

Narrator from a release title, only from the explicit shapes "read by NAME",
"narrated by NAME", "performed by NAME" and "NAME (narrator)". Each name must
look like a person (two to five words, no digits). An author name appearing in a
title is never read as a narrator on its own, and "by NAME" alone is the author
credit. An explicit "read by" the author does count, because author narrated
books are common and the credit is stated. Two credits that disagree give
unknown. Multiple names split on `,`, `&` and `and`.

Narrator and edition type from metadata sources, in order of trust:

1. ABS import: `Narrators` is already an array. The edition type comes from the
   same delimited marker parse over the ABS title and subtitle.
2. Audnex and Audible: narrators are already arrays; they will be carried to the
   edition in phase 2.
3. Release titles are never written to the edition on their own. They are only
   used to rank and to display.

## Ranking

Phase 1 adds terms to `scoreResult` in `internal/indexer/searcher.go`, gated by
the profile fields above:

- `edition_preference = prefer_unabridged`: a release whose parsed type is
  unabridged gets a bonus; abridged gets a penalty larger than today's.
- `edition_preference = require_unabridged`: an abridged release is rejected,
  like a format the profile does not allow. An unknown type is never rejected,
  because unknown is not evidence of abridgement.
- `preferred_narrators`: a release whose parsed narrator matches an entry gets a
  bonus that decreases down the list. A release with no parsed narrator gets
  nothing and loses nothing.

A narrator match is exact after case and diacritic folding of whole names. No
fuzzy matching.

The interactive search result shows the parsed narrator and edition type when
known, and the book page shows the effective narrators and edition type of its
audiobook edition.

## Tenancy

No new endpoint is required for the data itself. The preference fields ride on
the existing quality profile endpoints, which already scope by owner. Edition
fields are exposed through the book, which is already scoped by `owner_user_id`.

## Migration

One migration adds the four columns with defaults, so it needs no backfill and a
rollback is a no-op for old binaries (SQLite ignores extra columns). Existing rows
read as empty list and unknown type.

## Phases

1. Phase 1 (with this design): the columns, conservative parsing, the two profile
   preferences, ranking terms, and display on the release and the book.
2. Phase 2: carry narrators and type from Audnex, Audible and Hardcover onto
   editions; let the user edit them per edition; a narrator filter in the library
   view; use the grabbed release's type to mark the imported edition.
3. Phase 3: contributors as entities (narrator, translator, illustrator) with
   roles; grouping translations and other editions under one work (#1358). This is
   the part that needs the matching and series work in #2524.

## What stays out

- Merging or grouping books into works, and translation grouping. That is #1358
  proper, and phase 3.
- Per codec size targets and weights (#2740 remainder).
- Fuzzy narrator matching, narrator aliases, or narrator identity across providers.
- Writing narrator or edition type into audio file tags.
- Changing the renamer, which keeps using `Book.Narrator`.
- Any change to ranking for a profile that sets no preference.
