-- Revert M2-E notifications schema.

DROP INDEX IF EXISTS idx_notifications_sender;
DROP INDEX IF EXISTS idx_notifications_receiver_unread;
DROP TABLE IF EXISTS notifications;
