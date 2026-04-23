DROP INDEX IF EXISTS idx_messages_reply_to;
DROP INDEX IF EXISTS idx_channel_members_chid_lastreadseq;
DROP INDEX IF EXISTS idx_messages_channel_created;

ALTER TABLE messages DROP COLUMN IF EXISTS updated_at;
ALTER TABLE messages DROP COLUMN IF EXISTS deleted_at;
ALTER TABLE messages DROP COLUMN IF EXISTS deleted;
