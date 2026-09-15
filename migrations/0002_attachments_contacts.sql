-- +goose Up
-- Attachment metadata and the derived contacts table (tech-specs §2.2, §13.1).

CREATE TABLE attachments (
    id               INTEGER PRIMARY KEY,
    message_id       INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    filename         TEXT    NOT NULL DEFAULT '',
    mime_type        TEXT    NOT NULL DEFAULT '',
    size_bytes       INTEGER NOT NULL DEFAULT 0,
    content_hash     TEXT    NOT NULL DEFAULT '',
    storage_path     TEXT    NOT NULL DEFAULT '',
    content_id       TEXT    NOT NULL DEFAULT '',
    is_inline        INTEGER NOT NULL DEFAULT 0,
    fetch_state      TEXT    NOT NULL DEFAULT 'not_fetched',
    last_accessed_at INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX idx_attachments_message ON attachments (message_id);
CREATE INDEX idx_attachments_hash ON attachments (content_hash);

-- Contacts are never user-edited; they exist only to rank compose autocomplete.
CREATE TABLE contacts (
    id              INTEGER PRIMARY KEY,
    email           TEXT    NOT NULL UNIQUE,
    display_name    TEXT    NOT NULL DEFAULT '',
    last_used_at    INTEGER NOT NULL DEFAULT 0,
    frequency_count INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX idx_contacts_last_used ON contacts (last_used_at DESC);

-- +goose Down
DROP TABLE contacts;
DROP TABLE attachments;
