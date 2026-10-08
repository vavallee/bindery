# Migrating from Readarr

## Importing a readarr.db

In **Settings → Import / Migrate**, upload your `readarr.db`. The import brings in:

- **Authors**, with their monitored state
- **Indexers**
- **Download clients**
- **Blocklist**

Each imported author's catalogue is populated from metadata. Nothing is auto-grabbed — grab from the **Wanted** page when you are ready.

The import dedupes by name and by metadata id, so re-running it is safe: authors that already exist are skipped. An author already in your library under the same name counts as existing whichever provider it is linked to, so running the import again after a provider outage does not add a second copy of someone who was linked to a fallback provider the first time. Only the same name counts, however it is punctuated ("J.R.R. Tolkien" and "JRR Tolkien"), not a similar one. Skipping never changes the existing author. To combine two copies you already have, open the one to keep and use **Merge…**.

Each author is matched against your primary metadata provider and its fallbacks, and linked to the provider whose record matched. If your primary metadata provider does not answer during the import, an author it did not match is listed as failed rather than linked to another provider, because the link decides for good which provider the author's catalogue syncs from. The reason says the primary did not answer. Nothing is wrong with the name, so run the import again once the provider responds. Once the primary has failed to answer three lookups in a row, the import stops asking it and lists the remaining authors as failed with the same reason straight away, so an outage costs a few timeouts rather than one per author. A name that no provider matched while they were all answering is listed with the providers that were asked. A pasted or uploaded author list (**Settings → Import / Migrate**) works the same way.

An indexer or download client whose address Bindery will not call (link-local and cloud-metadata addresses) is reported as failed rather than imported, with the same message you would get typing it into the Add form.

## Two Readarr instances (separate ebook / audiobook)

Bindery is a single instance. One author record covers ebook, audiobook, or both, set per author or per book.

- **Run the import once per `readarr.db`.** The second run skips authors already imported and adds any new ones.
- **If your two instances are kept in sync** via Import Lists and hold the same authors, importing one database is enough — the other would be all-skipped.
- **Media type is not carried over.** The import does not know which database is "audiobooks"; every author arrives as a standard record. After importing, set ebook / audiobook / both per author or book where you want audiobooks.

## Bringing in books already on disk

For files already on disk, use **Library Scan**. It takes the author from your folder layout: a file under `{Author}/{Book}/` — Readarr's and Calibre's default structure — is matched on the author folder, so the filename convention (`Author - Title` vs `Title - Author`) does not matter for the author. An ebook's title is read from the file's own name first and from the book folder only when the filename matches nothing, so a `Series/01 - Title.epub` layout matches the book the file names. An audiobook's title comes from its folder, where the files are its tracks. Loose files with no author/book folders fall back to filename parsing, which can still be ambiguous, so keep an organised folder structure for the most reliable scan. **Bulk folder import** reads the same author folders when you point it at the folder that holds them.

One difference to know about before you start correcting matches: Readarr's fix-match only changes which record a file is linked to, while Bindery's **Fix match** re-runs the import, so it moves the file into the target book's folder and renames it from your naming template. The confirmation step names the destination path before anything happens, so you can back out if you would rather keep your existing layout. Reassigning without relocating is not available yet (#2055).

## Importing your Goodreads library (CSV)

If you tracked your reading on Goodreads, you can seed Bindery's wanted list from a Goodreads library export. This is a one-shot migration aid — it is **not** a live sync. Bindery does not poll Goodreads; if you add books on Goodreads later, re-export and re-import.

### 1. Export the CSV from Goodreads

1. Go to [goodreads.com/review/import](https://www.goodreads.com/review/import).
2. Click **Export Library**. Goodreads generates a CSV after a short delay — refresh the page and download the link when it appears.
3. The file is named like `goodreads_library_export.csv`.

### 2. Upload it in Bindery

1. Open **Settings → Import / Migrate**.
2. Under **Goodreads library CSV**, first pick which shelves to import using **Shelves to import** (see below), then click **Upload Goodreads CSV** and choose your exported `.csv`.

The importer reads columns by name, so it tolerates the few header spellings Goodreads has shipped over the years and any column reordering. It only needs a **Title** column; ISBN/ISBN13 columns are used when present but are not required — books with no ISBN fall through to a title+author search.

### 3. Shelf filter

Every Goodreads row carries exactly one **Exclusive Shelf** value: `to-read`, `currently-reading`, or `read`. The shelf filter decides which of those Bindery imports.

- The filter defaults to **`to-read` only** — the books you have not read yet, which is what most people want to start monitoring.
- Tick `currently-reading` and/or `read` to widen the import. The filter can never be empty; clearing the last box falls back to `to-read`.
- Rows on a shelf you did not select are counted as `skippedShelf` in the preview and are never imported.

### 4. Preview, then commit

The import is a two-step, dry-run-first flow — nothing is written until you confirm:

1. **Preview (dry run).** After upload, Bindery parses the CSV and resolves every in-scope row against its metadata providers (by ISBN-13, then ISBN-10, then a title+author search). No data is written. The preview summarises:
   - **resolved** — matched and ready to add
   - **skippedExisting** — already in your library (deduped by metadata id, so re-running an import is safe)
   - **skippedShelf** — filtered out by the shelf filter
   - **unresolved** — no provider could match the row
2. **Commit.** Click **Add N books** to persist the resolved books. Each is added as a **monitored, wanted** book — nothing is auto-grabbed; grab from the **Wanted** page when you are ready. The resolved preview is held server-side for 30 minutes; commit within that window or re-upload.

### 5. Failed rows

Rows that could not be matched are listed in the preview under **unresolved**, with a reason naming the providers that were asked, for example `no match on openlibrary, hardcover for ISBN or title+author`. Use **Download failed rows** to get a Goodreads-shaped CSV of just those rows, with a `Reason` column. Fix an ISBN or title in that file and re-upload it to retry only the misses.

A reason that starts `primary metadata provider ... did not answer` is different: the row is fine, so do not edit it. Your primary metadata provider was not answering during the preview, so the row either found nothing or was matched only by another provider, and importing that match would link its author to the other provider for good. Upload the failed rows again, unchanged, once the provider responds. After three lookups in a row get no answer from the primary, the preview stops asking and gives every remaining row this reason without a lookup, so it finishes quickly instead of waiting out the provider's timeout row by row.

Resolution quality depends on ISBN coverage: rows with a valid ISBN match most reliably. Older or self-published titles often have no ISBN in the export and fall back to title+author search, which can miss — that is expected.
