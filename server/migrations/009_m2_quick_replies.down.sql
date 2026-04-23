-- Revert M2-G quick replies schema.

DROP INDEX IF EXISTS idx_quick_replies_user;
DROP TABLE IF EXISTS quick_replies;
