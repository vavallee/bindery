# ABS Import Guide

This document is the single source of truth for the Audiobookshelf (ABS) import feature in Bindery.

It replaces the earlier phase-by-phase planning notes with one implementation-focused guide for contributors.

## Scope

The ABS importer lets an admin:

- configure one ABS source
- test API-key access and discover visible book libraries
- select one or more target book libraries from that source
- import ABS metadata into Bindery
- review import runs, dry-runs, rollback previews, and review/conflict items

The implementation is metadata-first and shared-filesystem-aware:

- Bindery imports catalog metadata and provenance first.
- If ABS-reported paths are visible under Bindery-managed storage, Bindery records them through the normal `book_files` path and status logic.
- If paths are missing or out of scope, the import still succeeds as metadata-only instead of failing the whole batch.

## What To Expect

The importer is designed to pull over the ABS catalog metadata it can read for each visible item rather than only creating empty placeholders. In practice that includes author, book, series, edition, provenance, and review/conflict state plus the main metadata fields Bindery tracks such as description, release date, genres, language, narrator, duration, ASIN, and media type.

Import quality depends heavily on ABS metadata quality. The more books you already have in Audiobookshelf with strong metadata, especially stable identifiers like ASINs, the better Bindery can match existing authors and books, avoid ambiguous title-only fallbacks, and keep the review queue small.

## Main Code Paths

Backend:

- [../internal/abs/client.go](../internal/abs/client.go): ABS HTTP client, auth, library and item fetches, retries, error decoding, and `bindery/<version>` user-agent handling
- [../internal/abs/enumerator.go](../internal/abs/enumerator.go): paged enumeration, checkpoint-aware traversal, detail-fetch fallback, and book-library validation
- [../internal/abs/importer.go](../internal/abs/importer.go): import orchestration, dry-run execution, progress, resume handling, and client construction
- [../internal/abs/import_author_matcher.go](../internal/abs/import_author_matcher.go): cached author and alias matching for an import run
- [../internal/abs/import_upserts.go](../internal/abs/import_upserts.go): author, book, series, edition, provenance, and review upserts
- [../internal/abs/import_files.go](../internal/abs/import_files.go): path remaps, file visibility checks, and owned-state reconciliation
- [../internal/abs/import_conflicts.go](../internal/abs/import_conflicts.go): ABS versus upstream metadata merge and conflict recording
- [../internal/abs/import_rollback.go](../internal/abs/import_rollback.go): recent run hydration, rollback planning, rollback preview, and rollback execution
- [../internal/abs/import_snapshots.go](../internal/abs/import_snapshots.go): before/after entity snapshots used by rollback
- [../internal/abs/import_types.go](../internal/abs/import_types.go): import config, progress, run, rollback, and helper types
- [../internal/abs/import_utils.go](../internal/abs/import_utils.go): shared importer normalization and utility helpers
- [../internal/abs/types.go](../internal/abs/types.go): ABS response and normalized item types
- [../internal/api/abs.go](../internal/api/abs.go): config storage, explicit connection test, and book-library discovery
- [../internal/api/abs_import.go](../internal/api/abs_import.go): start, status, recent runs, rollback endpoints
- [../internal/api/abs_review.go](../internal/api/abs_review.go): review queue actions
- [../internal/api/abs_conflicts.go](../internal/api/abs_conflicts.go): conflict listing and source resolution

Persistence:

- [../internal/db/abs_imports.go](../internal/db/abs_imports.go): run, provenance, run-entity, and review repositories
- [../internal/db/abs_metadata_conflicts.go](../internal/db/abs_metadata_conflicts.go): conflict persistence
- [../internal/db/migrations/029_abs_imports.sql](../internal/db/migrations/029_abs_imports.sql)
- [../internal/db/migrations/030_abs_metadata_conflicts.sql](../internal/db/migrations/030_abs_metadata_conflicts.sql)
- [../internal/db/migrations/031_abs_import_run_tracking.sql](../internal/db/migrations/031_abs_import_run_tracking.sql)
- [../internal/db/migrations/032_abs_review_queue.sql](../internal/db/migrations/032_abs_review_queue.sql)
- [../internal/db/migrations/033_abs_review_resolution.sql](../internal/db/migrations/033_abs_review_resolution.sql)

