-- C017 / C018 / C019 落地：channel_event 事件流水表 + PG sequence 对象
-- 详见 docs/harness/C017-channel-event-append-only-log.md
--      docs/harness/C018-pg-sequence-vs-row-lock-seq.md
--      docs/harness/C019-sync-cursor-event-seq.md

-- ──────────────────────────────────────────────────────────────────
-- 1. channel_event 事件流水表（append-only，hash 分区，万人群优化）
-- ──────────────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS channel_event (
    channel_id   TEXT     NOT NULL,
    event_seq    BIGINT   NOT NULL,
    event_type   SMALLINT NOT NULL,             -- 1=new, 2=edit, 3=delete, 4=reaction, 5=pin, 6=read_mark, 7=member
    msg_id       TEXT,                          -- nullable: reaction/read_mark 可能不挂 msg
    actor_id     TEXT     NOT NULL,
    payload      JSONB,                         -- event-specific 业务字段（如 reaction emoji / member event type）
    created_at   BIGINT   NOT NULL,             -- unix ms
    PRIMARY KEY (channel_id, event_seq)
) PARTITION BY HASH (channel_id);

-- 16 个 hash 分区（万人群分散到 16 个物理表，避免单表过大）
CREATE TABLE IF NOT EXISTS channel_event_p00 PARTITION OF channel_event FOR VALUES WITH (MODULUS 16, REMAINDER 0);
CREATE TABLE IF NOT EXISTS channel_event_p01 PARTITION OF channel_event FOR VALUES WITH (MODULUS 16, REMAINDER 1);
CREATE TABLE IF NOT EXISTS channel_event_p02 PARTITION OF channel_event FOR VALUES WITH (MODULUS 16, REMAINDER 2);
CREATE TABLE IF NOT EXISTS channel_event_p03 PARTITION OF channel_event FOR VALUES WITH (MODULUS 16, REMAINDER 3);
CREATE TABLE IF NOT EXISTS channel_event_p04 PARTITION OF channel_event FOR VALUES WITH (MODULUS 16, REMAINDER 4);
CREATE TABLE IF NOT EXISTS channel_event_p05 PARTITION OF channel_event FOR VALUES WITH (MODULUS 16, REMAINDER 5);
CREATE TABLE IF NOT EXISTS channel_event_p06 PARTITION OF channel_event FOR VALUES WITH (MODULUS 16, REMAINDER 6);
CREATE TABLE IF NOT EXISTS channel_event_p07 PARTITION OF channel_event FOR VALUES WITH (MODULUS 16, REMAINDER 7);
CREATE TABLE IF NOT EXISTS channel_event_p08 PARTITION OF channel_event FOR VALUES WITH (MODULUS 16, REMAINDER 8);
CREATE TABLE IF NOT EXISTS channel_event_p09 PARTITION OF channel_event FOR VALUES WITH (MODULUS 16, REMAINDER 9);
CREATE TABLE IF NOT EXISTS channel_event_p10 PARTITION OF channel_event FOR VALUES WITH (MODULUS 16, REMAINDER 10);
CREATE TABLE IF NOT EXISTS channel_event_p11 PARTITION OF channel_event FOR VALUES WITH (MODULUS 16, REMAINDER 11);
CREATE TABLE IF NOT EXISTS channel_event_p12 PARTITION OF channel_event FOR VALUES WITH (MODULUS 16, REMAINDER 12);
CREATE TABLE IF NOT EXISTS channel_event_p13 PARTITION OF channel_event FOR VALUES WITH (MODULUS 16, REMAINDER 13);
CREATE TABLE IF NOT EXISTS channel_event_p14 PARTITION OF channel_event FOR VALUES WITH (MODULUS 16, REMAINDER 14);
CREATE TABLE IF NOT EXISTS channel_event_p15 PARTITION OF channel_event FOR VALUES WITH (MODULUS 16, REMAINDER 15);

-- 关联索引：sync 算法用 FetchAfter(channel_id, after_event_seq, limit) → PK 直接覆盖
-- msg_id 索引：编辑历史回查 / event 关联 message 用（少量场景）
CREATE INDEX IF NOT EXISTS idx_channel_event_msg_id ON channel_event(msg_id) WHERE msg_id IS NOT NULL;

-- ──────────────────────────────────────────────────────────────────
-- 2. PG sequence 模板表（应用层 dynamic CREATE SEQUENCE 用，C018）
-- ──────────────────────────────────────────────────────────────────
--
-- channel 创建时 ChannelService.Create 会动态执行：
--   CREATE SEQUENCE IF NOT EXISTS channel_msg_seq_<channel_id> START 1 CACHE 50;
--   CREATE SEQUENCE IF NOT EXISTS channel_event_seq_<channel_id> START 1 CACHE 100;
--
-- 这里不预建任何具体 sequence；只建一个元数据表追踪 sequence 状态（便于运维 / 归档）

CREATE TABLE IF NOT EXISTS channel_sequence_meta (
    channel_id      TEXT    PRIMARY KEY REFERENCES channels(id) ON DELETE CASCADE,
    msg_seq_name    TEXT    NOT NULL,
    event_seq_name  TEXT    NOT NULL,
    created_at      BIGINT  NOT NULL
);

-- ──────────────────────────────────────────────────────────────────
-- 3. 回填脚本（现有 channel 同步建 sequence + 初始化）
-- ──────────────────────────────────────────────────────────────────
--
-- 在 P2 phase agent 落实 Go 代码时，需要单独写一个回填脚本：
--   server/cmd/migrate-channel-sequences/main.go
-- 它会：
--   1. SELECT id, seq FROM channels
--   2. 对每个 channel，CREATE SEQUENCE channel_msg_seq_<id> START <max(messages.seq)+1>
--   3. CREATE SEQUENCE channel_event_seq_<id> START 1
--   4. INSERT channel_sequence_meta
--   5. 历史 messages 回填 channel_event 行（type=new, msg_id=messages.id, actor_id=sender_id, event_seq=row_number()）
--
-- 本 migration 不执行回填（防 prod 单事务超时），只建 schema。
-- 回填脚本与 P2 agent 一起 ship，作为独立 Makefile target: `make migrate-channel-event-backfill`