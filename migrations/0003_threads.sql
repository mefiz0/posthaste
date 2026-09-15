-- +goose Up
-- Threading materialised at ingest (tech-specs §3.5). Additive changes only.

CREATE TABLE threads (
    id                 INTEGER PRIMARY KEY,
    subject_normalized TEXT    NOT NULL DEFAULT '',
    latest_date        INTEGER NOT NULL DEFAULT 0,
    message_count      INTEGER NOT NULL DEFAULT 0
);

ALTER TABLE messages ADD COLUMN thread_id INTEGER NOT NULL DEFAULT 0;

CREATE INDEX idx_messages_thread ON messages (thread_id, date DESC);
CREATE INDEX idx_threads_latest ON threads (latest_date DESC);

-- +goose Down
DROP INDEX idx_threads_latest;
DROP INDEX idx_messages_thread;
ALTER TABLE messages DROP COLUMN thread_id;
DROP TABLE threads;
