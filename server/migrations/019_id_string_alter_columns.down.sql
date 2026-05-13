-- ============================================================
-- C012 P-A 步骤 2/5 down —— 把 TEXT id 列还原成 BIGINT。
--
-- ⚠️ irreversible in prod：USING col::bigint 对 ULID/UUID 字符串会 ERROR：
-- "invalid input syntax for type bigint"。本 down 仅可在以下场景使用：
--   - 空库 reset（CI 重置 testcontainer）
--   - 数据未被任何 service 写入过（still 100% nextval 数字字符串）
-- 任何一条历史行 id 是非数字字符串都会让本 down 失败。生产回退请走
-- 备份恢复（C012 §8）。
--
-- 还原原有的 BIGSERIAL DEFAULT：复用原 sequence（xxx_id_seq）；
-- ALTER TYPE BIGINT 不会改 nextval(sequence) 的逻辑。
-- ============================================================

-- ---------- message_reactions ----------
ALTER TABLE message_reactions ALTER COLUMN message_id TYPE BIGINT USING message_id::bigint;

-- ---------- quick_replies ----------
ALTER TABLE quick_replies ALTER COLUMN id TYPE BIGINT USING id::bigint;
ALTER TABLE quick_replies ALTER COLUMN id SET DEFAULT nextval('quick_replies_id_seq');

-- ---------- scheduled_messages ----------
-- text[]::bigint[] 走 element-wise cast，与 019.up 对称。USING 不允许
-- subquery，所以用 plain expression。
ALTER TABLE scheduled_messages
    ALTER COLUMN file_ids TYPE BIGINT[] USING file_ids::bigint[];
ALTER TABLE scheduled_messages ALTER COLUMN reply_to             TYPE BIGINT USING reply_to::bigint;
ALTER TABLE scheduled_messages ALTER COLUMN delivered_message_id TYPE BIGINT USING delivered_message_id::bigint;
ALTER TABLE scheduled_messages ALTER COLUMN channel_id           TYPE BIGINT USING channel_id::bigint;
ALTER TABLE scheduled_messages ALTER COLUMN id                   TYPE BIGINT USING id::bigint;
ALTER TABLE scheduled_messages ALTER COLUMN id                   SET DEFAULT nextval('scheduled_messages_id_seq');

-- ---------- notifications ----------
ALTER TABLE notifications ALTER COLUMN id TYPE BIGINT USING id::bigint;
ALTER TABLE notifications ALTER COLUMN id SET DEFAULT nextval('notifications_id_seq');

-- ---------- approvals ----------
ALTER TABLE approvals ALTER COLUMN channel_id TYPE BIGINT USING channel_id::bigint;
ALTER TABLE approvals ALTER COLUMN id         TYPE BIGINT USING id::bigint;
ALTER TABLE approvals ALTER COLUMN id         SET DEFAULT nextval('approvals_id_seq');

-- ---------- urgent_confirmations ----------
ALTER TABLE urgent_confirmations ALTER COLUMN message_id TYPE BIGINT USING message_id::bigint;

-- ---------- announcement_acknowledgements ----------
ALTER TABLE announcement_acknowledgements
    ALTER COLUMN announcement_id TYPE BIGINT USING announcement_id::bigint;

-- ---------- announcements ----------
ALTER TABLE announcements ALTER COLUMN channel_id TYPE BIGINT USING channel_id::bigint;
ALTER TABLE announcements ALTER COLUMN id         TYPE BIGINT USING id::bigint;
ALTER TABLE announcements ALTER COLUMN id         SET DEFAULT nextval('announcements_id_seq');

-- ---------- channel_pinned_messages ----------
ALTER TABLE channel_pinned_messages ALTER COLUMN message_id TYPE BIGINT USING message_id::bigint;
ALTER TABLE channel_pinned_messages ALTER COLUMN channel_id TYPE BIGINT USING channel_id::bigint;

-- ---------- channel_managers ----------
ALTER TABLE channel_managers ALTER COLUMN channel_id TYPE BIGINT USING channel_id::bigint;

-- ---------- message_favorites ----------
ALTER TABLE message_favorites ALTER COLUMN message_id TYPE BIGINT USING message_id::bigint;

-- ---------- message_attachments ----------
ALTER TABLE message_attachments ALTER COLUMN file_id    TYPE BIGINT USING file_id::bigint;
ALTER TABLE message_attachments ALTER COLUMN message_id TYPE BIGINT USING message_id::bigint;

-- ---------- files ----------
ALTER TABLE files ALTER COLUMN id TYPE BIGINT USING id::bigint;
ALTER TABLE files ALTER COLUMN id SET DEFAULT nextval('files_id_seq');

-- ---------- friendships ----------
ALTER TABLE friendships ALTER COLUMN id TYPE BIGINT USING id::bigint;
ALTER TABLE friendships ALTER COLUMN id SET DEFAULT nextval('friendships_id_seq');

-- ---------- messages ----------
ALTER TABLE messages ALTER COLUMN forwarded_from TYPE BIGINT USING forwarded_from::bigint;
ALTER TABLE messages ALTER COLUMN reply_to       TYPE BIGINT USING reply_to::bigint;
ALTER TABLE messages ALTER COLUMN channel_id     TYPE BIGINT USING channel_id::bigint;
ALTER TABLE messages ALTER COLUMN id             TYPE BIGINT USING id::bigint;
ALTER TABLE messages ALTER COLUMN id             SET DEFAULT nextval('messages_id_seq');

-- ---------- channel_members ----------
ALTER TABLE channel_members ALTER COLUMN channel_id TYPE BIGINT USING channel_id::bigint;

-- ---------- channels ----------
ALTER TABLE channels ALTER COLUMN root_message_id TYPE BIGINT USING root_message_id::bigint;
ALTER TABLE channels ALTER COLUMN root_id         TYPE BIGINT USING root_id::bigint;
ALTER TABLE channels ALTER COLUMN id              TYPE BIGINT USING id::bigint;
ALTER TABLE channels ALTER COLUMN id              SET DEFAULT nextval('channels_id_seq');
