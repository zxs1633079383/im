-- 015 down — drop reactions table + remove is_top column.
DROP INDEX IF EXISTS idx_channel_members_user_top;
ALTER TABLE channel_members DROP COLUMN IF EXISTS is_top;

DROP INDEX IF EXISTS idx_reactions_user;
DROP INDEX IF EXISTS idx_reactions_message;
DROP TABLE IF EXISTS message_reactions;
