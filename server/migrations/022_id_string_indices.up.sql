-- ============================================================
-- C012 P-A 步骤 5/5：为最热的 TEXT id 等值 JOIN 列加 hash index 做性能
-- 补偿（VARCHAR PK B-tree 比 BIGINT 大 2-3x，等值 lookup 用 hash 反而更小）。
-- spec：docs/harness/C012-id-type-string-migration.md §3.1（实际编号 022，
-- spec 写 038）。
--
-- Hash index 注意事项（PG 10+ WAL-logged，已可生产使用）：
--   - 只支持等值（=），不支持范围 / ORDER BY
--   - 不支持 unique 约束（unique 走 PK btree）
--   - 体积小，等值 lookup 比 btree 快 ~20% on text key
--
-- 加 hash 的 4 个热路径列（避免与现有 btree 重复，只在"等值 JOIN 热但
-- 没有 partial btree"的位置补）：
--   1) messages.channel_id   —— 频道消息列表，热度极高
--   2) channel_members.user_id —— "我加入了哪些频道"
--      （channel_members PK = (user_id, channel_id) 已可 cover user_id
--       前缀；hash 额外提供一种执行计划选择，让 planner 在小表 / 大表
--       JOIN 时各自挑最优）
--   3) channel_members.channel_id —— 已有 btree idx_channel_members_channel
--      但 channel_id 在 014 还是 BIGINT，本次改 TEXT 后 btree 失效需重建。
--      （PG 14 ALTER COLUMN TYPE 会自动重建 index，但 hash 仍值得补）
--   4) message_attachments.message_id —— "消息的附件列表"
--
-- 不加 hash 的位置（避免冗余）：
--   - messages.id / channels.id 等 PK：已有 btree pkey
--   - urgent_confirmations / message_favorites 等 composite PK：低频
--   - announcements / approvals / notifications：写多读相对少，先不补
--
-- 性能仍有疑虑时，后续可走 EXPLAIN ANALYZE 实测再补；本 migration 只补
-- 4 个最稳赚的位置。
-- ============================================================

-- 1. messages.channel_id —— 频道消息列表，等值 JOIN 热点。已有 btree
--    idx_messages_channel_seq (channel_id, seq)，新 hash 仅对纯 channel_id
--    等值场景（如 channel-delete cascade scan）提速。
CREATE INDEX IF NOT EXISTS idx_messages_channel_id_hash
    ON messages USING hash (channel_id);

-- 2. channel_members.user_id —— "我所在的频道列表"，每次进入 IM 必查。
--    PK = (user_id, channel_id) 已 cover 但需要 btree range；hash 给
--    planner 多一个执行计划选择。
CREATE INDEX IF NOT EXISTS idx_channel_members_user_id_hash
    ON channel_members USING hash (user_id);

-- 3. channel_members.channel_id —— "频道成员列表"
CREATE INDEX IF NOT EXISTS idx_channel_members_channel_id_hash
    ON channel_members USING hash (channel_id);

-- 4. message_attachments.message_id —— 消息附件 JOIN
CREATE INDEX IF NOT EXISTS idx_message_attachments_message_id_hash
    ON message_attachments USING hash (message_id);
