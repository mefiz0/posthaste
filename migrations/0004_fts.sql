-- +goose Up
-- FTS5 index over sender, recipients, subject, and body text. The index is an
-- external-content table over `messages`, kept current by triggers so it never
-- needs a rebuild pass as mail arrives.

CREATE VIRTUAL TABLE messages_fts USING fts5(
    subject,
    from_address,
    from_name,
    to_addresses,
    cc_addresses,
    body_text,
    content='messages',
    content_rowid='id',
    tokenize='unicode61'
);

-- +goose StatementBegin
CREATE TRIGGER messages_fts_ai AFTER INSERT ON messages BEGIN
    INSERT INTO messages_fts (rowid, subject, from_address, from_name, to_addresses, cc_addresses, body_text)
    VALUES (new.id, new.subject, new.from_address, new.from_name, new.to_addresses, new.cc_addresses, new.body_text);
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER messages_fts_ad AFTER DELETE ON messages BEGIN
    INSERT INTO messages_fts (messages_fts, rowid, subject, from_address, from_name, to_addresses, cc_addresses, body_text)
    VALUES ('delete', old.id, old.subject, old.from_address, old.from_name, old.to_addresses, old.cc_addresses, old.body_text);
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER messages_fts_au AFTER UPDATE ON messages BEGIN
    INSERT INTO messages_fts (messages_fts, rowid, subject, from_address, from_name, to_addresses, cc_addresses, body_text)
    VALUES ('delete', old.id, old.subject, old.from_address, old.from_name, old.to_addresses, old.cc_addresses, old.body_text);
    INSERT INTO messages_fts (rowid, subject, from_address, from_name, to_addresses, cc_addresses, body_text)
    VALUES (new.id, new.subject, new.from_address, new.from_name, new.to_addresses, new.cc_addresses, new.body_text);
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER messages_fts_au;
DROP TRIGGER messages_fts_ad;
DROP TRIGGER messages_fts_ai;
DROP TABLE messages_fts;
