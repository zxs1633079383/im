-- ============================================================
-- M2-A: channel governance fields & auxiliary tables
-- Mattermost /channel/change/* semantic alignment: notice / purpose /
-- picture / props / orient / permission / is_top fine-grained fields;
-- plus channel_managers (N:N), channel_pinned_messages, and member
-- role / notify_pref extensions.
-- ============================================================

-- Extend channels with fine-grained fields.
ALTER TABLE channels ADD COLUMN IF NOT EXISTS notice      TEXT    NOT NULL DEFAULT '';
ALTER TABLE channels ADD COLUMN IF NOT EXISTS purpose     TEXT    NOT NULL DEFAULT '';
ALTER TABLE channels ADD COLUMN IF NOT EXISTS picture_url TEXT    NOT NULL DEFAULT '';
ALTER TABLE channels ADD COLUMN IF NOT EXISTS props       JSONB   NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE channels ADD COLUMN IF NOT EXISTS orient      SMALLINT NOT NULL DEFAULT 0;
ALTER TABLE channels ADD COLUMN IF NOT EXISTS permission  SMALLINT NOT NULL DEFAULT 0; -- 0=open 1=approval 2=closed
ALTER TABLE channels ADD COLUMN IF NOT EXISTS is_top      BOOLEAN  NOT NULL DEFAULT FALSE;

-- Channel managers: N:N between channels and users with admin rights.
-- This is a lighter concept than channel_members.role and allows multiple
-- managers per channel without altering existing role semantics.
CREATE TABLE IF NOT EXISTS channel_managers (
    channel_id BIGINT      NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    user_id    BIGINT      NOT NULL,
    added_by   BIGINT      NOT NULL,
    added_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (channel_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_channel_managers_user ON channel_managers(user_id);

-- Pinned messages in a channel.
CREATE TABLE IF NOT EXISTS channel_pinned_messages (
    channel_id BIGINT      NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    message_id BIGINT      NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    pinned_by  BIGINT      NOT NULL,
    pinned_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (channel_id, message_id)
);
CREATE INDEX IF NOT EXISTS idx_channel_pins_msg ON channel_pinned_messages(message_id);

-- Member role & notification preferences. role here is a coarse hint
-- (0=member 1=manager 2=owner) layered on top of the legacy role column;
-- we retain legacy semantics for back-compat but add the explicit new
-- notify_pref for push suppression decisions.
ALTER TABLE channel_members ADD COLUMN IF NOT EXISTS notify_pref SMALLINT NOT NULL DEFAULT 0; -- 0=all 1=mentions 2=none
