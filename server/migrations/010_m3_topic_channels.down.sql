DROP INDEX IF EXISTS idx_channels_root_id;
ALTER TABLE channels DROP COLUMN IF EXISTS root_message_id;
ALTER TABLE channels DROP COLUMN IF EXISTS root_id;