Frontend:

- [../web/src/api/client.ts](../web/src/api/client.ts): ABS API client types and methods
- [../web/src/pages/settings/ABSTab.tsx](../web/src/pages/settings/ABSTab.tsx): ABS settings/import UI
- [../web/src/components/ABSAuthorConflictsPanel.tsx](../web/src/components/ABSAuthorConflictsPanel.tsx): author conflict review panel

Bootstrap:

- [../cmd/bindery/main.go](../cmd/bindery/main.go): repo wiring, importer construction, versioned user-agent wiring, and route registration

## Runtime Flow

1. An admin saves ABS settings under the `abs.*` keys. `PUT /api/v1/abs/config` normalizes and stores local settings without contacting ABS.
2. The UI can explicitly probe the ABS instance to validate auth and list accessible book libraries. Probes require a saved base URL and use either a request API-key override or the stored write-only key.
3. `POST /api/v1/abs/import` starts an async import using the stored config.
4. The importer processes selected libraries sequentially in saved order. Each library receives its own `abs_import_runs` row, source snapshot, checkpoint updates, summary, rollback scope, and provenance keyed by that library ID.
5. The importer validates that each selected library response is for a book library, rejects non-book items, and fetches detail payloads when list data is incomplete.
6. If a library import fails, the active run is marked failed and later libraries are not started. Completed earlier library runs remain intact.
7. Interrupted-run resume restarts at the checkpointed library and then continues the remaining saved libraries in order.
8. Each normalized ABS item is mapped into Bindery authors, books, series, editions, provenance, and optional review/conflict records.
9. Progress is exposed through `GET /api/v1/abs/import/status`.
10. Completed runs are persisted and surfaced through recent-runs and rollback endpoints.

## Mapping Rules

### Authors

Resolution order:

1. `abs_provenance` lookup for the ABS author id
2. exact case-insensitive match in `authors.name`
3. exact case-insensitive match in `author_aliases.name`
4. create a new shared author

Imported/shared defaults:

- ABS-created rows are shared/global by default
- `owner_user_id` remains `NULL`
- `metadata_provider` is set to `audiobookshelf` for newly created rows

Secondary ABS authors are recorded as aliases where possible.

### Books

Resolution order:

1. `abs_provenance` lookup for the ABS item id
2. fallback `books.foreign_id = abs:book:<library_id>:<item_id>`
3. existing book under the resolved author with the same normalized title
4. create a new shared book

Important applied fields:

- title and sort title
- description when non-empty
- release date
- genres
- language
- narrator
- duration seconds
- ASIN
- media type

The importer distinguishes an *unmatched* item from an *ambiguous* one. When the local matcher finds nothing close, the item is unmatched: the book is created directly (step 4 above) and `enrichBook` performs a confidence-gated upstream lookup. Only an *ambiguous* match — a close-but-uncertain local candidate — is parked in the review queue rather than guessed. The same distinction applies to author resolution.

### Series

Resolution order:

1. `abs_provenance` lookup for the ABS series id
2. existing normalized-title series match
3. create a new series

Sequence metadata is written through `series_books` when ABS provides it.

### Editions

Each ABS item may upsert:

- one `ebook` edition
- one `audiobook` edition

Edition ids are deterministic and derived from library, item, and format so reruns stay idempotent.

## File Reconciliation

The importer does not create an ABS-only ownership model. It reuses Bindery's existing file and status logic.

Accepted paths:

- ebook ownership comes from `NormalizedLibraryItem.EbookPath`. That is `media.ebookFile` when ABS promoted a primary ebook; when the ABS library has **"Audiobooks only"** enabled ABS never promotes one, so the importer falls back to the item's supplementary ebook in `libraryFiles` (preferring `.epub`, matching ABS's own primary-ebook pick)
- audiobook ownership comes from the ABS item `Path`
- a path is only accepted when it resolves under a Bindery-visible library root

Effective roots can come from:

- `BINDERY_LIBRARY_DIR`
- `BINDERY_AUDIOBOOK_DIR`
- the author's explicit root folder
- `library.defaultRootFolderId`

