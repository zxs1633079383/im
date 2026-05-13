-- ============================================================
-- C012 P-A 步骤 3/5：给所有原 BIGSERIAL PK 设 TEXT DEFAULT 生成器。
-- spec：docs/harness/C012-id-type-string-migration.md §3.1（实际编号 020，
-- spec 写 036）。
--
-- DEFAULT 表达式选用 gen_random_uuid()::text：
--   - 用户 prompt 明确允许此选择
--   - PostgreSQL 13+ 内置，无需 extension（pgcrypto / uuid-ossp 不需要
--     enable —— 14.x 默认带 gen_random_uuid()）
--   - 26 char (ULID Crockford base32) vs 36 char (uuid-v4 dashed)，
--     存储多 10 byte/行，可接受（C012 §8 已声明 ULID +18 byte/行 acceptable）
--   - service 层 (pkg/id.NewULID()) 仍可显式生成 ID 后 INSERT；DEFAULT 仅
--     作 "INSERT without id" 的 fallback
--
-- 不为以下表设 DEFAULT：
--   - channel_members / message_attachments / message_favorites /
--     announcement_acknowledgements / urgent_confirmations /
--     channel_managers / channel_pinned_messages / message_reactions：
--     composite PK，无单一自增 id
--   - modules：name 是 PK，本来就是 string，无 DEFAULT 需求
--   - user_settings：user_id PK（TEXT），由 service 层填
--
-- 共 9 个表设 DEFAULT：channels / messages / friendships / files /
--   announcements / approvals / notifications / scheduled_messages /
--   quick_replies
-- ============================================================

ALTER TABLE channels           ALTER COLUMN id SET DEFAULT gen_random_uuid()::text;
ALTER TABLE messages           ALTER COLUMN id SET DEFAULT gen_random_uuid()::text;
ALTER TABLE friendships        ALTER COLUMN id SET DEFAULT gen_random_uuid()::text;
ALTER TABLE files              ALTER COLUMN id SET DEFAULT gen_random_uuid()::text;
ALTER TABLE announcements      ALTER COLUMN id SET DEFAULT gen_random_uuid()::text;
ALTER TABLE approvals          ALTER COLUMN id SET DEFAULT gen_random_uuid()::text;
ALTER TABLE notifications      ALTER COLUMN id SET DEFAULT gen_random_uuid()::text;
ALTER TABLE scheduled_messages ALTER COLUMN id SET DEFAULT gen_random_uuid()::text;
ALTER TABLE quick_replies      ALTER COLUMN id SET DEFAULT gen_random_uuid()::text;
