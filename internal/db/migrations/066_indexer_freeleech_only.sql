-- +migrate Up
-- Per-indexer "only auto-grab freeleech releases" policy (cleb's request).
-- Private-tracker users close to a ratio floor could previously only stay safe
-- by restricting the indexer itself to Freeleech/VIP in Jackett — which also
-- hid normal releases from interactive single-book search, where the user is
-- happy to pay the ratio cost on a book they actually care about.
--
-- Gating at the release level instead: when enabled, the scheduler's automatic
-- grab path only auto-grabs releases this indexer reports as freeleech
-- (torznab downloadvolumefactor == 0). Anything that would cost ratio is held
-- in pending_releases for manual approval rather than hidden or grabbed blind,
-- which also covers bulk/multi-book search (no picker, pure fire-and-forget).
-- Interactive search builds its own specification set and is unaffected.
--
-- 0 = off (the prior behaviour, and what every existing row gets). Mirrors the
-- per-indexer seed_ratio override (#883, migration 053) in shape and intent:
-- tracker economics are per-tracker, so this must not be a global switch that
-- also throttles public trackers and usenet where ratio is meaningless.
ALTER TABLE indexers ADD COLUMN freeleech_only INTEGER NOT NULL DEFAULT 0;

-- +migrate Down

ALTER TABLE indexers DROP COLUMN freeleech_only;