When a path is visible and valid, Bindery records it through the normal book-file write path. When it is not, the item remains metadata-only and contributes to pending/manual follow-up rather than failing the whole run.

## Storage Model

Config is stored in existing settings rows for the single ABS source:

- `abs.base_url`
- `abs.api_key`
- `abs.library_id`
- `abs.library_ids`
- `abs.enabled`
- `abs.label`
- `abs.path_remap`

`abs.library_ids` stores the ordered selected library IDs as JSON. `abs.library_id` remains the compatibility field and mirrors the first selected library. When `abs.library_ids` is empty or missing, Bindery falls back to `abs.library_id`.

Saving config validates local syntax only. It does not authorize against ABS or verify selected library IDs; `POST /api/v1/abs/test`, `POST /api/v1/abs/libraries`, and import enumeration handle live ABS validation.

Run and provenance persistence adds these tables:

- `abs_import_runs`: batch envelope, status, config snapshot, checkpoint, summary
- `abs_import_run_entities`: per-entity outcomes for rollback and inspection
- `abs_provenance`: ABS-to-Bindery entity linkage
- `abs_review_queue`: deferred/manual review work
- `abs_metadata_conflicts`: source-choice conflicts between ABS and upstream metadata

This gives the importer idempotent reruns, traceability, and rollback planning without overloading the generic settings store with run state.

## API Surface

Config and discovery:

- `GET /api/v1/abs/config`
- `PUT /api/v1/abs/config`: store normalized settings without a live ABS probe
- `POST /api/v1/abs/test`: authorize against ABS and return user/server/default-library details
- `POST /api/v1/abs/libraries`: list only accessible ABS libraries whose media type is `book`

Import and rollback:

- `POST /api/v1/abs/import`
- `GET /api/v1/abs/import/status`
- `GET /api/v1/abs/import/runs`
- `POST /api/v1/abs/import/runs/{runID}/rollback/preview`
- `POST /api/v1/abs/import/runs/{runID}/rollback`

Review and conflict handling:

- `GET /api/v1/abs/review`
- `POST /api/v1/abs/review/{id}/approve`
- `POST /api/v1/abs/review/{id}/resolve-author`
- `POST /api/v1/abs/review/{id}/resolve-book`
- `POST /api/v1/abs/review/{id}/dismiss`
- `GET /api/v1/abs/conflicts`
- `POST /api/v1/abs/conflicts/{id}/resolve`

## Testing

Unit and handler coverage lives in:

- [../internal/abs/client_test.go](../internal/abs/client_test.go)
- [../internal/abs/contract_test.go](../internal/abs/contract_test.go)
- [../internal/abs/enumerator_test.go](../internal/abs/enumerator_test.go)
- [../internal/abs/importer_test.go](../internal/abs/importer_test.go)
- [../internal/api/abs_import_test.go](../internal/api/abs_import_test.go)
- [../internal/api/abs_review_test.go](../internal/api/abs_review_test.go)
- [../internal/api/abs_conflicts_test.go](../internal/api/abs_conflicts_test.go)

Pinned contract coverage lives in [../tests/abscontract](../tests/abscontract) and is exposed through `make abs-contract`.

Pinned baseline:

- `2.33.2`

Seeded scenarios:

- `single-file-audiobook`
- `folder-multi-file-audiobook`
- `ebook-only-item`
- `mixed-metadata-completeness`
- `series-linked-item`
- `permission-limited-account`

Covered behaviors:

- auth success and failure
- permission-scoped library listing
- paging and detail-fetch fallback
- dry-run behavior
- idempotent reruns
- checkpoint-aware resume behavior

## Contributor Notes

When changing the ABS importer:

- prefer extending the existing ABS importer and enumerator helpers instead of introducing a second runner shape
- keep ABS-created rows shared unless product requirements explicitly change
- preserve the metadata-first, filesystem-aware behavior
- update `tests/abscontract` when external contract assumptions change
- run `go test ./cmd/... ./internal/... ./tests/abscontract/...` and `make abs-contract` when touching importer behavior

If this feature grows beyond one configured source or needs richer persisted source state, revisit whether `abs.*` settings should become a dedicated source table. Until then, keep this document focused on the implemented path rather than speculative phase planning.
