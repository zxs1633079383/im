-- Revert M2-F scheduled messages schema.

DROP INDEX IF EXISTS idx_scheduled_sender;
DROP INDEX IF EXISTS idx_scheduled_due;
DROP TABLE IF EXISTS scheduled_messages;
