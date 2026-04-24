-- M3: messages.props carries structured metadata for system messages
-- (msg_type=4). Normal text messages leave it NULL so we don't pay
-- the JSONB overhead for the hot path. Kept unindexed — system messages
-- are rare and not queried by props content.
ALTER TABLE messages
    ADD COLUMN props JSONB;
