-- M2-C: urgent messages reverse

DROP INDEX IF EXISTS idx_messages_urgent;
DROP TABLE IF EXISTS urgent_confirmations;
ALTER TABLE messages DROP COLUMN IF EXISTS is_urgent;
