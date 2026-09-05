# Migrating from Readarr

## Importing a readarr.db

In **Settings → Import**, upload your `readarr.db`. The import brings in:

- **Authors**, with their monitored state
- **Indexers**
- **Download clients**
- **Blocklist**

Each imported author's catalogue is populated from metadata. Nothing is auto-grabbed — grab from the **Wanted** page when you are ready.

The import dedupes by metadata id, so re-running it is safe: authors that already exist are skipped.

An indexer or download client whose address Bindery will not call (link-local and cloud-metadata addresses) is reported as failed rather than imported, with the same message you would get typing it into the Add form.

## Two Readarr instances (separate ebook / audiobook)

Bindery is a single instance. One author record covers ebook, audiobook, or both, set per author or per book.

- **Run the import once per `readarr.db`.** The second run skips authors already imported and adds any new ones.
- **If your two instances are kept in sync** via Import Lists and hold the same authors, importing one database is enough — the other would be all-skipped.
- **Media type is not carried over.** The import does not know which database is "audiobooks"; every author arrives as a standard record. After importing, set ebook / audiobook / both per author or book where you want audiobooks.

## Bringing in books already on disk

For files already on disk, use **Library Scan**. It takes the author and title from your folder layout: a file under `{Author}/{Book}/` — Readarr's and Calibre's default structure — is matched on the folder names, so the filename convention (`Author - Title` vs `Title - Author`) does not matter. Loose files with no author/book folders fall back to filename parsing, which can still be ambiguous, so keep an organised folder structure for the most reliable scan.

One difference to know about before you start correcting matches: Readarr's fix-match only changes which record a file is linked to, while Bindery's **Fix match** re-runs the import, so it moves the file into the target book's folder and renames it from your naming template. The confirmation step names the destination path before anything happens, so you can back out if you would rather keep your existing layout. Reassigning without relocating is not available yet (#2055).

## Importing your Goodreads library (CSV)

If you tracked your reading on Goodreads, you can seed Bindery's wanted list from a Goodreads library export. This is a one-shot migration aid — it is **not** a live sync. Bindery does not poll Goodreads; if you add books on Goodreads later, re-export and re-import.

### 1. Export the CSV from Goodreads

1. Go to [goodreads.com/review/import](https://www.goodreads.com/review/import).
2. Click **Export Library**. Goodreads generates a CSV after a short delay — refresh the page and download the link when it appears.
3. The file is named like `goodreads_library_export.csv`.

### 2. Upload it in Bindery

1. Open **Settings → Import**.
2. Under **Import from Goodreads CSV**, first pick which shelves to import using the **shelf filter** (see below), then click **Upload** and choose your exported `.csv`.

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
2. **Commit.** Click **Commit** to persist the resolved books. Each is added as a **monitored, wanted** book — nothing is auto-grabbed; grab from the **Wanted** page when you are ready. The resolved preview is held server-side for 30 minutes; commit within that window or re-upload.

### 5. Failed rows

Rows that could not be matched are listed in the preview under **unresolved**, with a reason (no ISBN match, title+author search found nothing, etc.). Use **Download failed rows** to get a Goodreads-shaped CSV of just those rows, with a `Reason` column. Fix an ISBN or title in that file and re-upload it to retry only the misses.

Resolution quality depends on ISBN coverage: rows with a valid ISBN match most reliably. Older or self-published titles often have no ISBN in the export and fall back to title+author search, which can miss — that is expected.
