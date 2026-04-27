-- ============================================================
-- v0.7.0: 补齐 cses-client 全量迁移所需的两块持久化结构
--
-- 1. message_reactions —— 消息表情回复（cses-client message-v3 把
--    /posts/quickReply 这个名字"快捷回复"实际用作 emoji 反应。一条
--    (message_id, user_id, emoji) 唯一一条，重复点同一个 emoji 视作取消。
-- 2. channel_members.is_top —— per-user 频道置顶标记。原 014 migration
--    在 channels 表上有一个 is_top boolean 是全局的（语义错），用
--    channel_members.is_top 重新承载 per-user 状态。
--
-- 不修 channels.is_top（保留向后兼容，业务层不再读它）。
-- ============================================================

CREATE TABLE message_reactions (
    message_id BIGINT      NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    user_id    TEXT        NOT NULL,
    emoji      VARCHAR(64) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (message_id, user_id, emoji)
);
CREATE INDEX idx_reactions_message ON message_reactions(message_id);
CREATE INDEX idx_reactions_user    ON message_reactions(user_id, created_at DESC);

ALTER TABLE channel_members
    ADD COLUMN is_top BOOLEAN NOT NULL DEFAULT FALSE;
CREATE INDEX idx_channel_members_user_top
    ON channel_members(user_id) WHERE is_top = TRUE;
