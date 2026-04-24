-- ============================================================
-- M1: soft-delete / edit / readers indices
-- ============================================================

-- Soft-delete + edit timestamps on messages.
ALTER TABLE messages ADD COLUMN IF NOT EXISTS deleted BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE messages ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;
ALTER TABLE messages ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ;

-- Index for "around a given timestamp" range scans (M1 B5).
CREATE INDEX IF NOT EXISTS idx_messages_channel_created
    ON messages(channel_id, created_at);

-- Index for the readers query (M1 B4) — ranges by last_read_seq within channel.
CREATE INDEX IF NOT EXISTS idx_channel_members_chid_lastreadseq
    ON channel_members(channel_id, last_read_seq);

-- Thread replies lookups (M1 B3).
CREATE INDEX IF NOT EXISTS idx_messages_reply_to
    ON messages(reply_to)
 WHERE reply_to IS NOT NULL;
