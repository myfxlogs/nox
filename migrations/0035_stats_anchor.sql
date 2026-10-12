-- 0035 stats_anchor：盈亏统计起点重置（F4，业主选 A「不删历史」）
-- 单行表（id=1 CHECK 约束保证唯一），anchor 默认 now() = 累计口径起点。
-- 总览「累计已实现」= SUM(realized_pnl) WHERE closed_at >= anchor；
-- 「今日已实现」不受 anchor 影响（当日口径，日亏停机基准解耦）。
-- 重置 = UPDATE stats_anchor SET anchor = now() WHERE id = 1。
CREATE TABLE IF NOT EXISTS stats_anchor (
    id     INTEGER PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    anchor TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- 初始行（INSERT ON CONFLICT 保证幂等，重复执行不报错）
INSERT INTO stats_anchor (id, anchor) VALUES (1, now())
ON CONFLICT (id) DO NOTHING;
