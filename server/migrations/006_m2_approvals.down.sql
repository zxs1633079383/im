-- Revert M2-D approvals schema.

DROP INDEX IF EXISTS idx_approvals_channel;
DROP INDEX IF EXISTS idx_approvals_requester;
DROP INDEX IF EXISTS idx_approvals_approver_pending;
DROP TABLE IF EXISTS approvals;
