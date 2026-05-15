-- v0.7.4 — messages.mention_list TEXT[] (NULL=无@; ['all']=@所有人; ['uid',...]=@指定)
-- Used by ChannelWithPreview.MentionInChannel + WS push_msg envelope.
ALTER TABLE messages
    ADD COLUMN IF NOT EXISTS mention_list TEXT[];

CREATE INDEX IF NOT EXISTS idx_messages_mention_gin
    ON messages USING gin (mention_list);
