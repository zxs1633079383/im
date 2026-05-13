-- ============================================================
-- C012 P-A 步骤 2/5：把所有自增数字 id 列 BIGINT → TEXT
-- spec：docs/harness/C012-id-type-string-migration.md §3.1（实际编号 019，
-- spec 写 035；spec 数字为示意，sequential 为准）。
--
-- 顺序：
--   1) DROP DEFAULT（去掉 BIGSERIAL 的 nextval — 否则 ALTER TYPE TEXT
--      会被 PG 拒：default expr 仍是 bigint。原 sequence (xxx_id_seq) 保留
--      为 orphan，由 022.up（idx + 清理）或 down 回滚负责。
--   2) ALTER COLUMN id TYPE TEXT USING id::text
--   3) FK 列同步 ALTER TYPE TEXT
--
-- 不动列（C012 §6 白名单）：
--   - channels.seq / messages.seq（BIGINT 单调 cursor）
--   - channel_members.last_read_seq / phantom_count / phantom_at_read（计数）
--   - files.file_size（字节数）
--   - users / *.user_id / *.sender_id / *.creator_id / *.team_id（已 TEXT）
--   - schema_migrations.version
--
-- 改造的 25 列 + 1 个 ARRAY：
--   PK BIGSERIAL → TEXT：channels.id messages.id friendships.id files.id
--     announcements.id approvals.id notifications.id scheduled_messages.id
--     quick_replies.id (9 个)
--   FK / id 引用 BIGINT → TEXT：
--     channels.root_id channels.root_message_id
--     channel_members.channel_id
--     messages.channel_id messages.reply_to messages.forwarded_from
--     message_attachments.message_id message_attachments.file_id
--     message_favorites.message_id
--     channel_managers.channel_id
--     channel_pinned_messages.channel_id channel_pinned_messages.message_id
--     announcements.channel_id
--     announcement_acknowledgements.announcement_id
--     urgent_confirmations.message_id
--     approvals.channel_id
--     scheduled_messages.channel_id scheduled_messages.delivered_message_id
--       scheduled_messages.reply_to
--     message_reactions.message_id
--     (16 个)
--   ARRAY BIGINT[] → TEXT[]：scheduled_messages.file_ids (1 个)
-- ============================================================

-- ---------- channels ----------
ALTER TABLE channels ALTER COLUMN id              DROP DEFAULT;
ALTER TABLE channels ALTER COLUMN id              TYPE TEXT USING id::text;
ALTER TABLE channels ALTER COLUMN root_id         TYPE TEXT USING root_id::text;
ALTER TABLE channels ALTER COLUMN root_message_id TYPE TEXT USING root_message_id::text;

-- ---------- channel_members ----------
ALTER TABLE channel_members ALTER COLUMN channel_id TYPE TEXT USING channel_id::text;

-- ---------- messages ----------
ALTER TABLE messages ALTER COLUMN id             DROP DEFAULT;
ALTER TABLE messages ALTER COLUMN id             TYPE TEXT USING id::text;
ALTER TABLE messages ALTER COLUMN channel_id     TYPE TEXT USING channel_id::text;
ALTER TABLE messages ALTER COLUMN reply_to       TYPE TEXT USING reply_to::text;
ALTER TABLE messages ALTER COLUMN forwarded_from TYPE TEXT USING forwarded_from::text;

-- ---------- friendships ----------
ALTER TABLE friendships ALTER COLUMN id DROP DEFAULT;
ALTER TABLE friendships ALTER COLUMN id TYPE TEXT USING id::text;

-- ---------- files ----------
ALTER TABLE files ALTER COLUMN id DROP DEFAULT;
ALTER TABLE files ALTER COLUMN id TYPE TEXT USING id::text;

-- ---------- message_attachments ----------
ALTER TABLE message_attachments ALTER COLUMN message_id TYPE TEXT USING message_id::text;
ALTER TABLE message_attachments ALTER COLUMN file_id    TYPE TEXT USING file_id::text;

-- ---------- message_favorites ----------
ALTER TABLE message_favorites ALTER COLUMN message_id TYPE TEXT USING message_id::text;

-- ---------- channel_managers ----------
ALTER TABLE channel_managers ALTER COLUMN channel_id TYPE TEXT USING channel_id::text;

-- ---------- channel_pinned_messages ----------
ALTER TABLE channel_pinned_messages ALTER COLUMN channel_id TYPE TEXT USING channel_id::text;
ALTER TABLE channel_pinned_messages ALTER COLUMN message_id TYPE TEXT USING message_id::text;

-- ---------- announcements ----------
ALTER TABLE announcements ALTER COLUMN id         DROP DEFAULT;
ALTER TABLE announcements ALTER COLUMN id         TYPE TEXT USING id::text;
ALTER TABLE announcements ALTER COLUMN channel_id TYPE TEXT USING channel_id::text;

-- ---------- announcement_acknowledgements ----------
ALTER TABLE announcement_acknowledgements
    ALTER COLUMN announcement_id TYPE TEXT USING announcement_id::text;

-- ---------- urgent_confirmations ----------
ALTER TABLE urgent_confirmations ALTER COLUMN message_id TYPE TEXT USING message_id::text;

-- ---------- approvals ----------
ALTER TABLE approvals ALTER COLUMN id         DROP DEFAULT;
ALTER TABLE approvals ALTER COLUMN id         TYPE TEXT USING id::text;
ALTER TABLE approvals ALTER COLUMN channel_id TYPE TEXT USING channel_id::text;

-- ---------- notifications ----------
ALTER TABLE notifications ALTER COLUMN id DROP DEFAULT;
ALTER TABLE notifications ALTER COLUMN id TYPE TEXT USING id::text;

-- ---------- scheduled_messages ----------
ALTER TABLE scheduled_messages ALTER COLUMN id                   DROP DEFAULT;
ALTER TABLE scheduled_messages ALTER COLUMN id                   TYPE TEXT USING id::text;
ALTER TABLE scheduled_messages ALTER COLUMN channel_id           TYPE TEXT USING channel_id::text;
ALTER TABLE scheduled_messages ALTER COLUMN delivered_message_id TYPE TEXT USING delivered_message_id::text;
ALTER TABLE scheduled_messages ALTER COLUMN reply_to             TYPE TEXT USING reply_to::text;
-- file_ids 是 BIGINT[]，PG 14 允许 bigint[]::text[] 直接 cast（每个元素
-- 走 bigint→text）。注意 USING 表达式里不能含 subquery（PG 限制），所以
-- 用 plain expression cast 而不是 ARRAY(SELECT unnest(...)::text)。
ALTER TABLE scheduled_messages
    ALTER COLUMN file_ids TYPE TEXT[] USING file_ids::text[];

-- ---------- quick_replies ----------
ALTER TABLE quick_replies ALTER COLUMN id DROP DEFAULT;
ALTER TABLE quick_replies ALTER COLUMN id TYPE TEXT USING id::text;

-- ---------- message_reactions ----------
ALTER TABLE message_reactions ALTER COLUMN message_id TYPE TEXT USING message_id::text;
