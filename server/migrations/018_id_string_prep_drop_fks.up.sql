-- ============================================================
-- C012 P-A 步骤 1/5：删除所有指向 BIGSERIAL/BIGINT id 列的外键约束
-- spec：docs/harness/C012-id-type-string-migration.md §3.1（表中 spec 写
-- 序号 034，本仓 migrations 实际接续编号为 018-022；spec 数字为示意，以
-- sequential 编号为准）。
--
-- 目的：先把 17 个 FK 解耦，下一步 (019) 才能安全 ALTER COLUMN TYPE
-- TEXT。FK 双侧 type 不一致会被 PG 拒。
--
-- 不动的列（C012 §6 白名单）：
--   - seq（messages.seq / channels.seq，单调 cursor 不是身份 ID）
--   - last_read_seq / phantom_count / phantom_at_read（计数）
--   - users.* / *.user_id / *.sender_id / *.creator_id 等已经是 TEXT
--     的列（M4 / C010 / C011）
--   - team_id（已 TEXT）
--   - *_at 时间戳
--   - schema_migrations.version（migrate 工具自管）
--
-- 涵盖的 17 个 FK 约束（顺序无关，因为只是 DROP）：
--   channels.fk_channels_root                                     (channels.root_id → channels.id)
--   channels.fk_channels_root_message                             (channels.root_message_id → messages.id)
--   channel_members.channel_id_fkey                               (→ channels.id)
--   messages.channel_id_fkey                                      (→ channels.id)
--   message_attachments.message_id_fkey / file_id_fkey            (→ messages.id / files.id)
--   message_favorites.message_id_fkey                             (→ messages.id)
--   channel_managers.channel_id_fkey                              (→ channels.id)
--   channel_pinned_messages.channel_id_fkey / message_id_fkey     (→ channels.id / messages.id)
--   announcements.channel_id_fkey                                 (→ channels.id)
--   announcement_acknowledgements.announcement_id_fkey            (→ announcements.id)
--   urgent_confirmations.message_id_fkey                          (→ messages.id)
--   approvals.channel_id_fkey                                     (→ channels.id)
--   scheduled_messages.channel_id_fkey / delivered_message_id_fkey
--   message_reactions.message_id_fkey                             (→ messages.id)
-- ============================================================

-- channels 自引用 + 引用 messages（来自 010_m3_topic_channels + 014_m4_userid_text §5）
ALTER TABLE channels DROP CONSTRAINT IF EXISTS fk_channels_root;
ALTER TABLE channels DROP CONSTRAINT IF EXISTS fk_channels_root_message;

-- channel_members → channels
ALTER TABLE channel_members DROP CONSTRAINT IF EXISTS channel_members_channel_id_fkey;

-- messages → channels
ALTER TABLE messages DROP CONSTRAINT IF EXISTS messages_channel_id_fkey;

-- message_attachments → messages / files
ALTER TABLE message_attachments DROP CONSTRAINT IF EXISTS message_attachments_message_id_fkey;
ALTER TABLE message_attachments DROP CONSTRAINT IF EXISTS message_attachments_file_id_fkey;

-- message_favorites → messages
ALTER TABLE message_favorites DROP CONSTRAINT IF EXISTS message_favorites_message_id_fkey;

-- channel_managers → channels
ALTER TABLE channel_managers DROP CONSTRAINT IF EXISTS channel_managers_channel_id_fkey;

-- channel_pinned_messages → channels / messages
ALTER TABLE channel_pinned_messages DROP CONSTRAINT IF EXISTS channel_pinned_messages_channel_id_fkey;
ALTER TABLE channel_pinned_messages DROP CONSTRAINT IF EXISTS channel_pinned_messages_message_id_fkey;

-- announcements → channels
ALTER TABLE announcements DROP CONSTRAINT IF EXISTS announcements_channel_id_fkey;

-- announcement_acknowledgements → announcements
ALTER TABLE announcement_acknowledgements DROP CONSTRAINT IF EXISTS announcement_acknowledgements_announcement_id_fkey;

-- urgent_confirmations → messages
ALTER TABLE urgent_confirmations DROP CONSTRAINT IF EXISTS urgent_confirmations_message_id_fkey;

-- approvals → channels
ALTER TABLE approvals DROP CONSTRAINT IF EXISTS approvals_channel_id_fkey;

-- scheduled_messages → channels / messages
ALTER TABLE scheduled_messages DROP CONSTRAINT IF EXISTS scheduled_messages_channel_id_fkey;
ALTER TABLE scheduled_messages DROP CONSTRAINT IF EXISTS scheduled_messages_delivered_message_id_fkey;

-- message_reactions → messages
ALTER TABLE message_reactions DROP CONSTRAINT IF EXISTS message_reactions_message_id_fkey;
