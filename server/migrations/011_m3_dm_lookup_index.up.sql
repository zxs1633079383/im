-- ============================================================
-- M3-C: FindDM 热路径索引 + repo SQL 重写协同优化
-- 2026-04-24 pre-5 benchmark 抓到 FindDM 双 JOIN 在并发 VU=300+
-- 下慢查询 3s 级 (gorm slow-sql threshold 触发)。
--
-- channel_members 主键是 (user_id, channel_id) —— 按 user_id 驱动走 PK。
-- FindDM 需要"两个 user 共同在的 DM channel"，旧 SQL 做双 INNER JOIN，
-- 在 mb 端只有单列 idx_channel_members_channel(channel_id) 能用，大量
-- 扫描后再 filter mb.user_id = ?。
--
-- 新索引 (channel_id, user_id) 是 PK 的"反向"形式，让 EXISTS 子查询按
-- mb.channel_id + mb.user_id 走 index-only lookup。搭配 repo.FindDM 的
-- EXISTS 重写，A 的 DM 平均 ≤ 10，整个 DM lookup < 1ms on warm cache。
-- ============================================================

CREATE INDEX IF NOT EXISTS idx_channel_members_channel_user
    ON channel_members(channel_id, user_id);
