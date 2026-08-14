-- +migrate Up
-- Records that an author's catalogue has been populated at least once, so a
-- refresh can tell "never populated" from "the user emptied this on purpose"
-- (#1815, #1816).
--
-- A refresh may only create rows for works the library doesn't have when the
-- author's monitoring says they are wanted, with one carve-out: an author with
-- no books at all is populated regardless, because repairing an import that
-- resolved the author but no catalogue is exactly what bulk "Refresh metadata"
-- and "Refresh all authors" are for. Deleting every book under an unmonitored
-- author landed the author in that same zero-book state, so the next bulk
-- refresh re-imported the whole bibliography -- which is the cleanup the docs
-- recommend, re-arming the bug it documents.
--
-- The column separates the two: set by BookRepo.Create on the author's first
-- book (every creation path flows through there, so an ABS import or a
-- Hardcover list counts as much as a catalogue sync), never cleared. Zero books
-- AND never populated is the repair case. Zero books AND populated before is a
-- library the user emptied.
--
-- Backfill: any author who currently has books has demonstrably been populated.
-- Authors emptied before this migration cannot be detected and get one more
-- refill, after which the marker sticks.
-- NOTE: no semicolons inside comments -- the migration runner splits on them.
ALTER TABLE authors ADD COLUMN catalogue_populated_at TIMESTAMP;

UPDATE authors
SET catalogue_populated_at = created_at
WHERE catalogue_populated_at IS NULL
  AND EXISTS (SELECT 1 FROM books WHERE books.author_id = authors.id);
