-- ============================================================
-- C012 P-A 步骤 4/5：重建 018.up 删除的 17 个外键约束。
-- spec：docs/harness/C012-id-type-string-migration.md §3.1（实际编号 021，
-- spec 写 037）。
--
-- 现在所有 PK 列和 FK 列 type 都是 TEXT，重建 FK 不再有类型冲突。
-- ON DELETE 语义保持与原约束一致（CASCADE / SET NULL）。
-- ============================================================

-- channels 自引用 + 引用 messages
ALTER TABLE channels
    ADD CONSTRAINT fk_channels_root
        FOREIGN KEY (root_id) REFERENCES channels(id) ON DELETE CASCADE;
ALTER TABLE channels
    ADD CONSTRAINT fk_channels_root_message
        FOREIGN KEY (root_message_id) REFERENCES messages(id) ON DELETE SET NULL;

-- channel_members → channels
ALTER TABLE channel_members
    ADD CONSTRAINT channel_members_channel_id_fkey
        FOREIGN KEY (channel_id) REFERENCES channels(id) ON DELETE CASCADE;

-- messages → channels
ALTER TABLE messages
    ADD CONSTRAINT messages_channel_id_fkey
        FOREIGN KEY (channel_id) REFERENCES channels(id) ON DELETE CASCADE;

-- message_attachments → messages / files
ALTER TABLE message_attachments
    ADD CONSTRAINT message_attachments_message_id_fkey
        FOREIGN KEY (message_id) REFERENCES messages(id) ON DELETE CASCADE;
ALTER TABLE message_attachments
    ADD CONSTRAINT message_attachments_file_id_fkey
        FOREIGN KEY (file_id) REFERENCES files(id) ON DELETE CASCADE;

-- message_favorites → messages
ALTER TABLE message_favorites
    ADD CONSTRAINT message_favorites_message_id_fkey
        FOREIGN KEY (message_id) REFERENCES messages(id) ON DELETE CASCADE;

-- channel_managers → channels
ALTER TABLE channel_managers
    ADD CONSTRAINT channel_managers_channel_id_fkey
        FOREIGN KEY (channel_id) REFERENCES channels(id) ON DELETE CASCADE;

-- channel_pinned_messages → channels / messages
ALTER TABLE channel_pinned_messages
    ADD CONSTRAINT channel_pinned_messages_channel_id_fkey
        FOREIGN KEY (channel_id) REFERENCES channels(id) ON DELETE CASCADE;
ALTER TABLE channel_pinned_messages
    ADD CONSTRAINT channel_pinned_messages_message_id_fkey
        FOREIGN KEY (message_id) REFERENCES messages(id) ON DELETE CASCADE;

-- announcements → channels
ALTER TABLE announcements
    ADD CONSTRAINT announcements_channel_id_fkey
        FOREIGN KEY (channel_id) REFERENCES channels(id) ON DELETE CASCADE;

-- announcement_acknowledgements → announcements
ALTER TABLE announcement_acknowledgements
    ADD CONSTRAINT announcement_acknowledgements_announcement_id_fkey
        FOREIGN KEY (announcement_id) REFERENCES announcements(id) ON DELETE CASCADE;

-- urgent_confirmations → messages
ALTER TABLE urgent_confirmations
    ADD CONSTRAINT urgent_confirmations_message_id_fkey
        FOREIGN KEY (message_id) REFERENCES messages(id) ON DELETE CASCADE;

-- approvals → channels
ALTER TABLE approvals
    ADD CONSTRAINT approvals_channel_id_fkey
        FOREIGN KEY (channel_id) REFERENCES channels(id) ON DELETE CASCADE;

-- scheduled_messages → channels / messages
ALTER TABLE scheduled_messages
    ADD CONSTRAINT scheduled_messages_channel_id_fkey
        FOREIGN KEY (channel_id) REFERENCES channels(id) ON DELETE CASCADE;
ALTER TABLE scheduled_messages
    ADD CONSTRAINT scheduled_messages_delivered_message_id_fkey
        FOREIGN KEY (delivered_message_id) REFERENCES messages(id) ON DELETE SET NULL;

-- message_reactions → messages
ALTER TABLE message_reactions
    ADD CONSTRAINT message_reactions_message_id_fkey
        FOREIGN KEY (message_id) REFERENCES messages(id) ON DELETE CASCADE;
