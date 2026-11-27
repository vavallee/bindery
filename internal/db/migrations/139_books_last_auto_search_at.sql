-- +migrate Up

-- When an automatic search for this book last reached an indexer. The
-- scheduled wanted sweep orders its queue by this column, never searched
-- first and then least recently searched, so a sweep that ends early (an
-- indexer's daily query cap, a restart) picks up where it stopped instead of
-- searching the head of the title order again and never reaching the rest.
--
-- NULL means never searched, which is also what every existing row reads as
-- after this runs: the first sweep after an upgrade runs in the old title
-- order and the rotation starts from there. No index: the sweep loads the
-- whole wanted list either way and sorts it once per run.
ALTER TABLE books ADD COLUMN last_auto_search_at DATETIME;

-- +migrate Down

ALTER TABLE books DROP COLUMN last_auto_search_at;
