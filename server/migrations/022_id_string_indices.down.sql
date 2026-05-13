-- ============================================================
-- C012 P-A 步骤 5/5 down —— 删除 4 个 hash index。
-- 安全的 forward-only rollback：drop index 不影响 schema 正确性。
-- ============================================================

DROP INDEX IF EXISTS idx_message_attachments_message_id_hash;
DROP INDEX IF EXISTS idx_channel_members_channel_id_hash;
DROP INDEX IF EXISTS idx_channel_members_user_id_hash;
DROP INDEX IF EXISTS idx_messages_channel_id_hash;
