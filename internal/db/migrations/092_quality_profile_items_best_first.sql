-- +migrate Up
-- Reverse every quality profile's format list once, so top is best (#2733).
--
-- The editor used to list formats worst first and the order was read by
-- nothing: decision.QualityAllowed treated items as an allow list and
-- models.QualityRank ranked releases. Ranking now follows the stored order,
-- top is best, per media type. A profile nobody ever reordered therefore
-- holds the old built in ranking in the old direction, and reversing it once
-- makes that profile express the same preference under the new rule. A
-- profile someone did reorder was reordered against a label that said the
-- last entry was best, so reversing it preserves that intent too.
--
-- json_each yields the array elements with their 0 based index as key, and
-- json_group_array with ORDER BY key DESC rebuilds the array back to front.
-- json(value) embeds each element as an object; without it the text of each
-- element would be re quoted as a JSON string and the list would no longer
-- unmarshal into []models.QualityItem.
--
-- Seeded factory profiles carry '[]' and a single entry list has no order,
-- so both are excluded by the length guard. json_valid and json_type guard a
-- row a third party client wrote badly, which is left as it is rather than
-- failing the whole migration.
--
-- This is a numbered migration and not a runBackfillOnce hook on purpose: the
-- Go hooks are documented as re runnable by deleting their marker, and a
-- reversal run twice undoes itself. schema_migrations makes this one shot.
UPDATE quality_profiles
SET items = (
    SELECT json_group_array(json(value) ORDER BY key DESC)
    FROM json_each(quality_profiles.items)
)
WHERE json_valid(items)
  AND json_type(items) = 'array'
  AND json_array_length(items) > 1;

-- +migrate Down
-- Not reversible by the runner: rerunning the UPDATE would flip the lists
-- back, but the runner never applies Down sections.
