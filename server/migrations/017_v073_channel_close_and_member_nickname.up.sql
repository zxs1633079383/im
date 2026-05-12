-- ============================================================
-- v0.7.3 cses-client 对接补丁，覆盖 4 个 gap：
--   #1+#3 channels.deleted_at  → owner 解散群聊（软删）+ channel_closed WS
--   #5    channel_members.nick_name → 群昵称（per-user, per-channel）
--
-- 列均带 sensible default，旧数据 0 改动可读：
--   - deleted_at 默认 NULL → 现存频道全部"未删除"
--   - nick_name  默认 ''   → 现存成员无群昵称（client 端 fallback 全局名）
--
-- 不动其他表 / 不动 channels.is_top（语义自 014 起业务侧已迁移到
-- channel_members.is_top；保留旧列以维持回滚兼容）。
-- ============================================================

ALTER TABLE channels
    ADD COLUMN deleted_at TIMESTAMPTZ NULL;
-- 部分索引：仅活跃频道走 hot path，软删行不进 index 不影响查询性能。
CREATE INDEX idx_channels_active ON channels(updated_at DESC) WHERE deleted_at IS NULL;

ALTER TABLE channel_members
    ADD COLUMN nick_name VARCHAR(64) NOT NULL DEFAULT '';
