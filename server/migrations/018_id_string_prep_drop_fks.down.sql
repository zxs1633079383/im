-- ============================================================
-- C012 P-A 步骤 1/5 down —— 重建被 018.up 删掉的 17 个外键约束。
--
-- ⚠️ irreversible in prod：本 down 仅用于本地 reset / CI 回归。生产
-- 一旦 P-A 跑完，回退方案走数据备份 + 滚动停服重建（由 SRE 单独立项，
-- 见 C012 §8）。
--
-- 本文件假设 019..022 也已 down 完成（id 列重新是 BIGINT）。如果只 down
-- 了 018 没 down 019+ 会失败（TEXT 列上不能建指向 BIGINT 主键的 FK）。
-- ============================================================

ALTER TABLE channels
    ADD CONSTRAINT fk_channels_root
        FOREIGN KEY (root_id) REFERENCES channels(id) ON DELETE CASCADE;
ALTER TABLE channels
    ADD CONSTRAINT fk_channels_root_message
        FOREIGN KEY (root_message_id) REFERENCES messages(id) ON DELETE SET NULL;

ALTER TABLE channel_members
    ADD CONSTRAINT channel_members_channel_id_fkey
        FOREIGN KEY (channel_id) REFERENCES channels(id) ON DELETE CASCADE;

ALTER TABLE messages
    ADD CONSTRAINT messages_channel_id_fkey
        FOREIGN KEY (channel_id) REFERENCES channels(id) ON DELETE CASCADE;

ALTER TABLE message_attachments
    ADD CONSTRAINT message_attachments_message_id_fkey
        FOREIGN KEY (message_id) REFERENCES messages(id) ON DELETE CASCADE;
ALTER TABLE message_attachments
    ADD CONSTRAINT message_attachments_file_id_fkey
        FOREIGN KEY (file_id) REFERENCES files(id) ON DELETE CASCADE;

ALTER TABLE message_favorites
    ADD CONSTRAINT message_favorites_message_id_fkey
        FOREIGN KEY (message_id) REFERENCES messages(id) ON DELETE CASCADE;

ALTER TABLE channel_managers
    ADD CONSTRAINT channel_managers_channel_id_fkey
        FOREIGN KEY (channel_id) REFERENCES channels(id) ON DELETE CASCADE;

ALTER TABLE channel_pinned_messages
    ADD CONSTRAINT channel_pinned_messages_channel_id_fkey
        FOREIGN KEY (channel_id) REFERENCES channels(id) ON DELETE CASCADE;
ALTER TABLE channel_pinned_messages
    ADD CONSTRAINT channel_pinned_messages_message_id_fkey
        FOREIGN KEY (message_id) REFERENCES messages(id) ON DELETE CASCADE;

ALTER TABLE announcements
    ADD CONSTRAINT announcements_channel_id_fkey
        FOREIGN KEY (channel_id) REFERENCES channels(id) ON DELETE CASCADE;

ALTER TABLE announcement_acknowledgements
    ADD CONSTRAINT announcement_acknowledgements_announcement_id_fkey
        FOREIGN KEY (announcement_id) REFERENCES announcements(id) ON DELETE CASCADE;

ALTER TABLE urgent_confirmations
    ADD CONSTRAINT urgent_confirmations_message_id_fkey
        FOREIGN KEY (message_id) REFERENCES messages(id) ON DELETE CASCADE;

ALTER TABLE approvals
    ADD CONSTRAINT approvals_channel_id_fkey
        FOREIGN KEY (channel_id) REFERENCES channels(id) ON DELETE CASCADE;

ALTER TABLE scheduled_messages
    ADD CONSTRAINT scheduled_messages_channel_id_fkey
        FOREIGN KEY (channel_id) REFERENCES channels(id) ON DELETE CASCADE;
ALTER TABLE scheduled_messages
    ADD CONSTRAINT scheduled_messages_delivered_message_id_fkey
        FOREIGN KEY (delivered_message_id) REFERENCES messages(id) ON DELETE SET NULL;

ALTER TABLE message_reactions
    ADD CONSTRAINT message_reactions_message_id_fkey
        FOREIGN KEY (message_id) REFERENCES messages(id) ON DELETE CASCADE;
