-- +migrate Up
-- Canonical cross-source dedup key on books (#940).
--
-- ABS and Calibre imported the same work into two rows, one with files and one
-- empty. Root cause was asymmetric title normalization at lookup time. The
-- Calibre importer matched on raw LOWER(title) = LOWER(?) SQL while the ABS
-- importer matched on an in-memory normalized key (subtitle and bracket
-- stripped, umlaut-folded). Whichever source imported first won, and
-- the other created a duplicate. Re-running an import resurrected deleted rows.
--
-- Fix. Persist a single canonical dedup key computed by exactly one Go
-- function (indexer.CanonicalDedupKey) at every book-create path, and switch
-- both importer lookups to author_id + dedup_key. The column is nullable so
-- existing rows that have not yet been recomputed are simply skipped by the
-- equality lookup (NULL never matches), never falsely merged.
ALTER TABLE books ADD COLUMN dedup_key TEXT;

-- Index the lookup the importers use, "is there already a book for this author
-- with this canonical key". Partial on non-NULL keeps the index small and
-- guarantees NULL rows (not yet backfilled) are not lookup-eligible.
CREATE INDEX IF NOT EXISTS idx_books_author_dedup_key
    ON books(author_id, dedup_key) WHERE dedup_key IS NOT NULL;

-- Best-effort SQL backfill so the column is non-NULL for the common case even
-- before the application starts. This is a deliberately COARSE approximation
-- of indexer.CanonicalDedupKey. It lowercases, collapses a trailing colon-space
-- subtitle, and trims. It does NOT do bracket or paren-suffix stripping,
-- Unicode NFC, or umlaut folding because SQLite cannot express those. The
-- Go-side backfill (db.backfillBookDedupKeys, run on startup)
-- recomputes the exact key for every row and overwrites these approximations,
-- so this clause only narrows the window where lookups could miss before the
-- app boots.
UPDATE books
SET dedup_key = TRIM(LOWER(
        CASE
            WHEN INSTR(title, ': ') > 0 THEN SUBSTR(title, 1, INSTR(title, ': ') - 1)
            ELSE title
        END))
WHERE dedup_key IS NULL;

-- +migrate Down

DROP INDEX IF EXISTS idx_books_author_dedup_key;
-- SQLite < 3.35 cannot DROP COLUMN. The forward migration is additive and
-- defaults to NULL on existing rows, so leaving the column in place is a no-op
-- for callers that do not read it. A true rollback requires a manual table
-- rebuild.
