-- ============================================================
-- C012 P-A 步骤 3/5 down —— 删除 9 个 PK 表上的 gen_random_uuid()::text
-- DEFAULT。
--
-- ⚠️ irreversible in prod：删 DEFAULT 后，service 层若依赖 DB 自动生成 id
-- 会 INSERT 失败。本 down 仅用于本地 reset；P-A 后 service 层应自己生成
-- ULID，DEFAULT 是 belt-and-suspenders。
-- ============================================================

ALTER TABLE channels           ALTER COLUMN id DROP DEFAULT;
ALTER TABLE messages           ALTER COLUMN id DROP DEFAULT;
ALTER TABLE friendships        ALTER COLUMN id DROP DEFAULT;
ALTER TABLE files              ALTER COLUMN id DROP DEFAULT;
ALTER TABLE announcements      ALTER COLUMN id DROP DEFAULT;
ALTER TABLE approvals          ALTER COLUMN id DROP DEFAULT;
ALTER TABLE notifications      ALTER COLUMN id DROP DEFAULT;
ALTER TABLE scheduled_messages ALTER COLUMN id DROP DEFAULT;
ALTER TABLE quick_replies      ALTER COLUMN id DROP DEFAULT;
