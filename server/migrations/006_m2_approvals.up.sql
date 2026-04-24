-- ============================================================
-- M2-D: approvals — a per-channel "request-and-review" flow.
-- Semantically aligned with Mattermost's csesapi/post_approval: a requester
-- opens an approval addressed to one specific approver; the approver decides
-- approve / reject (with an optional note); the requester may cancel while
-- pending. status values: 0=pending 1=approved 2=rejected 3=cancelled.
-- ============================================================

CREATE TABLE IF NOT EXISTS approvals (
    id            BIGSERIAL   PRIMARY KEY,
    channel_id    BIGINT      NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    requester_id  BIGINT      NOT NULL,
    approver_id   BIGINT      NOT NULL,
    subject       TEXT        NOT NULL,
    content       TEXT        NOT NULL,
    props         JSONB       NOT NULL DEFAULT '{}'::jsonb,
    status        SMALLINT    NOT NULL DEFAULT 0,
    decided_at    TIMESTAMPTZ,
    decision_note TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- "my pending" inbox for approvers — tight partial index.
CREATE INDEX IF NOT EXISTS idx_approvals_approver_pending
    ON approvals(approver_id, status) WHERE status = 0;

-- "my submissions" for requesters — newest-first.
CREATE INDEX IF NOT EXISTS idx_approvals_requester
    ON approvals(requester_id, created_at DESC);

-- Per-channel listing (e.g. channel admin audit).
CREATE INDEX IF NOT EXISTS idx_approvals_channel
    ON approvals(channel_id, created_at DESC);
