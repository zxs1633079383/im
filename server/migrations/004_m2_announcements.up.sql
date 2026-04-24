-- ============================================================
-- M2-B: channel announcements + acknowledgements
-- Mattermost /post/announcement/* alignment: save/read/list/detail/
-- delete/acceptList.
-- ============================================================

CREATE TABLE IF NOT EXISTS announcements (
    id         BIGSERIAL   PRIMARY KEY,
    channel_id BIGINT      NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    creator_id BIGINT      NOT NULL REFERENCES users(id),
    title      TEXT        NOT NULL,
    content    TEXT        NOT NULL,
    props      JSONB       NOT NULL DEFAULT '{}'::jsonb,
    deleted    BOOLEAN     NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_announcements_channel
    ON announcements(channel_id, created_at DESC);

CREATE TABLE IF NOT EXISTS announcement_acknowledgements (
    announcement_id BIGINT      NOT NULL REFERENCES announcements(id) ON DELETE CASCADE,
    user_id         BIGINT      NOT NULL REFERENCES users(id),
    acknowledged_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (announcement_id, user_id)
);
