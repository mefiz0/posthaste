-- +goose Up
-- Outgoing message pipeline: drafts, queued sends, and terminal states
-- (tech-specs §5.1). Persisted so a crash never loses composed work.

CREATE TABLE outbox (
    id                TEXT PRIMARY KEY,
    account_id        TEXT    NOT NULL DEFAULT '',
    from_address      TEXT    NOT NULL DEFAULT '',
    from_name         TEXT    NOT NULL DEFAULT '',
    to_addresses      TEXT    NOT NULL DEFAULT '',
    cc_addresses      TEXT    NOT NULL DEFAULT '',
    bcc_addresses     TEXT    NOT NULL DEFAULT '',
    subject           TEXT    NOT NULL DEFAULT '',
    body_text         TEXT    NOT NULL DEFAULT '',
    body_html         TEXT    NOT NULL DEFAULT '',
    in_reply_to       TEXT    NOT NULL DEFAULT '',
    references_header TEXT    NOT NULL DEFAULT '',
    attachment_hashes TEXT    NOT NULL DEFAULT '',
    state             TEXT    NOT NULL DEFAULT 'draft',
    attempts          INTEGER NOT NULL DEFAULT 0,
    last_error        TEXT    NOT NULL DEFAULT '',
    next_attempt_at   INTEGER NOT NULL DEFAULT 0,
    raw_mime_path     TEXT    NOT NULL DEFAULT '',
    created_at        INTEGER NOT NULL DEFAULT 0,
    updated_at        INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX idx_outbox_state ON outbox (state, next_attempt_at);

-- +goose Down
DROP TABLE outbox;
