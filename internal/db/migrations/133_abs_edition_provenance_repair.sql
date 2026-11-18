-- +migrate Up

-- Repoint Audiobookshelf edition provenance at the edition it names (#3978).
-- EditionRepo.Upsert used to read last_insert_rowid() after an ON CONFLICT
-- update, which returned the id of whatever row the connection inserted last,
-- so a re-import could store another edition's id as local_id. The importer
-- builds an edition's foreign_id as abs:edition:<library_id>:<external_id>, and
-- foreign_id is unique, so each provenance row maps to at most one edition.
-- Rows already correct, and rows whose edition no longer exists, are left as
-- they are. Safe to run again.
--
-- abs_import_run_entities is deliberately not touched: those rows are history,
-- and a local_id that points at a since deleted edition is indistinguishable
-- from a wrongly recorded one.
UPDATE abs_provenance
SET local_id = (
        SELECT e.id FROM editions e
        WHERE e.foreign_id = 'abs:edition:' || TRIM(abs_provenance.library_id) || ':' || TRIM(abs_provenance.external_id)
    )
WHERE entity_type = 'edition'
  AND EXISTS (
        SELECT 1 FROM editions e
        WHERE e.foreign_id = 'abs:edition:' || TRIM(abs_provenance.library_id) || ':' || TRIM(abs_provenance.external_id)
          AND e.id <> abs_provenance.local_id
    );

-- +migrate Down

-- The old local_id values were wrong, so there is nothing to restore.
SELECT 1;
