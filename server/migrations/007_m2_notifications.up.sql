-- ============================================================
-- M2-E: notifications — a lightweight per-user inbox aligned with Mattermost
-- csesapi/notification: sender pushes an arbitrary title+body to a receiver
-- out-of-band from any specific channel. type 0=generic 1=mention 2=system.
-- ============================================================

CREATE TABLE IF NOT EXISTS notifications (
    id          BIGSERIAL   PRIMARY KEY,
    sender_id   BIGINT      NOT NULL,
    receiver_id BIGINT      NOT NULL,
    title       TEXT        NOT NULL,
    body        TEXT        NOT NULL DEFAULT '',
    type        SMALLINT    NOT NULL DEFAULT 0,
    read_at     TIMESTAMPTZ,
    props       JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Unread inbox — tight partial index on (receiver, created_at desc).
CREATE INDEX IF NOT EXISTS idx_notifications_receiver_unread
    ON notifications(receiver_id, created_at DESC) WHERE read_at IS NULL;

-- Sender history — per-sender outbox listing.
CREATE INDEX IF NOT EXISTS idx_notifications_sender
    ON notifications(sender_id, created_at DESC);
