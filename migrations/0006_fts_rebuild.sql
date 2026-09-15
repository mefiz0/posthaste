-- +goose Up
-- Backfill the FTS5 index for messages that existed before the index was
-- added. The external-content table (0004) is populated by triggers on
-- insert/update/delete, so rows created before the index are invisible until
-- a rebuild. The command reads every message from the content table and
-- indexes it; it is a no-op on a fresh, empty database.
INSERT INTO messages_fts (messages_fts) VALUES ('rebuild');

-- +goose Down
-- Nothing to undo: the previous migration drops the index table entirely.
SELECT 1;
