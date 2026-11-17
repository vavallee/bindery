# BookOrbit Import

Bindery can learn what you already own from a [BookOrbit](https://github.com/bookorbit/bookorbit) server. It reads the books, authors, series and file paths BookOrbit holds, matches or creates them in Bindery, and registers each file as owned where it already sits.

It is a one way read. Bindery never writes to BookOrbit, and the import never moves, renames or deletes a file.

To run Bindery and BookOrbit side by side for new books, see [BookOrbit integration](./BookOrbit-Integration-Wiki.md). This page covers importing a library BookOrbit already holds.

Tested against BookOrbit 3.8.0. See [When BookOrbit changes](#when-bookorbit-changes) for what happens on other versions.

## Setup

### 1. Create a read only API key in BookOrbit

BookOrbit has static API keys from the version that added **Settings > You > API keys** (your account needs the **Create API keys** permission). Older versions only have login sessions and are not supported: Bindery does not store a BookOrbit password.

Create a key with:

- **Access: Read only.** The import only reads.
- **No extra permissions.**
- **Libraries: only the ones you want imported.** A key limited to those libraries cannot see the rest, whatever Bindery is told.

The key starts with `bok_` and is shown once.

### 2. Connect Bindery

Open **Settings > BookOrbit** (admins only) and fill in:

| Field | What to enter |
|---|---|
| Server URL | Where Bindery reaches BookOrbit, for example `http://bookorbit:3000`. A LAN or loopback address is fine. |
| API key | The `bok_` key. Bindery stores it, never returns it, and shows only whether one is saved. Leave the field blank later to keep the saved key. |
| BookOrbit path remap | See below. |

Save, then use **Test connection**. It reports the user the key belongs to and how many libraries the key can see. The test always uses the saved URL, so save a changed URL first.

### 3. Pick libraries

**Load libraries** lists what the key can see, with each library's folders as BookOrbit sees them. Tick the libraries to import and save. Only ticked libraries are read.

### 4. Path remap

BookOrbit reports every file by the path inside its own container, for example:

```
/books/Fantasy/L. Frank Baum/Oz/01 - The Wonderful Wizard of Oz/The Wonderful Wizard of Oz.epub
```

Bindery can only register a file it can see under one of its own library or root folders. If Bindery mounts the same storage at `/media/books`, add the remap:

```
/books:/media/books
```

The syntax is the same comma separated `from:to` list the Audiobookshelf import uses. The folders shown under each library in step 3 are the prefixes to translate.

If both containers mount the storage at the same path, leave the remap empty.

## Running an import

**Preview changes** is a dry run. It reads BookOrbit and reports what an import would do, and changes nothing in Bindery. Start there, and check the **Files not found** count: if it is not zero, the remap or the mounts are off.

**Import libraries** applies it. Each book is:

1. Matched to an existing Bindery author and book by identifier, then by normalised title and author, the same way the Audiobookshelf import matches. A book Bindery already has is linked, not duplicated.
2. Created if nothing matches, under its first author, with its series and position.
3. Given its files: every ebook and audiobook file Bindery can see at the remapped path is registered as owned, in place.

Running the import again is safe. Books it has already imported are recognised by their BookOrbit id and nothing is created twice. Run it again whenever BookOrbit has new books.

An Audiobookshelf import and a BookOrbit import cannot run at the same time. The second one is refused until the first finishes.

### What the counts mean

| Count | Meaning |
|---|---|
| Books listed by BookOrbit | Books read from the selected libraries |
| Books created | New Bindery books |
| Books matched | Books linked to a Bindery book that already existed |
| Books already imported | Books an earlier run brought in, refreshed |
| Authors created, Series created | New Bindery authors and series |
| Files registered | Ebook and audiobook files (or audiobook folders) recorded as owned. In a preview this is the number that would be. |
| Files not found | Files BookOrbit lists that Bindery cannot see at the remapped path. They are reported and never registered. The book still imports. |
| Books failed | Books that could not be imported, each with a reason in the run's results |

**Run history** at the bottom of the page lists past previews and imports, newest first, one row per library.

## What is imported

- Title, subtitle, description, language, publisher, published date, ISBN, and the Audible ASIN when BookOrbit has one
- The first author BookOrbit lists, as the Bindery author
- Series and position, including decimal positions such as `4.5`
- Narrators and duration for audiobooks
- Ebook files: every ebook format on the book (EPUB, AZW3, MOBI, PDF and so on)
- Audiobook files: a single file is registered as that file, a book of several tracks as their common folder

A book with both an ebook and an audiobook in BookOrbit becomes one Bindery book with both formats.

## What is not imported

- Reading progress, ratings, shelves, collections and annotations
- Covers and other non book files
- Books with no ebook or audiobook file
- Anything in a library that is not ticked, or that the key cannot see

Nothing is ever sent back to BookOrbit.

## Known limits

- **One author per book.** Bindery files a book under a single author, so a co written book lands under the first author BookOrbit lists.
- **No review queue.** The Audiobookshelf import can park an ambiguous match for a decision. The BookOrbit import does not yet: a book whose match is ambiguous is reported as failed with the reason, and nothing is guessed.
- **No rollback button.** Runs are recorded, but the run history has no undo. Preview first.
- **Unmatched books show Audiobookshelf as their provider.** A book that matches nothing upstream gets a placeholder id from the engine the two importers share, and Bindery currently labels that namespace Audiobookshelf. The book and its files are correct.
- **Multi track audiobooks are registered as a folder,** not as individual tracks. When a book's audio files sit directly in a library folder, or in folders with nothing but a library folder in common, no folder is registered for it and the log says so.
- **No scheduled import.** Run it by hand when BookOrbit has new books.
- **No resume.** A run cut short by a restart is not picked up again and stays listed as running in the history. Start it again: runs are idempotent.
- **Hardcover enhanced series matching** from the Audiobookshelf import is not used here. Series come from BookOrbit as written.

## When BookOrbit changes

BookOrbit does not promise a stable API, so the importer is written to fail loudly instead of importing something half understood.

An import first reads every selected library in full and checks each record for the fields it relies on. Only when all of it has been read and understood does it start writing to Bindery. So if a BookOrbit update renames or removes a field the importer needs:

- the run stops before anything is written, with nothing partly imported,
- the error names the request and the field, for example `BookOrbit answered GET /api/v1/books/42 in a shape this importer does not understand ("files" is missing); its API may have changed, so nothing was imported`,
- the failed run is in the run history.

Fields the importer does not use can come and go without affecting it. If you hit a shape error, open an issue with the BookOrbit version and the error text.

Other failures are reported the same way, before any write: a rejected or expired key, a key without access to a selected library, a library that no longer exists, or a server that cannot be reached. A listing that never ends, or keeps repeating books it already gave, stops the run the same way.

## Troubleshooting

| Symptom | Cause |
|---|---|
| Every file is "not found" or "outside Bindery storage" | The remap is missing or wrong, or the remapped path is not under a Bindery library or root folder |
| Test connection says the key was rejected | The key was revoked, has expired, or its owner lost the Create API keys permission |
| A library is missing from the list | The key is limited to other libraries |
| "another library import is running" | An Audiobookshelf or BookOrbit import is in progress |
| The address is refused when saving | Bindery blocks cloud metadata and link local addresses. LAN and loopback addresses are allowed. |

## For contributors

The client in `internal/bookorbit` was written from BookOrbit's public documentation and its observed HTTP responses. BookOrbit is AGPL-3.0 and Bindery is MIT, so none of its code, types, schemas or fixtures are in this repository. It calls four read routes with the `X-Api-Key` header: `GET /api/v1/auth/me`, `GET /api/v1/libraries`, `POST /api/v1/libraries/{id}/books` (a listing, paged) and `GET /api/v1/books/{id}`.

The import itself is the Audiobookshelf import engine (`internal/abs`) fed from a different source, so matching, dedupe, series handling and file registration are one code path. See [`abs_import.md`](./abs_import.md).
