-- ============================================================
-- C012 P-A 步骤 4/5 down —— 再次删除 021.up 重建的 17 个 FK。
-- 这一步是 down 顺序的关键：019.down 把 TEXT 列 ALTER 回 BIGINT 时，
-- 如果 FK 还存在会被 PG 拒（FK 双侧 type 必须一致）。所以 down 顺序为
-- 022→021→020→019→018，即先 drop fk，再 drop indices，再 drop default，
-- 再 alter type 回 BIGINT，最后重建 FK（在 018.down 里）。
--
-- ⚠️ irreversible in prod：见 C012 §8。
-- ============================================================

ALTER TABLE channels DROP CONSTRAINT IF EXISTS fk_channels_root;
ALTER TABLE channels DROP CONSTRAINT IF EXISTS fk_channels_root_message;

ALTER TABLE channel_members             DROP CONSTRAINT IF EXISTS channel_members_channel_id_fkey;
ALTER TABLE messages                    DROP CONSTRAINT IF EXISTS messages_channel_id_fkey;
ALTER TABLE message_attachments         DROP CONSTRAINT IF EXISTS message_attachments_message_id_fkey;
ALTER TABLE message_attachments         DROP CONSTRAINT IF EXISTS message_attachments_file_id_fkey;
ALTER TABLE message_favorites           DROP CONSTRAINT IF EXISTS message_favorites_message_id_fkey;
ALTER TABLE channel_managers            DROP CONSTRAINT IF EXISTS channel_managers_channel_id_fkey;
ALTER TABLE channel_pinned_messages     DROP CONSTRAINT IF EXISTS channel_pinned_messages_channel_id_fkey;
ALTER TABLE channel_pinned_messages     DROP CONSTRAINT IF EXISTS channel_pinned_messages_message_id_fkey;
ALTER TABLE announcements               DROP CONSTRAINT IF EXISTS announcements_channel_id_fkey;
ALTER TABLE announcement_acknowledgements
                                        DROP CONSTRAINT IF EXISTS announcement_acknowledgements_announcement_id_fkey;
ALTER TABLE urgent_confirmations        DROP CONSTRAINT IF EXISTS urgent_confirmations_message_id_fkey;
ALTER TABLE approvals                   DROP CONSTRAINT IF EXISTS approvals_channel_id_fkey;
ALTER TABLE scheduled_messages          DROP CONSTRAINT IF EXISTS scheduled_messages_channel_id_fkey;
ALTER TABLE scheduled_messages          DROP CONSTRAINT IF EXISTS scheduled_messages_delivered_message_id_fkey;
ALTER TABLE message_reactions           DROP CONSTRAINT IF EXISTS message_reactions_message_id_fkey;
