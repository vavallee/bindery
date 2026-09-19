-- min_edition_count on metadata_profiles (#2235): opt-in floor on the
-- edition count of a work's title cluster during author sync. 0 (the
-- default) disables the filter. Only OpenLibrary search results populate
-- Book.EditionCount; a work with no count is treated as unknown and passes,
-- the same "unknown is not zero" semantics MinPages uses for page counts.
ALTER TABLE metadata_profiles ADD COLUMN min_edition_count INTEGER NOT NULL DEFAULT 0;
