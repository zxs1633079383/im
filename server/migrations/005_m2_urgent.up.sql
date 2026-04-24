-- ============================================================
-- M2-C: urgent messages (confirm-required flag + confirmation table)
-- Semantically aligned with Mattermost's "urgent" post — a message that
-- requires per-recipient confirmation to clear the badge.
-- ============================================================

ALTER TABLE messages ADD COLUMN IF NOT EXISTS is_urgent BOOLEAN NOT NULL DEFAULT FALSE;

CREATE TABLE IF NOT EXISTS urgent_confirmations (
    message_id   BIGINT      NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    user_id      BIGINT      NOT NULL REFERENCES users(id),
    confirmed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (message_id, user_id)
);

-- Partial index for "unconfirmed urgent count" queries — scan only urgent msgs.
CREATE INDEX IF NOT EXISTS idx_messages_urgent
    ON messages(channel_id, is_urgent) WHERE is_urgent = TRUE;
