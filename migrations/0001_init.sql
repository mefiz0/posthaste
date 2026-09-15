-- +goose Up
-- Core per-account schema (tech-specs §2.4). One database file per account;
-- there is deliberately no account_id column because isolation is structural.

CREATE TABLE folders (
    id            INTEGER PRIMARY KEY,
    name          TEXT    NOT NULL,
    imap_path     TEXT    NOT NULL,
    type          TEXT    NOT NULL DEFAULT 'user',
    delimiter     TEXT    NOT NULL DEFAULT '/',
    uid_validity  INTEGER NOT NULL DEFAULT 0,
    uid_next      INTEGER NOT NULL DEFAULT 0,
    total_count   INTEGER NOT NULL DEFAULT 0,
    unread_count  INTEGER NOT NULL DEFAULT 0,
    attributes    TEXT    NOT NULL DEFAULT '',
    UNIQUE (imap_path)
);

CREATE TABLE messages (
    id                INTEGER PRIMARY KEY,
    folder_id         INTEGER NOT NULL REFERENCES folders(id) ON DELETE CASCADE,
    uid               INTEGER NOT NULL DEFAULT 0,
    message_id_header TEXT    NOT NULL DEFAULT '',
    in_reply_to       TEXT    NOT NULL DEFAULT '',
    references_header TEXT    NOT NULL DEFAULT '',
    from_address      TEXT    NOT NULL DEFAULT '',
    from_name         TEXT    NOT NULL DEFAULT '',
    to_addresses      TEXT    NOT NULL DEFAULT '',
    cc_addresses      TEXT    NOT NULL DEFAULT '',
    subject           TEXT    NOT NULL DEFAULT '',
    date              INTEGER NOT NULL DEFAULT 0,
    flags             INTEGER NOT NULL DEFAULT 0,
    size_bytes        INTEGER NOT NULL DEFAULT 0,
    raw_mime_path     TEXT    NOT NULL DEFAULT '',
    body_text         TEXT    NOT NULL DEFAULT '',
    body_html         TEXT    NOT NULL DEFAULT '',
    has_attachments   INTEGER NOT NULL DEFAULT 0,
    created_at        INTEGER NOT NULL DEFAULT 0,
    updated_at        INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX idx_messages_folder_date ON messages (folder_id, date DESC);
CREATE UNIQUE INDEX idx_messages_folder_uid ON messages (folder_id, uid) WHERE uid > 0;
CREATE INDEX idx_messages_message_id ON messages (message_id_header);

-- The account row is a singleton per file; id is the internal account ID.
CREATE TABLE accounts (
    id                             TEXT PRIMARY KEY,
    email                          TEXT    NOT NULL DEFAULT '',
    display_name                   TEXT    NOT NULL DEFAULT '',
    imap_host                      TEXT    NOT NULL DEFAULT '',
    imap_port                      INTEGER NOT NULL DEFAULT 993,
    imap_tls                       TEXT    NOT NULL DEFAULT 'tls',
    imap_username                  TEXT    NOT NULL DEFAULT '',
    smtp_host                      TEXT    NOT NULL DEFAULT '',
    smtp_port                      INTEGER NOT NULL DEFAULT 465,
    smtp_tls                       TEXT    NOT NULL DEFAULT 'tls',
    smtp_username                  TEXT    NOT NULL DEFAULT '',
    auth_method                    TEXT    NOT NULL DEFAULT 'password',
    credential_ref                 TEXT    NOT NULL DEFAULT '',
    signature                      TEXT    NOT NULL DEFAULT '',
    notifications_enabled          INTEGER NOT NULL DEFAULT 1,
    poll_interval_seconds          INTEGER NOT NULL DEFAULT 300,
    attachment_eager_threshold_bytes INTEGER NOT NULL DEFAULT 2097152,
    is_default                     INTEGER NOT NULL DEFAULT 0,
    paused                         INTEGER NOT NULL DEFAULT 0,
    created_at                     INTEGER NOT NULL DEFAULT 0,
    updated_at                     INTEGER NOT NULL DEFAULT 0
);

-- Durable offline action queue (tech-specs §3.3). Replayed in id order.
CREATE TABLE actions (
    id               INTEGER PRIMARY KEY,
    kind             TEXT    NOT NULL,
    message_id       INTEGER NOT NULL DEFAULT 0,
    uid              INTEGER NOT NULL DEFAULT 0,
    folder_id        INTEGER NOT NULL DEFAULT 0,
    target_folder_id INTEGER NOT NULL DEFAULT 0,
    add_flags        INTEGER NOT NULL DEFAULT 0,
    remove_flags     INTEGER NOT NULL DEFAULT 0,
    created_at       INTEGER NOT NULL DEFAULT 0,
    attempts         INTEGER NOT NULL DEFAULT 0,
    last_error       TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX idx_actions_order ON actions (id);

-- +goose Down
DROP TABLE actions;
DROP TABLE accounts;
DROP TABLE messages;
DROP TABLE folders;
