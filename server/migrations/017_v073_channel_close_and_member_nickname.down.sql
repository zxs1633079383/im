-- 017 down — revert the channel-close + member-nickname columns.
ALTER TABLE channel_members DROP COLUMN IF EXISTS nick_name;
DROP INDEX IF EXISTS idx_channels_active;
ALTER TABLE channels DROP COLUMN IF EXISTS deleted_at;
