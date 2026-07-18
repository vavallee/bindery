# ABS Import

Bindery can import Audiobookshelf (ABS) catalog metadata into the main Bindery library so you do not have to recreate authors, books, series, and editions by hand.

This page is the user-facing companion to [`docs/abs_import.md`](./abs_import.md), which remains the implementation-focused reference for contributors.

## What It Does

The current ABS importer supports one configured ABS source and one or more selected target book libraries at a time. From that source, Bindery can:

- test API-key access and discover the ABS book libraries visible to that key
- import the ABS catalog metadata it can read for each visible item
- create or match shared authors, books, series, and editions in Bindery
- preserve ABS provenance so reruns stay idempotent
- import unmatched items directly and queue only genuinely ambiguous matches for manual review
- show recent runs, dry-run previews, rollback previews, and conflict resolution state

The importer is metadata-first and shared-filesystem-aware. If ABS reports file paths that Bindery can see under its configured library roots, Bindery records those files through the normal file ownership logic. If the paths are missing, outside scope, or mounted differently, the import can still succeed as metadata-only.

The importer also supports ABS-to-Bindery path remaps when the same files are mounted at different prefixes on each side. For example, ABS might report `/audiobookshelf/media/Author/Book` while Bindery sees the same files at `/books/media/Author/Book`. In that case you can add a path remap so Bindery rewrites the ABS prefix before validating and attaching files.

## What Gets Imported

Bindery does not just create a shell entry with a title. It imports the ABS metadata fields it can map into Bindery's model, including:

- authors
- books
- series and sequence numbers when ABS provides them
- ebook and audiobook editions (a combined item — epub stored alongside the audio files — imports both, even when the ABS library has "Audiobooks only" set and the epub is only listed as a supplementary file)
- description
- release date
- genres
- language
- narrator
- duration
- ASIN
- media type
- ABS provenance and import-run history

If an ABS field has no Bindery equivalent, it may not be stored. This importer is broad, but it is not a raw mirror of every ABS field.

## Import Quality Matters

Import quality depends heavily on ABS metadata quality.

The more books you already have in Audiobookshelf with complete, high-quality metadata, the better the import will go. In particular, stable identifiers like ASINs make matching much more reliable, reduce ambiguous title-only decisions, and shrink the manual review queue.

A good pre-import cleanup pass in ABS is worth it. If your library already has strong author metadata, consistent titles, linked series, and ASINs where available, Bindery can resolve more items automatically and with fewer conflicts.

## Before You Start

- Use an ABS API key for a user that can see the target book libraries. ABS admin access is not required. The key only needs permission to authenticate and read the libraries you plan to import.
- Pick the ABS book libraries you want to import from.
- If ABS and Bindery see the same storage under different mount prefixes, configure path remaps such as `/audiobookshelf:/books/audiobookshelf`.
- Path remaps are prefix rewrites. The left side is the path prefix ABS reports, and the right side is the path prefix Bindery can actually read. Bindery applies the rewrite before checking whether the resolved file lives under a Bindery-visible library root.
- If you want the best initial match rate, it helps to make sure your ABS library metadata is already in good shape before importing.

## Import Flow

1. Open `Settings -> ABS`.
2. Save the ABS base URL, API key, label, target book libraries, and any optional path remaps. Saving stores normalized settings without contacting ABS.
3. Test the connection and list available book libraries.
4. Pick one or more book libraries from the list, or keep known saved library IDs until listing succeeds.
5. Start a dry run if you want a safe first pass.
6. Start the import.
7. Review any queued items or metadata conflicts.
8. Use rollback preview or rollback if you need to undo a run.

## Review Queue And Conflicts

Bindery distinguishes between an item it could not match locally and an item it matched *ambiguously*.

- **Unmatched** authors and books — where Bindery's local matcher finds nothing close — are **not** sent to review. They are created/imported directly, and a confidence-gated upstream lookup still relinks the new row to the metadata provider when it finds a confident match. This is deliberate: an unmatched author is not an uncertain one, and parking every unmatched item in review previously sent the bulk of a folder-backed ABS library to the queue even for well-known authors.
- **Ambiguous** matches — a close-but-uncertain local candidate — are the only items sent to the review queue, so you can confirm or correct the author or book match yourself.
- When ABS metadata and upstream metadata disagree for mapped fields, Bindery keeps the current applied value temporarily and records a conflict so you can choose the winning source.
- Placeholder ABS authors can be relinked during conflict review when Bindery can confidently connect them to upstream metadata.

## Known Behavior

- Only one ABS source is supported in the current MVP.
- Selected ABS book libraries are imported sequentially. Each selected library creates its own import run, rollback scope, and summary.
- If one selected library fails, later libraries are not started and earlier completed library runs are preserved.
- Saving settings does not test the ABS server. Use **Test connection** or **List libraries** when you want live validation.
- The library selector shows book libraries only, and imports reject non-book library or item responses before scanning.
- Imports are asynchronous.
- Non-visible file paths become metadata-only imports instead of hard failures.
- Ambiguous title matches are not auto-applied.

## Troubleshooting

- Connection test or library listing fails: verify the saved ABS base URL, API key, and that the key can see the target book libraries.
- Import rejects a selected library: use **List libraries** and choose book libraries. Podcast or other non-book ABS libraries are not imported.
- Files are not attaching after import: check path remaps and make sure the ABS-reported paths resolve under a Bindery-visible library root.
- Too many review items: improve ABS metadata quality first, especially ASIN coverage and author/title consistency, then rerun.
- Unexpected metadata disagreements: resolve them from the conflicts panel instead of rerunning until the same field flips back and forth.

## See Also

- [`docs/abs_import.md`](./abs_import.md)
- [`docs/DEPLOYMENT.md`](./DEPLOYMENT.md)
