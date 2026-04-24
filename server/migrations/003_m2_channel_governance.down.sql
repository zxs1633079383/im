-- M2-A: channel governance reverse

DROP INDEX IF EXISTS idx_channel_pins_msg;
DROP TABLE IF EXISTS channel_pinned_messages;

DROP INDEX IF EXISTS idx_channel_managers_user;
DROP TABLE IF EXISTS channel_managers;

ALTER TABLE channel_members DROP COLUMN IF EXISTS notify_pref;

ALTER TABLE channels DROP COLUMN IF EXISTS is_top;
ALTER TABLE channels DROP COLUMN IF EXISTS permission;
ALTER TABLE channels DROP COLUMN IF EXISTS orient;
ALTER TABLE channels DROP COLUMN IF EXISTS props;
ALTER TABLE channels DROP COLUMN IF EXISTS picture_url;
ALTER TABLE channels DROP COLUMN IF EXISTS purpose;
ALTER TABLE channels DROP COLUMN IF EXISTS notice;
