export const SCHEMA_VERSION = 1;

export const CREATE_TABLES_SQL = `
CREATE TABLE IF NOT EXISTS local_channels (
    id              TEXT PRIMARY KEY,
    type            INTEGER NOT NULL,
    name            TEXT NOT NULL DEFAULT '',
    avatar_url      TEXT NOT NULL DEFAULT '',
    server_seq      INTEGER NOT NULL DEFAULT 0,
    unread_count    INTEGER NOT NULL DEFAULT 0,
    last_msg_preview TEXT NOT NULL DEFAULT '',
    last_msg_time   INTEGER NOT NULL DEFAULT 0,
    updated_at      INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS local_messages (
    channel_id  TEXT NOT NULL,
    seq         INTEGER NOT NULL,
    server_id   TEXT NOT NULL DEFAULT '',
    client_id   TEXT NOT NULL DEFAULT '',
    sender_id   TEXT NOT NULL DEFAULT '',
    msg_type    INTEGER NOT NULL DEFAULT 1,
    content     TEXT NOT NULL DEFAULT '',
    visible     INTEGER NOT NULL DEFAULT 1,
    reply_to    TEXT,
    created_at  INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (channel_id, seq)
);

CREATE INDEX IF NOT EXISTS idx_msg_visible
    ON local_messages(channel_id, seq) WHERE visible = 1;

CREATE TABLE IF NOT EXISTS local_outbox (
    client_id   TEXT PRIMARY KEY,
    channel_id  TEXT NOT NULL,
    msg_type    INTEGER NOT NULL DEFAULT 1,
    content     TEXT NOT NULL DEFAULT '',
    reply_to    TEXT,
    retry_count INTEGER NOT NULL DEFAULT 0,
    created_at  INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS local_meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL DEFAULT ''
);
`;
