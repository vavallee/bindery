-- +migrate Up
-- Custom formats had a REST surface and nothing else (#1471). No search or grab
-- path ever scored a release against them and there was no UI, so a format
-- created through the API never had any effect. The endpoints are gone and so
-- is the table.
DROP TABLE IF EXISTS custom_formats;
