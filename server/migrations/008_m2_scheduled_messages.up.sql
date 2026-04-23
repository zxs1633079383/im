-- ============================================================
-- M2-F: scheduled_messages — semantically aligned with Mattermost's
-- csesapi/createScheduled: a deferred send that a background worker picks up
-- at scheduled_at. status values: 0=pending 1=delivered 2=cancelled 3=failed.
-- ============================================================

CREATE TABLE IF NOT EXISTS scheduled_messages (
    id                   BIGSERIAL   PRIMARY KEY,
    channel_id           BIGINT      NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    sender_id            BIGINT      NOT NULL,
    content              TEXT        NOT NULL,
    msg_type             SMALLINT    NOT NULL DEFAULT 1,
    visible_to           BIGINT[],
    reply_to             BIGINT,
    file_ids             BIGINT[],
    scheduled_at         TIMESTAMPTZ NOT NULL,
    status               SMALLINT    NOT NULL DEFAULT 0,
    delivered_message_id BIGINT      REFERENCES messages(id),
    error                TEXT,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- "due now" scan — tight partial index on (scheduled_at) where still pending.
CREATE INDEX IF NOT EXISTS idx_scheduled_due
    ON scheduled_messages(scheduled_at) WHERE status = 0;

-- per-sender listing.
CREATE INDEX IF NOT EXISTS idx_scheduled_sender
    ON scheduled_messages(sender_id, scheduled_at DESC);
