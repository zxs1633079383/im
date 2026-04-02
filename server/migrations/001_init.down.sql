DROP TRIGGER IF EXISTS trg_user_settings_updated_at ON user_settings;
DROP TRIGGER IF EXISTS trg_friendships_updated_at ON friendships;
DROP TRIGGER IF EXISTS trg_channels_updated_at ON channels;
DROP TRIGGER IF EXISTS trg_users_updated_at ON users;
DROP FUNCTION IF EXISTS update_updated_at;

DROP TABLE IF EXISTS message_favorites;
DROP TABLE IF EXISTS message_attachments;
DROP TABLE IF EXISTS files;
DROP TABLE IF EXISTS friendships;
DROP TABLE IF EXISTS messages;
DROP TABLE IF EXISTS channel_members;
DROP TABLE IF EXISTS channels;
DROP TABLE IF EXISTS user_settings;
DROP TABLE IF EXISTS users;
