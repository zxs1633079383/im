DROP INDEX IF EXISTS idx_messages_mention_gin;
ALTER TABLE messages DROP COLUMN IF EXISTS mention_list;
