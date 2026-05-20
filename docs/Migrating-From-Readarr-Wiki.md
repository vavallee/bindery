# Migrating from Readarr

## Importing a readarr.db

In **Settings → Import**, upload your `readarr.db`. The import brings in:

- **Authors**, with their monitored state
- **Indexers**
- **Download clients**
- **Blocklist**

Each imported author's catalogue is populated from metadata. Nothing is auto-grabbed — grab from the **Wanted** page when you are ready.

The import dedupes by metadata id, so re-running it is safe: authors that already exist are skipped.

## Two Readarr instances (separate ebook / audiobook)

Bindery is a single instance. One author record covers ebook, audiobook, or both, set per author or per book.

- **Run the import once per `readarr.db`.** The second run skips authors already imported and adds any new ones.
- **If your two instances are kept in sync** via Import Lists and hold the same authors, importing one database is enough — the other would be all-skipped.
- **Media type is not carried over.** The import does not know which database is "audiobooks"; every author arrives as a standard record. After importing, set ebook / audiobook / both per author or book where you want audiobooks.

## Bringing in books already on disk

For files already on disk, use **Library Scan**. Parsing of existing filenames is still being improved — if authors and titles come in wrong, check the open issues or ask in support before bulk-renaming.
