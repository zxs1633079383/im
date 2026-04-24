-- ============================================================
-- M3-A: Topic (子群聊 / group-in-group) 支持
-- 在 channels 表上加 root_id（指向父 channel）和 root_message_id
-- （话题从哪条消息右击创建的）。
-- root_id IS NOT NULL 即 topic；消息与普通 channel 共用 messages 表 + seq；
-- 成员与普通 channel 共用 channel_members。
-- ============================================================

ALTER TABLE channels
    ADD COLUMN IF NOT EXISTS root_id         BIGINT REFERENCES channels(id) ON DELETE CASCADE,
    ADD COLUMN IF NOT EXISTS root_message_id BIGINT REFERENCES messages(id) ON DELETE SET NULL;

-- 只给 topic 做索引（partial index，普通 channel 的 NULL 不占空间）。
CREATE INDEX IF NOT EXISTS idx_channels_root_id ON channels(root_id)
    WHERE root_id IS NOT NULL;
